package restore_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GreengageDB/gp-common-go-libs/cluster"
	"github.com/GreengageDB/gpbackup/backup"
	"github.com/GreengageDB/gpbackup/history"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/GreengageDB/gpbackup/restore"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"
	"github.com/jackc/pgconn"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("restore/data tests", func() {
	Describe("CopyTableIn", func() {
		BeforeEach(func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "cat", OutputCommand: utils.DefaultPipeThroughProgram, InputCommand: utils.DefaultPipeThroughProgram, Extension: ""})
			backup.SetPluginConfig(nil)
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "")
			backup.SetCluster(&cluster.Cluster{ContentIDs: []int{-1, 0, 1, 2}})
			restore.SetBackupConfig(&history.BackupConfig{})
		})
		It("will restore a table from its own file with gzip compression", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "gzip", OutputCommand: "gzip -c -1", InputCommand: "gzip -d -c", Extension: ".gz"})
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM 'cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.gz | gzip -d -c' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.gz"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0, "")

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file with zstd compression", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "zstd", OutputCommand: "zstd --compress -1 -c", InputCommand: "zstd --decompress -c", Extension: ".zst"})
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM 'cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.zst | zstd --decompress -c' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.zst"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0, "")

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file without compression", func() {
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM 'cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0, "")

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from a single data file", func() {
			execStr := regexp.QuoteMeta(fmt.Sprintf("COPY public.foo(i,j) FROM PROGRAM '(timeout --foreground 300 bash -c \"while [[ ! -p \"<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456\" && ! -f \"<SEG_DATA_DIR>/gpbackup_<SEGID>_20170101010101_%d_error\" ]]; do sleep 1; done\" || (echo \"Pipe not found <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456\">&2; exit 1)) && (test ! -f \"<SEG_DATA_DIR>/gpbackup_<SEGID>_20170101010101_%[1]d_error\" || (echo \"Error file found <SEG_DATA_DIR>/gpbackup_<SEGID>_20170101010101_%[1]d_error\">&2; exit 1)) && cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456' WITH CSV DELIMITER ',' ON SEGMENT", os.Getpid()))
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, true, 0, fmt.Sprintf("<SEG_DATA_DIR>/gpbackup_<SEGID>_20170101010101_%d_error", os.Getpid()))

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file with gzip compression using a plugin", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "gzip", OutputCommand: "gzip -c -1", InputCommand: "gzip -d -c", Extension: ".gz"})
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "/tmp/plugin_config")
			pluginConfig := utils.PluginConfig{ExecutablePath: "/tmp/fake-plugin.sh", ConfigPath: "/tmp/plugin_config"}
			restore.SetPluginConfig(&pluginConfig)
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM '/tmp/fake-plugin.sh restore_data /tmp/plugin_config <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.gz | gzip -d -c' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))

			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.gz"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0, "")

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file with zstd compression using a plugin", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "zstd", OutputCommand: "zstd --compress -1 -c", InputCommand: "zstd --decompress -c", Extension: ".zst"})
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "/tmp/plugin_config")
			pluginConfig := utils.PluginConfig{ExecutablePath: "/tmp/fake-plugin.sh", ConfigPath: "/tmp/plugin_config"}
			restore.SetPluginConfig(&pluginConfig)
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM '/tmp/fake-plugin.sh restore_data /tmp/plugin_config <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.zst | zstd --decompress -c' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))

			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.zst"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0, "")

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file without compression using a plugin", func() {
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "/tmp/plugin_config")
			pluginConfig := utils.PluginConfig{ExecutablePath: "/tmp/fake-plugin.sh", ConfigPath: "/tmp/plugin_config"}
			restore.SetPluginConfig(&pluginConfig)
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM '/tmp/fake-plugin.sh restore_data /tmp/plugin_config <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.gz' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))

			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.gz"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0, "")

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will output expected error string from COPY ON SEGMENT failure", func() {
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM 'cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT")
			pgErr := &pgconn.PgError{
				Severity: "ERROR",
				Code:     "22P04",
				Message:  "value of distribution key doesn't belong to segment with ID 0, it belongs to segment with ID 1",
				Where:    "COPY foo, line 1: \"5\"",
			}
			mock.ExpectExec(execStr).WillReturnError(pgErr)
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0, "")

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("Error loading data into table public.foo: " +
				"COPY foo, line 1: \"5\": " +
				"ERROR: value of distribution key doesn't belong to segment with ID 0, it belongs to segment with ID 1 (SQLSTATE 22P04)"))
		})
	})
	Describe("DataFileGroups", func() {
		// The entries each connection restores, the connections taking their next entry in turn
		entriesOfConns := func(groups *restore.DataFileGroups, numConns int) [][]int {
			nextEntryIdx := make([]func() (int, bool), numConns)
			entries := make([][]int, numConns)
			for conn := range nextEntryIdx {
				nextEntryIdx[conn] = groups.EntriesOfConn(context.Background(), conn)
				entries[conn] = []int{}
			}
			for done := 0; done < numConns; {
				done = 0
				for conn := range nextEntryIdx {
					if entryIdx, ok := nextEntryIdx[conn](); ok {
						entries[conn] = append(entries[conn], entryIdx)
					} else {
						done++
					}
				}
			}
			return entries
		}
		// Data files 0, 1 and 2 have 20, 100 and 60 rows
		entries := []toc.CoordinatorDataEntry{
			{Oid: 1, DataFileIdx: 0, RowsCopied: 10}, {Oid: 2, DataFileIdx: 1, RowsCopied: 50}, {Oid: 3, DataFileIdx: 2, RowsCopied: 30},
			{Oid: 4, DataFileIdx: 0, RowsCopied: 10}, {Oid: 5, DataFileIdx: 1, RowsCopied: 50}, {Oid: 6, DataFileIdx: 2, RowsCopied: 30},
		}
		It("spreads the entries round-robin over all connections when there is a single data file", func() {
			singleFileEntries := []toc.CoordinatorDataEntry{{Oid: 1}, {Oid: 2}, {Oid: 3}, {Oid: 4}, {Oid: 5}}
			groups := restore.NewDataFileGroups(singleFileEntries, 1, 2, 2)
			Expect(entriesOfConns(groups, 2)).To(Equal([][]int{{0, 2, 4}, {1, 3}}))
		})
		It("gives the largest data file left to the group that has finished its data file", func() {
			// Connection 0 takes data file 1 and connection 1 data file 2; connection 0 is then
			// done first and takes data file 0 as well
			groups := restore.NewDataFileGroups(entries, 3, 2, 1)
			Expect(entriesOfConns(groups, 2)).To(Equal([][]int{{1, 4, 0, 3}, {2, 5}}))
		})
		It("spreads the entries of each data file round-robin over the connections of its group", func() {
			groups := restore.NewDataFileGroups(entries, 3, 2, 2)
			Expect(entriesOfConns(groups, 2)).To(Equal([][]int{{1, 2, 0}, {4, 5, 3}}))
		})
		It("lets a connection of a group go on to the next data file of its group", func() {
			// Data file 1 has a single entry, which connection 0 restores while connection 1 takes data file 0
			fewEntries := []toc.CoordinatorDataEntry{
				{Oid: 1, DataFileIdx: 0, RowsCopied: 10}, {Oid: 2, DataFileIdx: 1, RowsCopied: 50}, {Oid: 3, DataFileIdx: 0, RowsCopied: 10},
			}
			groups := restore.NewDataFileGroups(fewEntries, 2, 2, 2)
			Expect(entriesOfConns(groups, 2)).To(Equal([][]int{{1, 2}, {0}}))
		})
		It("takes the next data file of a group only for a connection that restores its first entry", func() {
			// Three data files of one table each, restored by one group of two connections
			singleEntryFiles := []toc.CoordinatorDataEntry{
				{Oid: 1, DataFileIdx: 0, RowsCopied: 30}, {Oid: 2, DataFileIdx: 1, RowsCopied: 20}, {Oid: 3, DataFileIdx: 2, RowsCopied: 10},
			}
			groups := restore.NewDataFileGroups(singleEntryFiles, 3, 2, 2)
			started := make([]int, 0)
			groups.StartDataFile = func(file int) { started = append(started, file) }
			nextOfConn0, nextOfConn1 := groups.EntriesOfConn(context.Background(), 0), groups.EntriesOfConn(context.Background(), 1)
			nextEntry := func(nextEntryIdx func() (int, bool)) int {
				entryIdx, ok := nextEntryIdx()
				Expect(ok).To(BeTrue())
				return entryIdx
			}
			Expect(nextEntry(nextOfConn0)).To(Equal(0))
			// Connection 1 has no entry in data file 0, so it takes data file 1 for the group and restores it
			Expect(nextEntry(nextOfConn1)).To(Equal(1))
			Expect(started).To(Equal([]int{0, 1}))
			// Data file 2 is not taken until a connection is free to restore it
			Expect(nextEntry(nextOfConn0)).To(Equal(2))
			Expect(started).To(Equal([]int{0, 1, 2}))
			_, ok := nextOfConn1()
			Expect(ok).To(BeFalse())
		})
		It("gives no entries to the connections beyond the last full group", func() {
			groups := restore.NewDataFileGroups(entries, 3, 3, 2)
			Expect(entriesOfConns(groups, 3)).To(Equal([][]int{{1, 2, 0}, {4, 5, 3}, {}}))
		})
		It("starts each data file once, before the first of its entries is given", func() {
			groups := restore.NewDataFileGroups(entries, 3, 2, 1)
			started := make([]int, 0)
			groups.StartDataFile = func(file int) { started = append(started, file) }
			nextEntryIdx := groups.EntriesOfConn(context.Background(), 0)
			entryIdx, _ := nextEntryIdx()
			// Connection 0 takes data file 1, the largest
			Expect(entryIdx).To(Equal(1))
			Expect(started).To(Equal([]int{1}))
			nextEntryIdx()
			Expect(started).To(Equal([]int{1}))
			entryIdx, _ = nextEntryIdx()
			Expect(entryIdx).To(Equal(2))
			Expect(started).To(Equal([]int{1, 2}))
		})
		It("gives the oids of the entries of a data file", func() {
			groups := restore.NewDataFileGroups(entries, 3, 2, 1)
			Expect(groups.OidsOfDataFile(entries, 2)).To(Equal([]uint32{3, 6}))
		})
		It("skips data files without entries", func() {
			groups := restore.NewDataFileGroups([]toc.CoordinatorDataEntry{{Oid: 1, DataFileIdx: 2}}, 3, 2, 1)
			Expect(entriesOfConns(groups, 2)).To(Equal([][]int{{0}, {}}))
		})
		Describe("the turns of the transactions", func() {
			// Whether the transaction of an entry, waiting for its turn in the background, may go on
			waitForTurn := func(groups *restore.DataFileGroups, ctx context.Context, entryIdx int) chan bool {
				mayBegin := make(chan bool, 1)
				go func() { mayBegin <- groups.WaitForTurn(ctx, entryIdx) }()
				return mayBegin
			}
			It("begins the transactions of the tables of each data file in the order of the file", func() {
				groups := restore.NewDataFileGroups(entries, 3, 4, 2)
				groups.OrderTransactions = true
				Expect(groups.WaitForTurn(context.Background(), 0)).To(BeTrue())
				// Entry 3 comes after entry 0 in data file 0, entry 4 first in data file 1
				mayBegin := waitForTurn(groups, context.Background(), 3)
				Expect(groups.WaitForTurn(context.Background(), 1)).To(BeTrue())
				groups.TurnDone(1)
				Consistently(mayBegin, "200ms").ShouldNot(Receive())
				groups.TurnDone(0)
				Eventually(mayBegin).Should(Receive(BeTrue()))
			})
			It("lets a table be skipped before the transactions of the tables before it take their turns", func() {
				singleFileEntries := []toc.CoordinatorDataEntry{{Oid: 1}, {Oid: 2}, {Oid: 3}, {Oid: 4}}
				groups := restore.NewDataFileGroups(singleFileEntries, 1, 2, 2)
				groups.OrderTransactions = true
				groups.TurnDone(1)
				mayBegin := waitForTurn(groups, context.Background(), 2)
				Consistently(mayBegin, "200ms").ShouldNot(Receive())
				groups.TurnDone(0)
				Eventually(mayBegin).Should(Receive(BeTrue()))
			})
			It("stops waiting once the context is done", func() {
				groups := restore.NewDataFileGroups(entries, 3, 4, 2)
				groups.OrderTransactions = true
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				groups.WakeWhenDone(ctx)
				mayBegin := waitForTurn(groups, ctx, 3)
				Consistently(mayBegin, "200ms").ShouldNot(Receive())
				cancel()
				Eventually(mayBegin).Should(Receive(BeFalse()))
			})
			It("does not wait without OrderTransactions", func() {
				groups := restore.NewDataFileGroups(entries, 3, 4, 2)
				Expect(groups.WaitForTurn(context.Background(), 3)).To(BeTrue())
			})
		})
		Describe("the tables ahead of the first one whose COPY has not ended", func() {
			// The next entry of a connection, given in the background, or -1 if there is none
			nextEntry := func(nextEntryIdx func() (int, bool)) chan int {
				given := make(chan int, 1)
				go func() {
					entryIdx, ok := nextEntryIdx()
					if !ok {
						entryIdx = -1
					}
					given <- entryIdx
				}()
				return given
			}
			singleFileEntries := []toc.CoordinatorDataEntry{{Oid: 1}, {Oid: 2}, {Oid: 3}, {Oid: 4}}
			It("gives a table only fewer than connsPerDataFile tables after the first one whose COPY has not ended", func() {
				groups := restore.NewDataFileGroups(singleFileEntries, 1, 2, 2)
				groups.LimitAhead = true
				nextOfConn0, nextOfConn1 := groups.EntriesOfConn(context.Background(), 0), groups.EntriesOfConn(context.Background(), 1)
				Eventually(nextEntry(nextOfConn0)).Should(Receive(Equal(0)))
				Eventually(nextEntry(nextOfConn1)).Should(Receive(Equal(1)))
				// The COPY of entry 1 ends at once, as it fails, while entry 0 is restored
				groups.CopyDone(1)
				given := nextEntry(nextOfConn1)
				Consistently(given, "200ms").ShouldNot(Receive())
				groups.CopyDone(0)
				Eventually(given).Should(Receive(Equal(2)))
				Eventually(nextEntry(nextOfConn0)).Should(Receive(Equal(3)))
			})
			It("stops waiting once the context is done", func() {
				groups := restore.NewDataFileGroups(singleFileEntries, 1, 2, 2)
				groups.LimitAhead = true
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				groups.WakeWhenDone(ctx)
				nextOfConn0, nextOfConn1 := groups.EntriesOfConn(ctx, 0), groups.EntriesOfConn(ctx, 1)
				Eventually(nextEntry(nextOfConn0)).Should(Receive(Equal(0)))
				Eventually(nextEntry(nextOfConn1)).Should(Receive(Equal(1)))
				groups.CopyDone(1)
				given := nextEntry(nextOfConn1)
				Consistently(given, "200ms").ShouldNot(Receive())
				cancel()
				Eventually(given).Should(Receive(Equal(-1)))
			})
			It("does not wait without LimitAhead", func() {
				groups := restore.NewDataFileGroups(singleFileEntries, 1, 2, 2)
				Expect(entriesOfConns(groups, 2)).To(Equal([][]int{{0, 2}, {1, 3}}))
			})
		})
	})
	Describe("CheckRowsRestored", func() {
		var (
			expectedRows int64 = 10
			name               = "public.foo"
		)
		It("does nothing if the number of rows match ", func() {
			err := restore.CheckRowsRestored(10, expectedRows, name)
			Expect(err).ToNot(HaveOccurred())
		})
		It("returns an error if the numbers of rows do not match", func() {
			err := restore.CheckRowsRestored(5, expectedRows, name)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("Expected to restore 10 rows to table public.foo, but restored 5 instead"))
		})
	})
})

func batchMapToString(m map[int]map[int]int) string {
	outer := make([]string, len(m))
	for num, batch := range m {
		outer[num] = fmt.Sprintf("%d: %s", num, contentMapToString(batch))
	}
	return strings.Join(outer, "; ")
}

func contentMapToString(m map[int]int) string {
	inner := make([]string, len(m))
	index := 0
	for orig, dest := range m {
		inner[index] = fmt.Sprintf("%d:%d", orig, dest)
		index++
	}
	sort.Strings(inner)
	return fmt.Sprintf("{%s}", strings.Join(inner, ", "))
}
