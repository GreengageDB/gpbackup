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
	"time"

	"github.com/GreengageDB/gp-common-go-libs/operating"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

/*
 * Backup specific functions
 */

func doBackupAgent() error {
	if *heartbeatTimeout > 0 {
		go watchHeartbeat(utils.GetHeartbeatFilename(*pipeFile), time.Duration(*heartbeatTimeout)*time.Second, func() {
			// The same shutdown as on the request of the cleanup of gpbackup, which removes the pipes and writes the error file
			_ = unix.Kill(os.Getpid(), unix.SIGUSR1)
		})
	}
	var lastRead uint64
	var (
		pipeWriter BackupPipeWriterCloser
		writeCmd   *exec.Cmd
	)
	tocfile := &toc.SegmentTOC{}
	tocfile.DataEntries = make(map[uint]toc.SegmentDataEntry)

	/*
	 * gpbackup chooses the data file of each table when its COPY starts, so the
	 * tables of this data file are not known in advance.  The pipes are named by
	 * slot, the position of the table in the data file, and each COPY writes the oid
	 * of its table to the oid file of its slot before it opens the pipe.  Once all
	 * tables are backed up, gpbackup opens and closes the next slot without an oid
	 * file, which ends the data file.
	 */
	var currentPipe string
	var errBuf bytes.Buffer
	/*
	 * It is important that we create the reader before creating the writer
	 * so that we establish a connection to the first pipe (created by gpbackup)
	 * and properly clean it up if an error occurs while creating the writer.
	 */
	for slot := 0; ; slot++ {
		currentPipe = fmt.Sprintf("%s_%d", *pipeFile, slot)
		if wasTerminated.Load() {
			logError("Terminated due to user request")
			return errors.New("Terminated due to user request")
		}
		nextPipeToCreate := fmt.Sprintf("%s_%d", *pipeFile, slot+*copyQueue)
		logVerbose(fmt.Sprintf("Slot %d: Creating pipe %s\n", slot+*copyQueue, nextPipeToCreate))
		err := createPipe(nextPipeToCreate)
		if err != nil {
			logError(fmt.Sprintf("Slot %d: Failed to create pipe %s\n", slot+*copyQueue, nextPipeToCreate))
			return err
		}

		logInfo(fmt.Sprintf("Slot %d: Opening pipe %s", slot, currentPipe))
		reader, readHandle, err := getBackupPipeReader(currentPipe)
		if err != nil {
			logError(fmt.Sprintf("Slot %d: Error encountered getting backup pipe reader: %v", slot, err))
			return err
		}
		oid, found, err := readSlotOid(currentPipe)
		if err != nil {
			return err
		}
		// A data file without tables is written as well, with its segment TOC
		if slot == 0 {
			pipeWriter, writeCmd, err = getBackupPipeWriter(&errBuf)
			if err != nil {
				logError(fmt.Sprintf("Slot %d: Error encountered getting backup pipe writer: %v", slot, err))
				return err
			}
		}
		if !found {
			logInfo(fmt.Sprintf("Slot %d: No more tables to back up", slot))
			_ = readHandle.Close()
			deletePipe(currentPipe)
			break
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
	err := tocfile.WriteToFileAndMakeReadOnly(*tocFile)
	if err != nil {
		// error logging handled in util.go
		return err
	}
	logVerbose("Finished writing segment TOC")
	return nil
}

/*
 * watchHeartbeat calls heartbeatLost once gpbackup has not touched the heartbeat
 * file for the given time, as when gpbackup has died without its cleanup, as with
 * SIGKILL: the helper would otherwise wait for good for the COPY of its next slot,
 * which no one runs.  gpbackup touches the file before it starts the helper, and a
 * missing file counts as touched when the helper started.
 */
func watchHeartbeat(heartbeatFile string, timeout time.Duration, heartbeatLost func()) {
	started := time.Now()
	ticker := time.NewTicker(timeout / 10)
	defer ticker.Stop()
	for range ticker.C {
		lastTouched := started
		if info, err := os.Stat(heartbeatFile); err == nil && info.ModTime().After(lastTouched) {
			lastTouched = info.ModTime()
		}
		if time.Since(lastTouched) > timeout {
			logError(fmt.Sprintf("gpbackup has not touched the heartbeat file %s for %v, so it has stopped: ending", heartbeatFile, timeout))
			heartbeatLost()
			return
		}
	}
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
