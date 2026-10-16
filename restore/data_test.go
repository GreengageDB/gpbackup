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
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file with zstd compression", func() {
			utils.SetPipeThroughProgram(utils.PipeThroughProgram{Name: "zstd", OutputCommand: "zstd --compress -1 -c", InputCommand: "zstd --decompress -c", Extension: ".zst"})
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM 'cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.zst | zstd --decompress -c' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456.zst"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file without compression", func() {
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM 'cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_3456"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from a single data file", func() {
			execStr := regexp.QuoteMeta(fmt.Sprintf("COPY public.foo(i,j) FROM PROGRAM '(timeout --foreground 300 bash -c \"while [[ ! -p \"<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456\" && ! -f \"<SEG_DATA_DIR>/gpbackup_<SEGID>_20170101010101_%d_error\" ]]; do sleep 1; done\" || (echo \"Pipe not found <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456\">&2; exit 1)) && cat <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456' WITH CSV DELIMITER ',' ON SEGMENT", os.Getpid()))
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))
			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, true, 0)

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
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0)

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
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0)

			Expect(err).ShouldNot(HaveOccurred())
		})
		It("will restore a table from its own file without compression using a plugin", func() {
			_ = cmdFlags.Set(options.PLUGIN_CONFIG, "/tmp/plugin_config")
			pluginConfig := utils.PluginConfig{ExecutablePath: "/tmp/fake-plugin.sh", ConfigPath: "/tmp/plugin_config"}
			restore.SetPluginConfig(&pluginConfig)
			execStr := regexp.QuoteMeta("COPY public.foo(i,j) FROM PROGRAM '/tmp/fake-plugin.sh restore_data /tmp/plugin_config <SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.gz' WITH CSV DELIMITER ',' ON SEGMENT")
			mock.ExpectExec(execStr).WillReturnResult(sqlmock.NewResult(10, 0))

			filename := "<SEG_DATA_DIR>/backups/20170101/20170101010101/gpbackup_<SEGID>_20170101010101_pipe_3456.gz"
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0)

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
			_, err := restore.CopyTableIn(context.Background(), connectionPool, "public.foo", "(i,j)", filename, false, 0)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("Error loading data into table public.foo: " +
				"COPY foo, line 1: \"5\": " +
				"ERROR: value of distribution key doesn't belong to segment with ID 0, it belongs to segment with ID 1 (SQLSTATE 22P04)"))
		})
	})
	Describe("GetEntryIdxsForConns", func() {
		entries := []toc.CoordinatorDataEntry{
			{Oid: 1, DataFileIdx: 0}, {Oid: 2, DataFileIdx: 1}, {Oid: 3, DataFileIdx: 2},
			{Oid: 4, DataFileIdx: 0}, {Oid: 5, DataFileIdx: 2}, {Oid: 6, DataFileIdx: 1},
		}
		It("spreads the entries round-robin when there is a single data file", func() {
			Expect(restore.GetEntryIdxsForConns(entries, 1, 2, 1)).To(Equal([][]int{{0, 2, 4}, {1, 3, 5}}))
		})
		It("gives all entries of a data file to one connection, in the order of the file", func() {
			Expect(restore.GetEntryIdxsForConns(entries, 3, 3, 1)).To(Equal([][]int{{0, 3}, {1, 5}, {2, 4}}))
		})
		It("gives several data files to a connection when there are fewer connections than data files", func() {
			Expect(restore.GetEntryIdxsForConns(entries, 3, 2, 1)).To(Equal([][]int{{0, 2, 3, 4}, {1, 5}}))
			Expect(restore.GetEntryIdxsForConns(entries, 3, 1, 1)).To(Equal([][]int{{0, 1, 2, 3, 4, 5}}))
		})
		It("spreads the entries of a data file round-robin over its group of connections", func() {
			// Groups {0, 1} and {2, 3}: data files 0 and 2 go to the first, data file 1 to the second
			Expect(restore.GetEntryIdxsForConns(entries, 3, 4, 2)).To(Equal([][]int{{0, 2}, {3, 4}, {1}, {5}}))
		})
		It("keeps a connection within the pipes of its data file when a group restores several data files", func() {
			// With one group of 4 connections, the entries of data file 1 must not shift the
			// round-robin of data file 0, or connection 3 would wait for its 10th table
			// while the helper of data file 0 is still on the first one
			dataFiles := []int{0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0}
			manyEntries := make([]toc.CoordinatorDataEntry, 0, len(dataFiles))
			for i, dataFile := range dataFiles {
				manyEntries = append(manyEntries, toc.CoordinatorDataEntry{Oid: uint32(i + 1), DataFileIdx: dataFile})
			}
			Expect(restore.GetEntryIdxsForConns(manyEntries, 2, 4, 4)).To(Equal([][]int{{0, 3, 5, 10}, {1, 6, 7, 11}, {2, 8}, {4, 9}}))
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
