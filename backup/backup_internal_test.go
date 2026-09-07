package backup

import (
	"bytes"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
)

var _ = Describe("backup internal tests", func() {
	var log *Buffer
	BeforeEach(func() {
		_, _, log = testhelper.SetupTestLogger()
	})

	Describe("backupData", func() {
		It("returns successfully immediately if there is no table data to backup", func() {
			emptyTableSlice := make([]Table, 0)

			backupData(emptyTableSlice)
			Expect(string(log.Contents())).To(ContainSubstring("Data backup complete"))
		})
	})

	Describe("backupQDOnlyData", func() {
		// A private dbconn.DBConn/sqlmock pair, swapped in and restored around
		// each test, rather than sharing the outer backup_test suite's
		// connectionPool: that would create an import cycle (testutils, which
		// builds the shared one, imports this package).
		var (
			localMock sqlmock.Sqlmock
			buf       *bytes.Buffer
			metaFile  *utils.FileWithByteCount
			condition = ""
			testTable TableQDOnly
		)
		BeforeEach(func() {
			var localConn *dbconn.DBConn
			localConn, localMock = testhelper.CreateAndConnectMockDB(1)
			oldConn := connectionPool
			connectionPool = localConn
			DeferCleanup(func() { connectionPool = oldConn })

			oldTOC := globalTOC
			tocfile := &toc.TOC{}
			tocfile.InitializeMetadataEntryMap()
			SetTOC(tocfile)
			DeferCleanup(func() { globalTOC = oldTOC })

			buf = &bytes.Buffer{}
			metaFile = utils.NewFileWithByteCount(buf)

			testTable = TableQDOnly{Table{
				Relation: Relation{Schema: "local_ext", Name: "cfg"},
				TableDefinition: TableDefinition{
					ColumnDefs:           []ColumnDefinition{{Name: "id"}, {Name: "val"}},
					ExtensionTableConfig: &condition,
				},
			}}
		})
		It("includes OVERRIDING SYSTEM VALUE on GPDB7+, where identity columns exist", func() {
			connectionPool.Version = dbconn.NewVersion("7.0.0")
			localMock.ExpectQuery("FROM \\(SELECT").
				WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow("'1','a'"))

			backupQDOnlyData(metaFile, []TableQDOnly{testTable})

			Expect(buf.String()).To(ContainSubstring("OVERRIDING SYSTEM VALUE VALUES('1','a')"))
		})
		It("omits OVERRIDING SYSTEM VALUE before GPDB7, which doesn't parse it", func() {
			connectionPool.Version = dbconn.NewVersion("6.0.0")
			localMock.ExpectQuery("FROM \\(SELECT").
				WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow("'1','a'"))

			backupQDOnlyData(metaFile, []TableQDOnly{testTable})

			Expect(buf.String()).To(ContainSubstring("(id,val) VALUES('1','a')"))
			Expect(buf.String()).ToNot(ContainSubstring("OVERRIDING"))
		})
		It("wraps the row query in a subquery, so its own ORDER BY doesn't collide with a trailing clause in the extension's own condition", func() {
			connectionPool.Version = dbconn.NewVersion("7.0.0")
			trailingClauseCondition := "WHERE id > 0 LIMIT 1"
			testTable.ExtensionTableConfig = &trailingClauseCondition
			// A query with the ORDER BY appended directly after the condition text
			// (rather than an outer wrapping SELECT) would not match this pattern -
			// sqlmock would then reject the call as unexpected and Select would error.
			localMock.ExpectQuery("SELECT \\* FROM \\(SELECT .* WHERE id > 0 LIMIT 1\\) qd_only_row ORDER BY 1").
				WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow("'1','a'"))

			backupQDOnlyData(metaFile, []TableQDOnly{testTable})

			Expect(buf.String()).To(ContainSubstring("VALUES('1','a')"))
		})
	})
})
