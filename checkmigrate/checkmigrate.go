package checkmigrate

import (
	"github.com/GreengageDB/gp-common-go-libs/gplog"

	"github.com/spf13/cobra"
)

func DoInit(cmd *cobra.Command) {
	gplog.InitializeLogging("ggcheckmigrate", "")
	SetCmdFlags(cmd.Flags());
}

/*
 * This function handles argument parsing and validation, e.g. checking that a passed filename exists.
 * It should only validate; initialization with any sort of side effects should go in DoInit or DoSetup.
 */
func DoFlagValidation(cmd *cobra.Command) {
	ValidateFlagCombinations(cmd.Flags());
	ValidateFlagValues();
}

func DoSetup() {
	gplog.Info("ggcheckmigrate version = %s", GetVersion())
}

func DoCheckMigrate() {

}

func DoTeardown() {
}
