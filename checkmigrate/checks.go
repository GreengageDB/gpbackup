package checkmigrate

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/utils"
)

//go:embed sql/migration_check_setup.sql
var migrationCheckSetupQuery string

//go:embed sql/migration_check_setup_types.sql
var migrationCheckSetupTypesQuery string

//go:embed sql/source_database_names.sql
var sourceDatabaseNamesQuery string

// Checking for multi-column LIST partition keys.
//
//go:embed sql/multi_column_list_partitions.sql
var multiColumnListPartitionQuery string

// Checking for functions dependent on plpython2.
//
//go:embed sql/plpython2_dependent_functions.sql
var plpython2DependentFunctionQuery string

// Checking for views with removed operators.
//
//go:embed sql/removed_operator_views.sql
var removedOperatorViewQuery string

// Checking for views with removed functions.
//
//go:embed sql/removed_function_views.sql
var removedFunctionViewQuery string

// Checking for views with removed types.
//
//go:embed sql/removed_type_views.sql
var removedTypeViewQuery string

// Checking for removed "abstime", "reltime", "tinterval", "unknown" data type in user tables.
//
//go:embed sql/removed_data_types.sql
var removedDataTypeQuery string

// Checking for presence of required libraries.
//
//go:embed sql/required_libraries.sql
var requiredLibraryQuery string

// The difference in the AO parameters of partitioned tables.
//
//go:embed sql/missing_ao_options.sql
var missingAOOptionQuery string

// In the functions specified by EXECUTE ON, only RETURNS SETOF is used.
//
//go:embed sql/restricted_execute_on_functions.sql
var restrictedExecuteOnFunctionQuery string

// Unique constraint must include all partitioning keys.
//
//go:embed sql/incomplete_partition_indexes.sql
var incompletePartitionIndexQuery string

// Range partitions don't support START EXCLUSIVE or END INCLUSIVE for float and text.
//
//go:embed sql/incompatible_range_partitions.sql
var incompatibleRangePartitionQuery string

// Not supported triggers for statements.
//
//go:embed sql/statement_triggers.sql
var statementTriggerQuery string

type namedObjectResult struct {
	SchemaName string `db:"schema_name"`
	ObjectName string `db:"object_name"`
}

type databaseNameResult struct {
	DatabaseName string `db:"database_name"`
}

type functionResult struct {
	SchemaName        string `db:"schema_name"`
	ObjectName        string `db:"object_name"`
	IdentityArguments string `db:"identity_arguments"`
}

type viewResult struct {
	SchemaName   string `db:"schema_name"`
	ObjectName   string `db:"object_name"`
	RelationKind string `db:"relation_kind"`
}

type removedDataTypeResult struct {
	SchemaName string `db:"schema_name"`
	ObjectName string `db:"object_name"`
	ColumnName string `db:"column_name"`
}

type missingAOOptionResult struct {
	ParentSchema string `db:"parent_schema"`
	ParentName   string `db:"parent_name"`
	ChildSchema  string `db:"child_schema"`
	ChildName    string `db:"child_name"`
	ParentOption string `db:"parent_option"`
}

type incompletePartitionIndexResult struct {
	SchemaName string `db:"schema_name"`
	TableName  string `db:"table_name"`
	IndexName  string `db:"index_name"`
}

type incompatibleRangePartitionResult struct {
	ParentSchema    string `db:"parent_schema"`
	TableName       string `db:"table_name"`
	TypeName        string `db:"type_name"`
	PartitionSchema string `db:"partition_schema"`
	PartitionName   string `db:"partition_name"`
}

type statementTriggerResult struct {
	SchemaName  string `db:"schema_name"`
	TableName   string `db:"table_name"`
	TriggerName string `db:"trigger_name"`
}

type requiredLibraryResult struct {
	SchemaName        string `db:"schema_name"`
	ObjectName        string `db:"object_name"`
	IdentityArguments string `db:"identity_arguments"`
	LibraryName       string `db:"library_name"`
}

var relationKindLabels = map[string]string{
	"v": "view",
	"m": "materialized view",
	"f": "function",
}

func getRelationKindLabel(relationKind string) string {
	label, isKnown := relationKindLabels[relationKind]
	if isKnown {
		return label
	}

	return relationKind
}

func writeDatabaseFindingHeader(output *strings.Builder, databaseName string) {
	fmt.Fprintf(output, "Database %q contains these findings:\n", databaseName)
}

