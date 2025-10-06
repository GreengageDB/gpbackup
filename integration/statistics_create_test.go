package integration

import (
	"github.com/GreengageDB/gp-common-go-libs/structmatcher"
	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/GreengageDB/gpbackup/backup"
	"github.com/GreengageDB/gpbackup/testutils"
	"github.com/GreengageDB/gpbackup/toc"
	"github.com/GreengageDB/gpbackup/utils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func PrintStatisticsStatements(statisticsFile *utils.FileWithByteCount, tocfile *toc.TOC, tables []backup.Table, attStats map[uint32][]backup.AttributeStatistic, tupleStats map[uint32]backup.TupleStatistic) {
	for _, table := range tables {
		backup.PrintTupleStatisticsStatementForTable(statisticsFile, tocfile, table, tupleStats[table.Oid])
		for _, attStat := range attStats[table.Oid] {
			backup.PrintAttributeStatisticsStatementForTable(statisticsFile, tocfile, table, attStat)
		}
	}
}

var _ = Describe("backup integration tests", func() {
	BeforeEach(func() {
		tocfile, backupfile = testutils.InitializeTestTOC(buffer, "predata")
	})
	Describe("PrintStatisticsStatementsForTable", func() {
		It("prints attribute and tuple statistics for a table", func() {
			tables := []backup.Table{
				{Relation: backup.Relation{SchemaOid: 2200, Schema: "public", Name: "foo"}},
			}

			// Create and ANALYZE a table to generate statistics
			testhelper.AssertQueryRuns(connectionPool, "CREATE TABLE public.foo(i int, j text, k bool)")
			defer testhelper.AssertQueryRuns(connectionPool, "DROP TABLE public.foo")
			testhelper.AssertQueryRuns(connectionPool, "INSERT INTO public.foo VALUES (1, 'a', 't')")
			testhelper.AssertQueryRuns(connectionPool, "INSERT INTO public.foo VALUES (2, 'b', 'f')")
			testhelper.AssertQueryRuns(connectionPool, "ANALYZE public.foo")

			oldTableOid := testutils.OidFromObjectName(connectionPool, "public", "foo", backup.TYPE_RELATION)
			tables[0].Oid = oldTableOid

			beforeAttStats := make(map[uint32][]backup.AttributeStatistic)
			backup.GetAttributeStatistics(connectionPool, tables, func(attStat *backup.AttributeStatistic) {
				beforeAttStats[attStat.Oid] = append(beforeAttStats[attStat.Oid], *attStat)
			})
			beforeTupleStats := make(map[uint32]backup.TupleStatistic)
			backup.GetTupleStatistics(connectionPool, tables, func(tupleStat *backup.TupleStatistic) {
				beforeTupleStats[tupleStat.Oid] = *tupleStat
			})
			beforeTupleStat := beforeTupleStats[oldTableOid]

			// Drop and recreate the table to clear the statistics
			testhelper.AssertQueryRuns(connectionPool, "DROP TABLE public.foo")
			testhelper.AssertQueryRuns(connectionPool, "CREATE TABLE public.foo(i int, j text, k bool)")

			// Reload the retrieved statistics into the new table
			PrintStatisticsStatements(backupfile, tocfile, tables, beforeAttStats, beforeTupleStats)
			testhelper.AssertQueryRuns(connectionPool, buffer.String())

			newTableOid := testutils.OidFromObjectName(connectionPool, "public", "foo", backup.TYPE_RELATION)
			tables[0].Oid = newTableOid
			afterAttStats := make(map[uint32][]backup.AttributeStatistic)
			backup.GetAttributeStatistics(connectionPool, tables, func(attStat *backup.AttributeStatistic) {
				afterAttStats[attStat.Oid] = append(afterAttStats[attStat.Oid], *attStat)
			})
			afterTupleStats := make(map[uint32]backup.TupleStatistic)
			backup.GetTupleStatistics(connectionPool, tables, func(tupleStat *backup.TupleStatistic) {
				afterTupleStats[tupleStat.Oid] = *tupleStat
			})
			afterTupleStat := afterTupleStats[newTableOid]

			oldAtts := beforeAttStats[oldTableOid]
			newAtts := afterAttStats[newTableOid]

			// Ensure the statistics match
			Expect(afterTupleStats).To(HaveLen(len(beforeTupleStats)))
			structmatcher.ExpectStructsToMatchExcluding(&beforeTupleStat, &afterTupleStat, "Oid")
			Expect(oldAtts).To(HaveLen(3))
			Expect(newAtts).To(HaveLen(3))
			for i := range oldAtts {
				structmatcher.ExpectStructsToMatchExcluding(&oldAtts[i], &newAtts[i], "Oid", "Relid")
			}
		})
		It("prints attribute and tuple statistics for a table with dropped column", func() {
			tables := []backup.Table{
				{Relation: backup.Relation{SchemaOid: 2200, Schema: "public", Name: "foo"}},
			}

			// Create and ANALYZE a table to generate statistics
			testhelper.AssertQueryRuns(connectionPool, `CREATE TABLE public.foo(i int, j text, k bool, "i'2 3" int)`)
			defer testhelper.AssertQueryRuns(connectionPool, "DROP TABLE public.foo")
			testhelper.AssertQueryRuns(connectionPool, "INSERT INTO public.foo VALUES (1, 'a', 't', 1)")
			testhelper.AssertQueryRuns(connectionPool, "INSERT INTO public.foo VALUES (2, 'b', 'f', 2)")
			testhelper.AssertQueryRuns(connectionPool, "ANALYZE public.foo")
			testhelper.AssertQueryRuns(connectionPool, "ALTER TABLE public.foo DROP COLUMN j")

			oldTableOid := testutils.OidFromObjectName(connectionPool, "public", "foo", backup.TYPE_RELATION)
			tables[0].Oid = oldTableOid

			beforeAttStats := make(map[uint32][]backup.AttributeStatistic)
			backup.GetAttributeStatistics(connectionPool, tables, func(attStat *backup.AttributeStatistic) {
				beforeAttStats[attStat.Oid] = append(beforeAttStats[attStat.Oid], *attStat)
			})
			beforeTupleStats := make(map[uint32]backup.TupleStatistic)
			backup.GetTupleStatistics(connectionPool, tables, func(tupleStat *backup.TupleStatistic) {
				beforeTupleStats[tupleStat.Oid] = *tupleStat
			})
			beforeTupleStat := beforeTupleStats[oldTableOid]

			// Drop and recreate the table to clear the statistics
			testhelper.AssertQueryRuns(connectionPool, "DROP TABLE public.foo")
			testhelper.AssertQueryRuns(connectionPool, `CREATE TABLE public.foo(i int, k bool, "i'2 3" int)`)

			// Reload the retrieved statistics into the new table
			PrintStatisticsStatements(backupfile, tocfile, tables, beforeAttStats, beforeTupleStats)
			testhelper.AssertQueryRuns(connectionPool, buffer.String())

			newTableOid := testutils.OidFromObjectName(connectionPool, "public", "foo", backup.TYPE_RELATION)
			tables[0].Oid = newTableOid
			afterAttStats := make(map[uint32][]backup.AttributeStatistic)
			backup.GetAttributeStatistics(connectionPool, tables, func(attStat *backup.AttributeStatistic) {
				afterAttStats[attStat.Oid] = append(afterAttStats[attStat.Oid], *attStat)
			})
			afterTupleStats := make(map[uint32]backup.TupleStatistic)
			backup.GetTupleStatistics(connectionPool, tables, func(tupleStat *backup.TupleStatistic) {
				afterTupleStats[tupleStat.Oid] = *tupleStat
			})
			afterTupleStat := afterTupleStats[newTableOid]

			oldAtts := beforeAttStats[oldTableOid]
			newAtts := afterAttStats[newTableOid]

			// Ensure the statistics match
			Expect(afterTupleStats).To(HaveLen(len(beforeTupleStats)))
			structmatcher.ExpectStructsToMatchExcluding(&beforeTupleStat, &afterTupleStat, "Oid")
			Expect(oldAtts).To(HaveLen(3))
			Expect(newAtts).To(HaveLen(3))
			for i := range oldAtts {
				structmatcher.ExpectStructsToMatchExcluding(&oldAtts[i], &newAtts[i], "Oid", "Relid")
			}
		})
		It("prints attribute and tuple statistics for a quoted table", func() {
			tables := []backup.Table{
				{Relation: backup.Relation{SchemaOid: 2200, Schema: "public", Name: "\"foo'\"\"''bar\""}},
			}

			// Create and ANALYZE the tables to generate statistics
			testhelper.AssertQueryRuns(connectionPool, "CREATE TABLE public.\"foo'\"\"''bar\"(i int, j text, k bool)")
			defer testhelper.AssertQueryRuns(connectionPool, "DROP TABLE public.\"foo'\"\"''bar\"")
			testhelper.AssertQueryRuns(connectionPool, "INSERT INTO public.\"foo'\"\"''bar\" VALUES (1, 'a', 't')")
			testhelper.AssertQueryRuns(connectionPool, "ANALYZE public.\"foo'\"\"''bar\"")

			oldTableOid := testutils.OidFromObjectName(connectionPool, "public", "foo''\"''''bar", backup.TYPE_RELATION)
			tables[0].Oid = oldTableOid

			beforeAttStats := make(map[uint32][]backup.AttributeStatistic)
			backup.GetAttributeStatistics(connectionPool, tables, func(attStat *backup.AttributeStatistic) {
				beforeAttStats[attStat.Oid] = append(beforeAttStats[attStat.Oid], *attStat)
			})
			beforeTupleStats := make(map[uint32]backup.TupleStatistic)
			backup.GetTupleStatistics(connectionPool, tables, func(tupleStat *backup.TupleStatistic) {
				beforeTupleStats[tupleStat.Oid] = *tupleStat
			})
			beforeTupleStat := beforeTupleStats[oldTableOid]

			// Drop and recreate the table to clear the statistics
			testhelper.AssertQueryRuns(connectionPool, "DROP TABLE public.\"foo'\"\"''bar\"")
			testhelper.AssertQueryRuns(connectionPool, "CREATE TABLE public.\"foo'\"\"''bar\"(i int, j text, k bool)")

			// Reload the retrieved statistics into the new table
			PrintStatisticsStatements(backupfile, tocfile, tables, beforeAttStats, beforeTupleStats)
			testhelper.AssertQueryRuns(connectionPool, buffer.String())

			newTableOid := testutils.OidFromObjectName(connectionPool, "public", "foo''\"''''bar", backup.TYPE_RELATION)
			tables[0].Oid = newTableOid
			afterAttStats := make(map[uint32][]backup.AttributeStatistic)
			backup.GetAttributeStatistics(connectionPool, tables, func(attStat *backup.AttributeStatistic) {
				afterAttStats[attStat.Oid] = append(afterAttStats[attStat.Oid], *attStat)
			})
			afterTupleStats := make(map[uint32]backup.TupleStatistic)
			backup.GetTupleStatistics(connectionPool, tables, func(tupleStat *backup.TupleStatistic) {
				afterTupleStats[tupleStat.Oid] = *tupleStat
			})
			afterTupleStat := afterTupleStats[newTableOid]

			oldAtts := beforeAttStats[oldTableOid]
			newAtts := afterAttStats[newTableOid]

			// Ensure the statistics match
			Expect(afterTupleStats).To(HaveLen(len(beforeTupleStats)))
			structmatcher.ExpectStructsToMatchExcluding(&beforeTupleStat, &afterTupleStat, "Oid")
			Expect(oldAtts).To(HaveLen(3))
			Expect(newAtts).To(HaveLen(3))
			for i := range oldAtts {
				structmatcher.ExpectStructsToMatchExcluding(&oldAtts[i], &newAtts[i], "Oid", "Relid")
			}
		})
	})
})
