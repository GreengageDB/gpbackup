package integration

import (
	"strings"

	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/backup"
	"github.com/GreengageDB/gpbackup/options"
	"github.com/GreengageDB/gpbackup/restore"
	"github.com/GreengageDB/gpbackup/testutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

/*
 * The data restore locks the ancestors of the partitions by the names of the data
 * entries, so the names that these queries return must be those that the backup
 * writes to its TOC.  A name of another form would silently leave a table out.
 */
var _ = Describe("restore integration tests of the partitioned tables", func() {
	const (
		root          = `"Part Schema"."Root"`
		intermediate1 = `"Part Schema"."Root_1_prt_p1"`
		intermediate2 = `"Part Schema"."Root_1_prt_p2"`
	)
	// The leaf partitions and their ancestors
	leaves := map[string][]string{
		`"Part Schema"."Root_1_prt_p1_2_prt_s1"`: {intermediate1, root},
		`"Part Schema"."Root_1_prt_p1_2_prt_s2"`: {intermediate1, root},
		`"Part Schema"."Root_1_prt_p2_2_prt_s1"`: {intermediate2, root},
		`"Part Schema"."Root_1_prt_p2_2_prt_s2"`: {intermediate2, root},
	}
	// The names of the tables of the schema that the backup writes to its TOC
	backupNames := func() []string {
		names := make([]string, 0)
		for _, table := range backup.ConvertRelationsOptionsToBackup(backup.GetIncludedUserTableRelations(connectionPool, []options.Relation{})) {
			if strings.HasPrefix(table.FQN(), `"Part Schema".`) {
				names = append(names, table.FQN())
			}
		}
		return names
	}
	BeforeEach(func() {
		restore.SetConnection(connectionPool)
		// A schema and tables whose names are quoted
		testhelper.AssertQueryRuns(connectionPool, `CREATE SCHEMA "Part Schema";
			CREATE TABLE "Part Schema"."Root" (a int, b int) DISTRIBUTED BY (a)
				PARTITION BY RANGE (a) SUBPARTITION BY LIST (b)
				SUBPARTITION TEMPLATE (SUBPARTITION s1 VALUES (1), SUBPARTITION s2 VALUES (2))
				(PARTITION p1 START (1) END (10), PARTITION p2 START (10) END (20));
			CREATE TABLE "Part Schema".parent (i int) DISTRIBUTED BY (i);
			CREATE TABLE "Part Schema".child () INHERITS ("Part Schema".parent);
			CREATE TABLE "Part Schema".plain (i int) DISTRIBUTED BY (i);`)
		DeferCleanup(func() {
			testhelper.AssertQueryRuns(connectionPool, `DROP SCHEMA "Part Schema" CASCADE`)
		})
	})
	Describe("GetPartitionAncestorFQNs", func() {
		BeforeEach(func() {
			testutils.SkipIfBefore7(connectionPool)
		})
		It("returns the ancestors of each partition", func() {
			ancestors, err := restore.GetPartitionAncestorFQNs()
			Expect(err).ToNot(HaveOccurred())
			for leaf, leafAncestors := range leaves {
				Expect(ancestors[leaf]).To(ConsistOf(leafAncestors), leaf)
			}
			Expect(ancestors[intermediate1]).To(ConsistOf(root))
			Expect(ancestors[intermediate2]).To(ConsistOf(root))
			// Nor a child of an inheritance hierarchy or a table that is not a partition
			Expect(ancestors).ToNot(HaveKey(root))
			Expect(ancestors).ToNot(HaveKey(`"Part Schema".child`))
			Expect(ancestors).ToNot(HaveKey(`"Part Schema".plain`))
		})
		It("returns the ancestors of the leaf partitions by the names that the backup gives them", func() {
			_ = backupCmdFlags.Set(options.LEAF_PARTITION_DATA, "true")
			ancestors, err := restore.GetPartitionAncestorFQNs()
			Expect(err).ToNot(HaveOccurred())
			leavesOfBackup := make([]string, 0)
			for _, name := range backupNames() {
				if _, ok := leaves[name]; ok {
					leavesOfBackup = append(leavesOfBackup, name)
					Expect(ancestors).To(HaveKey(name))
				}
			}
			Expect(leavesOfBackup).To(HaveLen(len(leaves)))
		})
	})
})
