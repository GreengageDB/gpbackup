package checkmigrate

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gp-common-go-libs/testhelper"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

type sourceCheckTestCase struct {
	name                     string
	check                    func(*dbconn.DBConn) (int, error)
	query                    string
	columns                  []string
	rows                     [][]driver.Value
	problemText              string
	expectedObjects          []string
	shouldDisableTrackCounts bool
}

var sourceCheckTestCases = []sourceCheckTestCase{
	{
		name:    "multi-column LIST partitions",
		check:   checkMultiColumnListPartitions,
		query:   multiColumnListPartitionQuery,
		columns: []string{"schema_name", "object_name"},
		rows:    [][]driver.Value{{"sales", "orders"}, {"warehouse", "inventory"}},
		problemText: "Your cluster contains partitioned tables with a LIST partition key containing multiple " +
			"columns, which is not supported anymore. Consider modifying the partition key to use a single column " +
			"or dropping the tables.\n",
		expectedObjects: []string{
			`Object "orders" has type "partitioned table" in schema "sales"`,
			`Object "inventory" has type "partitioned table" in schema "warehouse"`,
		},
	},
	{
		name:    "plpython2 functions",
		check:   checkPlpython2DependentFunctions,
		query:   plpython2DependentFunctionQuery,
		columns: []string{"schema_name", "object_name", "identity_arguments"},
		rows: [][]driver.Value{
			{"analytics", "forecast", "integer"},
			{"public", "legacy_python", "text, integer"},
		},
		problemText: "Your cluster contains \"plpython\" functions which rely on Python 2. " +
			"These functions must be either updated to use Python 3 or dropped before upgrade.\n",
		expectedObjects: []string{
			`Object "forecast" has type "function" in schema "analytics"`,
			`Object "legacy_python" has type "function" in schema "public"`,
			`identity arguments are "integer"`,
			`identity arguments are "text, integer"`,
		},
	},
	{
		name:                     "views with removed operators",
		check:                    checkViewsWithRemovedOperators,
		query:                    removedOperatorViewQuery,
		shouldDisableTrackCounts: true,
		columns:                  []string{"schema_name", "object_name", "relation_kind"},
		rows:                     [][]driver.Value{{"public", "operator_view", "v"}, {"reports", "operator_materialized_view", "m"}},
		problemText: "Your cluster contains views using removed operators. " +
			"These operators are no longer present on the target version. " +
			"These views must be updated to use operators supported in the target version or removed before " +
			"upgrade can continue.\n",
		expectedObjects: []string{
			`Object "operator_view" has type "view" in schema "public"`,
			`Object "operator_materialized_view" has type "materialized view" in schema "reports"`,
		},
	},
	{
		name:                     "views with removed functions",
		check:                    checkViewsWithRemovedFunctions,
		query:                    removedFunctionViewQuery,
		shouldDisableTrackCounts: true,
		columns:                  []string{"schema_name", "object_name", "relation_kind"},
		rows:                     [][]driver.Value{{"public", "function_view", "v"}, {"reports", "function_materialized_view", "m"}},
		problemText: "Your cluster contains views using removed functions. " +
			"These functions are no longer present on the target version. " +
			"These views must be updated to use functions supported in the target version or removed before " +
			"upgrade can continue.\n",
		expectedObjects: []string{
			`Object "function_view" has type "view" in schema "public"`,
			`Object "function_materialized_view" has type "materialized view" in schema "reports"`,
		},
	},
	{
		name:                     "views with removed types",
		check:                    checkViewsWithRemovedTypes,
		query:                    removedTypeViewQuery,
		shouldDisableTrackCounts: true,
		columns:                  []string{"schema_name", "object_name", "relation_kind"},
		rows:                     [][]driver.Value{{"public", "type_view", "v"}, {"reports", "type_materialized_view", "m"}},
		problemText: "Your cluster contains views using removed types. " +
			"These types are no longer present on the target version. " +
			"These views must be updated to use types supported in the target version or removed before upgrade " +
			"can continue.\n",
		expectedObjects: []string{
			`Object "type_view" has type "view" in schema "public"`,
			`Object "type_materialized_view" has type "materialized view" in schema "reports"`,
		},
	},
	{
		name:    "removed data types",
		check:   checkRemovedDataTypes,
		query:   removedDataTypeQuery,
		columns: []string{"schema_name", "object_name", "column_name"},
		rows:    [][]driver.Value{{"public", "events", "created_at"}, {"archive", "old_events", "expired_at"}},
		problemText: "Your cluster contains the \"abstime\", \"reltime\", \"tinterval\", or \"unknown\" data " +
			"type in user tables. These data types have been removed in version 7. Drop the problem columns or " +
			"change them to another data type.\n",
		expectedObjects: []string{
			`Object "created_at" has type "column" in schema "public"`,
			`Object "expired_at" has type "column" in schema "archive"`,
		},
	},
	{
		name:    "missing AO options",
		check:   checkMissingAOOptions,
		query:   missingAOOptionQuery,
		columns: []string{"parent_schema", "parent_name", "child_schema", "child_name", "parent_option"},
		rows: [][]driver.Value{
			{"public", "ao_parent", "public", "ao_child_one", "compresstype=zlib"},
			{"archive", "ao_parent_two", "archive", "ao_child_two", "compresslevel=5"},
		},
		problemText: "Your cluster contains partitioned tables with child partitions, which do not have the parent " +
			"table's settings defined.\nIn version 7, they will be inherited from the parent table instead of being " +
			"taken by default.\nYou can recreate following tables with defined setting.\n" +
			"List of partitioned tables, partitions, and settings with the specified problem:\n",
		expectedObjects: []string{
			`Object "ao_child_one" has type "partition" in schema "public"`,
			`Object "ao_child_two" has type "partition" in schema "archive"`,
		},
	},
	{
		name:    "restricted EXECUTE ON functions",
		check:   checkRestrictedExecuteOnFunctions,
		query:   restrictedExecuteOnFunctionQuery,
		columns: []string{"schema_name", "object_name", "identity_arguments"},
		rows: [][]driver.Value{
			{"public", "master_function", "integer"},
			{"analytics", "segment_function", "text, integer"},
		},
		problemText: "Your cluster contains not set-returning functions with MASTER, ALL SEGMENTS or INITPLAN " +
			"EXECUTE ON.\nYou need to make the function set-returning or change EXECUTE ON to ANY.\n" +
			"List of functions with the specified problem:\n",
		expectedObjects: []string{
			`Object "master_function" has type "function" in schema "public"`,
			`Object "segment_function" has type "function" in schema "analytics"`,
			`identity arguments are "integer"`,
			`identity arguments are "text, integer"`,
		},
	},
	{
		name:    "incomplete partition indexes",
		check:   checkIncompletePartitionIndexes,
		query:   incompletePartitionIndexQuery,
		columns: []string{"schema_name", "table_name", "index_name"},
		rows:    [][]driver.Value{{"public", "sales", "sales_unique"}, {"archive", "orders", "orders_primary"}},
		problemText: "Your cluster contains partitioned tables with unique indexes, which do not have all partition " +
			"keys.\nIn version 7, unique index on partitioned table must include all partitioning keys.\n" +
			"You can recreate following indexes with all partitioning keys.\n" +
			"List of partitioned tables and indexes with the specified problem:\n",
		expectedObjects: []string{
			`Object "sales_unique" has type "index" in schema "public"`,
			`Object "orders_primary" has type "index" in schema "archive"`,
		},
	},
	{
		name:    "incompatible range partitions",
		check:   checkIncompatibleRangePartitions,
		query:   incompatibleRangePartitionQuery,
		columns: []string{"parent_schema", "table_name", "type_name", "partition_schema", "partition_name"},
		rows: [][]driver.Value{
			{"public", "prices", "numeric", "sales", "prices_1_prt_low"},
			{"archive", "labels", "text", "history", "labels_1_prt_a"},
		},
		problemText: "In version 7, range partitions don't support START EXCLUSIVE or END INCLUSIVE for " +
			"columns with types float and text.\nYou can recreate following tables without START EXCLUSIVE and " +
			"END INCLUSIVE.\nList of partitioned tables with the specified problem:\n",
		expectedObjects: []string{
			`Object "prices_1_prt_low" has type "partition" in schema "sales"`,
			`Object "labels_1_prt_a" has type "partition" in schema "history"`,
		},
	},
	{
		name:    "statement triggers",
		check:   checkStatementTriggers,
		query:   statementTriggerQuery,
		columns: []string{"schema_name", "table_name", "trigger_name"},
		rows:    [][]driver.Value{{"public", "orders", "orders_statement"}, {"audit", "events", "events_statement"}},
		problemText: "In version 7, statement triggers are not supported.\n" +
			"You can use row triggers.\n" +
			"List of triggers with the specified problem:\n",
		expectedObjects: []string{
			`Object "orders_statement" has type "trigger" in schema "public"`,
			`Object "events_statement" has type "trigger" in schema "audit"`,
		},
	},
}

