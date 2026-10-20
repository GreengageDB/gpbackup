package restore

import (
	"context"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/filepath"
	"github.com/GreengageDB/gpbackup/history"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/pkg/errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("restore internal tests", func() {
	statements := []toc.StatementWithType{
		{ // simple table
			Schema: "foo", Name: "bar", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE foo.bar (\n\ti integer\n) DISTRIBUTED BY (i);\n",
		},
		{ // simple schema
			Schema: "foo", Name: "foo", ObjectType: toc.OBJ_SCHEMA,
			Statement: "\n\nCREATE SCHEMA foo;\n",
		},
		{ // table with a schema containing dots
			Schema: "\"foo.bar\"", Name: "baz", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE \"foo.bar\".baz (\n\ti integer\n) DISTRIBUTED BY (i);\n",
		},
		{ // table with a schema containing quotes
			Schema: "\"foo\"bar\"", Name: "baz", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE \"foo\"bar\".baz (\n\ti integer\n) DISTRIBUTED BY (i);\n",
		},
		{ // view with multiple schema replacements
			Schema: "foo", Name: "myview", ObjectType: toc.OBJ_VIEW,
			Statement: "\n\nCREATE VIEW foo.myview AS  SELECT bar.i\n   FROM foo.bar;\n",
		},
		{ // schema and table are the same name
			Schema: "foo", Name: "foo", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE foo.foo (\n\ti integer\n) DISTRIBUTED BY (i);\n",
		},
		{ // multi-line permissions block for a schema
			Schema: "foo", Name: "foo", ObjectType: toc.OBJ_SCHEMA,
			Statement: "\n\nREVOKE ALL ON SCHEMA foo FROM PUBLIC;\nGRANT ALL ON SCHEMA foo TO testuser;\n",
		},
		{ // multi-line permissions block for a view
			Schema: "foo", Name: "myview", ObjectType: toc.OBJ_VIEW,
			Statement: "\n\nREVOKE ALL ON TABLE foo.myview FROM PUBLIC;\nGRANT ALL ON TABLE foo.myview TO testuser;\n",
		},
		{ // multi-line permissions block for a non-schema object
			Schema: "foo", Name: "myfunc", ObjectType: toc.OBJ_FUNCTION,
			Statement: "\n\nREVOKE ALL ON FUNCTION foo.myfunc(integer) FROM PUBLIC;\nGRANT ALL ON FUNCTION foo.myfunc(integer) TO testuser;\n",
		},
		{ // multi-line permissions block with a schema containing dots
			Schema: "\"foo.bar\"", Name: "myfunc", ObjectType: toc.OBJ_FUNCTION,
			Statement: "\n\nREVOKE ALL ON FUNCTION \"foo.bar\".myfunc(integer) FROM PUBLIC;\nGRANT ALL ON FUNCTION \"foo.bar\".myfunc(integer) TO testuser;\n",
		},
		{ // 1 level SET SUBPARTITION TEMPLATE
			Schema: "foo", Name: "foo", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE foo.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo.foo \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION a VALUES(1) WITH (tablename='foo'),\nSUBPARTITION b VALUES(2) WITH (tablename='foo'),\nDEFAULT SUBPARTITION c  WITH (tablename='foo')\n);\n",
		},
		{ // 2 level SET SUBPARTITION TEMPLATE with partition name
			Schema: "foo", Name: "foo", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE foo.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo.foo ALTER PARTITION a \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION d VALUES(3) WITH (tablename='foo'),\nDEFAULT SUBPARTITION e  WITH (tablename='foo')\n);\n",
		},
		{ // 2 level SET SUBPARTITION TEMPLATE with FOR (RANK)
			Schema: "foo", Name: "foo", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE foo.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo.foo ALTER PARTITION FOR (RANK(1)) \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION d VALUES(3) WITH (tablename='foo'),\nDEFAULT SUBPARTITION e  WITH (tablename='foo')\n);\n",
		},
		{ // 3 level SET SUBPARTITION TEMPLATE
			Schema: "foo", Name: "foo", ObjectType: toc.OBJ_TABLE,
			Statement: "\n\nCREATE TABLE foo.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo.foo ALTER PARTITION a ALTER PARTITION b \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION d VALUES(3) WITH (tablename='foo'),\nDEFAULT SUBPARTITION e  WITH (tablename='foo')\n);\n",
		},
		{ // ALTER TABLE ... ATTACH PARTITION statement
			Schema: "public", Name: "foopart_p1", ObjectType: toc.OBJ_TABLE, ReferenceObject: "public.foopart",
			Statement: "\n\nALTER TABLE public.foopart ATTACH PARTITION public.foopart_p1 FOR VALUES FROM (0) TO (1);\n",
		},
		{ // ALTER TABLE ONLY ... ATTACH PARTITION statement
			Schema: "public", Name: "foopart_p1", ObjectType: toc.OBJ_TABLE, ReferenceObject: "public.foopart",
			Statement: "\n\nALTER TABLE ONLY public.foopart ATTACH PARTITION public.foopart_p1 FOR VALUES FROM (0) TO (1);\n",
		},
		{
			Schema: "foo", Name: "bar", ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo.bar'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: "foo", Name: `"b'ar"`, ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo."b''ar"'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: "foo", Name: `"b.ar"`, ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo."b.ar"'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: `"fo.o"`, Name: "bar", ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = '"fo.o".bar'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: `"fo.o"`, Name: `"b'ar"`, ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = '"fo.o"."b''ar"'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: `"fo.o"`, Name: `"b.ar"`, ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = '"fo.o"."b.ar"'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: `"fo'o"`, Name: "bar", ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = '"fo''o".bar'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: `"fo'o"`, Name: `"b'ar"`, ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = '"fo''o"."b''ar"'::regclass::oid AND attname = 'i');`,
		},
		{
			Schema: `"fo'o"`, Name: `"b.ar"`, ObjectType: toc.OBJ_STATISTICS,
			Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = '"fo''o"."b.ar"'::regclass::oid AND attname = 'i');`,
		},
	}
	Describe("editStatementsRedirectStatements", func() {
		It("does not alter schemas if no redirect was specified", func() {
			originalStatements := make([]toc.StatementWithType, len(statements))
			copy(originalStatements, statements)

			editStatementsRedirectSchema(statements, "")

			// Loop through statements individually instead of comparing the whole arrays directly,
			// to make it easier to find the statements with issues
			for i := range statements {
				Expect(statements[i]).To(Equal(originalStatements[i]))
			}
		})
		It("changes schema in the sql statement", func() {
			// We need to temporarily set the version to 7 or later to test the ATTACH PARTITION replacement
			oldVersion := connectionPool.Version
			connectionPool.Version = dbconn.NewVersion("7.0.0")
			defer func() { connectionPool.Version = oldVersion }()

			editStatementsRedirectSchema(statements, "foo2")

			expectedStatements := []toc.StatementWithType{
				{
					Schema: "foo2", Name: "bar", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.bar (\n\ti integer\n) DISTRIBUTED BY (i);\n",
				},
				{
					Schema: "foo2", Name: "foo2", ObjectType: toc.OBJ_SCHEMA,
					Statement: "\n\nCREATE SCHEMA foo2;\n",
				},
				{
					Schema: "foo2", Name: "baz", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.baz (\n\ti integer\n) DISTRIBUTED BY (i);\n",
				},
				{
					Schema: "foo2", Name: "baz", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.baz (\n\ti integer\n) DISTRIBUTED BY (i);\n",
				},
				{
					Schema: "foo2", Name: "myview", ObjectType: toc.OBJ_VIEW,
					Statement: "\n\nCREATE VIEW foo2.myview AS  SELECT bar.i\n   FROM foo.bar;\n",
				},
				{
					Schema: "foo2", Name: "foo", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.foo (\n\ti integer\n) DISTRIBUTED BY (i);\n",
				},
				{
					Schema: "foo2", Name: "foo2", ObjectType: toc.OBJ_SCHEMA,
					Statement: "\n\nREVOKE ALL ON SCHEMA foo2 FROM PUBLIC;\nGRANT ALL ON SCHEMA foo2 TO testuser;\n",
				},
				{
					Schema: "foo2", Name: "myview", ObjectType: toc.OBJ_VIEW,
					Statement: "\n\nREVOKE ALL ON TABLE foo2.myview FROM PUBLIC;\nGRANT ALL ON TABLE foo2.myview TO testuser;\n",
				},
				{
					Schema: "foo2", Name: "myfunc", ObjectType: toc.OBJ_FUNCTION,
					Statement: "\n\nREVOKE ALL ON FUNCTION foo2.myfunc(integer) FROM PUBLIC;\nGRANT ALL ON FUNCTION foo2.myfunc(integer) TO testuser;\n",
				},
				{
					Schema: "foo2", Name: "myfunc", ObjectType: toc.OBJ_FUNCTION,
					Statement: "\n\nREVOKE ALL ON FUNCTION foo2.myfunc(integer) FROM PUBLIC;\nGRANT ALL ON FUNCTION foo2.myfunc(integer) TO testuser;\n",
				},
				{
					Schema: "foo2", Name: "foo", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo2.foo \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION a VALUES(1) WITH (tablename='foo'),\nSUBPARTITION b VALUES(2) WITH (tablename='foo'),\nDEFAULT SUBPARTITION c  WITH (tablename='foo')\n);\n",
				},
				{
					Schema: "foo2", Name: "foo", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo2.foo ALTER PARTITION a \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION d VALUES(3) WITH (tablename='foo'),\nDEFAULT SUBPARTITION e  WITH (tablename='foo')\n);\n",
				},
				{
					Schema: "foo2", Name: "foo", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo2.foo ALTER PARTITION FOR (RANK(1)) \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION d VALUES(3) WITH (tablename='foo'),\nDEFAULT SUBPARTITION e  WITH (tablename='foo')\n);\n",
				},
				{
					Schema: "foo2", Name: "foo", ObjectType: toc.OBJ_TABLE,
					Statement: "\n\nCREATE TABLE foo2.foo (\n\ti integer\n) DISTRIBUTED BY (i);\nALTER TABLE foo2.foo ALTER PARTITION a ALTER PARTITION b \nSET SUBPARTITION TEMPLATE \n(\nSUBPARTITION d VALUES(3) WITH (tablename='foo'),\nDEFAULT SUBPARTITION e  WITH (tablename='foo')\n);\n",
				},
				{ // ALTER TABLE ... ATTACH PARTITION statement
					Schema: "foo2", Name: "foopart_p1", ObjectType: toc.OBJ_TABLE, ReferenceObject: "foo2.foopart",
					Statement: "\n\nALTER TABLE foo2.foopart ATTACH PARTITION foo2.foopart_p1 FOR VALUES FROM (0) TO (1);\n",
				},
				{ // ALTER TABLE ONLY ... ATTACH PARTITION statement
					Schema: "foo2", Name: "foopart_p1", ObjectType: toc.OBJ_TABLE, ReferenceObject: "foo2.foopart",
					Statement: "\n\nALTER TABLE ONLY foo2.foopart ATTACH PARTITION foo2.foopart_p1 FOR VALUES FROM (0) TO (1);\n",
				},
				{
					Schema: "foo2", Name: "bar", ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2.bar'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: `"b'ar"`, ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2."b''ar"'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: `"b.ar"`, ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2."b.ar"'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: "bar", ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2.bar'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: `"b'ar"`, ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2."b''ar"'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: `"b.ar"`, ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2."b.ar"'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: "bar", ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2.bar'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: `"b'ar"`, ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2."b''ar"'::regclass::oid AND attname = 'i');`,
				},
				{
					Schema: "foo2", Name: `"b.ar"`, ObjectType: toc.OBJ_STATISTICS,
					Statement: `DELETE FROM pg_statistic WHERE (starelid, staattnum) =
(SELECT attrelid, attnum FROM pg_attribute WHERE attrelid = 'foo2."b.ar"'::regclass::oid AND attname = 'i');`,
				},
			}

			for i := range statements {
				//fmt.Println("\n\nACTUAL\n", statements[i], "\nEXPECTED\n", expectedStatements[i])
				Expect(statements[i]).To(Equal(expectedStatements[i]))
			}
		})
	})
	Describe("restoreSingleTableData", func() {
		var localMock sqlmock.Sqlmock
		BeforeEach(func() {
			var localConn *dbconn.DBConn
			localConn, localMock = testhelper.CreateAndConnectMockDB(1)
			oldConn, oldConfig := connectionPool, backupConfig
			connectionPool, backupConfig = localConn, &history.BackupConfig{}
			DeferCleanup(func() { connectionPool, backupConfig = oldConn, oldConfig })
		})
		It("returns the error of a failed COPY into a table distributed by an enum when the setting cannot be reset", func() {
			fpInfo := filepath.FilePathInfo{Timestamp: "20170101010101"}
			entry := toc.CoordinatorDataEntry{Schema: "public", Name: "foo", Oid: 1, AttributeString: "(i)", DistByEnum: true}
			localMock.ExpectExec("SET gp_enable_segment_copy_checking TO off").WillReturnResult(sqlmock.NewResult(0, 0))
			localMock.ExpectExec("COPY public.foo").WillReturnError(errors.New("invalid input value for enum"))
			// The transaction of the COPY, with --truncate-table, is aborted then
			localMock.ExpectExec("RESET gp_enable_segment_copy_checking").WillReturnError(errors.New("current transaction is aborted"))

			var err error
			Expect(func() {
				err = restoreSingleTableData(context.Background(), &fpInfo, entry, "public.foo", 0, []int{})
			}).ToNot(Panic())
			Expect(err).To(MatchError(ContainSubstring("invalid input value for enum")))
			Expect(localMock.ExpectationsWereMet()).To(Succeed())
		})
		It("returns the error of a failed redistribution of a table distributed by an enum", func() {
			fpInfo := filepath.FilePathInfo{Timestamp: "20170101010101"}
			entry := toc.CoordinatorDataEntry{Schema: "public", Name: "foo", Oid: 1, AttributeString: "(i)", DistByEnum: true, RowsCopied: 10}
			localMock.ExpectExec("SET gp_enable_segment_copy_checking TO off").WillReturnResult(sqlmock.NewResult(0, 0))
			localMock.ExpectExec("COPY public.foo").WillReturnResult(sqlmock.NewResult(0, 10))
			localMock.ExpectExec(`ALTER TABLE public.foo SET WITH \(REORGANIZE=true\)`).WillReturnError(errors.New("out of memory"))
			localMock.ExpectExec("RESET gp_enable_segment_copy_checking").WillReturnResult(sqlmock.NewResult(0, 0))

			err := restoreSingleTableData(context.Background(), &fpInfo, entry, "public.foo", 0, []int{})
			Expect(err).To(MatchError("out of memory"))
			Expect(localMock.ExpectationsWereMet()).To(Succeed())
		})
		It("leaves gp_enable_segment_copy_checking off for a table distributed by an enum with --resize-cluster", func() {
			// The session of a resize restore has it off, which a RESET would turn back on for the tables after
			_ = cmdFlags.Set(options.RESIZE_CLUSTER, "true")
			DeferCleanup(func() {
				_ = cmdFlags.Set(options.RESIZE_CLUSTER, "false")
				cmdFlags.Lookup(options.RESIZE_CLUSTER).Changed = false
			})
			fpInfo := filepath.FilePathInfo{Timestamp: "20170101010101"}
			entry := toc.CoordinatorDataEntry{Schema: "public", Name: "foo", Oid: 1, AttributeString: "(i)", DistByEnum: true, RowsCopied: 10}
			localMock.ExpectExec("COPY public.foo").WillReturnResult(sqlmock.NewResult(0, 10))
			localMock.ExpectExec(`ALTER TABLE public.foo SET WITH \(REORGANIZE=true\)`).WillReturnResult(sqlmock.NewResult(0, 0))

			err := restoreSingleTableData(context.Background(), &fpInfo, entry, "public.foo", 0, []int{})
			Expect(err).ToNot(HaveOccurred())
			Expect(localMock.ExpectationsWereMet()).To(Succeed())
		})
	})
	Describe("beginTableTransaction", func() {
		It("begins the transaction of a table at the default isolation level", func() {
			localConn, localMock := testhelper.CreateAndConnectMockDB(2)
			oldConn := connectionPool
			connectionPool = localConn
			DeferCleanup(func() { connectionPool = oldConn })
			// No SET TRANSACTION ISOLATION LEVEL SERIALIZABLE follows
			localMock.ExpectBegin()
			localMock.ExpectExec("TRUNCATE ONLY public.foo").WillReturnResult(sqlmock.NewResult(0, 0))
			localMock.ExpectCommit()

			Expect(beginTableTransaction(1)).To(Succeed())
			Expect(connectionPool.Tx[1]).ToNot(BeNil())
			_, err := connectionPool.Exec("TRUNCATE ONLY public.foo", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(connectionPool.Commit(1)).To(Succeed())
			Expect(localMock.ExpectationsWereMet()).To(Succeed())
		})
	})
	Describe("entriesOfHelper", func() {
		// The entries a connection restores, all at once
		entriesOf := func(helper int, numHelpers int, numEntries int) []int {
			entries := make([]int, 0)
			nextEntryIdx := entriesOfHelper(helper, numHelpers, numEntries)
			for entryIdx, ok := nextEntryIdx(); ok; entryIdx, ok = nextEntryIdx() {
				entries = append(entries, entryIdx)
			}
			return entries
		}
		It("gives a connection the entries of its own gpbackup_helper, whenever it asks", func() {
			Expect(entriesOf(0, 3, 7)).To(Equal([]int{0, 3, 6}))
			Expect(entriesOf(1, 3, 7)).To(Equal([]int{1, 4}))
			Expect(entriesOf(2, 3, 7)).To(Equal([]int{2, 5}))
		})
		It("gives no entries to a connection without a gpbackup_helper", func() {
			// With more connections than tables, there is a helper for each table only
			Expect(entriesOf(3, 3, 3)).To(BeEmpty())
		})
	})
	Describe("lockAncestors", func() {
		It("locks only the ancestors of a partition, not their other partitions", func() {
			localConn, localMock := testhelper.CreateAndConnectMockDB(1)
			oldConn := connectionPool
			connectionPool = localConn
			DeferCleanup(func() { connectionPool = oldConn })
			localMock.ExpectExec(`LOCK TABLE ONLY public.sales_1_prt_2, ONLY public.sales IN ACCESS SHARE MODE`).WillReturnResult(sqlmock.NewResult(0, 0))

			Expect(lockAncestors([]string{"public.sales_1_prt_2", "public.sales"}, 0)).To(Succeed())
			Expect(localMock.ExpectationsWereMet()).To(Succeed())
		})
	})
	Describe("numConnsPerDataFile", func() {
		// Data files 0, 1 and 2, with an entry each
		entries := []toc.CoordinatorDataEntry{{Oid: 1, DataFileIdx: 0}, {Oid: 2, DataFileIdx: 1}, {Oid: 3, DataFileIdx: 2}}
		It("gives all connections to a single data file", func() {
			Expect(numConnsPerDataFile([]toc.CoordinatorDataEntry{{Oid: 1}, {Oid: 2}}, 4, 1)).To(Equal(4))
			Expect(numConnsPerDataFile([]toc.CoordinatorDataEntry{{Oid: 1}, {Oid: 2}}, 8, 2)).To(Equal(8))
		})
		It("gives --copy-queue-size connections to each data file with as many data files as groups or more", func() {
			Expect(numConnsPerDataFile(entries, 3, 1)).To(Equal(1))
			Expect(numConnsPerDataFile(entries, 4, 2)).To(Equal(2))
			Expect(numConnsPerDataFile(entries, 6, 2)).To(Equal(2))
		})
		It("splits the connections among fewer data files than groups", func() {
			Expect(numConnsPerDataFile(entries, 6, 1)).To(Equal(2))
			Expect(numConnsPerDataFile(entries, 12, 2)).To(Equal(4))
			// The connections left over restore nothing
			Expect(numConnsPerDataFile(entries, 8, 1)).To(Equal(2))
		})
		It("counts only the data files with entries to restore", func() {
			// After a filter, only data file 2 of the backup is left
			Expect(numConnsPerDataFile([]toc.CoordinatorDataEntry{{Oid: 3, DataFileIdx: 2}, {Oid: 6, DataFileIdx: 2}}, 4, 1)).To(Equal(4))
			Expect(numConnsPerDataFile([]toc.CoordinatorDataEntry{{Oid: 2, DataFileIdx: 1}, {Oid: 3, DataFileIdx: 2}}, 8, 2)).To(Equal(4))
		})
	})
	Describe("getExtensionTableCondition", func() {
		// This function's own SQL round-trip is mocked directly here (a private
		// dbconn.DBConn/sqlmock pair, swapped in and restored around each test)
		// rather than sharing the outer restore_test suite's connectionPool: that
		// would create an import cycle (testutils, which builds the shared one,
		// imports this package).
		var localMock sqlmock.Sqlmock
		BeforeEach(func() {
			var localConn *dbconn.DBConn
			localConn, localMock = testhelper.CreateAndConnectMockDB(1)
			oldConn := connectionPool
			connectionPool = localConn
			DeferCleanup(func() { connectionPool = oldConn })
		})
		It("returns the extension's condition when the table is registered", func() {
			localMock.ExpectQuery("SELECT coalesce").
				WillReturnRows(sqlmock.NewRows([]string{"string"}).AddRow("WHERE active"))

			condition, err := getExtensionTableCondition("local_ext.test_local_cfg_filtered")

			Expect(err).ShouldNot(HaveOccurred())
			Expect(condition).To(Equal("WHERE active"))
		})
		It("returns the not-found sentinel when the table isn't registered in any extension's extconfig", func() {
			localMock.ExpectQuery("SELECT coalesce").
				WillReturnRows(sqlmock.NewRows([]string{"string"}).AddRow(extensionTableConditionNotFound))

			condition, err := getExtensionTableCondition("local_ext.orphaned_table")

			Expect(err).ShouldNot(HaveOccurred())
			Expect(condition).To(Equal(extensionTableConditionNotFound))
		})
		It("propagates a query error", func() {
			localMock.ExpectQuery("SELECT coalesce").
				WillReturnError(errors.New("relation \"local_ext.missing\" does not exist"))

			_, err := getExtensionTableCondition("local_ext.missing")

			Expect(err).To(HaveOccurred())
		})
	})
})
