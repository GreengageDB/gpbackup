package checkmigrate

import (
	"sync"
	"sync/atomic"

	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gpbackup/options"

	"github.com/spf13/pflag"
)

// Non-flag variables
var (
	sourceConnectionPool *dbconn.DBConn
	targetConnectionPool *dbconn.DBConn
	version              string
	wasTerminated        atomic.Bool
	cleanupOnce          sync.Once

	scrapeDbNames		 bool

	// Used for mocking purposes
	createDBConn = dbconn.NewDBConn

	// Used for synchronizing DoCleanup.  In DoInit() we increment the group
	// and then wait for at least one DoCleanup to finish, either in DoTeardown
	// or the signal handler.
	CleanupGroup *sync.WaitGroup
)

// Command-line flags
var cmdFlags *pflag.FlagSet

// Setter functions
func SetCmdFlags(flagSet *pflag.FlagSet) {
	cmdFlags = flagSet
	options.SetCheckMigrateFlagDefaults(cmdFlags)
}

func SetVersion(v string) {
	version = v
}

func SetCreateDBConn(f func(dbName, username, host string, port int) *dbconn.DBConn) {
	createDBConn = f
}

// Getter functions
func GetVersion() string {
	return version
}

func GetSourceConnectionPool() *dbconn.DBConn {
	return sourceConnectionPool
}

func GetTargetConnectionPool() *dbconn.DBConn {
	return targetConnectionPool
}

func GetScrapeDbNames() bool {
	return scrapeDbNames
}

// ResetGlobalState() clears the global variables before each test
func ResetGlobalState() {
	sourceConnectionPool = nil
	targetConnectionPool = nil
	scrapeDbNames = false
}

// Util functions to enable ease of access to global flag values
func FlagChanged(flagName string) bool {
	return cmdFlags.Changed(flagName)
}

func MustGetFlagBool(flagName string) bool {
	return options.MustGetFlagBool(cmdFlags, flagName)
}

func MustGetFlagString(flagName string) string {
	return options.MustGetFlagString(cmdFlags, flagName)
}
