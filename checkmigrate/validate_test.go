package checkmigrate_test

import (
	"strings"

	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/checkmigrate"
	"github.com/spf13/cobra"

	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("checkmigrate/validate tests", func() {
	BeforeEach(func() {
		_, _, _ = testhelper.SetupTestLogger()
	})
	Describe("Validate various flag values", func() {
		DescribeTable("Validate various flag values",
			func(argString string, valid bool) {
				testCmd := &cobra.Command{
					Use:  "flag validation",
					Args: cobra.NoArgs,
					Run: func(cmd *cobra.Command, args []string) {
						checkmigrate.DoFlagValidation(cmd)
					}}
				testCmd.SetArgs(strings.Split(argString, " "))
				checkmigrate.SetCmdFlags(testCmd.Flags())

				if !valid {
					defer testhelper.ShouldPanicWithMessage("[CRITICAL]")
				}

				err := testCmd.Execute()
				if err != nil && valid {
					Fail("Valid flag value failed validation check")
				}
			},

			/*
			 * Check validation for password file path flags
			 */
			Entry("--source-no-password check", "--source-no-password /tmp/file1", true),
			Entry("--source-no-password check", "--source-no-password file1", false),
			Entry("-w check", "-w /tmp/file1", true),
			Entry("-w check", "-w file1", false),

			Entry("--target-no-password check", "--target-no-password /tmp/file1", true),
			Entry("--target-no-password check", "--target-no-password file1", false),
			Entry("-W check", "-w /tmp/file1", true),
			Entry("-W check", "-w file1", false),

			Entry("password flags combos", "--source-no-password /tmp/file1 --target-no-password /tmp/file2", true),
			Entry("password flags combos", "--source-no-password /tmp/file1 --target-no-password file2", false),
			Entry("password flags combos", "--source-no-password file1 --target-no-password /tmp/file2", false),
			Entry("shorthand flags combos", "-w /tmp/file1 -W /tmp/file2", true),
			Entry("password flags combos", "-w /tmp/file1 -W file2", false),
			Entry("password flags combos", "-w file1 -W /tmp/file2", false),
		)
	})
})
