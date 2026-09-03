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
	name            string
	isClusterCheck  bool
	check           func(*dbconn.DBConn) (int, error)
	query           string
	columns         []string
	rows            [][]driver.Value
	problemText     string
	expectedObjects []string
}

var sourceCheckTestCases = []sourceCheckTestCase{
	{
		name:        "views with removed operators",
		check:       checkViewsWithRemovedOperators,
		query:       removedOperatorViewQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind"},
		rows:        [][]driver.Value{{"public", "operator_view", "v"}, {"reports", "operator_materialized_view", "m"}},
		problemText: "views that use removed operators",
		expectedObjects: []string{
			`Object "operator_view" has type "view" in schema "public"`,
			`Object "operator_materialized_view" has type "materialized view" in schema "reports"`,
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
			`Object "function_view" has type "view" in schema "public"`,
			`Object "function_materialized_view" has type "materialized view" in schema "reports"`,
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
			`Object "type_view" has type "view" in schema "public"`,
			`Object "type_materialized_view" has type "materialized view" in schema "reports"`,
		},
	},
	{
		name:        "views with changed function signatures",
		check:       checkViewsWithChangedFunctionSignatures,
		query:       changedFunctionSignatureViewQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind"},
		rows:        [][]driver.Value{{"public", "changed_signature_view", "v"}},
		problemText: "views that call functions whose signatures changed in version 7",
		expectedObjects: []string{
			`Object "changed_signature_view" has type "view" in schema "public"`,
		},
	},
	{
		name:        "views with removed catalog columns",
		check:       checkViewsWithRemovedCatalogColumns,
		query:       removedCatalogColumnViewQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind", "removed_columns"},
		rows:        [][]driver.Value{{"public", "removed_column_view", "v", "pg_class.relstorage"}},
		problemText: "views that reference system columns removed from version 7",
		expectedObjects: []string{
			`Object "removed_column_view" has type "view" in schema "public"`,
		},
	},
	{
		name:        "views with removed catalog relations",
		check:       checkViewsWithRemovedCatalogRelations,
		query:       removedCatalogRelationViewQuery,
		columns:     []string{"schema_name", "object_name", "relation_kind", "removed_relations"},
		rows:        [][]driver.Value{{"public", "removed_relation_view", "v", "pg_partition"}},
		problemText: "views that reference system relations removed from version 7",
		expectedObjects: []string{
			`Object "removed_relation_view" has type "view" in schema "public"`,
		},
	},
	{
		name:           "incompatible storage options",
		isClusterCheck: true,
		check:          checkIncompatibleStorageOptions,
		query:          incompatibleStorageOptionQuery,
		columns:        []string{"database_name", "role_name", "setting", "option_name"},
		rows:           [][]driver.Value{{"source_database", "application_role", "gp_default_storage_options=appendonly=true", "appendonly"}},
		problemText:    "gp_default_storage_options assignments with options that are incompatible with version 7",
		expectedObjects: []string{
			`database "source_database" and role "application_role" contains option "appendonly"`,
		},
	},
	{
		name:           "removed GUC settings",
		isClusterCheck: true,
		check:          checkRemovedGUCSettings,
		query:          removedGUCSettingQuery,
		columns:        []string{"database_name", "role_name", "guc_name", "setting"},
		rows:           [][]driver.Value{{"source_database", "application_role", "password_hash_algorithm", "password_hash_algorithm=sha-256"}},
		problemText:    "persistent assignments for settings that were removed from version 7",
		expectedObjects: []string{
			`database "source_database" and role "application_role" contains removed setting "password_hash_algorithm"`,
		},
	},
	{
		name:        "disallowed arrow operators",
		check:       checkDisallowedArrowOperators,
		query:       disallowedArrowOperatorQuery,
		columns:     []string{"schema_name", "object_name"},
		rows:        [][]driver.Value{{"public", "=>"}},
		problemText: "user-defined => operators",
		expectedObjects: []string{
			`Object "=>" has type "operator" in schema "public"`,
		},
	},
	{
		name:        "partition operator families",
		check:       checkPartitionOpfamilies,
		query:       partitionOpfamilyQuery,
		columns:     []string{"schema_name", "object_name", "operator_class", "operator_family"},
		rows:        [][]driver.Value{{"public", "partitioned_table", "custom_ops", "custom_family"}},
		problemText: "partition keys whose operator families lack support procedure 1",
		expectedObjects: []string{
			`Object "partitioned_table" has type "partitioned table" in schema "public"`,
			`Operator class "custom_ops" uses operator family "custom_family"`,
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
		if testCase.isClusterCheck {
			continue
		}
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
}

func expectMigrationSetupQueries(mock sqlmock.Sqlmock) {
	for _, setupQuery := range []string{migrationCheckSetupQuery, migrationCheckSetupCatalogQuery} {
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

func expectClusterChecksEmpty(mock sqlmock.Sqlmock) {
	expectReadOnlyMigrationTransaction(mock)
	for _, query := range []string{incompatibleStorageOptionQuery, removedGUCSettingQuery} {
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		var columns []string
		switch query {
		case incompatibleStorageOptionQuery:
			columns = []string{"database_name", "role_name", "setting", "option_name"}
		default:
			columns = []string{"database_name", "role_name", "guc_name", "setting"}
		}
		mock.ExpectQuery(regexp.QuoteMeta(query)).WillReturnRows(sqlmock.NewRows(columns))
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectRollback()
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
			if !testCase.isClusterCheck {
				databaseHeader := fmt.Sprintf(
					"Database %q contains these findings:",
					connection.DBName,
				)
				if strings.Count(output, databaseHeader) != 1 {
					t.Fatalf("The database heading occurred an unexpected number of times in %q", output)
				}
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
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))
	targetMock.ExpectExec(regexp.QuoteMeta("LOAD 'odd''lib'")).WillReturnError(errors.New("missing quoted library"))
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))

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
	targetMock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))

	findingCount, err := checkRequiredLibraries(sourceConnection, targetConnection)

	if err != nil {
		t.Fatalf("The library check returned an error with %v", err)
	}
	if findingCount != 2 {
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
		sqlmock.NewRows([]string{"schema_name", "object_name", "identity_arguments", "library_name"}).
			AddRow("public", "missing_function", "integer", "$libdir/missing").
			AddRow("public", "unreachable_function", "integer", "$libdir/unreachable"),
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

func TestMigrationSetupUsesTemporarySchema(t *testing.T) {
	if strings.Contains(migrationCheckSetupQuery, "__ggcheckmigrate_tmp") {
		t.Fatal("The migration setup uses the shared schema")
	}
	if !strings.Contains(migrationCheckSetupQuery, "pg_temp") {
		t.Fatal("The migration setup does not use the temporary schema")
	}
	for _, query := range []string{removedOperatorViewQuery, removedFunctionViewQuery, removedTypeViewQuery, changedFunctionSignatureViewQuery, removedCatalogColumnViewQuery, removedCatalogRelationViewQuery} {
		if !strings.Contains(query, "pg_temp") {
			t.Fatalf("The support query does not use the temporary schema in %q", query)
		}
	}
}

func TestUnknownRelationKindUsesCatalogCode(t *testing.T) {
	if actualLabel := getRelationKindLabel("x"); actualLabel != "x" {
		t.Fatalf("The unknown relation kind label is %q", actualLabel)
	}
}

func TestSourceDatabaseEnumerationIncludesConnectableTemplateDatabases(t *testing.T) {
	if !strings.Contains(sourceDatabaseNamesQuery, "datallowconn") {
		t.Fatal("The source database enumeration does not require connectable databases")
	}
	if !strings.Contains(sourceDatabaseNamesQuery, "datname <> 'template0'") {
		t.Fatal("The source database enumeration does not exclude template0")
	}
	if strings.Contains(sourceDatabaseNamesQuery, "datistemplate") {
		t.Fatal("The source database enumeration excludes connectable template databases")
	}
}

func TestSourceChecksUseNamespaceFilters(t *testing.T) {
	queries := []string{
		removedOperatorViewQuery,
		removedFunctionViewQuery,
		removedTypeViewQuery,
		changedFunctionSignatureViewQuery,
		removedCatalogColumnViewQuery,
		removedCatalogRelationViewQuery,
		requiredLibraryQuery,
		disallowedArrowOperatorQuery,
		partitionOpfamilyQuery,
	}
	for _, query := range queries {
		if !strings.Contains(query, "pg_temp_") || !strings.Contains(query, "information_schema") {
			t.Fatalf("The source check does not filter non-user schemas in %q", query)
		}
	}
}

func TestRequiredLibrariesMatchBackedUpFunctionScope(t *testing.T) {
	for _, schemaName := range []string{"gp_toolkit", "pg_aoseg", "pg_bitmapindex"} {
		if !strings.Contains(requiredLibraryQuery, schemaName) {
			t.Fatalf("The required library check includes excluded schema %s", schemaName)
		}
	}
	if !strings.Contains(requiredLibraryQuery, "dependency.deptype = 'e'") {
		t.Fatal("The required library check includes extension-owned functions")
	}
}

func TestConfigurationQueriesUseValidCoalesceExpressions(t *testing.T) {
	for _, query := range []string{incompatibleStorageOptionQuery, removedGUCSettingQuery} {
		if strings.Contains(query, "pg_catalog.coalesce") {
			t.Fatal("The configuration check schema-qualifies the COALESCE expression")
		}
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

	expectClusterChecksEmpty(sourceMock)
	expectMigrationTransaction(sourceMock)
	expectAllSourceChecksEmpty(sourceMock)
	sourceMock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	sourceMock.ExpectQuery(regexp.QuoteMeta(requiredLibraryQuery)).WillReturnRows(
		sqlmock.NewRows([]string{"schema_name", "object_name", "identity_arguments", "library_name"}).AddRow("public", "missing_fn", "integer", "$libdir/missing"),
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

	expectClusterChecksEmpty(mock)
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
	expectClusterChecksEmpty(mock)
	expectMigrationTransaction(mock)
	expectAllSourceChecksEmpty(mock)
	mock.ExpectRollback()

	expectMigrationTransaction(applicationMock)
	firstCheck := sourceCheckTestCases[0]
	applicationMock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	applicationMock.ExpectQuery(regexp.QuoteMeta(firstCheck.query)).WillReturnRows(rowsForCheck(firstCheck, true))
	applicationMock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	for _, testCase := range sourceCheckTestCases[1:] {
		if testCase.isClusterCheck {
			continue
		}
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
		"  completed cluster checks:          2\n" +
		"  failed cluster checks:             0\n" +
		"  completed database checks:        16\n" +
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

func TestDoCheckMigrateChecksBootstrapDatabaseWhenEnumerationIsEmpty(t *testing.T) {
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
	expectClusterChecksEmpty(mock)
	expectMigrationTransaction(mock)
	expectAllSourceChecksEmpty(mock)
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 0 {
		t.Fatalf("The bootstrap database run returned exit code %d", gplog.GetErrorCode())
	}
	expectedWarning := `Source database enumeration returned no rows. Database "source_database" will be checked.`
	if !strings.Contains(string(stdout.Contents()), expectedWarning) {
		t.Fatalf("The bootstrap database run did not print %q in %q", expectedWarning, stdout.Contents())
	}
}

func TestDoCheckMigrateContinuesAfterFinding(t *testing.T) {
	connection, mock, stderr := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectClusterChecksEmpty(mock)
	expectMigrationTransaction(mock)
	firstCheck := sourceCheckTestCases[0]
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(firstCheck.query)).WillReturnRows(rowsForCheck(firstCheck, true))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	for _, testCase := range sourceCheckTestCases[1:] {
		if testCase.isClusterCheck {
			continue
		}
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

func TestDoCheckMigrateReportsCheckSavepointReleaseFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectClusterChecksEmpty(mock)
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

func TestRunMigrationChecksKeepsIndependentChecksAfterCatalogSetupFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(migrationCheckSetupQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(migrationCheckSetupCatalogQuery)).WillReturnError(errors.New("catalog support unavailable"))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_setup")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	expectReadOnlyMigrationTransaction(mock)
	for _, testCase := range sourceCheckTestCases {
		if testCase.isClusterCheck || testCase.query == removedCatalogColumnViewQuery || testCase.query == removedCatalogRelationViewQuery {
			continue
		}
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectRollback()

	summary, executionError := runMigrationChecks(connection, nil)
	if executionError != nil {
		t.Fatalf("The partial capability run returned an error with %v", executionError)
	}
	if summary.completedCheckCount != 6 || summary.unavailableCheckCount != 2 || summary.failedCheckCount != 0 {
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

	expectClusterChecksEmpty(mock)
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

func TestDoCheckMigrateReportsFindingAndQueryFailureAsCheckResults(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectClusterChecksEmpty(mock)
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
		if testCase.isClusterCheck {
			continue
		}
		mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta(testCase.query)).WillReturnRows(rowsForCheck(testCase, false))
		mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectRollback()

	if recoveredValue := callDoCheckMigrate(); recoveredValue != nil {
		t.Fatalf("DoCheckMigrate panicked with %v", recoveredValue)
	}
	if gplog.GetErrorCode() != 1 {
		t.Fatalf("The failed run returned exit code %d", gplog.GetErrorCode())
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

func TestRunClusterChecksPreservesCheckRecoveryAndRollbackFailures(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	checkError := errors.New("check failed")
	recoveryError := errors.New("recovery failed")
	releaseError := errors.New("release failed")
	rollbackError := errors.New("rollback failed")
	expectReadOnlyMigrationTransaction(mock)
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK TO SAVEPOINT ggcheckmigrate_check")).WillReturnError(recoveryError)
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnError(releaseError)
	mock.ExpectRollback().WillReturnError(rollbackError)

	summary, executionError := runClusterChecks(
		connection,
		[]migrationCheck{{
			name: "failing check",
			doRunCheck: func(*dbconn.DBConn) (int, error) {
				return 0, checkError
			},
		}},
	)

	if summary.failedCheckCount != 0 {
		t.Fatalf("The unrecoverable check reported %d failed checks", summary.failedCheckCount)
	}
	if !errors.Is(executionError, checkError) ||
		!errors.Is(executionError, recoveryError) ||
		!errors.Is(executionError, releaseError) ||
		!errors.Is(executionError, rollbackError) {
		t.Fatalf("The check, recovery, release, and rollback errors were not preserved: %v", executionError)
	}
}

func TestRunClusterChecksCountsSuccessfulCheckBeforeReleaseFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	releaseError := errors.New("release failed")
	expectReadOnlyMigrationTransaction(mock)
	mock.ExpectExec(regexp.QuoteMeta("SAVEPOINT ggcheckmigrate_check")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("RELEASE SAVEPOINT ggcheckmigrate_check")).WillReturnError(releaseError)
	mock.ExpectRollback()

	summary, executionError := runClusterChecks(
		connection,
		[]migrationCheck{{
			name: "successful check",
			doRunCheck: func(*dbconn.DBConn) (int, error) {
				return 0, nil
			},
		}},
	)

	if summary.completedCheckCount != 1 || summary.failedCheckCount != 0 {
		t.Fatalf("The successful check summary was %+v", summary)
	}
	if !errors.Is(executionError, releaseError) {
		t.Fatalf("The release failure was not returned: %v", executionError)
	}
}

func TestRunClusterChecksRollsBackAfterIsolationFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")).WillReturnError(errors.New("isolation failed"))
	mock.ExpectRollback()

	summary, executionError := runClusterChecks(connection, []migrationCheck{{
		name: "cluster check",
		doRunCheck: func(*dbconn.DBConn) (int, error) {
			return 0, nil
		},
	}})
	if executionError == nil || !strings.Contains(executionError.Error(), "isolation failed") {
		t.Fatalf("The cluster isolation failure was not reported: %v", executionError)
	}
	if summary.failedCheckCount != 0 {
		t.Fatalf("The cluster isolation failure reported %d failed checks", summary.failedCheckCount)
	}
	if connection.Tx[0] != nil {
		t.Fatal("The failed cluster transaction remained installed")
	}
}

func TestDoCheckMigrateReportsRollbackFailure(t *testing.T) {
	connection, mock, _ := setupCheckTest(t)
	bootstrapSourceConnection = connection
	targetConnection = nil
	t.Cleanup(func() {
		bootstrapSourceConnection = nil
	})

	expectClusterChecksEmpty(mock)
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
	expectClusterChecksEmpty(mock)
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
		!strings.Contains(output, "  completed database checks:         8\n") {
		t.Fatalf("The partial run printed an unexpected summary in %q", output)
	}
}
