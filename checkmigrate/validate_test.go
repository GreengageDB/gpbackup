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
	Describe("Validate checkmigrate flags", func() {
		DescribeTable("parses valid flags and rejects invalid combinations",
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

			Entry("target host and port", "--target-host localhost --target-port 7000", true),
			Entry("target host without port", "--target-host localhost", false),
			Entry("target port without host", "--target-port 7000", false),
			Entry("debug flag", "--debug", true),
			Entry("quiet flag", "--quiet", true),
			Entry("verbose flag", "--verbose", true),
			Entry("debug and quiet flags", "--debug --quiet", false),
			Entry("debug and verbose flags", "--debug --verbose", false),
			Entry("quiet and verbose flags", "--quiet --verbose", false),
		)
	})
})