func writeObjectFinding(
	output *strings.Builder,
	objectName string,
	objectType string,
	schemaName string,
	detailFormat string,
	detailArguments ...interface{},
) {
	fmt.Fprintf(output, "  Object %q has type %q in schema %q.", objectName, objectType, schemaName)
	if detailFormat != "" {
		output.WriteByte(' ')
		fmt.Fprintf(output, detailFormat, detailArguments...)
	}
	output.WriteByte('\n')
}

func logFindingOutput(output *strings.Builder) {
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))
}

type migrationCheck struct {
	name               string
	requiredCapability string
	doRunCheck         func(*dbconn.DBConn) (int, error)
}

// Database checks inspect catalogs whose contents are scoped to the current database.
var databaseChecks = []migrationCheck{
	{name: "Checking for multi-column LIST partition keys", doRunCheck: checkMultiColumnListPartitions},
	{name: "Checking for functions dependent on plpython2", doRunCheck: checkPlpython2DependentFunctions},
	{
		name:               "Checking for views with removed operators",
		requiredCapability: migrationSupportCapability,
		doRunCheck:         checkViewsWithRemovedOperators,
	},
	{
		name:               "Checking for views with removed functions",
		requiredCapability: migrationSupportCapability,
		doRunCheck:         checkViewsWithRemovedFunctions,
	},
	{
		name:               "Checking for views with removed types",
		requiredCapability: migrationSupportCapability,
		doRunCheck:         checkViewsWithRemovedTypes,
	},
	{
		name: "Checking for removed \"abstime\", \"reltime\", \"tinterval\", \"unknown\" " +
			"data type in user tables",
		requiredCapability: dataTypeSupportCapability,
		doRunCheck:         checkRemovedDataTypes,
	},
	{name: "The difference in the AO parameters of partitioned tables", doRunCheck: checkMissingAOOptions},
	{
		name:       "In the functions specified by EXECUTE ON, only RETURNS SETOF is used",
		doRunCheck: checkRestrictedExecuteOnFunctions,
	},
	{name: "Unique constraint must include all partitioning keys", doRunCheck: checkIncompletePartitionIndexes},
	{
		name:       "Range partitions don't support START EXCLUSIVE or END INCLUSIVE for float and text",
		doRunCheck: checkIncompatibleRangePartitions,
	},
	{name: "Not supported triggers for statements", doRunCheck: checkStatementTriggers},
}

const (
	migrationSupportCapability  = "migration support functions"
	dataTypeSupportCapability   = "data type support function"
	setTransactionReadOnlyQuery = "SET TRANSACTION READ ONLY"
)

type migrationCheckSummary struct {
	completedCheckCount   int
	failedCheckCount      int
	unavailableCheckCount int
	findingCount          int
}

var errTargetDatabaseUnavailable = errors.New("target database is unavailable")

func beginMigrationTransaction(connection *dbconn.DBConn) error {
	beginError := connection.Begin()
	if beginError == nil {
		return nil
	}
	// DBConn.Begin can leave the transaction initialized when setting its isolation level fails.
	if len(connection.Tx) == 0 || connection.Tx[0] == nil {
		return beginError
	}

	return rollbackMigrationTransactionAfterError(connection, beginError)
}

func beginReadOnlyMigrationTransaction(connection *dbconn.DBConn) error {
	if beginError := beginMigrationTransaction(connection); beginError != nil {
		return beginError
	}
	if _, readOnlyError := connection.Exec(setTransactionReadOnlyQuery); readOnlyError != nil {
		return rollbackMigrationTransactionAfterError(
			connection,
			fmt.Errorf("setting source transaction read only failed with %w", readOnlyError),
		)
	}
	return nil
}

func rollbackMigrationTransactionAfterError(connection *dbconn.DBConn, transactionError error) error {
	rollbackError := connection.Rollback()
	if rollbackError != nil {
		return errors.Join(
			transactionError,
			fmt.Errorf("transaction rollback failed with %w", rollbackError),
		)
	}

	return transactionError
}

