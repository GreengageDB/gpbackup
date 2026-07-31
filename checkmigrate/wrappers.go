package checkmigrate

import (
	"os"
	"strconv"

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

// We kind of replicate NewDBConnFromEnvironment(), but as we have
// host/port/user flags we need to do their checking and assignment here.
func CreateConnectionPool() {
	sourceHost := options.MustGetFlagString(cmdFlags, options.SOURCE_HOST)
	sourcePort := options.MustGetFlagInt(cmdFlags, options.SOURCE_PORT)
	sourceDb := options.MustGetFlagString(cmdFlags, options.SOURCE_DATABASE)
	sourceUser := options.MustGetFlagString(cmdFlags, options.SOURCE_USER)

	//If port is not passed, grab PGPORT (if defined), else use 5432
	if sourcePort == 0 {
		sourcePort = 5432
		if envPort := os.Getenv("PGPORT"); envPort != "" {
			envPortInt, conversionError := strconv.Atoi(envPort)
			if conversionError == nil {
				sourcePort = envPortInt
			}
		}
	}

	// We will need to check for all DBs if sourceDB not specified,
	// but use postgres for initial connection.
	scrapeDbNames = false
	if sourceDb == "" {
		sourceDb = "postgres"
		scrapeDbNames = true
	}

	// Try using PGUSER first, then USER
	if sourceUser == "" {
		sourceUser = os.Getenv("USER")
		if envUser := os.Getenv("PGUSER"); envUser != "" {
			sourceUser = envUser
		}
	}

	sourceConnectionPool = createDBConn(sourceDb, sourceUser, sourceHost, sourcePort)
	if connectionError := sourceConnectionPool.Connect(1); connectionError != nil {
		gplog.SetErrorCode(5)
		panic(connectionError)
	}
	if !sourceConnectionPool.Version.Is("6") {
		gplog.SetErrorCode(2)
		panic(errors.New("This utility can only check for migrate from Greengage version 6"))
	}

	targetHost := options.MustGetFlagString(cmdFlags, options.TARGET_HOST)
	targetPort := options.MustGetFlagInt(cmdFlags, options.TARGET_PORT)
	targetDb := "postgres"
	targetUser := options.MustGetFlagString(cmdFlags, options.TARGET_USER)

	if targetUser == "" {
		targetUser = os.Getenv("USER")
		if envUser := os.Getenv("PGUSER"); envUser != "" {
			targetUser = envUser
		}
	}

	if targetHost != "" && targetPort != 0 {
		targetConnectionPool = createDBConn(targetDb, targetUser, targetHost, targetPort)
		if connectionError := targetConnectionPool.Connect(1); connectionError != nil {
			gplog.SetErrorCode(5)
			panic(connectionError)
		}
		if !targetConnectionPool.Version.Is("7") {
			gplog.SetErrorCode(3)
			panic(errors.New("This utility can only check for migrate from Greengage version 6 to Greengage version 7"))
		}
	}
}