func setupCheckTest(t *testing.T) (*dbconn.DBConn, sqlmock.Sqlmock, *gbytes.Buffer) {
	t.Helper()
	gomega.RegisterTestingT(t)
	connection, mock, _, stderr, _ := testhelper.SetupTestEnvironment()
	connection.DBName = "source_database"
	gplog.SetErrorCode(0)
	t.Cleanup(connection.Close)
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("The SQL expectations were not met with %v", err)
		}
	})

	return connection, mock, stderr
}

func rowsForCheck(testCase sourceCheckTestCase, hasRows bool) *sqlmock.Rows {
	rows := sqlmock.NewRows(testCase.columns)
	if !hasRows {
		return rows
	}

	for _, row := range testCase.rows {
		rows.AddRow(row...)
	}

	return rows
}

func expectAllSourceChecksEmpty(mock sqlmock.Sqlmock) {
	for _, testCase := range sourceCheckTestCases {
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(resetTrackCountsQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func expectMigrationSetupQueries(mock sqlmock.Sqlmock) {
	setupQueries := []string{
		migrationCheckSetupQuery,
		migrationCheckSetupTypesQuery,
	}
	for _, setupQuery := range setupQueries {
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta(setupQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func expectMigrationSetupTransaction(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	expectMigrationSetupQueries(mock)
	mock.ExpectCommit()
}

func expectReadOnlyMigrationTransaction(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(setTransactionReadOnlyQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectMigrationTransaction(mock sqlmock.Sqlmock) {
	expectMigrationSetupTransaction(mock)
	expectReadOnlyMigrationTransaction(mock)
}

func callDoCheckMigrate() interface{} {
	var recoveredValue interface{}
	func() {
		defer func() {
			recoveredValue = recover()
		}()
		DoCheckMigrate()
	}()

	return recoveredValue
}

func TestSourceChecksReportEveryObject(t *testing.T) {
	for _, testCase := range sourceCheckTestCases {
		t.Run(testCase.name, func(t *testing.T) {
			connection, mock, stderr := setupCheckTest(t)
			mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, true))

			findingCount, err := testCase.check(connection)

			if err != nil {
				t.Fatalf("The check returned an error with %v", err)
			}
			if findingCount != len(testCase.rows) {
				t.Fatalf("The check reported %d findings for %d rows", findingCount, len(testCase.rows))
			}
			output := string(stderr.Contents())
			if strings.Count(output, testCase.problemText) != 1 {
				t.Fatalf("The problem text occurred an unexpected number of times in %q", output)
			}
			databaseHeader := fmt.Sprintf(
				"Database %q contains these findings:",
				connection.DBName,
			)
			if strings.Count(output, databaseHeader) != 1 {
				t.Fatalf("The database heading occurred an unexpected number of times in %q", output)
			}
			for _, expectedObject := range testCase.expectedObjects {
				if !strings.Contains(output, expectedObject) {
					t.Errorf("The output %q did not contain %q", output, expectedObject)
				}
			}
			if gplog.GetErrorCode() != 0 {
				t.Fatalf("The individual check changed the exit code to %d", gplog.GetErrorCode())
			}
		})
	}
}

func TestSourceChecksIgnoreEmptyResults(t *testing.T) {
	for _, testCase := range sourceCheckTestCases {
		t.Run(testCase.name, func(t *testing.T) {
			connection, mock, stderr := setupCheckTest(t)
			mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))

			findingCount, err := testCase.check(connection)

			if err != nil {
				t.Fatalf("The check returned an error with %v", err)
			}
			if findingCount != 0 {
				t.Fatal("The check reported findings for an empty result")
			}
			if len(stderr.Contents()) != 0 {
				t.Fatalf("The check printed output %q for an empty result", stderr.Contents())
			}
		})
	}
}

func TestRequiredLibrariesReportsEveryFailedLoad(t *testing.T) {
	sourceConnection, sourceMock, _ := setupCheckTest(t)
	targetConnection, targetMock, _, stderr, _ := testhelper.SetupTestEnvironment()
	targetConnection.DBName = "target_database"
	t.Cleanup(targetConnection.Close)
	t.Cleanup(func() {
		if err := targetMock.ExpectationsWereMet(); err != nil {
			t.Errorf("The target SQL expectations were not met with %v", err)
		}
	})

	libraryRows := sqlmock.NewRows([]string{"library_name"}).
		AddRow("$libdir/shared").
		AddRow("$libdir/missing").
		AddRow("odd'lib")
	sourceMock.ExpectQuery(regexp.QuoteMeta(requiredLibraryQuery)).WillReturnRows(libraryRows)
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/shared'")).WillReturnResult(sqlmock.NewResult(0, 0))
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/missing'")).WillReturnError(errors.New("missing library"))
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD 'odd''lib'")).WillReturnError(errors.New("missing quoted library"))
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))

	findingCount, err := checkRequiredLibraries(sourceConnection, targetConnection)

	if err != nil {
		t.Fatalf("The library check returned an error with %v", err)
	}
	if findingCount != 2 {
		t.Fatalf("The library check reported %d failed loads", findingCount)
	}
	output := string(stderr.Contents())
	expectedProblemText := "Your cluster references loadable libraries that are missing from the new cluster.\n" +
		"You can add these libraries to the new installation,\n" +
		"or remove the functions using them from the old installation.\n" +
		"The problematic libraries are:\n"
	if strings.Count(output, expectedProblemText) != 1 {
		t.Fatalf("The problem text occurred an unexpected number of times in %q", output)
	}
	for _, expectedLibrary := range []string{"$libdir/missing", "odd'lib"} {
		if !strings.Contains(output, expectedLibrary) {
			t.Errorf("The output %q did not contain library %q", output, expectedLibrary)
		}
	}
	if strings.Contains(output, "Object ") {
		t.Fatalf("The library check reported functions in %q", output)
	}
}

func TestRequiredLibrariesLoadsEachLibraryOnce(t *testing.T) {
	sourceConnection, sourceMock, _ := setupCheckTest(t)
	targetConnection, targetMock, _, _, _ := testhelper.SetupTestEnvironment()
	t.Cleanup(targetConnection.Close)
	t.Cleanup(func() {
		if err := targetMock.ExpectationsWereMet(); err != nil {
			t.Errorf("The target SQL expectations were not met with %v", err)
		}
	})

	sourceMock.ExpectQuery(regexp.QuoteMeta(requiredLibraryQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"library_name"}).
			AddRow("$libdir/missing").
			AddRow("$libdir/missing"),
	)
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/missing'")).WillReturnError(errors.New("missing library"))
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))

	findingCount, err := checkRequiredLibraries(sourceConnection, targetConnection)

	if err != nil {
		t.Fatalf("The library check returned an error with %v", err)
	}
	if findingCount != 1 {
		t.Fatalf("The library check returned %d findings", findingCount)
	}
}