func prepareMigrationCheckCapabilities(connection *dbconn.DBConn) (map[string]bool, error) {
	if beginError := beginMigrationTransaction(connection); beginError != nil {
		return nil, beginError
	}

	setupQueries := []struct {
		capability string
		query      string
	}{
		{capability: migrationSupportCapability, query: migrationCheckSetupQuery},
		{capability: dataTypeSupportCapability, query: migrationCheckSetupTypesQuery},
	}
	availableCapabilities := make(map[string]bool, len(setupQueries))
	for _, setup := range setupQueries {
		if _, savepointError := connection.Exec("SAVEPOINT ggcheckmigrate_setup"); savepointError != nil {
			setupError := fmt.Errorf("%s setup savepoint failed with %w", setup.capability, savepointError)

			return nil, rollbackMigrationTransactionAfterError(connection, setupError)
		}
		_, setupError := connection.Exec(setup.query)
		if setupError != nil {
			_, recoveryError := connection.Exec("ROLLBACK TO SAVEPOINT ggcheckmigrate_setup")
			if recoveryError != nil {
				setupError = fmt.Errorf(
					"%s setup failed with %v and savepoint recovery failed with %w",
					setup.capability,
					setupError,
					recoveryError,
				)

				return nil, rollbackMigrationTransactionAfterError(connection, setupError)
			}
			gplog.Error(
				"Database %q could not provide %s because setup failed with %v",
				connection.DBName,
				setup.capability,
				setupError,
			)
		} else {
			availableCapabilities[setup.capability] = true
		}
		if _, releaseError := connection.Exec("RELEASE SAVEPOINT ggcheckmigrate_setup"); releaseError != nil {
			setupError := fmt.Errorf("%s setup savepoint release failed with %w", setup.capability, releaseError)

			return nil, rollbackMigrationTransactionAfterError(connection, setupError)
		}
	}

	if commitError := connection.Commit(); commitError != nil {
		return nil, fmt.Errorf("migration check setup transaction commit failed with %w", commitError)
	}

	return availableCapabilities, nil
}

func runMigrationCheck(connection *dbconn.DBConn, check migrationCheck) (
	summary migrationCheckSummary,
	executionError error,
) {
	if _, savepointError := connection.Exec("SAVEPOINT ggcheckmigrate_check"); savepointError != nil {
		return summary, fmt.Errorf("check %q savepoint failed with %w", check.name, savepointError)
	}

	var checkError error
	defer func() {
		if _, releaseError := connection.Exec("RELEASE SAVEPOINT ggcheckmigrate_check"); releaseError != nil {
			if executionError == nil && checkError != nil {
				executionError = fmt.Errorf("check %q failed with %w", check.name, checkError)
			}
			executionError = errors.Join(
				executionError,
				fmt.Errorf("check %q savepoint release failed with %w", check.name, releaseError),
			)
		}
	}()

	findingCount := 0
	findingCount, checkError = check.doRunCheck(connection)
	summary.findingCount = findingCount
	if checkError != nil {
		if _, recoveryError := connection.Exec("ROLLBACK TO SAVEPOINT ggcheckmigrate_check"); recoveryError != nil {
			executionError = errors.Join(
				fmt.Errorf("check %q failed with %w", check.name, checkError),
				fmt.Errorf("check %q savepoint recovery failed with %w", check.name, recoveryError),
			)

			return summary, executionError
		}
		if errors.Is(checkError, errTargetDatabaseUnavailable) {
			return summary, fmt.Errorf("check %q failed with %w", check.name, checkError)
		}

		summary.failedCheckCount++
		gplog.Error("Database %q failed check %q with %v", connection.DBName, check.name, checkError)
		gplog.Debug("Database %q completed check %q with an execution failure", connection.DBName, check.name)

		return summary, nil
	}

	summary.completedCheckCount++
	gplog.Debug("Database %q completed check %q with %d findings", connection.DBName, check.name, findingCount)

	return summary, nil
}

func runMigrationCheckPlan(
	connection *dbconn.DBConn,
	checks []migrationCheck,
	availableCapabilities map[string]bool,
) (migrationCheckSummary, error) {
	var summary migrationCheckSummary
	for _, check := range checks {
		if check.requiredCapability != "" && !availableCapabilities[check.requiredCapability] {
			summary.unavailableCheckCount++
			gplog.Error(
				"Database %q skipped check %q because %s is unavailable",
				connection.DBName,
				check.name,
				check.requiredCapability,
			)

			continue
		}

		gplog.Debug("Database %q is starting check %q", connection.DBName, check.name)
		checkSummary, executionError := runMigrationCheck(connection, check)
		summary.completedCheckCount += checkSummary.completedCheckCount
		summary.failedCheckCount += checkSummary.failedCheckCount
		summary.findingCount += checkSummary.findingCount
		if executionError != nil {
			return summary, executionError
		}
	}

	return summary, nil
}

