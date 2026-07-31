package checkmigrate_test

import (
	"errors"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/spf13/pflag"

	"github.com/GreengageDB/gpbackup/checkmigrate"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("checkmigrate wrapper tests", func() {
	Describe("CreateConnectionPool", func() {
		var (
			mockSource *dbconn.DBConn
			mockTarget *dbconn.DBConn
			sourceMock sqlmock.Sqlmock
			targetMock sqlmock.Sqlmock
			cmdFlags   *pflag.FlagSet
		)

		BeforeEach(func() {
			mockSource, sourceMock = testhelper.CreateMockDBConn()
			mockTarget, targetMock = testhelper.CreateMockDBConn()
			testhelper.SetDBVersion(mockSource, "6.0.0")
			testhelper.SetDBVersion(mockTarget, "7.0.0")
			testhelper.ExpectVersionQuery(sourceMock, "6.0.0")
			testhelper.ExpectVersionQuery(targetMock, "7.0.0")
			checkmigrate.ResetGlobalState()
			cmdFlags = pflag.NewFlagSet("checkmigrate", pflag.ContinueOnError)
			checkmigrate.SetCmdFlags(cmdFlags)
			gplog.SetErrorCode(0)
			GinkgoT().Setenv("PGPORT", "")
			GinkgoT().Setenv("PGUSER", "")
		})

		AfterEach(func() {
			checkmigrate.SetCreateDBConn(dbconn.NewDBConn)

			if mockSource != nil {
				mockSource.Close()
			}
			if mockTarget != nil {
				mockTarget.Close()
			}
		})

		It("defaults to port 5432, postgres db, and scrapeDbNames=true when no flags/envs are set", func() {
			GinkgoT().Setenv("USER", "test_os_user")

			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				mockSource.DBName = dbName
				mockSource.User = username
				mockSource.Host = host
				mockSource.Port = port
				return mockSource
			})

			checkmigrate.CreateConnectionPool()

			sourcePool := checkmigrate.GetSourceConnectionPool()
			Expect(sourcePool).NotTo(BeNil())
			Expect(sourcePool.Port).To(Equal(5432))
			Expect(sourcePool.DBName).To(Equal("postgres"))
			Expect(sourcePool.User).To(Equal("test_os_user"))
			Expect(checkmigrate.GetScrapeDbNames()).To(BeTrue())

			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("uses PGPORT if the source-port flag is not provided", func() {
			GinkgoT().Setenv("PGPORT", "6000")

			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				mockSource.DBName = dbName
				mockSource.User = username
				mockSource.Host = host
				mockSource.Port = port
				return mockSource
			})

			checkmigrate.CreateConnectionPool()

			sourcePool := checkmigrate.GetSourceConnectionPool()
			Expect(sourcePool).NotTo(BeNil())
			Expect(sourcePool.Port).To(Equal(6000))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("uses port 5432 if PGPORT is invalid", func() {
			GinkgoT().Setenv("PGPORT", "invalid")

			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				mockSource.Port = port
				return mockSource
			})

			checkmigrate.CreateConnectionPool()

			Expect(checkmigrate.GetSourceConnectionPool().Port).To(Equal(5432))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("returns execution error code for a source connection failure", func() {
			failingSource, _ := testhelper.CreateMockDBConn(errors.New("source connection failed"))
			DeferCleanup(failingSource.Close)
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				return failingSource
			})

			Expect(checkmigrate.CreateConnectionPool).To(Panic())
			Expect(gplog.GetErrorCode()).To(Equal(5))
		})

		It("successfully creates both source and target connections when target-host and target-port are provided", func() {
			_ = cmdFlags.Set(options.TARGET_HOST, "localhost")
			_ = cmdFlags.Set(options.TARGET_PORT, "7000")

			callCount := 0
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				callCount++
				if callCount == 1 {
					mockSource.DBName = dbName
					mockSource.User = username
					mockSource.Host = host
					mockSource.Port = port
					return mockSource
				}
				mockTarget.DBName = dbName
				mockTarget.User = username
				mockTarget.Host = host
				mockTarget.Port = port
				return mockTarget
			})

			checkmigrate.CreateConnectionPool()

			targetPool := checkmigrate.GetTargetConnectionPool()
			Expect(targetPool).NotTo(BeNil())
			Expect(targetPool.Host).To(Equal("localhost"))
			Expect(targetPool.Port).To(Equal(7000))

			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
			Expect(targetMock.ExpectationsWereMet()).To(Succeed())
		})

		It("returns execution error code for a target connection failure", func() {
			failingTarget, _ := testhelper.CreateMockDBConn(errors.New("target connection failed"))
			DeferCleanup(failingTarget.Close)
			_ = cmdFlags.Set(options.TARGET_HOST, "localhost")
			_ = cmdFlags.Set(options.TARGET_PORT, "7000")

			connectionCount := 0
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				connectionCount++
				if connectionCount == 1 {
					return mockSource
				}

				return failingTarget
			})

			Expect(checkmigrate.CreateConnectionPool).To(Panic())
			Expect(gplog.GetErrorCode()).To(Equal(5))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})
	})
})
