package checkmigrate

import (
	"database/sql/driver"
	"errors"
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
	name            string
	check           func(*dbconn.DBConn) (int, error)
	query           string
	columns         []string
	rows            [][]driver.Value
	problemText     string
	expectedObjects []string
}

var sourceCheckTestCases = []sourceCheckTestCase{
	{
		name:        "multi-column LIST partitions",
		check:       checkMultiColumnListPartitions,
		query:       multiColumnListPartitionQuery,
		columns:     []string{"schema_name", "object_name"},
		rows:        [][]driver.Value{{"sales", "orders"}, {"warehouse", "inventory"}},
		problemText: "partitioned tables with a LIST partition key containing multiple columns",
		expectedObjects: []string{
			`object "orders" of type "partitioned table" in schema "sales"`,
			`object "inventory" of type "partitioned table" in schema "warehouse"`,
		},
	},
	{
		name:        "plpython2 functions",
		check:       checkPlpython2DependentFunctions,
		query:       plpython2DependentFunctionQuery,
		columns:     []string{"schema_name", "object_name", "identity_arguments"},
		rows:        [][]driver.Value{{"analytics", "forecast", "integer"}, {"public", "legacy_python", "text, integer"}},
		problemText: "PL/Python functions that rely on Python 2",
		expectedObjects: []string{
			`object "forecast" of type "function" in schema "analytics"`,
			`object "legacy_python" of type "function" in schema "public"`,
			`identity arguments are "integer"`,
			`identity arguments are "text, integer"`,
		},
	},
	{
		name:        "views with removed operators",
		check:       checkViewsWithRemovedOperators,
		query:       removedOperatorViewQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind"},
		rows:        [][]driver.Value{{"public", "operator_view", "v"}, {"reports", "operator_materialized_view", "m"}},
		problemText: "views that use removed operators",
		expectedObjects: []string{
			`object "operator_view" of type "view" in schema "public"`,
			`object "operator_materialized_view" of type "materialized view" in schema "reports"`,
		},
	},
	{
		name:        "views with removed functions",
		check:       checkViewsWithRemovedFunctions,
		query:       removedFunctionViewQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind"},
		rows:        [][]driver.Value{{"public", "function_view", "v"}, {"reports", "function_materialized_view", "m"}},
		problemText: "views that use removed functions",
		expectedObjects: []string{
			`object "function_view" of type "view" in schema "public"`,
			`object "function_materialized_view" of type "materialized view" in schema "reports"`,
		},
	},
	{
		name:        "views with removed types",
		check:       checkViewsWithRemovedTypes,
		query:       removedTypeViewQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind"},
		rows:        [][]driver.Value{{"public", "type_view", "v"}, {"reports", "type_materialized_view", "m"}},
		problemText: "views that use removed types",
		expectedObjects: []string{
			`object "type_view" of type "view" in schema "public"`,
			`object "type_materialized_view" of type "materialized view" in schema "reports"`,
		},
	},
	{
		name:        "removed data types",
		check:       checkRemovedDataTypes,
		query:       removedDataTypeQuery,
		columns:     []string{"schema_name", "object_name", "column_name"},
		rows:        [][]driver.Value{{"public", "events", "created_at"}, {"archive", "old_events", "expired_at"}},
		problemText: "removed abstime, reltime, tinterval, or unknown data types",
		expectedObjects: []string{
			`object "created_at" of type "column" in schema "public"`,
			`object "expired_at" of type "column" in schema "archive"`,
		},
	},
	{
		name:        "missing AO options",
		check:       checkMissingAOOptions,
		query:       missingAOOptionQuery,
		columns:     []string{"parent_schema", "parent_name", "child_schema", "child_name", "parent_option"},
		rows:        [][]driver.Value{{"public", "ao_parent", "public", "ao_child_one", "compresstype=zlib"}, {"archive", "ao_parent_two", "archive", "ao_child_two", "compresslevel=5"}},
		problemText: "child partitions that do not define the parent table settings",
		expectedObjects: []string{
			`object "ao_child_one" of type "partition" in schema "public"`,
			`object "ao_child_two" of type "partition" in schema "archive"`,
		},
	},
	{
		name:        "restricted EXECUTE ON functions",
		check:       checkRestrictedExecuteOnFunctions,
		query:       restrictedExecuteOnFunctionQuery,
		columns:     []string{"schema_name", "object_name", "identity_arguments"},
		rows:        [][]driver.Value{{"public", "master_function", "integer"}, {"analytics", "segment_function", "text, integer"}},
		problemText: "functions that are not set-returning and use MASTER, ALL SEGMENTS, or INITPLAN EXECUTE ON",
		expectedObjects: []string{
			`object "master_function" of type "function" in schema "public"`,
			`object "segment_function" of type "function" in schema "analytics"`,
			`identity arguments are "integer"`,
			`identity arguments are "text, integer"`,
		},
	},
	{
		name:        "incomplete partition indexes",
		check:       checkIncompletePartitionIndexes,
		query:       incompletePartitionIndexQuery,
		columns:     []string{"schema_name", "table_name", "index_name"},
		rows:        [][]driver.Value{{"public", "sales", "sales_unique"}, {"archive", "orders", "orders_primary"}},
		problemText: "partitioned tables with unique indexes that omit partition keys",
		expectedObjects: []string{
			`object "sales_unique" of type "index" in schema "public"`,
			`object "orders_primary" of type "index" in schema "archive"`,
		},
	},
	{
		name:        "incompatible range partitions",
		check:       checkIncompatibleRangePartitions,
		query:       incompatibleRangePartitionQuery,
		columns:     []string{"parent_schema", "table_name", "type_name", "partition_schema", "partition_name"},
		rows:        [][]driver.Value{{"public", "prices", "numeric", "sales", "prices_1_prt_low"}, {"archive", "labels", "text", "history", "labels_1_prt_a"}},
		problemText: "range partitions that use START EXCLUSIVE or END INCLUSIVE boundaries",
		expectedObjects: []string{
			`object "prices_1_prt_low" of type "partition" in schema "sales"`,
			`object "labels_1_prt_a" of type "partition" in schema "history"`,
		},
	},
	{
		name:        "statement triggers",
		check:       checkStatementTriggers,
		query:       statementTriggerQuery,
		columns:     []string{"schema_name", "table_name", "trigger_name"},
		rows:        [][]driver.Value{{"public", "orders", "orders_statement"}, {"audit", "events", "events_statement"}},
		problemText: "Your cluster contains statement triggers",
		expectedObjects: []string{
			`object "orders_statement" of type "trigger" in schema "public"`,
			`object "events_statement" of type "trigger" in schema "audit"`,
		},
	},
	{
		name:        "removed extensions",
		check:       checkRemovedExtensions,
		query:       removedExtensionQuery,
		columns:     []string{"schema_name", "object_name"},
		rows:        [][]driver.Value{{"public", "gp_array_agg"}},
		problemText: "extensions that are absent from version 7",
		expectedObjects: []string{
			`object "gp_array_agg" of type "extension" in schema "public"`,
		},
	},
	{
		name:        "arenadata toolkit schema",
		check:       checkArenadataToolkitSchema,
		query:       arenadataToolkitSchemaQuery,
		columns:     []string{"schema_name", "object_name"},
		rows:        [][]driver.Value{{"arenadata_toolkit", "arenadata_toolkit"}},
		problemText: "contains the arenadata_toolkit schema",
		expectedObjects: []string{
			`object "arenadata_toolkit" of type "schema" in schema "arenadata_toolkit"`,
		},
	},
	{
		name:        "system object dependencies",
		check:       checkSystemObjectDependencies,
		query:       systemObjectDependencyQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind", "referenced_object"},
		rows:        [][]driver.Value{{"public", "catalog_view", "v", "pg_catalog.pg_class"}},
		problemText: "user objects that reference system relations",
		expectedObjects: []string{
			`object "catalog_view" of type "view" in schema "public"`,
		},
	},
	{
		name:        "deep partition templates",
		check:       checkDeepPartitionTemplates,
		query:       deepPartitionTemplateQuery,
		columns:     []string{"schema_name", "object_name"},
		rows:        [][]driver.Value{{"public", "deep_parts"}},
		problemText: "subpartition templates deeper than the second partition level",
		expectedObjects: []string{
			`object "deep_parts" of type "partitioned table" in schema "public"`,
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
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func expectMigrationTransaction(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(migrationCheckSetupQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectResourceGroupsEmpty(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(resourceGroupQuery)).WillReturnRows(sqlmock.NewRows([]string{"object_name"}))
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

	libraryRows := sqlmock.NewRows([]string{"schema_name", "object_name", "identity_arguments", "library_name"}).
		AddRow("public", "shared_fn", "integer", "$libdir/shared").
		AddRow("public", "missing_fn", "integer", "$libdir/missing").
		AddRow("public", "quoted_fn", "integer", "odd'lib")
	sourceMock.ExpectQuery(regexp.QuoteMeta(requiredLibraryQuery)).WillReturnRows(libraryRows)
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/shared'")).WillReturnResult(sqlmock.NewResult(0, 0))
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/missing'")).WillReturnError(errors.New("missing library"))
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD 'odd''lib'")).WillReturnError(errors.New("missing quoted library"))

	findingCount, err := checkRequiredLibraries(sourceConnection, targetConnection)

	if err != nil {
		t.Fatalf("The library check returned an error with %v", err)
	}
	if findingCount == 0 {
		t.Fatal("The library check did not report failed loads")
	}
	output := string(stderr.Contents())
	for _, expectedLibrary := range []string{"$libdir/missing", "odd'lib"} {
		if !strings.Contains(output, expectedLibrary) {
			t.Errorf("The output %q did not contain library %q", output, expectedLibrary)
		}
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
		sqlmock.NewRows([]string{"schema_name", "object_name", "identity_arguments", "library_name"}).
			AddRow("public", "first_function", "integer", "$libdir/missing").
			AddRow("public", "second_function", "text", "$libdir/missing"),
	)
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/missing'")).WillReturnError(errors.New("missing library"))

	findingCount, err := checkRequiredLibraries(sourceConnection, targetConnection)

	if err != nil {
		t.Fatalf("The library check returned an error with %v", err)
	}
	if findingCount != 2 {
		t.Fatalf("The library check returned %d findings", findingCount)
	}
}

func TestMigrationSetupUsesTemporarySchema(t *testing.T) {
	if strings.Contains(migrationCheckSetupQuery, "__ggcheckmigrate_tmp") {
		t.Fatal("The migration setup uses the shared schema")
	}
	if !strings.Contains(migrationCheckSetupQuery, "pg_temp") {
		t.Fatal("The migration setup does not use the temporary schema")
	}
	for _, query := range []string{removedOperatorViewQuery, removedFunctionViewQuery, removedTypeViewQuery, removedDataTypeQuery} {
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

func TestDoCheckMigrateChecksRequiredLibrariesBeforeSourceChecks(t *testing.T) {
	sourceConnection, sourceMock, _ := setupCheckTest(t)
	targetConnection, targetMock, _, stderr, _ := testhelper.SetupTestEnvironment()
	targetConnection.DBName = "target_database"
	sourceConnectionPool = sourceConnection
	targetConnectionPool = targetConnection
	t.Cleanup(targetConnection.Close)
	t.Cleanup(func() {
		sourceConnectionPool = nil
		targetConnectionPool = nil
	})
	t.Cleanup(func() {
		if err := targetMock.ExpectationsWereMet(); err != nil {
			t.Errorf("The target SQL expectations were not met with %v", err)
		}
	})

	expectResourceGroupsEmpty(sourceMock)
	sourceMock.ExpectBegin()
	sourceMock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_libraries")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectQuery(regexp.QuoteMeta(requiredLibraryQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"schema_name", "object_name", "identity_arguments", "library_name"}).AddRow("public", "missing_fn", "integer", "$libdir/missing"),
	)
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD '$libdir/missing'")).WillReturnError(errors.New("missing library"))
	sourceMock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_libraries")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectExec(regexp.QuoteMeta(migrationCheckSetupQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	expectAllSourceChecksEmpty(sourceMock)
	sourceMock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 1 {
		t.Fatalf("The missing library run returned exit code %d", gplog.GetErrorCode())
	}
	if !strings.Contains(string(stderr.Contents()), "$libdir/missing") {
		t.Fatalf("The missing library run did not print the library in %q", stderr.Contents())
	}
}

func TestDoCheckMigrateReturnsZeroForCleanSource(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	sourceConnectionPool = connection
	targetConnectionPool = nil
	t.Cleanup(func() {
		sourceConnectionPool = nil
	})

	expectResourceGroupsEmpty(mock)
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
	sourceConnectionPool = connection
	targetConnectionPool = nil
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
		sourceConnectionPool = nil
		shouldScrapeDatabaseNames = false
		createDBConn = originalCreateDBConn
	})

	mock.ExpectQuery(regexp.QuoteMeta(sourceDatabaseNamesQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"database_name"}).AddRow("postgres").AddRow("application"),
	)
	expectResourceGroupsEmpty(mock)
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
		applicationMock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		applicationMock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	applicationMock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 1 {
		t.Fatalf("The multi-database run returned exit code %d", gplog.GetErrorCode())
	}
	if !strings.Contains(string(stderr.Contents()), `Database "application"`) {
		t.Fatalf("The multi-database run did not print the application database in %q", stderr.Contents())
	}
	if applicationConnection.DBName != "application" ||
		applicationConnection.User != connection.User ||
		applicationConnection.Host != connection.Host ||
		applicationConnection.Port != connection.Port {
		t.Fatalf("The application connection did not reuse source connection parameters")
	}
}

func TestDoCheckMigrateReportsDatabaseEnumerationFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	sourceConnectionPool = connection
	targetConnectionPool = nil
	shouldScrapeDatabaseNames = true
	t.Cleanup(func() {
		sourceConnectionPool = nil
		shouldScrapeDatabaseNames = false
	})

	mock.ExpectQuery(regexp.QuoteMeta(sourceDatabaseNamesQuery)).WillReturnError(errors.New("database enumeration failed"))

	if recoveredValue := callDoCheckMigrate(); recoveredValue == nil {
		t.Fatal("DoCheckMigrate did not panic for a database enumeration failure")
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The database enumeration failure returned exit code %d", gplog.GetErrorCode())
	}
}

func TestDoCheckMigrateContinuesAfterFinding(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	sourceConnectionPool = connection
	targetConnectionPool = nil
	t.Cleanup(func() {
		sourceConnectionPool = nil
	})

	expectResourceGroupsEmpty(mock)
	expectMigrationTransaction(mock)
	firstCheck := sourceCheckTestCases[0]
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(firstCheck.query)).WillReturnRows(rowsForCheck(firstCheck, true))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	for _, testCase := range sourceCheckTestCases[1:] {
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
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

func TestDoCheckMigrateOverridesFindingWithQueryFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	sourceConnectionPool = connection
	targetConnectionPool = nil
	t.Cleanup(func() {
		sourceConnectionPool = nil
	})

	expectResourceGroupsEmpty(mock)
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
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The failed run returned exit code %d", gplog.GetErrorCode())
	}
}

func TestDoCheckMigrateReportsRollbackFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	sourceConnectionPool = connection
	targetConnectionPool = nil
	t.Cleanup(func() {
		sourceConnectionPool = nil
	})

	expectResourceGroupsEmpty(mock)
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
	connection, mock, _ := setupCheckTest(t)
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
	sourceConnectionPool = connection
	targetConnectionPool = nil
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
		sourceConnectionPool = nil
		shouldScrapeDatabaseNames = false
		createDBConn = originalCreateDBConn
	})

	mock.ExpectQuery(regexp.QuoteMeta(sourceDatabaseNamesQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"database_name"}).AddRow("unavailable").AddRow("working"),
	)
	expectResourceGroupsEmpty(mock)
	expectMigrationTransaction(workingMock)
	expectAllSourceChecksEmpty(workingMock)
	workingMock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 5 {
		t.Fatalf("The partial run returned exit code %d", gplog.GetErrorCode())
	}
}
