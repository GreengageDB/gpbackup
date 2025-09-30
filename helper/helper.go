package helper

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gp-common-go-libs/operating"
	"github.com/GreengageDB/gpbackup/utils"
)

/*
 * Non-flag variables
 */

var (
	CleanupGroup  *sync.WaitGroup
	version       string
	wasTerminated atomic.Bool
	wasSigpiped   atomic.Bool
)

/*
 * Command-line flags
 */
var (
	backupAgent      *bool
	compressionLevel *int
	compressionType  *string
	content          *int
	dataFile         *string
	oidFile          *string
	onErrorContinue  *bool
	pipeFile         *string
	pluginConfigFile *string
	printVersion     *bool
	restoreAgent     *bool
	tocFile          *string
	isFiltered       *bool
	copyQueue        *int
	singleDataFile   *bool
	isResizeRestore  *bool
	origSize         *int
	destSize         *int
	verbosity        *int
)

func DoHelper() {
	var err error
	defer func() {
		if wasTerminated.Load() {
			CleanupGroup.Wait()
			return
		}
		DoCleanup()
		os.Exit(gplog.GetErrorCode())
	}()

	InitializeGlobals()
	go InitializeSignalHandler()

	if *backupAgent {
		err = doBackupAgent()
	} else if *restoreAgent {
		err = doRestoreAgent()
	}
	if err != nil {
		// error logging handled in doBackupAgent and doRestoreAgent
		errFile := utils.GetErrorFilename(*pipeFile)
		gplog.Debug("Writing error file %s", errFile)
		handle, err := utils.OpenFileForWrite(errFile)
		if err != nil {
			logVerbose("Encountered error creating error file: %v", err)
		}
		err = handle.Close()
		if err != nil {
			logVerbose("Encountered error closing error file: %v", err)
		}
	}
}

func InitializeGlobals() {
	CleanupGroup = &sync.WaitGroup{}
	CleanupGroup.Add(1)

	backupAgent = flag.Bool("backup-agent", false, "Use gpbackup_helper as an agent for backup")
	content = flag.Int("content", -2, "Content ID of the corresponding segment")
	compressionLevel = flag.Int("compression-level", 0, "The level of compression. O indicates no compression. Range of valid values depends on compression type")
	compressionType = flag.String("compression-type", "gzip", "The type of compression. Valid values are 'gzip' and 'zstd'")
	dataFile = flag.String("data-file", "", "Absolute path to the data file")
	oidFile = flag.String("oid-file", "", "Absolute path to the file containing a list of oids to restore")
	onErrorContinue = flag.Bool("on-error-continue", false, "Continue restore even when encountering an error")
	pipeFile = flag.String("pipe-file", "", "Absolute path to the pipe file")
	pluginConfigFile = flag.String("plugin-config", "", "The configuration file to use for a plugin")
	printVersion = flag.Bool("version", false, "Print version number and exit")
	restoreAgent = flag.Bool("restore-agent", false, "Use gpbackup_helper as an agent for restore")
	tocFile = flag.String("toc-file", "", "Absolute path to the table of contents file")
	isFiltered = flag.Bool("with-filters", false, "Used with table/schema filters")
	copyQueue = flag.Int("copy-queue-size", 1, "Used to know how many COPIES are being queued up")
	singleDataFile = flag.Bool("single-data-file", false, "Used with single data file restore.")
	isResizeRestore = flag.Bool("resize-cluster", false, "Used with resize cluster restore.")
	origSize = flag.Int("orig-seg-count", 0, "Used with resize restore.  Gives the segment count of the backup.")
	destSize = flag.Int("dest-seg-count", 0, "Used with resize restore.  Gives the segment count of the current cluster.")
	verbosity = flag.Int("verbosity", gplog.LOGINFO, "Log file verbosity")

	flag.Parse()
	if *printVersion {
		fmt.Printf("gpbackup_helper version %s\n", version)
		os.Exit(0)
	}
	if *onErrorContinue && !*restoreAgent {
		fmt.Printf("--on-error-continue flag can only be used with --restore-agent flag")
		os.Exit(1)
	}
	if (*origSize > 0 && *destSize == 0) || (*destSize > 0 && *origSize == 0) {
		fmt.Printf("Both --orig-seg-count and --dest-seg-count must be used during a resize restore")
		os.Exit(1)
	}
	operating.InitializeSystemFunctions()

	gplog.InitializeLogging("gpbackup_helper", "")
	gplog.SetLogFileVerbosity(*verbosity)
}