func runMigrationChecks(
	sourceConnection *dbconn.DBConn,
	targetConnection *dbconn.DBConn,
) (summary migrationCheckSummary, executionError error) {
	if sourceConnection == nil {
		return summary, errors.New("source connection is not initialized")
	}
	availableCapabilities, setupError := prepareMigrationCheckCapabilities(sourceConnection)
	if setupError != nil {
		return summary, setupError
	}
	if beginError := beginReadOnlyMigrationTransaction(sourceConnection); beginError != nil {
		return summary, beginError
	}
	defer func() {
		rollbackError := sourceConnection.Rollback()
		if rollbackError != nil {
			executionError = errors.Join(
				executionError,
				fmt.Errorf("source transaction rollback failed with %w", rollbackError),
			)
		}
	}()

	checks := append([]migrationCheck(nil), databaseChecks...)
	if targetConnection != nil {
		checks = append(checks, migrationCheck{
			name: "Checking for presence of required libraries",
			doRunCheck: func(connection *dbconn.DBConn) (int, error) {
				return checkRequiredLibraries(connection, targetConnection)
			},
		})
	}

	return runMigrationCheckPlan(sourceConnection, checks, availableCapabilities)
}

func checkMultiColumnListPartitions(connection *dbconn.DBConn) (int, error) {
	results := make([]namedObjectResult, 0)
	if queryError := connection.Select(&results, multiColumnListPartitionQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains partitioned tables with a LIST partition key containing multiple columns, " +
			"which is not supported anymore. Consider modifying the partition key to use a single column or " +
			"dropping the tables.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"partitioned table",
			result.SchemaName,
			"The LIST partition key contains multiple columns.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkPlpython2DependentFunctions(connection *dbconn.DBConn) (int, error) {
	results := make([]functionResult, 0)
	if queryError := connection.Select(&results, plpython2DependentFunctionQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains \"plpython\" functions which rely on Python 2. " +
			"These functions must be either updated to use Python 3 or dropped before upgrade.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"function",
			result.SchemaName,
			"The function depends on plpython2 and its identity arguments are %q.",
			result.IdentityArguments,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkViewsWithRemovedOperators(connection *dbconn.DBConn) (int, error) {
	results := make([]viewResult, 0)
	if queryError := connection.Select(&results, removedOperatorViewQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains views using removed operators. " +
			"These operators are no longer present on the target version. " +
			"These views must be updated to use operators supported in the target version or removed before " +
			"upgrade can continue.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			getRelationKindLabel(result.RelationKind),
			result.SchemaName,
			"The view uses a removed operator.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkViewsWithRemovedFunctions(connection *dbconn.DBConn) (int, error) {
	results := make([]viewResult, 0)
	if queryError := connection.Select(&results, removedFunctionViewQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains views using removed functions. " +
			"These functions are no longer present on the target version. " +
			"These views must be updated to use functions supported in the target version or removed before " +
			"upgrade can continue.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			getRelationKindLabel(result.RelationKind),
			result.SchemaName,
			"The view uses a removed function.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkViewsWithRemovedTypes(connection *dbconn.DBConn) (int, error) {
	results := make([]viewResult, 0)
	if queryError := connection.Select(&results, removedTypeViewQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains views using removed types. " +
			"These types are no longer present on the target version. " +
			"These views must be updated to use types supported in the target version or removed before upgrade " +
			"can continue.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			getRelationKindLabel(result.RelationKind),
			result.SchemaName,
			"The view uses a removed type.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkRemovedDataTypes(connection *dbconn.DBConn) (int, error) {
	results := make([]removedDataTypeResult, 0)
	if queryError := connection.Select(&results, removedDataTypeQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains the \"abstime\", \"reltime\", \"tinterval\", or \"unknown\" data type " +
			"in user tables. These data types have been removed in version 7. Drop the problem columns or " +
			"change them to another data type.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ColumnName,
			"column",
			result.SchemaName,
			"Relation %q contains the affected column.",
			result.ObjectName,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkMissingAOOptions(connection *dbconn.DBConn) (int, error) {
	results := make([]missingAOOptionResult, 0)
	if queryError := connection.Select(&results, missingAOOptionQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains partitioned tables with child partitions, which do not have the parent table's " +
			"settings defined.\nIn version 7, they will be inherited from the parent table instead of being " +
			"taken by default.\nYou can recreate following tables with defined setting.\n" +
			"List of partitioned tables, partitions, and settings with the specified problem:\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ChildName,
			"partition",
			result.ChildSchema,
			"Parent table %q in schema %q defines option %q.",
			result.ParentName,
			result.ParentSchema,
			result.ParentOption,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkRestrictedExecuteOnFunctions(connection *dbconn.DBConn) (int, error) {
	results := make([]functionResult, 0)
	if queryError := connection.Select(&results, restrictedExecuteOnFunctionQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains not set-returning functions with MASTER, ALL SEGMENTS or INITPLAN EXECUTE ON.\n" +
			"You need to make the function set-returning or change EXECUTE ON to ANY.\n" +
			"List of functions with the specified problem:\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"function",
			result.SchemaName,
			"The function uses a restricted EXECUTE ON location and its identity arguments are %q.",
			result.IdentityArguments,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkIncompletePartitionIndexes(connection *dbconn.DBConn) (int, error) {
	results := make([]incompletePartitionIndexResult, 0)
	if queryError := connection.Select(&results, incompletePartitionIndexQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains partitioned tables with unique indexes, which do not have all partition keys.\n" +
			"In version 7, unique index on partitioned table must include all partitioning keys.\n" +
			"You can recreate following indexes with all partitioning keys.\n" +
			"List of partitioned tables and indexes with the specified problem:\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.IndexName,
			"index",
			result.SchemaName,
			"Partitioned table %q owns the index.",
			result.TableName,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkIncompatibleRangePartitions(connection *dbconn.DBConn) (int, error) {
	results := make([]incompatibleRangePartitionResult, 0)
	if queryError := connection.Select(&results, incompatibleRangePartitionQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"In version 7, range partitions don't support START EXCLUSIVE or END INCLUSIVE for columns with " +
			"types float and text.\nYou can recreate following tables without START EXCLUSIVE and " +
			"END INCLUSIVE.\nList of partitioned tables with the specified problem:\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.PartitionName,
			"partition",
			result.PartitionSchema,
			"Parent table %q in schema %q uses key type %q.",
			result.TableName,
			result.ParentSchema,
			result.TypeName,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkStatementTriggers(connection *dbconn.DBConn) (int, error) {
	results := make([]statementTriggerResult, 0)
	if queryError := connection.Select(&results, statementTriggerQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"In version 7, statement triggers are not supported.\n" +
			"You can use row triggers.\n" +
			"List of triggers with the specified problem:\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.TriggerName,
			"trigger",
			result.SchemaName,
			"Table %q owns the trigger.",
			result.TableName,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkRequiredLibraries(sourceConnection *dbconn.DBConn, targetConnection *dbconn.DBConn) (int, error) {
	results := make([]requiredLibraryResult, 0)
	if queryError := sourceConnection.Select(&results, requiredLibraryQuery); queryError != nil {
		return 0, queryError
	}

	isLibraryMissingByName := make(map[string]bool)
	missingFunctions := make([]requiredLibraryResult, 0)
	var targetExecutionError error
	for _, result := range results {
		isMissing, wasChecked := isLibraryMissingByName[result.LibraryName]
		if !wasChecked {
			loadQuery := fmt.Sprintf("LOAD '%s'", utils.EscapeSingleQuotes(result.LibraryName))
			_, loadError := targetConnection.Exec(loadQuery)
			if loadError != nil {
				if _, livenessError := targetConnection.Exec("SELECT 1"); livenessError != nil {
					targetExecutionError = errors.Join(
						errTargetDatabaseUnavailable,
						fmt.Errorf("loading target library %q failed with %w", result.LibraryName, loadError),
						fmt.Errorf("checking target database liveness failed with %w", livenessError),
					)

					break
				}
			}
			isMissing = loadError != nil
			isLibraryMissingByName[result.LibraryName] = isMissing
		}
		if isMissing {
			missingFunctions = append(missingFunctions, result)
		}
	}
	if len(missingFunctions) == 0 {
		return 0, targetExecutionError
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster references loadable libraries that are missing from the new cluster.\n" +
			"You can add these libraries to the new installation,\n" +
			"or remove the functions using them from the old installation.\n" +
			"The problematic libraries are:\n",
	)
	writeDatabaseFindingHeader(&output, sourceConnection.DBName)
	for _, result := range missingFunctions {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"function",
			result.SchemaName,
			"The function has identity arguments %q and requires library %q.",
			result.IdentityArguments,
			result.LibraryName,
		)
	}
	logFindingOutput(&output)

	return len(missingFunctions), targetExecutionError
}
