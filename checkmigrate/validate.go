package checkmigrate

import (
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/options"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"
)

func ValidateFlagCombinations(flags *pflag.FlagSet) {
	options.CheckExclusiveFlags(flags, options.DEBUG, options.QUIET, options.VERBOSE)

	if flags.Changed(options.SOURCE_PORT) {
		sourcePort := options.MustGetFlagInt(flags, options.SOURCE_PORT)
		if sourcePort < 1 || sourcePort > 65535 {
			gplog.Fatal(errors.New("The source port must be between 1 and 65535"), "")
		}
	}

	hasTargetHost := flags.Changed(options.TARGET_HOST)
	hasTargetPort := flags.Changed(options.TARGET_PORT)
	hasTargetUser := flags.Changed(options.TARGET_USER)
	if hasTargetHost != hasTargetPort || hasTargetUser && !hasTargetHost {
		gplog.Fatal(errors.New("Both -H and -P options need to be provided to check target cluster"), "")
	}
	if hasTargetHost {
		targetHost := options.MustGetFlagString(flags, options.TARGET_HOST)
		targetPort := options.MustGetFlagInt(flags, options.TARGET_PORT)
		if targetHost == "" {
			gplog.Fatal(errors.New("The target host must not be empty"), "")
		}
		if targetPort < 1 || targetPort > 65535 {
			gplog.Fatal(errors.New("The target port must be between 1 and 65535"), "")
		}
	}
}