func InitializeSignalHandler() {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, unix.SIGINT, unix.SIGTERM, unix.SIGPIPE, unix.SIGUSR1)
	terminatedChan := make(chan bool, 1)
	for {
		go func() {
			sig := <-signalChan
			fmt.Println() // Add newline after "^C" is printed
			switch sig {
			case unix.SIGINT:
				gplog.Warn("Received an interrupt signal on segment %d: aborting", *content)
				terminatedChan <- true
			case unix.SIGTERM:
				gplog.Warn("Received a termination signal on segment %d: aborting", *content)
				terminatedChan <- true
			case unix.SIGPIPE:
				wasSigpiped.Store(true)
				if *onErrorContinue {
					gplog.Warn("Received a broken pipe signal on segment %d: on-error-continue set, continuing", *content)
					terminatedChan <- false
				} else {
					gplog.Warn("Received a broken pipe signal on segment %d: aborting", *content)
					terminatedChan <- true
				}
			case unix.SIGUSR1:
				gplog.Warn("Received shutdown request on segment %d: beginning cleanup", *content)
				terminatedChan <- true
			}
		}()
		wasTerminated.Store(<-terminatedChan)
		if wasTerminated.Load() {
			DoCleanup()
			os.Exit(2)
		} else {
			continue
		}
	}
}

/*
 * Shared functions
 */

func createPipe(pipe string) error {
	err := unix.Mkfifo(pipe, 0700)
	if err != nil {
		return err
	}

	return nil
}

func deletePipe(pipe string) error {
	err := utils.RemoveFileIfExists(pipe)
	if err != nil {
		return err
	}

	return nil
}

func openClosePipe(filename string) error {
	flag := unix.O_NONBLOCK
	if *backupAgent {
		flag |= os.O_RDONLY
	} else if *restoreAgent {
		flag |= os.O_WRONLY
	}
	handle, err := os.OpenFile(filename, flag, os.ModeNamedPipe)
	if err != nil {
		return err
	}
	err = handle.Close()
	if err != nil {
		return err
	}
	return nil
}

func getOidListFromFile(oidFileName string) ([]int, error) {
	oidStr, err := operating.System.ReadFile(oidFileName)
	if err != nil {
		logError(fmt.Sprintf("Error encountered reading oid list from file: %v", err))
		return nil, err
	}
	oidStrList := strings.Split(strings.TrimSpace(fmt.Sprintf("%s", oidStr)), "\n")
	oidList := make([]int, len(oidStrList))
	for i, oid := range oidStrList {
		num, _ := strconv.Atoi(oid)
		oidList[i] = num
	}
	return oidList, nil
}

/*
 * Shared helper functions
 */

func DoCleanup() {
	defer CleanupGroup.Done()
	if wasTerminated.Load() {
		/*
		 * If the agent dies during the last table copy, it can still report
		 * success, so we create an error file and check for its presence in
		 * gprestore after the COPYs are finished.
		 */
		errFile := utils.GetErrorFilename(*pipeFile)
		gplog.Debug("Writing error file %s", errFile)
		handle, err := utils.OpenFileForWrite(errFile)
		if err != nil {
			logVerbose("Encountered error creating error file: %v", err)
		}
		err = handle.Close()
		if err != nil {
			logVerbose("Encountered error closing error file: %v", err)
		}
	}

	pipeFiles, _ := filepath.Glob(fmt.Sprintf("%s_[0-9]*", *pipeFile))
	for _, pipeName := range pipeFiles {
		if !wasSigpiped.Load() {
			/*
			 * The main process doesn't know about the error yet, so it needs to
			 * open/close pipes so that the COPY commands hanging on them can complete.
			 */
			logVerbose("Opening/closing pipe %s", pipeName)
			err := openClosePipe(pipeName)
			if err != nil {
				logVerbose("Encountered error opening/closing pipe %s: %v", pipeName, err)
			}
		}
		logVerbose("Removing pipe %s", pipeName)
		err := deletePipe(pipeName)
		if err != nil {
			logVerbose("Encountered error removing pipe %s: %v", pipeName, err)
		}
	}

	skipFiles, _ := filepath.Glob(utils.GetSkipFilename(*pipeFile) + "*")
	for _, skipFile := range skipFiles {
		err := utils.RemoveFileIfExists(skipFile)
		if err != nil {
			logVerbose("Encountered error during cleanup skip files: %v", err)
		}
	}
	logVerbose("Cleanup complete")
}

func logInfo(s string, v ...interface{}) {
	s = fmt.Sprintf("Segment %d: %s", *content, s)
	gplog.Info(s, v...)
}

func logWarn(s string, v ...interface{}) {
	s = fmt.Sprintf("Segment %d: %s", *content, s)
	gplog.Warn(s, v...)
}

func logVerbose(s string, v ...interface{}) {
	s = fmt.Sprintf("Segment %d: %s", *content, s)
	gplog.Verbose(s, v...)
}

func logError(s string, v ...interface{}) {
	s = fmt.Sprintf("Segment %d: %s", *content, s)
	gplog.Error(s, v...)
}