func TestRequiredLibrariesReportsTargetDatabaseOutage(t *testing.T) {
	sourceConnection, sourceMock, _ := setupCheckTest(t)
	targetConnection, targetMock, _, stderr, _ := testhelper.SetupTestEnvironment()
	t.Cleanup(targetConnection.Close)
	t.Cleanup(func() {
		if err := targetMock.ExpectationsWereMet(); err != nil {
			t.Errorf("The target SQL expectations were not met with %v", err)
		}
	})

	sourceMock.ExpectQuery(regexp.QuoteMeta(requiredLibraryQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"library_name"}).
			AddRow("$libdir/missing").
			AddRow("$libdir/unreachable"),
	)
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/missing'")).WillReturnError(errors.New("missing library"))
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))
	loadError := errors.New("load connection failure")
	livenessError := errors.New("liveness connection failure")
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/unreachable'")).WillReturnError(loadError)
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnError(livenessError)

	findingCount, executionError := checkRequiredLibraries(sourceConnection, targetConnection)

	if findingCount != 1 {
		t.Fatalf("The target outage retained %d earlier missing library findings", findingCount)
	}
	if !strings.Contains(string(stderr.Contents()), "$libdir/missing") {
		t.Fatalf("The earlier missing library was not printed in %q", stderr.Contents())
	}
	if !errors.Is(executionError, errTargetDatabaseUnavailable) ||
		!errors.Is(executionError, loadError) ||
		!errors.Is(executionError, livenessError) {
		t.Fatalf("The target outage errors were not preserved: %v", executionError)
	}
}

