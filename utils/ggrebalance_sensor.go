package utils

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/blang/vfs"
	"github.com/pkg/errors"
)

const (
	BackupPreventedByGgrebalanceMessage  GgrebalanceFailureMessage = `Greengage rebalance currently in process, please re-run gpbackup when the rebalance has completed`
	RestorePreventedByGgrebalanceMessage GgrebalanceFailureMessage = `Greengage rebalance currently in process.  Once rebalance is complete, it will be possible to restart gprestore, but please note existing backup sets taken with a different cluster configuration may no longer be compatible with the newly rebalanced cluster configuration`

	GgrebalanceCheckSchemaQuery    = "SELECT COUNT(1) AS rebalance_schema_exists FROM pg_namespace WHERE nspname = 'ggrebalance'"
	GgrebalanceGetLatestStateQuery = "SELECT state FROM ggrebalance.rebalance_status WHERE state_category = 'MAIN' ORDER BY updated DESC LIMIT 1"

	GgrebalancePidFilename = "ggrebalance.pid"
)

type GgrebalanceSensor struct {
	GGDBToolSensor
}

type GgrebalanceFailureMessage string

func CheckGgrebalanceRunning(errMsg GgrebalanceFailureMessage) {
	postgresConn := dbconn.NewDBConnFromEnvironment("postgres")
	postgresConn.MustConnect(1)
	defer postgresConn.Close()
	if postgresConn.Version.AtLeast("7") {
		ggrebalanceSensor := NewGgrebalanceSensor(vfs.OS(), postgresConn)
		isGgrebalanceRunning, err := ggrebalanceSensor.IsGgrebalanceRunning()
		gplog.FatalOnError(err)
		if isGgrebalanceRunning {
			gplog.Fatal(errors.New(string(errMsg)), "")
		}
	}
}

func NewGgrebalanceSensor(myfs vfs.Filesystem, conn *dbconn.DBConn) GgrebalanceSensor {
	return GgrebalanceSensor{
		GGDBToolSensor: GGDBToolSensor{
			fs:           myfs,
			postgresConn: conn,
		},
	}
}

func (sensor GgrebalanceSensor) IsGgrebalanceRunning() (bool, error) {
	coordinatorDataDir, err := getCoordinatorDataDir(sensor.postgresConn, "ggrebalance", "7")
	if err != nil {
		return false, err
	}

	_, err = sensor.fs.Stat(filepath.Join(coordinatorDataDir, GgrebalancePidFilename))
	if err == nil {
		// file exists, so ggrebalance is running
		return true, nil
	}
	if os.IsNotExist(err) {
		// File not present means ggrebalance could be interrupted,
		// and the cluster was left somewhere in the middle of rebalance.
		// check ggrebalance schema
		schemaCheck, err := dbconn.SelectInt(sensor.postgresConn, GgrebalanceCheckSchemaQuery)
		if err != nil {
			gplog.Error(fmt.Sprintf("Error encountered retrieving ggrebalance schema status: %v", err))
			return false, err
		}
		if schemaCheck <= 0 {
			// ggrebalance schema does not exist
			return false, nil
		}

		// and check latest state
		latestState, err := dbconn.SelectString(sensor.postgresConn, GgrebalanceGetLatestStateQuery)
		if err != nil {
			gplog.Error(fmt.Sprintf("Error encountered retrieving ggrebalance state status: %v", err))
			return false, err
		}
		if latestState == "STATE_EXECUTOR_DONE" {
			// latest state is the final one - ggrebalance has completed all operations
			return false, nil
		}

		return true, nil
	}

	return false, err
}
