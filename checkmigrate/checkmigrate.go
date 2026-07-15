package checkmigrate

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"

	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/utils"

	"github.com/spf13/cobra"
)

// This function handles setup that can be done before parsing flags.
func DoInit(cmd *cobra.Command) {
	CleanupGroup = &sync.WaitGroup{}
	CleanupGroup.Add(1)
	gplog.InitializeLogging("ggcheckmigrate", "")
	utils.InitializeSignalHandler(DoCleanup, "checkmigrate process", &wasTerminated)
}

// This function handles setup that must be done after parsing flags.
func DoSetup() {
	// TODO: This one requires parsed flags
	//SetLoggerVerbosity()
	gplog.Verbose("CheckMigrate Command: %s", os.Args)
	gplog.Info("checkmigrate version = %s", GetVersion())

	// TODO: Use flags in CreateConnectionPool
	//CreateConnectionPool()
}

func DoCheckMigrate() {

}

func DoTeardown() {
	checkmigrateFailed := false
	defer func() {
		// If the checkmigrate was terminated, the signal handler will handle cleanup
		if wasTerminated.Load() {
			CleanupGroup.Wait()
		} else {
			DoCleanup(checkmigrateFailed)
		}

		errorCode := gplog.GetErrorCode()
		if errorCode == 0 {
			gplog.Info("CheckMigrate completed successfully")
		}
		os.Exit(errorCode)

	}()

	errStr := ""
	if err := recover(); err != nil {
		// Check if gplog.Fatal did not cause the panic
		if gplog.GetErrorCode() != 2 {
			gplog.Error(fmt.Sprintf("%v: %s", err, debug.Stack()))
			gplog.SetErrorCode(2)
		} else {
			errStr = fmt.Sprintf("%+v", err)
		}
		checkmigrateFailed = true
	}
	if wasTerminated.Load() {
		/*
		 * Don't print an error if the checkmigrate was canceled, as the signal handler
		 * will take care of cleanup and return codes.  Just wait until the signal
		 * handler's DoCleanup completes so the main goroutine doesn't exit while
		 * cleanup is still in progress.
		 */
		CleanupGroup.Wait()
		checkmigrateFailed = true
		return
	}
	if errStr != "" {
		fmt.Println(errStr)
	}
}

func DoCleanup(checkmigrateFailed bool) {
	defer func() {
		if err := recover(); err != nil {
			gplog.Warn("Encountered error during cleanup: %+v", err)
		}
		gplog.Info("Cleanup complete")
		CleanupGroup.Done()
	}()

	gplog.Info("Beginning cleanup")

	if sourceConnectionPool != nil {
		sourceConnectionPool.Close()
	}
	if targetConnectionPool != nil {
		targetConnectionPool.Close()
	}
}