func TestRunMigrationCheckPlanReturnsTargetOutageWithoutFailedCheck(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	connectionError := errors.New("target connection failed")
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))

	summary, executionError := runMigrationCheckPlan(
		connection,
		[]migrationCheck{{
			name: "target check",
			doRunCheck: func(*dbconn.DBConn) (int, error) {
				return 0, errors.Join(errTargetDatabaseUnavailable, connectionError)
			},
		}},
		nil,
	)

	if summary.failedCheckCount != 0 {
		t.Fatalf("The target outage reported %d failed checks", summary.failedCheckCount)
	}
	if !errors.Is(executionError, errTargetDatabaseUnavailable) || !errors.Is(executionError, connectionError) {
		t.Fatalf("The target outage was not returned as a database error: %v", executionError)
	}
}

func TestRunMigrationCheckPlanReturnsEveryRecoveredCheckError(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	firstError := errors.New("first query failed")
	secondError := errors.New("second query failed")
	checks := []migrationCheck{
		{name: "first check", doRunCheck: func(*dbconn.DBConn) (int, error) { return 0, firstError }},
		{name: "successful check", doRunCheck: func(*dbconn.DBConn) (int, error) { return 0, nil }},
		{name: "second check", doRunCheck: func(*dbconn.DBConn) (int, error) { return 0, secondError }},
	}
	for _, check := range checks {
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		if check.name != "successful check" {
			mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}

	summary, executionError := runMigrationCheckPlan(connection, checks, nil)

	if summary.completedCheckCount != 1 || summary.failedCheckCount != 2 {
		t.Fatalf("The recovered failure summary was %+v", summary)
	}
	if !errors.Is(executionError, firstError) || !errors.Is(executionError, secondError) {
		t.Fatalf("The recovered check errors were not preserved: %v", executionError)
	}
}

func TestRunMigrationCheckPlanStopsAfterSavepointReleaseFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	queryError := errors.New("query failed")
	releaseError := errors.New("release failed")
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnError(releaseError)

	summary, executionError := runMigrationCheckPlan(
		connection,
		[]migrationCheck{
			{name: "failed check", doRunCheck: func(*dbconn.DBConn) (int, error) { return 0, queryError }},
			{name: "unsafe check", doRunCheck: func(*dbconn.DBConn) (int, error) { return 0, nil }},
		},
		nil,
	)

	if summary.failedCheckCount != 1 || summary.completedCheckCount != 0 {
		t.Fatalf("The savepoint release failure summary was %+v", summary)
	}
	if !errors.Is(executionError, queryError) || !errors.Is(executionError, releaseError) {
		t.Fatalf("The query and savepoint release errors were not preserved: %v", executionError)
	}
}

func TestMigrationSetupUsesTemporarySchema(t *testing.T) {
	if strings.Contains(migrationCheckSetupQuery, "__ggcheckmigrate_tmp") {
		t.Fatal("The migration setup uses the shared schema")
	}
	if !strings.Contains(migrationCheckSetupQuery, "pg_temp") {
		t.Fatal("The migration setup does not use the temporary schema")
	}
	queries := []string{
		removedOperatorViewQuery,
		removedFunctionViewQuery,
		removedTypeViewQuery,
		removedDataTypeQuery,
	}
	for _, query := range queries {
		if !strings.Contains(query, "pg_temp") {
			t.Fatalf("The support query does not use the temporary schema in %q", query)
		}
	}
}

func TestPlpythonCheckUsesLanguageHandler(t *testing.T) {
	if !strings.Contains(plpython2DependentFunctionQuery, "lanplcallfoid") {
		t.Fatal("The PL/Python check does not inspect the language handler")
	}
	if strings.Contains(plpython2DependentFunctionQuery, "pg_pltemplate") {
		t.Fatal("The PL/Python check still depends on the language template")
	}
}

func TestRemovedViewChecksUseUpstreamOIDFilters(t *testing.T) {
	for _, query := range []string{removedFunctionViewQuery, removedTypeViewQuery} {
		if !strings.Contains(query, "c.oid >= 16384") {
			t.Fatalf("The removed view check does not exclude system relations in %q", query)
		}
	}
	if strings.Contains(removedOperatorViewQuery, "c.oid >= 16384") {
		t.Fatal("The removed operator view check does not match the upstream query")
	}
}

func TestUnknownRelationKindUsesCatalogCode(t *testing.T) {
	if actualLabel := getRelationKindLabel("x"); actualLabel != "x" {
		t.Fatalf("The unknown relation kind label is %q", actualLabel)
	}
}

