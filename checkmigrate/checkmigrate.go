package checkmigrate

import (
	"errors"
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
	cleanupOnce = sync.Once{}
	gplog.InitializeLogging("ggcheckmigrate", "")
	SetCmdFlags(cmd.Flags())
	utils.InitializeSignalHandler(DoCleanup, "checkmigrate process", &wasTerminated)
}

// This function handles argument parsing and validation, e.g. checking that a passed filename exists.
// It should only validate; initialization with any sort of side effects should go in DoInit or DoSetup.
func DoFlagValidation(cmd *cobra.Command) {
	ValidateFlagCombinations(cmd.Flags())
}

// This function handles setup that must be done after parsing flags.
func DoSetup() {
	SetLoggerVerbosity()
	gplog.Verbose("CheckMigrate Command: %s", os.Args)
	gplog.Info("ggcheckmigrate version = %s", GetVersion())

	CreateConnectionPool()
}

func DoCheckMigrate() {
	if sourceConnectionPool == nil {
		gplog.SetErrorCode(5)
		panic(errors.New("source connection is not initialized"))
	}

	databaseNames := []databaseNameResult{{DatabaseName: sourceConnectionPool.DBName}}
	if shouldScrapeDatabaseNames {
		gplog.Debug("Starting source database enumeration")
		databaseNames = make([]databaseNameResult, 0)
		if queryError := sourceConnectionPool.Select(&databaseNames, sourceDatabaseNamesQuery); queryError != nil {
			gplog.SetErrorCode(5)
			panic(queryError)
		}
		gplog.Debug("Completed source database enumeration with %d databases", len(databaseNames))
	}

	enumeratedDatabaseCount := len(databaseNames)
	checkedDatabaseCount := 0
	unreachableDatabaseCount := 0
	unavailableDatabaseCount := 0
	completedClusterCheckCount := 0
	failedClusterCheckCount := 0
	completedDatabaseCheckCount := 0
	failedDatabaseCheckCount := 0
	unavailableDatabaseCheckCount := 0
	findingCount := 0
	hasExecutionError := false

	clusterChecks := []migrationCheck{
		{name: "resource groups", doRunCheck: checkResourceGroups},
		{name: "incompatible storage options", doRunCheck: checkIncompatibleStorageOptions},
		{name: "removed GUC settings", doRunCheck: checkRemovedGUCSettings},
	}
	clusterSummary := runClusterChecks(sourceConnectionPool, clusterChecks)
	completedClusterCheckCount = clusterSummary.completedCheckCount
	failedClusterCheckCount = clusterSummary.failedCheckCount
	findingCount += clusterSummary.findingCount
	if clusterSummary.failedCheckCount > 0 {
		hasExecutionError = true
	}
	if clusterSummary.databaseError != nil {
		hasExecutionError = true
		gplog.Error("Cluster checks could not complete with %v", clusterSummary.databaseError)
	}

	for _, database := range databaseNames {
		gplog.Debug("Starting checks for database %q", database.DatabaseName)
		sourceConnection := sourceConnectionPool
		shouldCloseConnection := false
		if database.DatabaseName != sourceConnectionPool.DBName {
			sourceConnection = createDBConn(database.DatabaseName, sourceConnectionPool.User, sourceConnectionPool.Host, sourceConnectionPool.Port)
			if connectError := sourceConnection.Connect(1); connectError != nil {
				sourceConnection.Close()
				unreachableDatabaseCount++
				hasExecutionError = true
				gplog.Error("Database %q could not be checked because its connection failed with %v", database.DatabaseName, connectError)
				gplog.Debug("Completed checks for database %q with a connection failure", database.DatabaseName)

				continue
			}
			shouldCloseConnection = true
		}

		databaseSummary := runMigrationChecks(sourceConnection, targetConnectionPool)
		if shouldCloseConnection {
			sourceConnection.Close()
		}
		if databaseSummary.databaseError != nil {
			hasExecutionError = true
			gplog.Error("Database %q could not complete its checks with %v", database.DatabaseName, databaseSummary.databaseError)
		}
		if databaseSummary.completedCheckCount > 0 {
			checkedDatabaseCount++
		} else {
			unavailableDatabaseCount++
		}
		completedDatabaseCheckCount += databaseSummary.completedCheckCount
		failedDatabaseCheckCount += databaseSummary.failedCheckCount
		unavailableDatabaseCheckCount += databaseSummary.unavailableCheckCount
		findingCount += databaseSummary.findingCount
		if databaseSummary.failedCheckCount > 0 || databaseSummary.unavailableCheckCount > 0 {
			hasExecutionError = true
		}
		gplog.Debug("Completed checks for database %q with %d completed checks, %d failed checks, %d unavailable checks, and %d findings", database.DatabaseName, databaseSummary.completedCheckCount, databaseSummary.failedCheckCount, databaseSummary.unavailableCheckCount, databaseSummary.findingCount)
	}

	summaryShellVerbosity := gplog.LOGINFO
	if findingCount > 0 || hasExecutionError {
		summaryShellVerbosity = gplog.LOGERROR
	}
	gplog.Custom(gplog.LOGINFO, summaryShellVerbosity, "Summary contains %d enumerated databases, %d checked databases, %d unreachable databases, %d unavailable databases, %d completed cluster checks, %d failed cluster checks, %d completed database checks, %d failed database checks, %d unavailable database checks, and %d findings", enumeratedDatabaseCount, checkedDatabaseCount, unreachableDatabaseCount, unavailableDatabaseCount, completedClusterCheckCount, failedClusterCheckCount, completedDatabaseCheckCount, failedDatabaseCheckCount, unavailableDatabaseCheckCount, findingCount)

	if hasExecutionError {
		gplog.SetErrorCode(5)
	} else if findingCount > 0 {
		gplog.SetErrorCode(1)
	}
}

func DoTeardown() {
	didCheckmigrateFail := false
	defer func() {
		// If the checkmigrate was terminated, the signal handler will handle cleanup
		if wasTerminated.Load() {
			CleanupGroup.Wait()
		} else {
			DoCleanup(didCheckmigrateFail)
		}

		errorCode := gplog.GetErrorCode()
		if errorCode == 0 {
			gplog.Info("CheckMigrate completed successfully")
		}
		os.Exit(errorCode)

	}()

	errStr := ""
	if err := recover(); err != nil {
		errorCode := gplog.GetErrorCode()
		if errorCode < 2 || errorCode > 5 {
			gplog.Error(fmt.Sprintf("%v: %s", err, debug.Stack()))
			gplog.SetErrorCode(5)
		} else {
			errStr = fmt.Sprintf("%+v", err)
		}
		didCheckmigrateFail = true
	}
	if wasTerminated.Load() {
		// Don't print an error if the checkmigrate was canceled, as the signal handler
		// will take care of cleanup and return codes.  Just wait until the signal
		// handler's DoCleanup completes so the main goroutine doesn't exit while
		// cleanup is still in progress.
		CleanupGroup.Wait()
		didCheckmigrateFail = true
		return
	}
	if errStr != "" {
		fmt.Println(errStr)
	}
}

func DoCleanup(didCheckmigrateFail bool) {
	cleanupOnce.Do(func() {
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
	})
}
