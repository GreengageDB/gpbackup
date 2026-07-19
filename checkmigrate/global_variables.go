package checkmigrate

import (
	"github.com/GreengageDB/gpbackup/options"
	"github.com/spf13/pflag"
)

/*
 * Non-flag variables
 */
var (
	version              	string
)


/*
 * Command-line flags
 */
var cmdFlags *pflag.FlagSet

/*
 * Setter functions
 */
func SetCmdFlags(flagSet *pflag.FlagSet) {
	cmdFlags = flagSet
	options.SetCheckMigrateFlagDefaults(cmdFlags)
}

func SetVersion(v string) {
	version = v
}

/*
 * Getter functions
 */
func GetVersion() string {
	return version
}

/*
 * Util functions to enable ease of access to global flag values
 */
func FlagChanged(flagName string) bool {
	return cmdFlags.Changed(flagName)
}

func MustGetFlagBool(flagName string) bool {
	return options.MustGetFlagBool(cmdFlags, flagName)
}

func MustGetFlagString(flagName string) string {
	return options.MustGetFlagString(cmdFlags, flagName)
}