func TestSourceDatabaseEnumerationExcludesTemplateDatabases(t *testing.T) {
	if !strings.Contains(sourceDatabaseNamesQuery, "datallowconn") {
		t.Fatal("The source database enumeration does not require connectable databases")
	}
	if !strings.Contains(sourceDatabaseNamesQuery, "NOT datistemplate") {
		t.Fatal("The source database enumeration does not exclude template databases")
	}
	if !strings.Contains(sourceDatabaseNamesQuery, "datname = 'postgres'") {
		t.Fatal("The source database enumeration excludes postgres on Greengage 6")
	}
}

func TestSourceChecksUseNamespaceFilters(t *testing.T) {
	queries := []string{
		multiColumnListPartitionQuery,
		plpython2DependentFunctionQuery,
		removedOperatorViewQuery,
		removedFunctionViewQuery,
		removedTypeViewQuery,
		migrationCheckSetupTypesQuery,
		missingAOOptionQuery,
		restrictedExecuteOnFunctionQuery,
		incompletePartitionIndexQuery,
		incompatibleRangePartitionQuery,
		statementTriggerQuery,
		requiredLibraryQuery,
	}
	for _, query := range queries {
		for _, namespaceFilter := range []string{
			"NOT LIKE 'pg_temp_%'",
			"NOT LIKE 'pg_toast%'",
			"NOT IN ('gp_toolkit', 'information_schema', 'pg_aoseg', 'pg_bitmapindex', 'pg_catalog')",
		} {
			if !strings.Contains(query, namespaceFilter) {
				t.Fatalf("The source check does not contain namespace filter %q in %q", namespaceFilter, query)
			}
		}
	}
	for _, query := range []string{missingAOOptionQuery, incompatibleRangePartitionQuery} {
		if !strings.Contains(query, "child_namespace.nspname NOT LIKE 'pg_temp_%'") ||
			!strings.Contains(query, "child_namespace.nspname NOT LIKE 'pg_toast%'") ||
			!strings.Contains(query, "child_namespace.nspname NOT IN") {
			t.Fatalf("The partition check does not filter the child namespace in %q", query)
		}
	}
}

func TestRequiredLibrariesIncludePersistentExtensionOwnedFunctions(t *testing.T) {
	if !strings.Contains(requiredLibraryQuery, "SELECT DISTINCT p.probin::text AS library_name") {
		t.Fatal("The required library check does not return unique library names")
	}
	if !strings.Contains(requiredLibraryQuery, "p.oid >= 16384") {
		t.Fatal("The required library check does not select user-defined functions")
	}
	if strings.Contains(requiredLibraryQuery, "dependency.deptype = 'e'") {
		t.Fatal("The required library check excludes extension-owned functions")
	}
	for _, schemaPattern := range []string{"pg_temp_%", "pg_toast%"} {
		if !strings.Contains(requiredLibraryQuery, schemaPattern) {
			t.Fatalf("The required library check does not exclude schema pattern %q", schemaPattern)
		}
	}
}

func TestMissingAOOptionsUseImmediateAOParents(t *testing.T) {
	for _, catalog := range []string{"pg_catalog.pg_partition_rule", "pg_catalog.pg_partition"} {
		if !strings.Contains(missingAOOptionQuery, catalog) {
			t.Fatalf("The AO option check does not use %s", catalog)
		}
	}
	if !strings.Contains(missingAOOptionQuery, "parent_rule.parchildrelid, root_partition.parrelid") {
		t.Fatal("The AO option check does not resolve immediate partition parents")
	}
	if !strings.Contains(missingAOOptionQuery, "child_relation.relstorage = parent_relation.relstorage") {
		t.Fatal("The AO option check compares partitions with a different storage type")
	}
	if strings.Contains(missingAOOptionQuery, "pg_catalog.pg_partitions") {
		t.Fatal("The AO option check still resolves parents through the root-only view")
	}
	for _, option := range []string{
		"appendonly",
		"appendoptimized",
		"orientation",
		"compresstype",
		"compresslevel",
		"blocksize",
		"checksum",
	} {
		if !strings.Contains(missingAOOptionQuery, "'"+option+"'") {
			t.Fatalf("The AO option check does not compare %q", option)
		}
	}
	if strings.Contains(missingAOOptionQuery, "'fillfactor'") {
		t.Fatal("The AO option check compares an option outside the Solution list")
	}
}

func TestRunMigrationCheckDisablesAndResetsTrackCounts(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(resetTrackCountsQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))

	summary, executionError, shouldContinue := runMigrationCheck(connection, migrationCheck{
		name:                     "view check",
		shouldDisableTrackCounts: true,
		doRunCheck: func(*dbconn.DBConn) (int, error) {
			return 0, nil
		},
	})

	if executionError != nil {
		t.Fatalf("The view check returned an error with %v", executionError)
	}
	if !shouldContinue {
		t.Fatal("The successful view check stopped later checks")
	}
	if summary.completedCheckCount != 1 || summary.failedCheckCount != 0 {
		t.Fatalf("The successful view check summary was %+v", summary)
	}
}

