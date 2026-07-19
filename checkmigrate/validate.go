package checkmigrate

import (
	"github.com/GreengageDB/gpbackup/options"

	"github.com/spf13/pflag"
)

func ValidateFlagCombinations(flags *pflag.FlagSet) {
	options.CheckExclusiveFlags(flags, options.DEBUG, options.QUIET, options.VERBOSE)
}
