//go:build ggcheckmigrate
// +build ggcheckmigrate

package main

import (
	"os"

	"github.com/GreengageDB/gp-common-go-libs/gplog"
	. "github.com/GreengageDB/gpbackup/checkmigrate"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/spf13/cobra"
)

func main() {
	var rootCmd = &cobra.Command{
		Use:     "ggcheckmigrate",
		Short:   "ggcheckmigrate is the utility to check migration ability for Greengage",
		Args:    cobra.NoArgs,
		Version: GetVersion(),
		Run: func(cmd *cobra.Command, args []string) {
			defer DoTeardown()
			DoFlagValidation(cmd)
			if setupError := DoSetup(); setupError != nil {
				gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%v", setupError)

				return
			}
			DoCheckMigrate()
		}}
	rootCmd.SetArgs(options.HandleSingleDashes(os.Args[1:]))
	DoInit(rootCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(4)
	}
}