func TestRunMigrationCheckReportsTrackCountsSetupFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnError(errors.New("track counts setup failed"))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))

	summary, executionError, shouldContinue := runMigrationCheck(connection, migrationCheck{
		name:                     "view check",
		shouldDisableTrackCounts: true,
		doRunCheck: func(*dbconn.DBConn) (int, error) {
			t.Fatal("The view check ran after track_counts setup failed")
			return 0, nil
		},
	})

	if executionError == nil || !strings.Contains(executionError.Error(), "track counts setup failed") {
		t.Fatalf("The track_counts setup failure was not returned: %v", executionError)
	}
	if !shouldContinue {
		t.Fatal("The recoverable track_counts setup failure stopped later checks")
	}
	if summary.completedCheckCount != 0 || summary.failedCheckCount != 1 {
		t.Fatalf("The failed view check summary was %+v", summary)
	}
}

func TestRunMigrationCheckRollsBackTrackCountsAfterQueryFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))

	summary, executionError, shouldContinue := runMigrationCheck(connection, migrationCheck{
		name:                     "view check",
		shouldDisableTrackCounts: true,
		doRunCheck: func(*dbconn.DBConn) (int, error) {
			return 0, errors.New("view query failed")
		},
	})

	if executionError == nil || !strings.Contains(executionError.Error(), "view query failed") {
		t.Fatalf("The view query failure was not returned: %v", executionError)
	}
	if !shouldContinue {
		t.Fatal("The recoverable view query failure stopped later checks")
	}
	if summary.completedCheckCount != 0 || summary.failedCheckCount != 1 {
		t.Fatalf("The failed view check summary was %+v", summary)
	}
}

func TestRunMigrationCheckReportsTrackCountsResetFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(resetTrackCountsQuery)).WillReturnError(errors.New("track counts reset failed"))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))

	summary, executionError, shouldContinue := runMigrationCheck(connection, migrationCheck{
		name:                     "view check",
		shouldDisableTrackCounts: true,
		doRunCheck: func(*dbconn.DBConn) (int, error) {
			return 0, nil
		},
	})

	if executionError == nil || !strings.Contains(executionError.Error(), "track counts reset failed") {
		t.Fatalf("The track_counts reset failure was not returned: %v", executionError)
	}
	if !shouldContinue {
		t.Fatal("The recoverable track_counts reset failure stopped later checks")
	}
	if summary.completedCheckCount != 0 || summary.failedCheckCount != 1 {
		t.Fatalf("The failed view check summary was %+v", summary)
	}
}

func TestDoCheckMigrateChecksRequiredLibrariesAfterSourceChecks(t *testing.T) {
	sourceConnection, sourceMock, _ := setupCheckTest(t)
	targetDatabaseConnection, targetMock, _, stderr, _ := testhelper.SetupTestEnvironment()
	targetDatabaseConnection.DBName = "target_database"
	bootstrapSourceConnection = sourceConnection
	targetConnection = targetDatabaseConnection
	t.Cleanup(targetDatabaseConnection.Close)
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
		targetConnection = nil
	})
	t.Cleanup(func() {
		if err := targetMock.ExpectationsWereMet(); err != nil {
			t.Errorf("The target SQL expectations were not met with %v", err)
		}
	})

	expectMigrationTransaction(sourceMock)
	expectAllSourceChecksEmpty(sourceMock)
	sourceMock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectQuery(regexp.QuoteMeta(requiredLibraryQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"library_name"}).AddRow("$libdir/missing"),
	)
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/missing'")).WillReturnError(errors.New("missing library"))
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnError(errors.New("target database unavailable"))
	sourceMock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The target outage run returned exit code %d", gplog.GetErrorCode())
	}
	if !strings.Contains(string(stderr.Contents()), "target database unavailable") {
		t.Fatalf("The target outage run did not print the database error in %q", stderr.Contents())
	}
}

func TestDoCheckMigrateReturnsZeroForCleanSource(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectMigrationTransaction(mock)
	expectAllSourceChecksEmpty(mock)
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 0 {
		t.Fatalf("The clean run returned exit code %d", gplog.GetErrorCode())
	}
	if len(stderr.Contents()) != 0 {
		t.Fatalf("The clean run printed %q", stderr.Contents())
	}
}

