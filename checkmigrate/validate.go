package checkmigrate

import (
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/options"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"
)

func ValidateFlagCombinations(flags *pflag.FlagSet) {
	options.CheckExclusiveFlags(flags, options.DEBUG, options.QUIET, options.VERBOSE)
	if flags.Changed(options.TARGET_HOST) != flags.Changed(options.TARGET_PORT) {
		gplog.Fatal(errors.New("Both -H and -P options need to be provided to check target cluster"), "")
	}
}
