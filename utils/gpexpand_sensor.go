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
	BackupPreventedByGpexpandMessage = `Greengage expansion currently in process, please re-run gpbackup when the expansion has completed`

	RestorePreventedByGpexpandMessage = `Greengage expansion currently in process.  Once expansion is complete, it will be possible to restart gprestore, but please note existing backup sets taken with a different cluster configuration may no longer be compatible with the newly expanded cluster configuration`

	GpexpandTemporaryTableStatusQuery = `SELECT status FROM gpexpand.status ORDER BY updated DESC LIMIT 1`
	GpexpandStatusTableExistsQuery    = `select relname from pg_class JOIN pg_namespace on (pg_class.relnamespace = pg_namespace.oid)  where relname = 'status' and pg_namespace.nspname = 'gpexpand'`

	GpexpandStatusFilename = "gpexpand.status"
)

type GpexpandSensor struct {
	GGDBToolSensor
}

func CheckGpexpandRunning(errMsg string) {
	postgresConn := dbconn.NewDBConnFromEnvironment("postgres")
	postgresConn.MustConnect(1)
	defer postgresConn.Close()
	if postgresConn.Version.AtLeast("6") {
		gpexpandSensor := NewGpexpandSensor(vfs.OS(), postgresConn)
		isGpexpandRunning, err := gpexpandSensor.IsGpexpandRunning()
		gplog.FatalOnError(err)
		if isGpexpandRunning {
			gplog.Fatal(errors.New(errMsg), "")
		}
	}
}

func NewGpexpandSensor(myfs vfs.Filesystem, conn *dbconn.DBConn) GpexpandSensor {
	return GpexpandSensor{
		GGDBToolSensor: GGDBToolSensor{
			fs:           myfs,
			postgresConn: conn,
		},
	}
}

func (sensor GpexpandSensor) IsGpexpandRunning() (bool, error) {
	coordinatorDataDir, err := getCoordinatorDataDir(sensor.postgresConn, "gpexpand", "6")
	if err != nil {
		return false, err
	}

	_, err = sensor.fs.Stat(filepath.Join(coordinatorDataDir, GpexpandStatusFilename))
	// error has 3 possible states:
	if err == nil {
		// file exists, so gpexpand is running
		return true, nil
	}
	if os.IsNotExist(err) {
		// file not present means gpexpand is not in "phase 1".
		// now check whether the postgres database has evidence of a "phase 2" status for gpexpand,
		// by querying a temporary status table
		var tableName string
		tableName, err = dbconn.SelectString(sensor.postgresConn, GpexpandStatusTableExistsQuery)
		if err != nil {
			gplog.Error(fmt.Sprintf("Error encountered retrieving gpexpand status: %v", err))
			return false, err
		}
		if len(tableName) <= 0 {
			// table does not exist
			return false, nil
		}

		var status string
		status, err = dbconn.SelectString(sensor.postgresConn, GpexpandTemporaryTableStatusQuery)
		if err != nil {
			gplog.Error(fmt.Sprintf("Error encountered retrieving gpexpand status: %v", err))
			return false, err
		}

		// gpexpand should indicate being finished with either of 3 possible status messages:
		if status == "EXPANSION STOPPED" || // error case
			status == "EXPANSION COMPLETE" || // success case
			status == "SETUP DONE" { // only one phase completed case
			return false, nil
		}

		return true, nil
	}

	// Stat command returned a "real" error
	return false, err
}
