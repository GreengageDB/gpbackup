package helper

import (
	"bufio"
	"bytes"
	"compress/gzip"
	errorsStd "errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GreengageDB/gp-common-go-libs/operating"
	"github.com/greenplum-db/gpbackup/toc"
	"github.com/greenplum-db/gpbackup/utils"
	"github.com/klauspost/compress/zstd"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

/*
 * Restore specific functions
 */
type ReaderType string

const (
	SEEKABLE    ReaderType = "seekable" // reader which supports seek
	NONSEEKABLE            = "discard"  // reader which is not seekable
	SUBSET                 = "subset"   // reader which operates on pre filtered data
)

var (
	writeHandle *os.File
	writer      *bufio.Writer
	contentRE   *regexp.Regexp
)

/* IRestoreReader interface to wrap the underlying reader.
 * getReaderType() identifies how the reader can be used
 * SEEKABLE uses seekReader. Used when restoring from uncompressed data with filters from local filesystem
 * NONSEEKABLE and SUBSET types uses bufReader.
 * SUBSET type applies when restoring using plugin(if compatible) from uncompressed data with filters
 * NONSEEKABLE type applies for every other restore scenario
 */
type IRestoreReader interface {
	waitForPlugin() error
	positionReader(pos uint64, oid int) error
	copyData(num int64) (int64, error)
	copyAllData() (int64, error)
	closeFileHandle()
	getReaderType() ReaderType
	discardData(num int64) (int64, error)
}

type RestoreReader struct {
	fileHandle *os.File
	bufReader  *bufio.Reader
	seekReader io.ReadSeeker
	pluginCmd  IPluginCmd
	readerType ReaderType
	discardErr bool
}

// Wait for plugin process that should be already finished. This should be
// called on every reader that used a plugin as to not leave any zombies behind.
func (r *RestoreReader) waitForPlugin() error {
	var err error
	if r.pluginCmd != nil && r.pluginCmd.hasProcess() {
		logVerbose(fmt.Sprintf("Waiting for the plugin process (%d)", r.pluginCmd.pid()))
		err = r.pluginCmd.Wait()
		if err != nil {
			logError(fmt.Sprintf("Plugin process exited with an error: %s", err))
		}
		// Log plugin's stderr as warnings.
		r.pluginCmd.errLog()
	}
	return err
}

func (r *RestoreReader) positionReader(pos uint64, oid int) error {
	switch r.readerType {
	case SEEKABLE:
		seekPosition, err := r.seekReader.Seek(int64(pos), io.SeekCurrent)
		if err != nil {
			// Always hard quit if data reader has issues
			return err
		}
		logVerbose(fmt.Sprintf("Oid %d: Data Reader seeked forward to %d byte offset", oid, seekPosition))
	case NONSEEKABLE:
		numDiscarded, err := r.bufReader.Discard(int(pos))
		if err != nil {
			// Always hard quit if data reader has issues
			return err
		}
		logVerbose(fmt.Sprintf("Oid %d: Data Reader discarded %d bytes", oid, numDiscarded))
	case SUBSET:
		// Do nothing as the stream is pre filtered
	}
	return nil
}

func (r *RestoreReader) discardData(num int64) (int64, error) {
	if r.readerType != SUBSET {
		panic("discardData should be called for readerType == SUBSET only")
	}

	if r.discardErr {
		err := fmt.Errorf("%d bytes to discard, but discard error has already occurred. Skipping read.", num)
		logVerbose(err.Error())
		return 0, err
	}

	n, err := io.CopyN(io.Discard, r.bufReader, num)
	if err == nil {
		logVerbose(fmt.Sprintf("discarded %d bytes", n))
	} else {
		r.discardErr = true
		err = errors.Wrapf(err, "discarded %d bytes from %d", n, num)
		logError(err.Error())
	}
	return n, err
}

func (r *RestoreReader) copyData(num int64) (int64, error) {
	var bytesRead int64
	var err error
	switch r.readerType {
	case SEEKABLE:
		bytesRead, err = io.CopyN(writer, r.seekReader, num)
	case NONSEEKABLE:
		bytesRead, err = io.CopyN(writer, r.bufReader, num)
	case SUBSET:
		if r.discardErr {
			err := fmt.Errorf("%d bytes to copy, but discard error has already occurred. Skipping read.", num)
			logVerbose(err.Error())
			return 0, err
		}

		bytesRead, err = io.CopyN(writer, r.bufReader, num)
		if err != nil && err != io.EOF && *onErrorContinue {
			err = errors.Wrapf(err, "copied %d bytes from %d", bytesRead, num)
			bytesDiscard, errDiscard := r.discardData(num - bytesRead)
			bytesRead += bytesDiscard
			if errDiscard != nil {
				err = errorsStd.Join(errDiscard, err)
				err = errors.Wrap(err, "discard error in copyData")
			}
		}
	}
	return bytesRead, err
}

func (r *RestoreReader) copyAllData() (int64, error) {
	var bytesRead int64
	var err error
	switch r.readerType {
	case SEEKABLE:
		bytesRead, err = io.Copy(writer, r.seekReader)
	case NONSEEKABLE, SUBSET:
		bytesRead, err = io.Copy(writer, r.bufReader)
	}
	return bytesRead, err
}

func (r *RestoreReader) getReaderType() ReaderType {
	return r.readerType
}

func (r *RestoreReader) closeFileHandle() {
	r.fileHandle.Close()
}

func (RestoreHelper) createPipe(pipe string) error {
	return createPipe(pipe)
}

func (rh RestoreHelper) closeAndDeletePipe(tableOid int, batchNum int) {
	pipe := fmt.Sprintf("%s_%d_%d", *pipeFile, tableOid, batchNum)
	logInfo(fmt.Sprintf("Oid %d, Batch %d: Closing pipe %s", tableOid, batchNum, pipe))
	err := rh.flushAndCloseRestoreWriter(pipe, tableOid)
	if err != nil {
		logVerbose(fmt.Sprintf("Oid %d, Batch %d: Failed to flush and close pipe: %s", tableOid, batchNum, err))
	}

	logVerbose(fmt.Sprintf("Oid %d, Batch %d: Attempt to delete pipe %s", tableOid, batchNum, pipe))
	err = deletePipe(pipe)
	if err != nil {
		logError("Oid %d, Batch %d: Failed to remove pipe %s: %v", tableOid, batchNum, pipe, err)
	}
}

type oidWithBatch struct {
	oid   int
	batch int
}

type IRestoreHelper interface {
	getOidWithBatchListFromFile(oidFileName string) ([]oidWithBatch, error)
	flushAndCloseRestoreWriter(pipeName string, oid int) error
	doRestoreAgentCleanup()

	createPipe(pipe string) error
	closeAndDeletePipe(tableOid int, batchNum int)

	getRestoreDataReader(fileToRead string, objToc *toc.SegmentTOC, oidList []int) (IRestoreReader, error)
	getRestorePipeWriter(currentPipe string) (*bufio.Writer, *os.File, error)
	checkForSkipFile(pipeFile string, tableOid int) bool
}

type RestoreHelper struct{}

func (RestoreHelper) checkForSkipFile(pipeFile string, tableOid int) bool {
	return utils.FileExists(fmt.Sprintf("%s_%d", utils.GetSkipFilename(pipeFile), tableOid))
}

func doRestoreAgent() error {
	return doRestoreAgentInternal(new(RestoreHelper))
}

func doRestoreAgentInternal(restoreHelper IRestoreHelper) error {
	// We need to track various values separately per content for resize restore
	var segmentTOC map[int]*toc.SegmentTOC
	var tocEntries map[int]map[uint]toc.SegmentDataEntry
	var start map[int]uint64
	var end map[int]uint64
	var lastByte map[int]uint64

	var bytesRead int64
	var lastError error

	readers := make(map[int]IRestoreReader)

	oidWithBatchList, err := restoreHelper.getOidWithBatchListFromFile(*oidFile)
	if err != nil {
		return err
	}

	defer restoreHelper.doRestoreAgentCleanup()

	// During a larger-to-smaller restore, we need to do multiple passes for each oid, so the table
	// restore goes into another nested for loop below.  In the normal or smaller-to-larger cases,
	// this is equivalent to doing a single loop per table.
	batches := 1
	if *isResizeRestore && *origSize > *destSize {
		batches = *origSize / *destSize
		// If dest doesn't divide evenly into orig, there's one more incomplete batch
		if *origSize%*destSize != 0 {
			batches += 1
		}
	}

	// With the change to make oidlist include batch numbers we need to pull
	// them out. We also need to remove duplicate oids.
	var oidList []int
	var prevOid int
	for _, v := range oidWithBatchList {
		if v.oid == prevOid {
			continue
		} else {
			oidList = append(oidList, v.oid)
			prevOid = v.oid
		}
	}

	if *singleDataFile {
		contentToRestore := *content
		segmentTOC = make(map[int]*toc.SegmentTOC)
		tocEntries = make(map[int]map[uint]toc.SegmentDataEntry)
		start = make(map[int]uint64)
		end = make(map[int]uint64)
		lastByte = make(map[int]uint64)
		for b := 0; b < batches; b++ {
			// When performing a resize restore, if the content of the file we're being asked to read from
			// is higher than any backup content, then no such file exists and we shouldn't try to open it.
			if *isResizeRestore && contentToRestore >= *origSize {
				break
			}
			tocFileForContent := replaceContentInFilename(*tocFile, contentToRestore)
			segmentTOC[contentToRestore] = toc.NewSegmentTOC(tocFileForContent)
			tocEntries[contentToRestore] = segmentTOC[contentToRestore].DataEntries

			filename := replaceContentInFilename(*dataFile, contentToRestore)
			readers[contentToRestore], err = restoreHelper.getRestoreDataReader(filename, segmentTOC[contentToRestore], oidList)
			if readers[contentToRestore] != nil {
				// NOTE: If we reach here with batches > 1, there will be
				// *origSize / *destSize (N old segments / N new segments)
				// readers + 1, which is presumably a small number, so we just
				// defer the cleanup.
				//
				// The loops under are constructed in a way that needs to keep
				// all readers open for the entire duration of restore (oid is
				// in outer loop -- batches in inner loop, we'll need all
				// readers for every outer loop iteration), so we can't properly
				// close any of the readers until we restore every oid yet,
				// unless The Big Refactoring will arrive.
				defer readers[contentToRestore].waitForPlugin()
			}
			if err != nil {
				logError(fmt.Sprintf("Error encountered getting restore data reader for single data file: %v", err))
				return err
			}
			logVerbose(fmt.Sprintf("Using reader type: %s", readers[contentToRestore].getReaderType()))

			contentToRestore += *destSize
		}
	}

	var currentPipe string

	// If skip file is detected for the particular tableOid, will not process batches related to this oid
	skipOid := -1

	for i, oidWithBatch := range oidWithBatchList {
		tableOid := oidWithBatch.oid
		batchNum := oidWithBatch.batch

		contentToRestore := *content + (*destSize * batchNum)
		if wasTerminated.Load() {
			logError("Terminated due to user request")
			return errors.New("Terminated due to user request")
		}

		currentPipe = fmt.Sprintf("%s_%d_%d", *pipeFile, tableOid, batchNum)
		if i < len(oidWithBatchList)-*copyQueue {
			nextOidWithBatch := oidWithBatchList[i+*copyQueue]
			nextOid := nextOidWithBatch.oid

			if nextOid != skipOid {
				nextBatchNum := nextOidWithBatch.batch
				nextPipeToCreate := fmt.Sprintf("%s_%d_%d", *pipeFile, nextOid, nextBatchNum)
				logVerbose(fmt.Sprintf("Oid %d, Batch %d: Creating pipe %s\n", nextOid, nextBatchNum, nextPipeToCreate))
				err := restoreHelper.createPipe(nextPipeToCreate)
				if err != nil {
					logError(fmt.Sprintf("Oid %d, Batch %d: Failed to create pipe %s\n", nextOid, nextBatchNum, nextPipeToCreate))
					// In the case this error is hit it means we have lost the
					// ability to create pipes normally, so hard quit even if
					// --on-error-continue is given
					return err
				}
			}
		}

		if tableOid == skipOid {
			logVerbose(fmt.Sprintf("Oid %d, Batch %d: skip due to skip file\n", tableOid, batchNum))
			goto LoopEnd
		}

		if *singleDataFile {
			start[contentToRestore] = tocEntries[contentToRestore][uint(tableOid)].StartByte
			end[contentToRestore] = tocEntries[contentToRestore][uint(tableOid)].EndByte
		} else if *isResizeRestore {
			if contentToRestore < *origSize {
				// We can only pass one filename to the helper, so we still pass in the single-data-file-style
				// filename in a non-SDF resize case, then add the oid manually and set up the reader for that.
				filename := constructSingleTableFilename(*dataFile, contentToRestore, tableOid)

				// Close file before it gets overwritten. Free up these
				// resources when the reader is not needed anymore.
				if reader, ok := readers[contentToRestore]; ok {
					reader.closeFileHandle()
				}
				// We pre-create readers above for the sake of not re-opening SDF readers.  For MDF we can't
				// re-use them but still having them in a map simplifies overall code flow.  We repeatedly assign
				// to a map entry here intentionally.
				readers[contentToRestore], err = restoreHelper.getRestoreDataReader(filename, nil, nil)
				if err != nil {
					logError(fmt.Sprintf("Oid: %d, Batch %d: Error encountered getting restore data reader: %v", tableOid, batchNum, err))
					return err
				}
			}
		}

		logInfo(fmt.Sprintf("Oid %d, Batch %d: Opening pipe %s", tableOid, batchNum, currentPipe))
		for {
			writer, writeHandle, err = restoreHelper.getRestorePipeWriter(currentPipe)
			if err != nil {
				if errors.Is(err, unix.ENXIO) {
					// COPY (the pipe reader) has not tried to access the pipe yet so our restore_helper
					// process will get ENXIO error on its nonblocking open call on the pipe. We loop in
					// here while looking to see if gprestore has created a skip file for this restore entry.
					//
					// TODO: Skip files will only be created when gprestore is run against GPDB 6+ so it
					// might be good to have a GPDB version check here. However, the restore helper should
					// not contain a database connection so the version should be passed through the helper
					// invocation from gprestore (e.g. create a --db-version flag option).
					if *onErrorContinue && restoreHelper.checkForSkipFile(*pipeFile, tableOid) {
						logWarn(fmt.Sprintf("Oid %d, Batch %d: Skip file discovered, skipping this relation.", tableOid, batchNum))
						err = nil
						skipOid = tableOid
						if *singleDataFile && readers[contentToRestore] != nil && readers[contentToRestore].getReaderType() == SUBSET {
							bytesToDiscard := int64(end[contentToRestore] - start[contentToRestore])
							_, err = readers[contentToRestore].discardData(bytesToDiscard)
						}
						/* Close up to *copyQueue files with this tableOid */
						for idx := 0; idx < *copyQueue; idx++ {
							batchToDelete := batchNum + idx
							if batchToDelete < batches {
								restoreHelper.closeAndDeletePipe(tableOid, batchToDelete)
							}
						}
						goto LoopEnd
					} else {
						// keep trying to open the pipe
						time.Sleep(50 * time.Millisecond)
					}
				} else {
					// In the case this error is hit it means we have lost the
					// ability to open pipes normally, so hard quit even if
					// --on-error-continue is given
					logError(fmt.Sprintf("Oid %d, Batch %d: Pipes can no longer be opened. Exiting with error: %s", tableOid, batchNum, err))
					return err
				}
			} else {
				// A reader has connected to the pipe and we have successfully opened
				// the writer for the pipe. To avoid having to write complex buffer
				// logic for when os.write() returns EAGAIN due to full buffer, set
				// the file descriptor to block on IO.
				unix.SetNonblock(int(writeHandle.Fd()), false)
				logVerbose(fmt.Sprintf("Oid %d, Batch %d: Reader connected to pipe %s", tableOid, batchNum, path.Base(currentPipe)))
				break
			}
		}

		// Only position reader in case of SDF.  MDF case reads entire file, and does not need positioning.
		// Further, in SDF case, map entries for contents that were not part of original backup will be nil,
		// and calling methods on them errors silently.
		if *singleDataFile && !(*isResizeRestore && contentToRestore >= *origSize) {
			logVerbose(fmt.Sprintf("Oid %d, Batch %d: Data Reader - Start Byte: %d; End Byte: %d; Last Byte: %d", tableOid, batchNum, start[contentToRestore], end[contentToRestore], lastByte[contentToRestore]))
			err = readers[contentToRestore].positionReader(start[contentToRestore]-lastByte[contentToRestore], tableOid)
			if err != nil {
				logError(fmt.Sprintf("Oid %d, Batch %d: Error reading from pipe: %s", tableOid, batchNum, err))
				return err
			}
		}

		logVerbose(fmt.Sprintf("Oid %d, Batch %d: Start table restore", tableOid, batchNum))
		if *isResizeRestore {
			if contentToRestore < *origSize {
				if *singleDataFile {
					bytesRead, err = readers[contentToRestore].copyData(int64(end[contentToRestore] - start[contentToRestore]))
				} else {
					bytesRead, err = readers[contentToRestore].copyAllData()
				}
			} else {
				// Write "empty" data to the pipe for COPY ON SEGMENT to read.
				bytesRead = 0
			}
		} else {
			bytesRead, err = readers[contentToRestore].copyData(int64(end[contentToRestore] - start[contentToRestore]))
		}
		if err != nil {
			// In case COPY FROM or copyN fails in the middle of a load. We
			// need to update the lastByte with the amount of bytes that was
			// copied before it errored out
			if *singleDataFile {
				lastByte[contentToRestore] = start[contentToRestore] + uint64(bytesRead)
			}
			err = errors.Wrap(err, "Error copying data")
			goto LoopEnd
		}

		if *singleDataFile {
			lastByte[contentToRestore] = end[contentToRestore]
		}
		logInfo(fmt.Sprintf("Oid %d, Batch %d: Copied %d bytes into the pipe", tableOid, batchNum, bytesRead))
	LoopEnd:
		if tableOid != skipOid {
			restoreHelper.closeAndDeletePipe(tableOid, batchNum)
		}

		logVerbose(fmt.Sprintf("Oid %d, Batch %d: End batch restore", tableOid, batchNum))

		// On resize restore reader might be nil,
		// for example, if contentToRestore >= *origSize,
		// and also for the next batch after detecting a skip file.
		if !*singleDataFile && readers[contentToRestore] != nil {
			if errPlugin := readers[contentToRestore].waitForPlugin(); errPlugin != nil {
				if err != nil {
					err = errors.Wrap(err, errPlugin.Error())
				} else {
					err = errPlugin
				}
			}
		}

		if err != nil {
			logError(fmt.Sprintf("Oid %d, Batch %d: Error encountered: %v", tableOid, batchNum, err))
			if *onErrorContinue {
				lastError = err
				err = nil
				continue
			} else {
				return err
			}
		}
	}

	return lastError
}

func (rh *RestoreHelper) doRestoreAgentCleanup() {
	err := rh.flushAndCloseRestoreWriter("Current writer pipe on cleanup", 0)
	if err != nil {
		logVerbose("Encountered error during cleanup: %v", err)
	}
}

func (RestoreHelper) flushAndCloseRestoreWriter(pipeName string, oid int) error {
	if writer != nil {
		writer.Write([]byte{}) // simulate writer connected in case of error
		err := writer.Flush()
		if err != nil {
			logError("Oid %d: Failed to flush pipe %s", oid, pipeName)
			return err
		}
		writer = nil
		logVerbose("Oid %d: Successfully flushed pipe %s", oid, pipeName)
	}
	if writeHandle != nil {
		err := writeHandle.Close()
		if err != nil {
			logError("Oid %d: Failed to close pipe handle", oid)
			return err
		}
		writeHandle = nil
		logVerbose("Oid %d: Successfully closed pipe handle", oid)
	}
	return nil
}

func constructSingleTableFilename(name string, contentToRestore int, oid int) string {
	name = strings.ReplaceAll(name, fmt.Sprintf("gpbackup_%d", *content), fmt.Sprintf("gpbackup_%d", contentToRestore))
	nameParts := strings.Split(name, ".")
	filename := fmt.Sprintf("%s_%d", nameParts[0], oid)
	if len(nameParts) > 1 { // We only expect filenames ending in ".gz" or ".zst", but they can contain dots so handle arbitrary numbers of dots
		prefix := strings.Join(nameParts[0:len(nameParts)-1], ".")
		suffix := nameParts[len(nameParts)-1]
		filename = fmt.Sprintf("%s_%d.%s", prefix, oid, suffix)
	}
	return filename
}

func replaceContentInFilename(filename string, content int) string {
	if contentRE == nil {
		contentRE = regexp.MustCompile("gpbackup_([0-9]+)_")
	}
	return contentRE.ReplaceAllString(filename, fmt.Sprintf("gpbackup_%d_", content))
}

func (RestoreHelper) getOidWithBatchListFromFile(oidFileName string) ([]oidWithBatch, error) {
	oidStr, err := operating.System.ReadFile(oidFileName)
	if err != nil {
		logError(fmt.Sprintf("Error encountered reading oid batch list from file: %v", err))
		return nil, err
	}
	oidStrList := strings.Split(strings.TrimSpace(fmt.Sprintf("%s", oidStr)), "\n")
	oidList := make([]oidWithBatch, len(oidStrList))
	for i, entry := range oidStrList {
		oidWithBatchEntry := strings.Split(entry, ",")
		oidNum, _ := strconv.Atoi(oidWithBatchEntry[0])
		batchNum, _ := strconv.Atoi(oidWithBatchEntry[1])

		oidList[i] = oidWithBatch{oid: oidNum, batch: batchNum}
	}
	return oidList, nil
}

func (RestoreHelper) getRestoreDataReader(fileToRead string, objToc *toc.SegmentTOC, oidList []int) (IRestoreReader, error) {
	var readHandle io.Reader
	var seekHandle io.ReadSeeker
	var isSubset bool
	var err error = nil
	var pluginCmd IPluginCmd
	restoreReader := new(RestoreReader)

	if *pluginConfigFile != "" {
		pluginCmd, readHandle, isSubset, err = startRestorePluginCommand(fileToRead, objToc, oidList)
		restoreReader.pluginCmd = pluginCmd
		if isSubset {
			// Reader that operates on subset data
			restoreReader.readerType = SUBSET
		} else {
			// Regular reader which doesn't support seek
			restoreReader.readerType = NONSEEKABLE
		}
	} else {
		if *isFiltered && !strings.HasSuffix(fileToRead, ".gz") && !strings.HasSuffix(fileToRead, ".zst") {
			// Seekable reader if backup is not compressed and filters are set
			restoreReader.fileHandle, err = os.Open(fileToRead)
			seekHandle = restoreReader.fileHandle
			restoreReader.readerType = SEEKABLE

		} else {
			// Regular reader which doesn't support seek
			restoreReader.fileHandle, err = os.Open(fileToRead)
			readHandle = restoreReader.fileHandle
			restoreReader.readerType = NONSEEKABLE
		}
	}
	if err != nil {
		// error logging handled by calling functions
		return nil, err
	}
	if pluginCmd != nil {
		logVerbose(fmt.Sprintf("Started plugin process (%d)", pluginCmd.pid()))
	}

	// Set the underlying stream reader in restoreReader
	if restoreReader.readerType == SEEKABLE {
		restoreReader.seekReader = seekHandle
	} else if strings.HasSuffix(fileToRead, ".gz") {
		gzipReader, err := gzip.NewReader(readHandle)
		if err != nil {
			// error logging handled by calling functions
			return nil, err
		}
		restoreReader.bufReader = bufio.NewReader(gzipReader)
	} else if strings.HasSuffix(fileToRead, ".zst") {
		zstdReader, err := zstd.NewReader(readHandle)
		if err != nil {
			// error logging handled by calling functions
			return nil, err
		}
		restoreReader.bufReader = bufio.NewReader(zstdReader)
	} else {
		restoreReader.bufReader = bufio.NewReader(readHandle)
	}

	return restoreReader, err
}

func (RestoreHelper) getRestorePipeWriter(currentPipe string) (*bufio.Writer, *os.File, error) {
	fileHandle, err := os.OpenFile(currentPipe, os.O_WRONLY|unix.O_NONBLOCK, os.ModeNamedPipe)
	if err != nil {
		// error logging handled by calling functions
		return nil, nil, err
	}

	// At the moment (Golang 1.15), the copy_file_range system call from the os.File
	// ReadFrom method is only supported for Linux platforms. Furthermore, cross-filesystem
	// support only works on kernel versions 5.3 and above. Until modern OS platforms start
	// adopting the new kernel, we must only use the bare essential methods Write() and
	// Close() for the pipe to avoid an extra buffer read that can happen in error
	// scenarios with --on-error-continue.
	pipeWriter := bufio.NewWriter(struct{ io.WriteCloser }{fileHandle})

	return pipeWriter, fileHandle, nil
}

func getSubsetFlag(fileToRead string, pluginConfig *utils.PluginConfig) bool {
	// Restore subset is disabled in the plugin config
	if !pluginConfig.CanRestoreSubset() {
		return false
	}
	// Helper's option does not allow to use subset
	if !*isFiltered {
		return false
	}
	// Restore subset and compression does not allow together
	if strings.HasSuffix(fileToRead, ".gz") || strings.HasSuffix(fileToRead, ".zst") {
		return false
	}

	return true
}

// IPluginCmd is needed to keep track of readable stderr and whether the command
// has already been ended.
type IPluginCmd interface {
	hasProcess() bool
	pid() int
	Wait() error
	errLog()
}

type PluginCmd struct {
	*exec.Cmd
	errBuf bytes.Buffer
}

func (p PluginCmd) hasProcess() bool {
	return p.Process != nil
}

func (p PluginCmd) pid() int {
	return p.Process.Pid
}

func (p PluginCmd) errLog() {
	errLog := strings.Trim(p.errBuf.String(), "\x00")
	if len(errLog) != 0 {
		logWarn(fmt.Sprintf("Plugin log: %s", errLog))
		// Consume the entire buffer.
		p.errBuf.Next(p.errBuf.Len())
	}
}

func startRestorePluginCommand(fileToRead string, objToc *toc.SegmentTOC, oidList []int) (IPluginCmd, io.Reader, bool, error) {
	isSubset := false
	pluginConfig, err := utils.ReadPluginConfig(*pluginConfigFile)
	if err != nil {
		logError(fmt.Sprintf("Error encountered when reading plugin config: %v", err))
		return nil, nil, false, err
	}
	cmdStr := ""
	if objToc != nil && getSubsetFlag(fileToRead, pluginConfig) {
		offsetsFile, _ := ioutil.TempFile("/tmp", "gprestore_offsets_")
		defer func() {
			offsetsFile.Close()
		}()
		w := bufio.NewWriter(offsetsFile)
		w.WriteString(fmt.Sprintf("%v", len(oidList)))

		for _, oid := range oidList {
			w.WriteString(fmt.Sprintf(" %v %v", objToc.DataEntries[uint(oid)].StartByte, objToc.DataEntries[uint(oid)].EndByte))
		}
		w.Flush()
		cmdStr = fmt.Sprintf("%s restore_data_subset %s %s %s", pluginConfig.ExecutablePath, pluginConfig.ConfigPath, fileToRead, offsetsFile.Name())
		isSubset = true
	} else {
		cmdStr = fmt.Sprintf("%s restore_data %s %s", pluginConfig.ExecutablePath, pluginConfig.ConfigPath, fileToRead)
	}
	logVerbose(cmdStr)

	pluginCmd := &PluginCmd{}
	pluginCmd.Cmd = exec.Command("bash", "-c", cmdStr)
	pluginCmd.Stderr = &pluginCmd.errBuf

	readHandle, err := pluginCmd.StdoutPipe()
	if err != nil {
		return nil, nil, false, err
	}

	err = pluginCmd.Start()

	return pluginCmd, readHandle, isSubset, err
}
