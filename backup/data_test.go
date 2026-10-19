package backup_test

import (
	"context"
	"fmt"
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GreengageDB/gpbackup/backup"
	"github.com/GreengageDB/gpbackup/history"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/GreengageDB/gpbackup/report"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"
	"gopkg.in/cheggaaa/pb.v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("backup/data tests", func() {
	Describe("ConstructTableAttributesList", func() {
		It("creates an attribute list for a table with one column", func() {
			columnDefs := []backup.ColumnDefinition{{Name: "a"}}
			atts := backup.ConstructTableAttributesList(columnDefs)
			Expect(atts).To(Equal("(a)"))
		})
		It("creates an attribute list for a table with multiple columns", func() {
			columnDefs := []backup.ColumnDefinition{{Name: "a"}, {Name: "b"}}
			atts := backup.ConstructTableAttributesList(columnDefs)
			Expect(atts).To(Equal("(a,b)"))
		})
		It("creates an attribute list for a table with no columns", func() {
			columnDefs := make([]backup.ColumnDefinition, 0)
			atts := backup.ConstructTableAttributesList(columnDefs)
			Expect(atts).To(Equal(""))
		})
	})
	Describe("AddTableDataEntriesToTOC", func() {
		var (
			tocfile        *toc.TOC
			rowsCopiedMaps []map[uint32]int64
			table          backup.Table
		)
		BeforeEach(func() {
			tocfile = &toc.TOC{}
			backup.SetTOC(tocfile)
			rowsCopiedMaps = make([]map[uint32]int64, connectionPool.NumConns)
			columnDefs := []backup.ColumnDefinition{{Oid: 1, Name: "a"}}
			table = backup.Table{
				Relation:        backup.Relation{Oid: 1, Schema: "public", Name: "table"},
				TableDefinition: backup.TableDefinition{ColumnDefs: columnDefs},
			}
		})
		It("adds an entry for a regular table to the TOC", func() {
			tables := []backup.Table{table}
			backup.AddTableDataEntriesToTOC(tables, rowsCopiedMaps)
			expectedDataEntries := []toc.CoordinatorDataEntry{{Schema: "public", Name: "table", Oid: 1, AttributeString: "(a)"}}
			Expect(tocfile.DataEntries).To(Equal(expectedDataEntries))
		})
		It("adds the entries in the order of their slots when data is written to several data files", func() {
			otherTable := backup.Table{
				Relation:        backup.Relation{Oid: 2, Schema: "public", Name: "other_table"},
				TableDefinition: backup.TableDefinition{ColumnDefs: []backup.ColumnDefinition{{Oid: 1, Name: "a"}}},
			}
			slots := backup.NewDataFileSlots(2, 1)
			slots.Release(slots.Take(2))
			slots.Take(1)
			backup.SetDataFiles(slots)
			defer backup.SetDataFiles(nil)
			backup.AddTableDataEntriesToTOC([]backup.Table{table, otherTable}, rowsCopiedMaps)
			expectedDataEntries := []toc.CoordinatorDataEntry{
				{Schema: "public", Name: "other_table", Oid: 2, AttributeString: "(a)", DataFileIdx: 0},
				{Schema: "public", Name: "table", Oid: 1, AttributeString: "(a)", DataFileIdx: 1},
			}
			Expect(tocfile.DataEntries).To(Equal(expectedDataEntries))
		})
		It("does not add an entry for an external table to the TOC", func() {
			table.IsExternal = true
			tables := []backup.Table{table}
			backup.AddTableDataEntriesToTOC(tables, rowsCopiedMaps)
			Expect(tocfile.DataEntries).To(BeNil())
		})
		It("does not add an entry for a foreign table to the TOC", func() {
			foreignDef := backup.ForeignTableDefinition{Oid: 23, Options: "", Server: "fs"}
			table.ForeignDef = foreignDef
			tables := []backup.Table{table}
			backup.AddTableDataEntriesToTOC(tables, rowsCopiedMaps)
			Expect(tocfile.DataEntries).To(BeNil())
		})
	})
	Describe("DataFileSlots", func() {
		It("gives each data file a table before any data file gets a second one", func() {
			slots := backup.NewDataFileSlots(3, 2)
			for oid := uint32(1); oid <= 3; oid++ {
				slots.Release(slots.Take(oid))
			}
			Expect(slots.Take(4)).To(Equal(backup.DataFileSlot{DataFile: 0, Slot: 1}))
			Expect(slots.GetNextSlots()).To(Equal([]int{2, 1, 1}))
			Expect([]int{slots.GetDataFile(1), slots.GetDataFile(2), slots.GetDataFile(3)}).To(Equal([]int{0, 1, 2}))
		})
		It("gives the next table to the data file with the fewest COPY commands in progress", func() {
			slots := backup.NewDataFileSlots(2, 2)
			Expect(slots.Take(1)).To(Equal(backup.DataFileSlot{DataFile: 0, Slot: 0}))
			second := slots.Take(2)
			Expect(second).To(Equal(backup.DataFileSlot{DataFile: 1, Slot: 0}))
			Expect(slots.Take(3)).To(Equal(backup.DataFileSlot{DataFile: 0, Slot: 1}))
			slots.Release(second)
			// Data file 1 has finished its table while data file 0 still has two in progress
			Expect(slots.Take(4)).To(Equal(backup.DataFileSlot{DataFile: 1, Slot: 1}))
			Expect(slots.Take(5)).To(Equal(backup.DataFileSlot{DataFile: 1, Slot: 2}))
			Expect(slots.GetNextSlots()).To(Equal([]int{2, 3}))
			Expect(slots.GetDataFile(5)).To(Equal(1))
		})
		It("waits for a free slot when every data file has as many COPY commands in progress as it has pipes", func() {
			slots := backup.NewDataFileSlots(2, 1)
			first := slots.Take(1)
			slots.Take(2)
			taken := make(chan backup.DataFileSlot)
			go func() {
				taken <- slots.Take(3)
			}()
			Consistently(taken, "100ms").ShouldNot(Receive())
			slots.Release(first)
			Eventually(taken).Should(Receive(Equal(backup.DataFileSlot{DataFile: 0, Slot: 1})))
		})
		It("orders the tables by slot, followed by the tables without a slot", func() {
			slots := backup.NewDataFileSlots(2, 2)
			slots.Take(3)
			slots.Take(1)
			tables := []backup.Table{{Relation: backup.Relation{Oid: 1}}, {Relation: backup.Relation{Oid: 2}}, {Relation: backup.Relation{Oid: 3}}}
			ordered := slots.OrderTables(tables)
			Expect([]uint32{ordered[0].Oid, ordered[1].Oid, ordered[2].Oid}).To(Equal([]uint32{3, 1, 2}))
		})
	})
	Describe("CopyTableOut", func() {
		testTable := backup.Table{Relation: backup.Relation{SchemaOid: 2345, Oid: 3456, Schema: "public", Name: "foo"}}
		It("will back up a table to its own file with gzip compression", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "gzip", OutputCommand: "gzip -c -8", InputCommand: "gzip -d -c", Extension: ".gz"})
			execStr := regexp.QuoteMeta("COPY public.foo TO PROGRAM 'gzip -c -8 > <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.gz' WITH CSV DELIMITER ',' ON SEGMENT IGNORE EXTERNAL PARTITIONS;")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.gz"

			_, err := backup.CopyTableOut(context.Background(), connectionPool, testTable, filename, defaultConnNum)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will back up a table to its own file with gzip compression using a plugin", func() {
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "/tmp/plugin_config")
			pluginConfig := utils.PluginConfig{ExecutablePath: "/tmp/fake-plugin.sh", ConfigPath: "/tmp/plugin_config"}
			backup.SetPluginConfig(&pluginConfig)
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "gzip", OutputCommand: "gzip -c -8", InputCommand: "gzip -d -c", Extension: ".gz"})
			execStr := regexp.QuoteMeta("COPY public.foo TO PROGRAM 'gzip -c -8 | /tmp/fake-plugin.sh backup_data /tmp/plugin_config <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT IGNORE EXTERNAL PARTITIONS;")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))

			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"
			_, err := backup.CopyTableOut(context.Background(), connectionPool, testTable, filename, defaultConnNum)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will back up a table to its own file with zstd compression", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "zstd", OutputCommand: "zstd --compress -3 -c", InputCommand: "zstd --decompress -c", Extension: ".zst"})
			execStr := regexp.QuoteMeta("COPY public.foo TO PROGRAM 'zstd --compress -3 -c > <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.zst' WITH CSV DELIMITER ',' ON SEGMENT IGNORE EXTERNAL PARTITIONS;")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.zst"

			_, err := backup.CopyTableOut(context.Background(), connectionPool, testTable, filename, defaultConnNum)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will back up a table to its own file with zstd compression using a plugin", func() {
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "/tmp/plugin_config")
			pluginConfig := utils.PluginConfig{ExecutablePath: "/tmp/fake-plugin.sh", ConfigPath: "/tmp/plugin_config"}
			backup.SetPluginConfig(&pluginConfig)
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "zstd", OutputCommand: "zstd --compress -3 -c", InputCommand: "zstd --decompress -c", Extension: ".zst"})
			execStr := regexp.QuoteMeta("COPY public.foo TO PROGRAM 'zstd --compress -3 -c | /tmp/fake-plugin.sh backup_data /tmp/plugin_config <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT IGNORE EXTERNAL PARTITIONS;")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))

			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"
			_, err := backup.CopyTableOut(context.Background(), connectionPool, testTable, filename, defaultConnNum)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will back up a table to its own file without compression", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "cat", OutputCommand: "cat -", InputCommand: "cat -", Extension: ""})
			execStr := regexp.QuoteMeta("COPY public.foo TO PROGRAM 'cat - > <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT IGNORE EXTERNAL PARTITIONS;")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"

			_, err := backup.CopyTableOut(context.Background(), connectionPool, testTable, filename, defaultConnNum)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will back up a table to its own file without compression using a plugin", func() {
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "/tmp/plugin_config")
			pluginConfig := utils.PluginConfig{ExecutablePath: "/tmp/fake-plugin.sh", ConfigPath: "/tmp/plugin_config"}
			backup.SetPluginConfig(&pluginConfig)
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "cat", OutputCommand: "cat -", InputCommand: "cat -", Extension: ""})
			execStr := regexp.QuoteMeta("COPY public.foo TO PROGRAM 'cat - | /tmp/fake-plugin.sh backup_data /tmp/plugin_config <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT IGNORE EXTERNAL PARTITIONS;")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))

			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"
			_, err := backup.CopyTableOut(context.Background(), connectionPool, testTable, filename, defaultConnNum)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will back up a table to a single file", func() {
			_ = cmdFlags.Set(options.SINGLE_DATA_FILE, "true")
			execStr := regexp.QuoteMeta(`COPY public.foo TO PROGRAM '(test -p "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456" || (echo "Pipe not found <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456">&2; exit 1)) && cat - > <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT IGNORE EXTERNAL PARTITIONS;`)
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"

			_, err := backup.CopyTableOut(context.Background(), connectionPool, testTable, filename, defaultConnNum)

			Expect(err).ShouldNot(HaveOccurred())
		})
	})
	Describe("BackupSingleTableData", func() {
		var (
			testTable     backup.Table
			rowsCopiedMap map[uint32]int64
			counters      backup.BackupProgressCounters
			copyFmtStr    = "COPY(.*)%s(.*)"
		)
		BeforeEach(func() {
			testTable = backup.Table{
				Relation:        backup.Relation{Oid: 0, Schema: "public", Name: "testtable"},
				TableDefinition: backup.TableDefinition{IsExternal: false},
			}
			_ = cmdFlags.Set(options.SINGLE_DATA_FILE, "false")
			rowsCopiedMap = make(map[uint32]int64)
			counters = backup.BackupProgressCounters{NumRegTables: 0, TotalRegTables: 1}
			counters.ProgressBar = utils.NewProgressBar(int(counters.TotalRegTables), "Tables backed up: ", utils.PB_INFO)
			counters.ProgressBar.(*pb.ProgressBar).NotPrint = true
			counters.ProgressBar.Start()
		})
		It("backs up a single regular table with single data file", func() {
			_ = cmdFlags.Set(options.SINGLE_DATA_FILE, "true")

			backupFile := fmt.Sprintf("<SEG_DATA_DIR>/gpbackup_<SEGID>_20170101010101_\\d+_pipe_%d", testTable.Oid)
			copyCmd := fmt.Sprintf(copyFmtStr, backupFile)
			mock.ExpectExec(copyCmd).WillReturnResult(sqlmock.NewResult(0, 10))
			err := backup.BackupSingleTableData(context.Background(), testTable, rowsCopiedMap, &counters, 0)

			Expect(err).ShouldNot(HaveOccurred())
			Expect(rowsCopiedMap[0]).To(Equal(int64(10)))
			Expect(counters.NumRegTables).To(Equal(int64(1)))
		})
		It("backs up a table to the pipe of its slot and writes its oid next to it with several data files", func() {
			_ = cmdFlags.Set(options.SINGLE_DATA_FILE, "true")
			testTable.Oid = 3456
			slots := backup.NewDataFileSlots(2, 1)
			slots.Take(1)
			backup.SetDataFiles(slots)
			defer backup.SetDataFiles(nil)

			slotPipe := `<SEG_DATA_DIR>/gpbackup_<SEGID>_20170101010101_\d+_pipe1_0`
			copyCmd := fmt.Sprintf(`COPY public.testtable TO PROGRAM '\(test -p "%[1]s" \|\| \(echo "Pipe not found %[1]s">&2; exit 1\)\) && echo 3456 > "%[1]s_oid" && cat - > %[1]s'`, slotPipe)
			mock.ExpectExec(copyCmd).WillReturnResult(sqlmock.NewResult(0, 10))
			err := backup.BackupSingleTableData(context.Background(), testTable, rowsCopiedMap, &counters, 0)

			Expect(err).ShouldNot(HaveOccurred())
			Expect(rowsCopiedMap[3456]).To(Equal(int64(10)))
			// The slot is free again once the COPY is done
			Expect(slots.Take(2)).To(Equal(backup.DataFileSlot{DataFile: 1, Slot: 1}))
		})
		It("backs up a single regular table without a single data file", func() {
			_ = cmdFlags.Set(options.SINGLE_DATA_FILE, "false")

			backupFile := fmt.Sprintf("<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_%d", testTable.Oid)
			copyCmd := fmt.Sprintf(copyFmtStr, backupFile)
			mock.ExpectExec(copyCmd).WillReturnResult(sqlmock.NewResult(0, 10))
			err := backup.BackupSingleTableData(context.Background(), testTable, rowsCopiedMap, &counters, 0)

			Expect(err).ShouldNot(HaveOccurred())
			Expect(rowsCopiedMap[0]).To(Equal(int64(10)))
			Expect(counters.NumRegTables).To(Equal(int64(1)))
		})
	})
	Describe("GetBackupDataSet", func() {
		config := history.BackupConfig{}
		var testTable backup.Table
		BeforeEach(func() {
			config.MetadataOnly = false
			backup.SetReport(&report.Report{BackupConfig: config})
			testTable = backup.Table{
				Relation:        backup.Relation{Schema: "public", Name: "testtable"},
				TableDefinition: backup.TableDefinition{},
			}
		})
		It("excludes a foreign table", func() {
			foreignDef := backup.ForeignTableDefinition{Oid: 23, Options: "", Server: "fs"}
			testTable.ForeignDef = foreignDef
			tables := []backup.Table{testTable}

			dataTables, numExtOrForeignTables := backup.GetBackupDataSet(tables)
			Expect(len(dataTables)).To(Equal(0))
			Expect(numExtOrForeignTables).To(Equal(int64(1)))
		})
		It("excludes an external table", func() {
			testTable.IsExternal = true
			tables := []backup.Table{testTable}

			dataTables, numExtOrForeignTables := backup.GetBackupDataSet(tables)
			Expect(len(dataTables)).To(Equal(0))
			Expect(numExtOrForeignTables).To(Equal(int64(1)))
		})
		It("doesn't exclude regular table", func() {
			tables := []backup.Table{testTable}

			dataTables, numExtOrForeignTables := backup.GetBackupDataSet(tables)
			Expect(len(dataTables)).To(Equal(1))
			Expect(numExtOrForeignTables).To(Equal(int64(0)))
		})
		It("returns an empty set, if --metadata-only flag is set and a regular table is given", func() {
			config.MetadataOnly = true
			backup.SetReport(&report.Report{BackupConfig: config})
			tables := []backup.Table{testTable}

			dataTables, numExtOrForeignTables := backup.GetBackupDataSet(tables)
			Expect(len(dataTables)).To(Equal(0))
			Expect(numExtOrForeignTables).To(Equal(int64(0)))
		})
	})
	Describe("SplitOutQDOnlyTables", func() {
		config := history.BackupConfig{}
		condition := ""
		var qdOnlyTable, regularTable backup.Table
		BeforeEach(func() {
			config.MetadataOnly = false
			backup.SetReport(&report.Report{BackupConfig: config})
			qdOnlyTable = backup.Table{
				Relation: backup.Relation{Schema: "local_ext", Name: "cfg"},
				TableDefinition: backup.TableDefinition{
					ExtensionTableConfig: &condition,
				},
			}
			regularTable = backup.Table{
				Relation:        backup.Relation{Schema: "public", Name: "testtable"},
				TableDefinition: backup.TableDefinition{},
			}
		})
		It("separates QD-only tables from regular tables", func() {
			tables := []backup.Table{qdOnlyTable, regularTable}

			qdOnlyTables, remaining := backup.SplitOutQDOnlyTables(tables)

			Expect(qdOnlyTables).To(HaveLen(1))
			Expect(qdOnlyTables[0].Table).To(Equal(qdOnlyTable))
			Expect(remaining).To(Equal([]backup.Table{regularTable}))
		})
		It("does not treat a distributed table with an extension config as QD-only", func() {
			distributedExtTable := qdOnlyTable
			distributedExtTable.DistPolicy = backup.DistPolicy{Policy: "DISTRIBUTED BY (id)"}
			tables := []backup.Table{distributedExtTable}

			qdOnlyTables, remaining := backup.SplitOutQDOnlyTables(tables)

			Expect(qdOnlyTables).To(BeEmpty())
			Expect(remaining).To(Equal([]backup.Table{distributedExtTable}))
		})
		It("treats every table as regular during a metadata-only backup, even one that would otherwise be QD-only", func() {
			config.MetadataOnly = true
			backup.SetReport(&report.Report{BackupConfig: config})
			tables := []backup.Table{qdOnlyTable, regularTable}

			qdOnlyTables, remaining := backup.SplitOutQDOnlyTables(tables)

			Expect(qdOnlyTables).To(BeEmpty())
			Expect(remaining).To(Equal(tables))
		})
		It("returns nothing for an empty input", func() {
			qdOnlyTables, remaining := backup.SplitOutQDOnlyTables([]backup.Table{})

			Expect(qdOnlyTables).To(BeEmpty())
			Expect(remaining).To(BeEmpty())
		})
	})
})
