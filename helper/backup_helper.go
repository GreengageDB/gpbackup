package helper

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/GreengageDB/gp-common-go-libs/operating"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/pkg/errors"
)

/*
 * Backup specific functions
 */

func doBackupAgent() error {
	var lastRead uint64
	var (
		pipeWriter BackupPipeWriterCloser
		writeCmd   *exec.Cmd
	)
	tocfile := &toc.SegmentTOC{}
	tocfile.DataEntries = make(map[uint]toc.SegmentDataEntry)

	/*
	 * With --slot-pipes, gpbackup chooses the data file of each table when its COPY
	 * starts, so the tables of this data file are not known in advance.  The pipes
	 * are named by slot, the position of the table in the data file, and each COPY
	 * writes the oid of its table to the oid file of its slot before it opens the
	 * pipe.  Once all tables are backed up, gpbackup opens and closes the next slot
	 * without an oid file, which ends the data file.
	 */
	var oidList []int
	var err error
	if !*slotPipes {
		oidList, err = getOidListFromFile(*oidFile)
		if err != nil {
			// error logging handled in getOidListFromFile
			return err
		}
	}

	var currentPipe string
	var errBuf bytes.Buffer
	/*
	 * It is important that we create the reader before creating the writer
	 * so that we establish a connection to the first pipe (created by gpbackup)
	 * and properly clean it up if an error occurs while creating the writer.
	 */
	for i := 0; *slotPipes || i < len(oidList); i++ {
		if *slotPipes {
			currentPipe = fmt.Sprintf("%s_%d", *pipeFile, i)
		} else {
			currentPipe = fmt.Sprintf("%s_%d", *pipeFile, oidList[i])
		}
		if wasTerminated.Load() {
			logError("Terminated due to user request")
			return errors.New("Terminated due to user request")
		}
		if *slotPipes {
			nextPipeToCreate := fmt.Sprintf("%s_%d", *pipeFile, i+*copyQueue)
			logVerbose(fmt.Sprintf("Slot %d: Creating pipe %s\n", i+*copyQueue, nextPipeToCreate))
			err := createPipe(nextPipeToCreate)
			if err != nil {
				logError(fmt.Sprintf("Slot %d: Failed to create pipe %s\n", i+*copyQueue, nextPipeToCreate))
				return err
			}
		} else if i < len(oidList)-*copyQueue {
			nextPipeToCreate := fmt.Sprintf("%s_%d", *pipeFile, oidList[i+*copyQueue])
			logVerbose(fmt.Sprintf("Oid %d: Creating pipe %s\n", oidList[i+*copyQueue], nextPipeToCreate))
			err := createPipe(nextPipeToCreate)
			if err != nil {
				logError(fmt.Sprintf("Oid %d: Failed to create pipe %s\n", oidList[i+*copyQueue], nextPipeToCreate))
				return err
			}
		}

		var oid int
		if *slotPipes {
			logInfo(fmt.Sprintf("Slot %d: Opening pipe %s", i, currentPipe))
		} else {
			oid = oidList[i]
			logInfo(fmt.Sprintf("Oid %d: Opening pipe %s", oid, currentPipe))
		}
		reader, readHandle, err := getBackupPipeReader(currentPipe)
		if err != nil {
			logError(fmt.Sprintf("Oid %d: Error encountered getting backup pipe reader: %v", oid, err))
			return err
		}
		if *slotPipes {
			var found bool
			oid, found, err = readSlotOid(currentPipe)
			if err != nil {
				return err
			}
			if !found {
				logInfo(fmt.Sprintf("Slot %d: No more tables to back up", i))
				_ = readHandle.Close()
				deletePipe(currentPipe)
				break
			}
		}
		if i == 0 {
			pipeWriter, writeCmd, err = getBackupPipeWriter(&errBuf)
			if err != nil {
				logError(fmt.Sprintf("Oid %d: Error encountered getting backup pipe writer: %v", oid, err))
				return err
			}
		}

		logInfo(fmt.Sprintf("Oid %d: Backing up table with pipe %s", oid, currentPipe))
		numBytes, err := io.Copy(pipeWriter, reader)
		if err != nil {
			logError(fmt.Sprintf("Oid %d: Error encountered copying bytes from pipeWriter to reader: %v", oid, err))
			return errors.Wrap(err, strings.Trim(errBuf.String(), "\x00"))
		}
		logInfo(fmt.Sprintf("Oid %d: Read %d bytes\n", oid, numBytes))

		lastProcessed := lastRead + uint64(numBytes)
		tocfile.AddSegmentDataEntry(uint(oid), lastRead, lastProcessed)
		lastRead = lastProcessed

		_ = readHandle.Close()
		logInfo(fmt.Sprintf("Oid %d: Deleting pipe: %s\n", oid, currentPipe))
		deletePipe(currentPipe)
	}

	_ = pipeWriter.Close()
	if *pluginConfigFile != "" {
		/*
		 * When using a plugin, the agent may take longer to finish than the
		 * main gpbackup process. We either write the TOC file if the agent finishes
		 * successfully or write an error file if it has an error after the COPYs have
		 * finished. We then wait on the gpbackup side until one of those files is
		 * written to verify the agent completed.
		 */
		logVerbose("Uploading remaining data to plugin destination")
		err := writeCmd.Wait()
		if err != nil {
			logError(fmt.Sprintf("Error encountered writing either TOC file or error file: %v", err))
			return errors.Wrap(err, strings.Trim(errBuf.String(), "\x00"))
		}
	}
	err = tocfile.WriteToFileAndMakeReadOnly(*tocFile)
	if err != nil {
		// error logging handled in util.go
		return err
	}
	logVerbose("Finished writing segment TOC")
	return nil
}

