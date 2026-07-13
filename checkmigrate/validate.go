package ggcheckmigrate

import (
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/GreengageDB/gpbackup/utils"

	"github.com/spf13/pflag"
)

func ValidateFlagCombinations(flags *pflag.FlagSet) { }

func ValidateFlagValues() {
	err := utils.ValidateFullPath(MustGetFlagString(options.SOURCE_NO_PASSWORD))
	gplog.FatalOnError(err)
	err = utils.ValidateFullPath(MustGetFlagString(options.TARGET_NO_PASSWORD))
	gplog.FatalOnError(err)
}