func TestDoCheckMigrateChecksEverySourceDatabase(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	connection.DBName = "postgres"
	connection.User = "source_user"
	connection.Host = "source_host"
	connection.Port = 6000
	applicationConnection, applicationMock := testhelper.CreateMockDBConn()
	testhelper.ExpectVersionQuery(applicationMock, "6.27.1")
	t.Cleanup(applicationConnection.Close)
	t.Cleanup(func() {
		if err := applicationMock.ExpectationsWereMet(); err != nil {
			t.Errorf("The application database SQL expectations were not met with %v", err)
		}
	})
	bootstrapSourceConnection = connection
	targetConnection = nil
	shouldScrapeDatabaseNames = true
	originalCreateDBConn := createDBConn
	createDBConn = func(dbName, username, host string, port int) *dbconn.DBConn {
		applicationConnection.DBName = dbName
		applicationConnection.User = username
		applicationConnection.Host = host
		applicationConnection.Port = port

		return applicationConnection
	}
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
		shouldScrapeDatabaseNames = false
		createDBConn = originalCreateDBConn
	})

	mock.ExpectQuery(regexp.QuoteMeta(sourceDatabaseNamesQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"database_name"}).AddRow("postgres").AddRow("application"),
	)
	expectMigrationTransaction(mock)
	expectAllSourceChecksEmpty(mock)
	mock.ExpectRollback()

	expectMigrationTransaction(applicationMock)
	firstCheck := sourceCheckTestCases[0]
	applicationMock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	applicationMock.ExpectQuery(regexp.QuoteMeta(firstCheck.query)).WillReturnRows(rowsForCheck(firstCheck, true))
	applicationMock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	for _, testCase := range sourceCheckTestCases[1:] {
		applicationMock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		if testCase.shouldDisableTrackCounts {
			applicationMock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		applicationMock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		if testCase.shouldDisableTrackCounts {
			applicationMock.ExpectExec(regexp.QuoteMeta(resetTrackCountsQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		applicationMock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	applicationMock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 1 {
		t.Fatalf("The multi-database run returned exit code %d", gplog.GetErrorCode())
	}
	output := string(stderr.Contents())
	if !strings.Contains(output, `Database "application"`) {
		t.Fatalf("The multi-database run did not print the application database in %q", stderr.Contents())
	}
	if strings.Count(output, "Execution summary:") != 1 {
		t.Fatalf("The multi-database run printed an unexpected summary count in %q", output)
	}
	expectedSummary := "Execution summary:\n" +
		"  enumerated databases:              2\n" +
		"  checked databases:                 2\n" +
		"  unreachable databases:             0\n" +
		"  unavailable databases:             0\n" +
		"  completed cluster checks:          0\n" +
		"  failed cluster checks:             0\n" +
		"  completed database checks:        22\n" +
		"  failed database checks:            0\n" +
		"  unavailable database checks:       0\n" +
		"  findings:                          2"
	if !strings.Contains(output, expectedSummary) {
		t.Fatalf("The multi-database run printed an unexpected summary in %q", output)
	}
	if applicationConnection.DBName != "application" ||
		applicationConnection.User != connection.User ||
		applicationConnection.Host != connection.Host ||
		applicationConnection.Port != connection.Port {
		t.Fatalf("The application connection did not reuse source connection parameters")
	}
}

func TestDoCheckMigrateReportsDatabaseEnumerationFailure(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	shouldScrapeDatabaseNames = true
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
		shouldScrapeDatabaseNames = false
	})

	mock.ExpectQuery(regexp.QuoteMeta(sourceDatabaseNamesQuery)).WillReturnError(errors.New("database enumeration failed"))

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The database enumeration failure returned exit code %d", gplog.GetErrorCode())
	}
	if !strings.Contains(string(stderr.Contents()), "Source database enumeration failed with database enumeration failed") {
		t.Fatalf("The database enumeration failure was not printed in %q", stderr.Contents())
	}
}

func TestDoCheckMigrateDoesNotCheckBootstrapDatabaseWhenEnumerationIsEmpty(t *testing.T) {
	connection, mock, stdout, _, _ := testhelper.SetupTestEnvironment()
	connection.DBName = "source_database"
	gplog.SetErrorCode(0)
	t.Cleanup(connection.Close)
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("The SQL expectations were not met with %v", err)
		}
	})
	bootstrapSourceConnection = connection
	targetConnection = nil
	shouldScrapeDatabaseNames = true
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
		shouldScrapeDatabaseNames = false
	})

	mock.ExpectQuery(regexp.QuoteMeta(sourceDatabaseNamesQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"database_name"}),
	)

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 0 {
		t.Fatalf("The empty database run returned exit code %d", gplog.GetErrorCode())
	}
	for _, expectedText := range []string{
		"enumerated databases:              0",
		"checked databases:                 0",
		"completed database checks:         0",
	} {
		if !strings.Contains(string(stdout.Contents()), expectedText) {
			t.Fatalf("The empty database run did not print %q in %q", expectedText, stdout.Contents())
		}
	}
	if strings.Contains(string(stdout.Contents()), "Starting checks for database") {
		t.Fatalf("The empty database run checked a database in %q", stdout.Contents())
	}
}

func TestDoCheckMigrateContinuesAfterFinding(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectMigrationTransaction(mock)
	firstCheck := sourceCheckTestCases[0]
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(firstCheck.query)).WillReturnRows(rowsForCheck(firstCheck, true))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	for _, testCase := range sourceCheckTestCases[1:] {
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(resetTrackCountsQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 1 {
		t.Fatalf("The finding run returned exit code %d", gplog.GetErrorCode())
	}
	if !strings.Contains(string(stderr.Contents()), firstCheck.expectedObjects[0]) {
		t.Fatalf("The finding run did not print the affected object in %q", stderr.Contents())
	}
}

func TestDoCheckMigrateReportsCheckSavepointReleaseFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectMigrationTransaction(mock)
	firstCheck := sourceCheckTestCases[0]
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(firstCheck.query)).WillReturnRows(rowsForCheck(firstCheck, false))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnError(errors.New("release failed"))
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The savepoint release failure returned exit code %d", gplog.GetErrorCode())
	}
}

func TestRunMigrationChecksKeepsIndependentChecksAfterDataTypeSetupFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(migrationCheckSetupQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(migrationCheckSetupTypesQuery)).WillReturnError(errors.New("data type support unavailable"))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	expectReadOnlyMigrationTransaction(mock)
	for _, testCase := range sourceCheckTestCases {
		if testCase.query == removedDataTypeQuery {
			continue
		}
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(resetTrackCountsQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectRollback()

	summary, executionError := runMigrationChecks(connection, nil)
	if executionError == nil || !strings.Contains(executionError.Error(), "data type support unavailable") {
		t.Fatalf("The partial capability run did not return its setup error: %v", executionError)
	}
	if summary.completedCheckCount != 10 || summary.unavailableCheckCount != 1 || summary.failedCheckCount != 0 {
		t.Fatalf("The partial capability summary was %+v", summary)
	}
}

func TestDoCheckMigrateReportsSetupSavepointReleaseFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(migrationCheckSetupQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnError(errors.New("release failed"))
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The setup savepoint release failure returned exit code %d", gplog.GetErrorCode())
	}
}

