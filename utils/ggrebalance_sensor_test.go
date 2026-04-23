package utils_test

import (
	"errors"
	"path/filepath"
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/blang/vfs"
	"github.com/blang/vfs/memfs"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ggrabalance_sensor", func() {
	const sampleCoordinatorDataDir = "/my_fake_database/demoDataDir-1"
	var (
		memoryfs   vfs.Filesystem
		mddPathRow *sqlmock.Rows
	)

	BeforeEach(func() {
		memoryfs = memfs.Create()
		mddPathRow = sqlmock.NewRows([]string{"datadir"}).AddRow(sampleCoordinatorDataDir)
		connectionPool.DBName = "postgres"

		if connectionPool.Version.Before("7") {
			Skip("ggrebalance sensor only runs against GPDB 7+")
		}
	})
	Context("IsGgrebalanceRunning", func() {
		Describe("happy path", func() {
			It("senses ggrebalance is not running (no pid file, no schema)", func() {
				mock.ExpectQuery(utils.CoordinatorDataDirQuery).WillReturnRows(mddPathRow)
				Expect(vfs.MkdirAll(memoryfs, sampleCoordinatorDataDir, 0755)).To(Succeed())

				rebalanceSchemaExistenceRows := sqlmock.NewRows([]string{"rebalance_schema_exists"}).AddRow("0")
				mock.ExpectQuery(regexp.QuoteMeta(utils.GgrebalanceCheckSchemaQuery)).WillReturnRows(rebalanceSchemaExistenceRows)

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				result, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeFalse())
			})
			It("senses ggrebalance is not running (no pid file, but schema exists, and latest state is the final one)", func() {
				mock.ExpectQuery(utils.CoordinatorDataDirQuery).WillReturnRows(mddPathRow)
				Expect(vfs.MkdirAll(memoryfs, sampleCoordinatorDataDir, 0755)).To(Succeed())

				rebalanceSchemaExistenceRows := sqlmock.NewRows([]string{"rebalance_schema_exists"}).AddRow("1")
				mock.ExpectQuery(regexp.QuoteMeta(utils.GgrebalanceCheckSchemaQuery)).WillReturnRows(rebalanceSchemaExistenceRows)

				rebalanceLatestStateRows := sqlmock.NewRows([]string{"state"}).AddRow("STATE_EXECUTOR_DONE")
				mock.ExpectQuery(regexp.QuoteMeta(utils.GgrebalanceGetLatesttateQuery)).WillReturnRows(rebalanceLatestStateRows)

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				result, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeFalse())
			})
			It("senses when ggrebalance is running, as determined by existence of the pid file in the coordinator data directory", func() {
				mock.ExpectQuery(utils.CoordinatorDataDirQuery).WillReturnRows(mddPathRow)
				Expect(vfs.MkdirAll(memoryfs, sampleCoordinatorDataDir, 0755)).To(Succeed())
				path := filepath.Join(sampleCoordinatorDataDir, utils.GgrebalancePidFilename)
				Expect(vfs.WriteFile(memoryfs, path, []byte{0}, 0400)).To(Succeed())

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				result, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeTrue())
			})
			It("senses ggrebalance is not running (no pid file), but its previous invokation wasn't complete (schema exists, and latest state is not the final one)", func() {
				mock.ExpectQuery(utils.CoordinatorDataDirQuery).WillReturnRows(mddPathRow)
				Expect(vfs.MkdirAll(memoryfs, sampleCoordinatorDataDir, 0755)).To(Succeed())

				rebalanceSchemaExistenceRows := sqlmock.NewRows([]string{"rebalance_schema_exists"}).AddRow("1")
				mock.ExpectQuery(regexp.QuoteMeta(utils.GgrebalanceCheckSchemaQuery)).WillReturnRows(rebalanceSchemaExistenceRows)

				rebalanceLatestStateRows := sqlmock.NewRows([]string{"state"}).AddRow("STATE_EXECUTOR_STARTED")
				mock.ExpectQuery(regexp.QuoteMeta(utils.GgrebalanceGetLatesttateQuery)).WillReturnRows(rebalanceLatestStateRows)

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				result, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeTrue())
			})
			It("senses ggrebalance is not running (no pid file), but its previous invokation wasn't complete (schema exists, and no latest state (empty state table))", func() {
				mock.ExpectQuery(utils.CoordinatorDataDirQuery).WillReturnRows(mddPathRow)
				Expect(vfs.MkdirAll(memoryfs, sampleCoordinatorDataDir, 0755)).To(Succeed())

				rebalanceSchemaExistenceRows := sqlmock.NewRows([]string{"rebalance_schema_exists"}).AddRow("1")
				mock.ExpectQuery(regexp.QuoteMeta(utils.GgrebalanceCheckSchemaQuery)).WillReturnRows(rebalanceSchemaExistenceRows)

				rebalanceLatestStateRows := sqlmock.NewRows([]string{"state"})
				mock.ExpectQuery(regexp.QuoteMeta(utils.GgrebalanceGetLatesttateQuery)).WillReturnRows(rebalanceLatestStateRows)

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				result, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeTrue())
			})
		})
		Describe("sad paths", func() {
			It("returns an error when MDD query fails", func() {
				mock.ExpectQuery(utils.CoordinatorDataDirQuery).WillReturnError(errors.New("query error"))

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				_, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("query error"))
			})
			It("returns an error when Stat for file fails for a reason besides 'does not exist'", func() {
				mock.ExpectQuery(utils.CoordinatorDataDirQuery).WillReturnRows(mddPathRow)

				ggrebalanceSensor := utils.NewGgrebalanceSensor(vfs.Dummy(errors.New("fs error")), connectionPool)
				_, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("fs error"))
			})
			It("returns an error when supplied with a connection to a database != postgres", func() {
				connectionPool.DBName = "notThePostgresDatabase"

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				_, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("ggrebalance sensor requires a connection to the postgres database"))
			})
			It("returns an error when supplied with Greengage version < 7", func() {
				testhelper.SetDBVersion(connectionPool, "6.1.0")

				ggrebalanceSensor := utils.NewGgrebalanceSensor(memoryfs, connectionPool)
				_, err := ggrebalanceSensor.IsGgrebalanceRunning()

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("ggrebalance sensor requires a connection to Greengage version >= 7"))
			})
		})
	})
})
