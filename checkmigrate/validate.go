package checkmigrate

import (
	"strconv"

	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gp-common-go-libs/operating"
	"github.com/GreengageDB/gpbackup/options"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"
)

func ValidateFlagCombinations(flags *pflag.FlagSet) {
	options.CheckExclusiveFlags(flags, options.DEBUG, options.QUIET, options.VERBOSE)

	sourcePort := options.MustGetFlagInt(flags, options.SOURCE_PORT)
	if !flags.Changed(options.SOURCE_PORT) {
		if environmentPort := operating.System.Getenv("PGPORT"); environmentPort != "" {
			parsedPort, conversionError := strconv.Atoi(environmentPort)
			if conversionError != nil {
				gplog.Fatal(errors.New("PGPORT must be an integer between 1 and 65535"), "")
			}
			sourcePort = parsedPort
		}
	}
	if sourcePort < 1 || sourcePort > 65535 {
		gplog.Fatal(errors.New("the source port must be between 1 and 65535"), "")
	}

	hasTargetHost := flags.Changed(options.TARGET_HOST)
	hasTargetPort := flags.Changed(options.TARGET_PORT)
	hasTargetUser := flags.Changed(options.TARGET_USER)
	if hasTargetHost != hasTargetPort || (hasTargetUser && !hasTargetHost) {
		gplog.Fatal(errors.New("both -H and -P options must be provided to check the target cluster"), "")
	}
	if hasTargetHost {
		targetHost := options.MustGetFlagString(flags, options.TARGET_HOST)
		targetPort := options.MustGetFlagInt(flags, options.TARGET_PORT)
		if targetHost == "" {
			gplog.Fatal(errors.New("the target host must not be empty"), "")
		}
		if targetPort < 1 || targetPort > 65535 {
			gplog.Fatal(errors.New("the target port must be between 1 and 65535"), "")
		}
	}
}
