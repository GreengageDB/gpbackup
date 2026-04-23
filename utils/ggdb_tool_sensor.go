package utils

import (
	"fmt"

	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/blang/vfs"
	"github.com/pkg/errors"
)

const (
	CoordinatorDataDirQuery = `select datadir from gp_segment_configuration where content=-1 and role='p'`
)

type GGDBToolSensor struct {
	fs           vfs.Filesystem
	postgresConn *dbconn.DBConn
}

func getCoordinatorDataDir(conn *dbconn.DBConn, sensorName string, minGreengageVersion string) (string, error) {
	err := validateConnection(conn, sensorName, minGreengageVersion)
	if err != nil {
		gplog.Error(fmt.Sprintf("Error encountered validating db connection: %v", err))
		return "", err
	}
	coordinatorDataDir, err := dbconn.SelectString(conn, CoordinatorDataDirQuery)
	if err != nil {
		gplog.Error(fmt.Sprintf("Error encountered retrieving data directory: %v", err))
		return "", err
	}
	return coordinatorDataDir, nil
}

func validateConnection(conn *dbconn.DBConn, sensorName string, minGreengageVersion string) error {
	if conn.DBName != "postgres" {
		return errors.New(sensorName + " sensor requires a connection to the postgres database")
	}
	if conn.Version.Before(minGreengageVersion) {
		return errors.New(sensorName + " sensor requires a connection to Greengage version >= " + minGreengageVersion)
	}
	return nil
}
