// +build ggcheckmigrate

package main

import (
	"os"

	"github.com/GreengageDB/gpbackup/options"
	. "github.com/GreengageDB/gpbackup/checkmigrate"
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
			DoSetup()
			DoCheckMigrate()
		}}
	rootCmd.SetArgs(options.HandleSingleDashes(os.Args[1:]))
	DoInit(rootCmd)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(2)
	}
}
