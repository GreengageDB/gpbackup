package checkmigrate

import (
	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/options"

	"github.com/pkg/errors"
)

// This file contains wrapper functions that group together functions relating
// to querying and printing metadata, so that the logic for each object type
// can all be in one place and backup.go can serve as a high-level look at the
// overall backup flow.

// Setup and validation wrapper functions

func SetLoggerVerbosity() {
	gplog.SetLogFileVerbosity(gplog.LOGINFO)
	if MustGetFlagBool(options.QUIET) {
		gplog.SetVerbosity(gplog.LOGERROR)
		gplog.SetLogFileVerbosity(gplog.LOGERROR)
	} else if MustGetFlagBool(options.DEBUG) {
		gplog.SetVerbosity(gplog.LOGDEBUG)
		gplog.SetLogFileVerbosity(gplog.LOGDEBUG)
	} else if MustGetFlagBool(options.VERBOSE) {
		gplog.SetVerbosity(gplog.LOGVERBOSE)
		gplog.SetLogFileVerbosity(gplog.LOGVERBOSE)
	}
}

// TODO: Handle source and target flags here and pass them to NewDBConn
//
// Also, there seem to be no Conn() function to pass a password to it,
// Might need to be written.
func CreateConnectionPool() {
	sourceConnectionPool = dbconn.NewDBConn("postgres", "gpadmin", "/tmp", 6000)
	err := sourceConnectionPool.Connect(1)
	if err != nil {
		gplog.SetErrorCode(5)
		panic(err)
	}
	if !sourceConnectionPool.Version.Is("6") {
		gplog.SetErrorCode(2)
		panic(errors.Errorf(`Source GPDB version %s is not supported. Utility is used only on GPDB %s as source.`, sourceConnectionPool.Version.VersionString, "6"))
	}

	// TODO: Handle this, only if target-host flag is defined
	targetConnectionPool = dbconn.NewDBConn("postgres", "gpadmin", "/tmp", 7000)
	err = targetConnectionPool.Connect(1)
	if err != nil {
		gplog.SetErrorCode(5)
		panic(err)
	}
	if !targetConnectionPool.Version.Is("7") {
		gplog.SetErrorCode(3)
		panic(errors.Errorf(`Target GPDB version %s is not supported. Utility is used only on GPDB %s as target.`, targetConnectionPool.Version.VersionString, "7"))
	}
}
