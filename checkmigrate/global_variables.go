package checkmigrate

import (
	"sync"
	"sync/atomic"

	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gpbackup/options"

	"github.com/spf13/pflag"
)

/*
 * Non-flag variables
 */
var (
	sourceConnectionPool *dbconn.DBConn
	targetConnectionPool *dbconn.DBConn
	version              string
	wasTerminated        atomic.Bool
	cleanupOnce          sync.Once

	/*
	 * Used for synchronizing DoCleanup.  In DoInit() we increment the group
	 * and then wait for at least one DoCleanup to finish, either in DoTeardown
	 * or the signal handler.
	 */
	CleanupGroup *sync.WaitGroup
)

/*
 * Command-line flags
 */
var cmdFlags *pflag.FlagSet

func SetCmdFlags(flagSet *pflag.FlagSet) {
	cmdFlags = flagSet
}

func GetVersion() string {
	return version
}

func MustGetFlagBool(flagName string) bool {
	return options.MustGetFlagBool(cmdFlags, flagName)
}