func TestDoCheckMigrateReportsFindingAndQueryFailureAsExecutionError(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectMigrationTransaction(mock)
	firstCheck := sourceCheckTestCases[0]
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(firstCheck.query)).WillReturnRows(rowsForCheck(firstCheck, true))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(sourceCheckTestCases[1].query)).WillReturnError(errors.New("query failed"))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	for _, testCase := range sourceCheckTestCases[2:] {
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(setTrackCountsOffQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		if testCase.shouldDisableTrackCounts {
			mock.ExpectExec(regexp.QuoteMeta(resetTrackCountsQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The failed run returned exit code %d", gplog.GetErrorCode())
	}
	if !strings.Contains(string(stderr.Contents()), "query failed") {
		t.Fatalf("The failed run did not print the check error in %q", stderr.Contents())
	}
}

func TestRunMigrationChecksRollsBackAfterIsolationFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnError(errors.New("isolation failed"))
	mock.ExpectRollback()

	_, executionError := runMigrationChecks(connection, nil)
	if executionError == nil || !strings.Contains(executionError.Error(), "isolation failed") {
		t.Fatalf("The isolation failure was not reported: %v", executionError)
	}
	if connection.Tx[0] != nil {
		t.Fatal("The failed transaction remained installed")
	}
}

func TestRunMigrationChecksReportsSetupCommitFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	expectMigrationSetupQueries(mock)
	mock.ExpectCommit().WillReturnError(errors.New("setup commit failed"))

	_, executionError := runMigrationChecks(connection, nil)
	if executionError == nil || !strings.Contains(executionError.Error(), "setup commit failed") {
		t.Fatalf("The setup commit failure was not reported: %v", executionError)
	}
	if connection.Tx[0] != nil {
		t.Fatal("The failed setup transaction remained installed")
	}
}

func TestRunMigrationChecksRollsBackAfterReadOnlyFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	expectMigrationSetupTransaction(mock)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(setTransactionReadOnlyQuery)).WillReturnError(errors.New("read only failed"))
	mock.ExpectRollback()

	_, executionError := runMigrationChecks(connection, nil)
	if executionError == nil || !strings.Contains(executionError.Error(), "read only failed") {
		t.Fatalf("The read-only failure was not reported: %v", executionError)
	}
	if connection.Tx[0] != nil {
		t.Fatal("The failed read-only transaction remained installed")
	}
}

func TestRunMigrationChecksReportsRollbackFailureAfterBeginFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	isolationError := errors.New("isolation failed")
	rollbackError := errors.New("rollback failed")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).
		WillReturnError(isolationError)
	mock.ExpectRollback().WillReturnError(rollbackError)

	_, executionError := runMigrationChecks(connection, nil)
	if !errors.Is(executionError, isolationError) || !errors.Is(executionError, rollbackError) {
		t.Fatalf("The begin and rollback failures were not reported: %v", executionError)
	}
}

func TestDoCheckMigrateReportsRollbackFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectMigrationTransaction(mock)
	expectAllSourceChecksEmpty(mock)
	mock.ExpectRollback().WillReturnError(errors.New("rollback failed"))

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The rollback failure returned exit code %d", gplog.GetErrorCode())
	}
}

func TestDoCheckMigrateContinuesAfterDatabaseConnectionFailure(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	connection.DBName = "postgres"
	connection.User = "source_user"
	connection.Host = "source_host"
	connection.Port = 6000
	failingConnection, _ := testhelper.CreateMockDBConn(errors.New("connection failed"))
	workingConnection, workingMock := testhelper.CreateMockDBConn()
	testhelper.ExpectVersionQuery(workingMock, "6.27.1")
	t.Cleanup(failingConnection.Close)
	t.Cleanup(workingConnection.Close)
	t.Cleanup(func() {
		if err := workingMock.ExpectationsWereMet(); err != nil {
			t.Errorf("The working database SQL expectations were not met with %v", err)
		}
	})
	bootstrapSourceConnection = connection
	targetConnection = nil
	shouldScrapeDatabaseNames = true
	originalCreateDBConn := createDBConn
	createDBConn = func(dbName, username, host string, port int) *dbconn.DBConn {
		if dbName == "unavailable" {
			return failingConnection
		}
		workingConnection.DBName = dbName

		return workingConnection
	}
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
		shouldScrapeDatabaseNames = false
		createDBConn = originalCreateDBConn
	})

	mock.ExpectQuery(regexp.QuoteMeta(sourceDatabaseNamesQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"database_name"}).AddRow("unavailable").AddRow("working"),
	)
	expectMigrationTransaction(workingMock)
	expectAllSourceChecksEmpty(workingMock)
	workingMock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The partial run returned exit code %d", gplog.GetErrorCode())
	}
	output := string(stderr.Contents())
	if !strings.Contains(output, "Execution summary:\n") ||
		!strings.Contains(output, "  enumerated databases:              2\n") ||
		!strings.Contains(output, "  checked databases:                 1\n") ||
		!strings.Contains(output, "  unreachable databases:             1\n") ||
		!strings.Contains(output, "  completed database checks:        11\n") {
		t.Fatalf("The partial run printed an unexpected summary in %q", output)
	}
}
