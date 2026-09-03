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
	Describe("CreateConnections", func() {
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
			testhelper.SetDBVersion(mockSource, "6.27.1")
			testhelper.SetDBVersion(mockTarget, "7.0.0")
			testhelper.ExpectVersionQuery(sourceMock, "6.27.1")
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

		It("defaults to port 5432, postgres db, and shouldScrapeDatabaseNames=true when no flags/envs are set", func() {
			GinkgoT().Setenv("USER", "test_os_user")

			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				mockSource.DBName = dbName
				mockSource.User = username
				mockSource.Host = host
				mockSource.Port = port
				return mockSource
			})

			Expect(checkmigrate.CreateConnections()).To(Succeed())

			sourceConnection := checkmigrate.GetSourceConnection()
			Expect(sourceConnection).NotTo(BeNil())
			Expect(sourceConnection.Port).To(Equal(5432))
			Expect(sourceConnection.DBName).To(Equal("postgres"))
			Expect(sourceConnection.User).To(Equal("test_os_user"))
			Expect(checkmigrate.GetShouldScrapeDatabaseNames()).To(BeTrue())

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

			Expect(checkmigrate.CreateConnections()).To(Succeed())

			sourceConnection := checkmigrate.GetSourceConnection()
			Expect(sourceConnection).NotTo(BeNil())
			Expect(sourceConnection.Port).To(Equal(6000))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("uses the source-port flag when PGPORT is also set", func() {
			GinkgoT().Setenv("PGPORT", "6000")
			_ = cmdFlags.Set(options.SOURCE_PORT, "7000")

			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				mockSource.Port = port
				return mockSource
			})

			Expect(checkmigrate.CreateConnections()).To(Succeed())

			Expect(checkmigrate.GetSourceConnection().Port).To(Equal(7000))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("uses port 5432 if PGPORT is invalid", func() {
			GinkgoT().Setenv("PGPORT", "invalid")

			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				mockSource.Port = port
				return mockSource
			})

			Expect(checkmigrate.CreateConnections()).To(Succeed())

			Expect(checkmigrate.GetSourceConnection().Port).To(Equal(5432))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("uses template1 when postgres does not accept connections", func() {
			failingSource, _ := testhelper.CreateMockDBConn(errors.New("postgres connection failed"))
			DeferCleanup(failingSource.Close)
			GinkgoT().Setenv("USER", "test_os_user")

			connectionCount := 0
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				connectionCount++
				if connectionCount == 1 {
					Expect(dbName).To(Equal("postgres"))
					return failingSource
				}

				mockSource.DBName = dbName
				mockSource.User = username
				mockSource.Host = host
				mockSource.Port = port
				return mockSource
			})

			Expect(checkmigrate.CreateConnections()).To(Succeed())

			sourceConnection := checkmigrate.GetSourceConnection()
			Expect(sourceConnection.DBName).To(Equal("template1"))
			Expect(sourceConnection.User).To(Equal("test_os_user"))
			Expect(sourceConnection.Port).To(Equal(5432))
			Expect(checkmigrate.GetShouldScrapeDatabaseNames()).To(BeTrue())
			Expect(connectionCount).To(Equal(2))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("returns execution error code for a source connection failure", func() {
			_ = cmdFlags.Set(options.SOURCE_DATABASE, "application")
			failingSource, _ := testhelper.CreateMockDBConn(errors.New("source connection failed"))
			DeferCleanup(failingSource.Close)
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				return failingSource
			})

			Expect(checkmigrate.CreateConnections()).To(MatchError("source connection failed (testhost:5432)"))
			Expect(gplog.GetErrorCode()).To(Equal(5))
		})

		It("returns the documented error for an invalid source version", func() {
			invalidSource, invalidSourceMock := testhelper.CreateMockDBConn()
			testhelper.ExpectVersionQuery(invalidSourceMock, "5.0.0")
			DeferCleanup(invalidSource.Close)
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				return invalidSource
			})

			Expect(checkmigrate.CreateConnections()).To(MatchError("this utility can only check for migrate from Greengage version 6"))
			Expect(gplog.GetErrorCode()).To(Equal(2))
			Expect(invalidSourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("returns the source version error below version 6.27.1", func() {
			oldSource, oldSourceMock := testhelper.CreateMockDBConn()
			testhelper.ExpectVersionQuery(oldSourceMock, "6.27.0")
			DeferCleanup(oldSource.Close)
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				return oldSource
			})

			Expect(checkmigrate.CreateConnections()).To(MatchError("this utility requires Greengage version 6.27.1 or newer"))
			Expect(gplog.GetErrorCode()).To(Equal(2))
			Expect(oldSourceMock.ExpectationsWereMet()).To(Succeed())
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

			Expect(checkmigrate.CreateConnections()).To(Succeed())

			targetConnection := checkmigrate.GetTargetConnection()
			Expect(targetConnection).NotTo(BeNil())
			Expect(targetConnection.Host).To(Equal("localhost"))
			Expect(targetConnection.Port).To(Equal(7000))

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

			Expect(checkmigrate.CreateConnections()).To(MatchError("target connection failed (testhost:5432)"))
			Expect(gplog.GetErrorCode()).To(Equal(5))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
		})

		It("returns the documented error for an invalid target version", func() {
			invalidTarget, invalidTargetMock := testhelper.CreateMockDBConn()
			testhelper.ExpectVersionQuery(invalidTargetMock, "8.0.0")
			DeferCleanup(invalidTarget.Close)
			_ = cmdFlags.Set(options.TARGET_HOST, "localhost")
			_ = cmdFlags.Set(options.TARGET_PORT, "7000")

			connectionCount := 0
			checkmigrate.SetCreateDBConn(func(dbName, username, host string, port int) *dbconn.DBConn {
				connectionCount++
				if connectionCount == 1 {
					return mockSource
				}

				return invalidTarget
			})

			Expect(checkmigrate.CreateConnections()).To(MatchError("this utility can only check for migrate to Greengage version 7"))
			Expect(gplog.GetErrorCode()).To(Equal(3))
			Expect(sourceMock.ExpectationsWereMet()).To(Succeed())
			Expect(invalidTargetMock.ExpectationsWereMet()).To(Succeed())
		})
	})
})
