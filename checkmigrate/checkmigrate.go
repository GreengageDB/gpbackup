package checkmigrate

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
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
	defer func() {
		if recoveredValue := recover(); recoveredValue != nil {
			gplog.SetErrorCode(4)
			panic(recoveredValue)
		}
	}()

	ValidateFlagCombinations(cmd.Flags())
}

// This function handles setup that must be done after parsing flags.
func DoSetup() error {
	SetLoggerVerbosity()
	gplog.Verbose("CheckMigrate Command: %s", os.Args)
	gplog.Info("ggcheckmigrate version = %s", GetVersion())

	return CreateConnections()
}

func DoCheckMigrate() {
	if bootstrapSourceConnection == nil {
		gplog.SetErrorCode(5)
		panic(errors.New("source connection is not initialized"))
	}

	databaseNames := []databaseNameResult{{DatabaseName: bootstrapSourceConnection.DBName}}
	if shouldScrapeDatabaseNames {
		gplog.Debug("Starting source database enumeration")
		databaseNames = make([]databaseNameResult, 0)
		if queryError := bootstrapSourceConnection.Select(&databaseNames, sourceDatabaseNamesQuery); queryError != nil {
			gplog.Error("Source database enumeration failed with %v", queryError)
			gplog.SetErrorCode(5)

			return
		}
		if len(databaseNames) == 0 {
			databaseNames = append(
				databaseNames,
				databaseNameResult{DatabaseName: bootstrapSourceConnection.DBName},
			)
			gplog.Warn(
				"Source database enumeration returned no rows. Database %q will be checked.",
				bootstrapSourceConnection.DBName,
			)
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

	clusterSummary, clusterExecutionError := runClusterChecks(bootstrapSourceConnection, clusterChecks)
	completedClusterCheckCount = clusterSummary.completedCheckCount
	failedClusterCheckCount = clusterSummary.failedCheckCount
	findingCount += clusterSummary.findingCount
	if clusterExecutionError != nil {
		hasExecutionError = true
		gplog.Error("Cluster checks could not complete with %v", clusterExecutionError)
	}

	for _, database := range databaseNames {
		gplog.Debug("Starting checks for database %q", database.DatabaseName)
		databaseConnection := bootstrapSourceConnection
		if database.DatabaseName != bootstrapSourceConnection.DBName {
			databaseConnection = createDBConn(
				database.DatabaseName,
				bootstrapSourceConnection.User,
				bootstrapSourceConnection.Host,
				bootstrapSourceConnection.Port,
			)
			if connectError := databaseConnection.Connect(1); connectError != nil {
				databaseConnection.Close()
				unreachableDatabaseCount++
				hasExecutionError = true
				gplog.Error(
					"Database %q could not be checked because its connection failed with %v",
					database.DatabaseName,
					connectError,
				)
				gplog.Debug(
					"Completed checks for database %q with a connection failure",
					database.DatabaseName,
				)

				continue
			}
		}

		databaseSummary, databaseExecutionError := runMigrationChecks(databaseConnection, targetConnection)
		if database.DatabaseName != bootstrapSourceConnection.DBName {
			databaseConnection.Close()
		}
		if databaseExecutionError != nil {
			hasExecutionError = true
			gplog.Error(
				"Database %q could not complete its checks with %v",
				database.DatabaseName,
				databaseExecutionError,
			)
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
		gplog.Debug(
			"Completed checks for database %q with %d completed checks, %d failed checks, "+
				"%d unavailable checks, and %d findings",
			database.DatabaseName,
			databaseSummary.completedCheckCount,
			databaseSummary.failedCheckCount,
			databaseSummary.unavailableCheckCount,
			databaseSummary.findingCount,
		)
	}

	hasCheckResultIssue := failedClusterCheckCount > 0 ||
		failedDatabaseCheckCount > 0 ||
		unavailableDatabaseCheckCount > 0
	summaryShellVerbosity := gplog.LOGINFO
	if findingCount > 0 || hasCheckResultIssue || hasExecutionError {
		summaryShellVerbosity = gplog.LOGERROR
	}
	summaryRows := []struct {
		label string
		count int
	}{
		{label: "enumerated databases:", count: enumeratedDatabaseCount},
		{label: "checked databases:", count: checkedDatabaseCount},
		{label: "unreachable databases:", count: unreachableDatabaseCount},
		{label: "unavailable databases:", count: unavailableDatabaseCount},
		{label: "completed cluster checks:", count: completedClusterCheckCount},
		{label: "failed cluster checks:", count: failedClusterCheckCount},
		{label: "completed database checks:", count: completedDatabaseCheckCount},
		{label: "failed database checks:", count: failedDatabaseCheckCount},
		{label: "unavailable database checks:", count: unavailableDatabaseCheckCount},
		{label: "findings:", count: findingCount},
	}
	var summaryOutput strings.Builder
	summaryOutput.WriteString("Execution summary:")
	for _, row := range summaryRows {
		fmt.Fprintf(&summaryOutput, "\n  %-31s%5d", row.label, row.count)
	}
	gplog.Custom(
		gplog.LOGINFO,
		summaryShellVerbosity,
		"%s",
		summaryOutput.String(),
	)

	if hasExecutionError {
		gplog.SetErrorCode(5)
	} else if findingCount > 0 || hasCheckResultIssue {
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
			gplog.Info("CheckMigrate completed successfully with exit code %d", errorCode)
		} else {
			gplog.Info("CheckMigrate completed with exit code %d", errorCode)
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
			errStr = fmt.Sprintf("%v", err)
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

		if bootstrapSourceConnection != nil {
			bootstrapSourceConnection.Close()
		}
		if targetConnection != nil {
			targetConnection.Close()
		}
	})
}