/*
 * readSlotOid returns the oid of the table of a slot, which the COPY of the table
 * writes to the oid file of the slot before it opens the pipe, so the file is
 * complete once the pipe is open.  A slot without an oid file ends the data file.
 */
func readSlotOid(slotPipe string) (int, bool, error) {
	slotOidFile := utils.GetSlotOidFilename(slotPipe)
	if !utils.FileExists(slotOidFile) {
		return 0, false, nil
	}
	oidStr, err := operating.System.ReadFile(slotOidFile)
	if err != nil {
		logError(fmt.Sprintf("Error encountered reading oid file %s: %v", slotOidFile, err))
		return 0, false, err
	}
	oid, err := strconv.Atoi(strings.TrimSpace(string(oidStr)))
	if err != nil {
		logError(fmt.Sprintf("Error encountered parsing oid file %s: %v", slotOidFile, err))
		return 0, false, err
	}
	_ = utils.RemoveFileIfExists(slotOidFile)
	return oid, true, nil
}

func getBackupPipeReader(currentPipe string) (io.Reader, io.ReadCloser, error) {
	readHandle, err := os.OpenFile(currentPipe, os.O_RDONLY, os.ModeNamedPipe)
	if err != nil {
		// error logging handled by calling functions
		return nil, nil, err
	}
	// This is a workaround for https://github.com/golang/go/issues/24164.
	// Once this bug is fixed, the call to Fd() can be removed
	readHandle.Fd()
	reader := bufio.NewReader(readHandle)
	return reader, readHandle, nil
}

func getBackupPipeWriter(errBuf *bytes.Buffer) (pipe BackupPipeWriterCloser, writeCmd *exec.Cmd, err error) {
	var writeHandle io.WriteCloser
	if *pluginConfigFile != "" {
		writeCmd, writeHandle, err = startBackupPluginCommand(errBuf)
	} else {
		writeHandle, err = os.Create(*dataFile)
	}
	if err != nil {
		// error logging handled by calling functions
		return nil, nil, err
	}

	if *compressionLevel == 0 {
		pipe = NewCommonBackupPipeWriterCloser(writeHandle)
		return
	}

	if *compressionType == "gzip" {
		pipe, err = NewGZipBackupPipeWriterCloser(writeHandle, *compressionLevel)
		return
	}
	if *compressionType == "zstd" {
		pipe, err = NewZSTDBackupPipeWriterCloser(writeHandle, *compressionLevel)
		return
	}

	writeHandle.Close()
	// error logging handled by calling functions
	return nil, nil, fmt.Errorf("unknown compression type '%s' (compression level %d)", *compressionType, *compressionLevel)
}

func startBackupPluginCommand(errBuf *bytes.Buffer) (*exec.Cmd, io.WriteCloser, error) {
	pluginConfig, err := utils.ReadPluginConfig(*pluginConfigFile)
	if err != nil {
		// error logging handled by calling functions
		return nil, nil, err
	}
	cmdStr := fmt.Sprintf("%s backup_data %s %s", pluginConfig.ExecutablePath, pluginConfig.ConfigPath, *dataFile)
	writeCmd := exec.Command("bash", "-c", cmdStr)

	writeHandle, err := writeCmd.StdinPipe()
	if err != nil {
		// error logging handled by calling functions
		return nil, nil, err
	}
	writeCmd.Stderr = errBuf
	err = writeCmd.Start()
	if err != nil {
		// error logging handled by calling functions
		return nil, nil, err
	}
	return writeCmd, writeHandle, nil
}
