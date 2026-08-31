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

//go:embed sql/migration_check_setup_catalog.sql
var migrationCheckSetupCatalogQuery string

//go:embed sql/migration_check_setup_types.sql
var migrationCheckSetupTypesQuery string

//go:embed sql/source_database_names.sql
var sourceDatabaseNamesQuery string

//go:embed sql/multi_column_list_partitions.sql
var multiColumnListPartitionQuery string

//go:embed sql/plpython2_dependent_functions.sql
var plpython2DependentFunctionQuery string

//go:embed sql/removed_operator_views.sql
var removedOperatorViewQuery string

//go:embed sql/removed_function_views.sql
var removedFunctionViewQuery string

//go:embed sql/removed_type_views.sql
var removedTypeViewQuery string

//go:embed sql/removed_data_types.sql
var removedDataTypeQuery string

//go:embed sql/required_libraries.sql
var requiredLibraryQuery string

//go:embed sql/missing_ao_options.sql
var missingAOOptionQuery string

//go:embed sql/restricted_execute_on_functions.sql
var restrictedExecuteOnFunctionQuery string

//go:embed sql/incomplete_partition_indexes.sql
var incompletePartitionIndexQuery string

//go:embed sql/incompatible_range_partitions.sql
var incompatibleRangePartitionQuery string

//go:embed sql/statement_triggers.sql
var statementTriggerQuery string

//go:embed sql/removed_extensions.sql
var removedExtensionQuery string

//go:embed sql/arenadata_toolkit_schema.sql
var arenadataToolkitSchemaQuery string

//go:embed sql/resource_groups.sql
var resourceGroupQuery string

//go:embed sql/system_object_dependencies.sql
var systemObjectDependencyQuery string

//go:embed sql/deep_partition_templates.sql
var deepPartitionTemplateQuery string

//go:embed sql/incompatible_storage_options.sql
var incompatibleStorageOptionQuery string

//go:embed sql/removed_guc_settings.sql
var removedGUCSettingQuery string

//go:embed sql/disallowed_arrow_operators.sql
var disallowedArrowOperatorQuery string

//go:embed sql/partition_opfamilies.sql
var partitionOpfamilyQuery string

//go:embed sql/changed_function_signature_views.sql
var changedFunctionSignatureViewQuery string

//go:embed sql/removed_catalog_column_views.sql
var removedCatalogColumnViewQuery string

//go:embed sql/removed_catalog_relation_views.sql
var removedCatalogRelationViewQuery string

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

type resourceGroupResult struct {
	ObjectName string `db:"object_name"`
}

type systemObjectDependencyResult struct {
	SchemaName       string `db:"schema_name"`
	ObjectName       string `db:"object_name"`
	RelationKind     string `db:"relation_kind"`
	ReferencedObject string `db:"referenced_object"`
}

type configurationSettingResult struct {
	DatabaseName string `db:"database_name"`
	RoleName     string `db:"role_name"`
	Setting      string `db:"setting"`
	OptionName   string `db:"option_name"`
	GUCName      string `db:"guc_name"`
}

type partitionOpfamilyResult struct {
	SchemaName     string `db:"schema_name"`
	ObjectName     string `db:"object_name"`
	OperatorClass  string `db:"operator_class"`
	OperatorFamily string `db:"operator_family"`
}

type removedCatalogDependencyResult struct {
	SchemaName       string `db:"schema_name"`
	ObjectName       string `db:"object_name"`
	RelationKind     string `db:"relation_kind"`
	RemovedColumns   string `db:"removed_columns"`
	RemovedRelations string `db:"removed_relations"`
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

// Cluster checks inspect shared catalogs or settings through the bootstrap connection.
var clusterChecks = []migrationCheck{
	{name: "resource groups", doRunCheck: checkResourceGroups},
	{name: "incompatible storage options", doRunCheck: checkIncompatibleStorageOptions},
	{name: "removed GUC settings", doRunCheck: checkRemovedGUCSettings},
}

// Database checks inspect catalogs whose contents are scoped to the current database.
var databaseChecks = []migrationCheck{
	{name: "multi-column LIST partitions", doRunCheck: checkMultiColumnListPartitions},
	{name: "PL/Python 2 functions", doRunCheck: checkPlpython2DependentFunctions},
	{
		name:               "views with removed operators",
		requiredCapability: migrationSupportCapability,
		doRunCheck:         checkViewsWithRemovedOperators,
	},
	{
		name:               "views with removed functions",
		requiredCapability: migrationSupportCapability,
		doRunCheck:         checkViewsWithRemovedFunctions,
	},
	{
		name:               "views with removed types",
		requiredCapability: migrationSupportCapability,
		doRunCheck:         checkViewsWithRemovedTypes,
	},
	{
		name:               "views with changed function signatures",
		requiredCapability: migrationSupportCapability,
		doRunCheck:         checkViewsWithChangedFunctionSignatures,
	},
	{
		name:               "views with removed catalog columns",
		requiredCapability: catalogSupportCapability,
		doRunCheck:         checkViewsWithRemovedCatalogColumns,
	},
	{
		name:               "views with removed catalog relations",
		requiredCapability: catalogSupportCapability,
		doRunCheck:         checkViewsWithRemovedCatalogRelations,
	},
	{name: "removed data types", requiredCapability: dataTypeSupportCapability, doRunCheck: checkRemovedDataTypes},
	{name: "missing AO options", doRunCheck: checkMissingAOOptions},
	{name: "restricted EXECUTE ON functions", doRunCheck: checkRestrictedExecuteOnFunctions},
	{name: "incomplete partition indexes", doRunCheck: checkIncompletePartitionIndexes},
	{name: "incompatible range partitions", doRunCheck: checkIncompatibleRangePartitions},
	{name: "statement triggers", doRunCheck: checkStatementTriggers},
	{name: "removed extensions", doRunCheck: checkRemovedExtensions},
	{name: "arenadata_toolkit schema", doRunCheck: checkArenadataToolkitSchema},
	{name: "system object dependencies", doRunCheck: checkSystemObjectDependencies},
	{name: "deep partition templates", doRunCheck: checkDeepPartitionTemplates},
	{name: "disallowed arrow operators", doRunCheck: checkDisallowedArrowOperators},
	{name: "partition operator families", doRunCheck: checkPartitionOpfamilies},
}

const (
	migrationSupportCapability  = "migration support functions"
	catalogSupportCapability    = "catalog support functions"
	dataTypeSupportCapability   = "data type support function"
	setTransactionReadOnlyQuery = "SET TRANSACTION READ ONLY"
	setLocalTrackCountsQuery    = "SET LOCAL track_counts TO off"
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
	if _, trackCountsError := connection.Exec(setLocalTrackCountsQuery); trackCountsError != nil {
		return rollbackMigrationTransactionAfterError(
			connection,
			fmt.Errorf("disabling source transaction statistics failed with %w", trackCountsError),
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
		{capability: catalogSupportCapability, query: migrationCheckSetupCatalogQuery},
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
		if _, savepointError := connection.Exec("SAVEPOINT ggcheckmigrate_check"); savepointError != nil {
			return summary, fmt.Errorf("check %q savepoint failed with %w", check.name, savepointError)
		}
		findingCount, checkError := check.doRunCheck(connection)
		if checkError != nil {
			if _, recoveryError := connection.Exec("ROLLBACK TO SAVEPOINT ggcheckmigrate_check"); recoveryError != nil {
				return summary, errors.Join(
					fmt.Errorf("check %q failed with %w", check.name, checkError),
					fmt.Errorf("check %q savepoint recovery failed with %w", check.name, recoveryError),
				)
			}
			summary.findingCount += findingCount
			if errors.Is(checkError, errTargetDatabaseUnavailable) {
				if _, releaseError := connection.Exec("RELEASE SAVEPOINT ggcheckmigrate_check"); releaseError != nil {
					return summary, errors.Join(
						fmt.Errorf("check %q failed with %w", check.name, checkError),
						fmt.Errorf("check %q savepoint release failed with %w", check.name, releaseError),
					)
				}

				return summary, fmt.Errorf("check %q failed with %w", check.name, checkError)
			}

			summary.failedCheckCount++
			gplog.Error("Database %q failed check %q with %v", connection.DBName, check.name, checkError)
			if _, releaseError := connection.Exec("RELEASE SAVEPOINT ggcheckmigrate_check"); releaseError != nil {
				return summary, fmt.Errorf("check %q savepoint release failed with %w", check.name, releaseError)
			}
			gplog.Debug("Database %q completed check %q with an execution failure", connection.DBName, check.name)

			continue
		}

		summary.findingCount += findingCount
		summary.completedCheckCount++
		if _, releaseError := connection.Exec("RELEASE SAVEPOINT ggcheckmigrate_check"); releaseError != nil {
			return summary, fmt.Errorf("check %q savepoint release failed with %w", check.name, releaseError)
		}
		gplog.Debug("Database %q completed check %q with %d findings", connection.DBName, check.name, findingCount)
	}

	return summary, nil
}

func runClusterChecks(
	sourceConnection *dbconn.DBConn,
	checks []migrationCheck,
) (summary migrationCheckSummary, executionError error) {
	if beginError := beginReadOnlyMigrationTransaction(sourceConnection); beginError != nil {
		return summary, beginError
	}
	defer func() {
		rollbackError := sourceConnection.Rollback()
		if rollbackError != nil {
			executionError = errors.Join(
				executionError,
				fmt.Errorf("cluster transaction rollback failed with %w", rollbackError),
			)
		}
	}()

	return runMigrationCheckPlan(sourceConnection, checks, nil)
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

	checks := databaseChecks
	if targetConnection != nil {
		checks = append([]migrationCheck{
			{
				name: "required libraries",
				doRunCheck: func(connection *dbconn.DBConn) (int, error) {
					return checkRequiredLibraries(connection, targetConnection)
				},
			},
		}, checks...)
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
		"Your cluster contains partitioned tables with a LIST partition key containing multiple columns. " +
			"Modify the partition key to use one column or drop the affected tables.\n",
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
		"Your cluster contains PL/Python functions that rely on Python 2. " +
			"Update the functions to use Python 3 or drop them before migration.\n",
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
		"Your cluster contains views that use removed operators. " +
			"Update the views to use supported operators or remove them before migration.\n",
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
		"Your cluster contains views that use removed functions. " +
			"Update the views to use supported functions or remove them before migration.\n",
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
		"Your cluster contains views that use removed types. " +
			"Update the views to use supported types or remove them before migration.\n",
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

func checkViewsWithChangedFunctionSignatures(connection *dbconn.DBConn) (int, error) {
	results := make([]viewResult, 0)
	if queryError := connection.Select(&results, changedFunctionSignatureViewQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains views that call functions whose signatures changed in version 7. " +
			"Recreate the views with compatible function calls before migration.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			getRelationKindLabel(result.RelationKind),
			result.SchemaName,
			"The view calls a function with a changed signature.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkViewsWithRemovedCatalogColumns(connection *dbconn.DBConn) (int, error) {
	results := make([]removedCatalogDependencyResult, 0)
	if queryError := connection.Select(&results, removedCatalogColumnViewQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains views that reference system columns removed from version 7. " +
			"Update or remove the views before migration.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			getRelationKindLabel(result.RelationKind),
			result.SchemaName,
			"The view references removed columns %q.",
			result.RemovedColumns,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkViewsWithRemovedCatalogRelations(connection *dbconn.DBConn) (int, error) {
	results := make([]removedCatalogDependencyResult, 0)
	if queryError := connection.Select(&results, removedCatalogRelationViewQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains views that reference system relations removed from version 7. " +
			"Update or remove the views before migration.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			getRelationKindLabel(result.RelationKind),
			result.SchemaName,
			"The view references removed relations %q.",
			result.RemovedRelations,
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
		"Your cluster contains user columns that depend on the removed abstime, reltime, tinterval, " +
			"or unknown data types. Convert each column to a supported type or drop the affected column.\n",
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
		"Your cluster contains child partitions that do not define the parent table settings. " +
			"Version 7 inherits these settings from the parent table. " +
			"Recreate the affected tables with explicit settings.\n",
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
		"Your cluster contains functions that are not set-returning and use MASTER, ALL SEGMENTS, " +
			"or INITPLAN EXECUTE ON. Make each function set-returning or change EXECUTE ON to ANY.\n",
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
		"Your cluster contains partitioned tables with unique indexes that omit partition keys. " +
			"Version 7 requires every partition key in each unique index. " +
			"Recreate the affected indexes with every partition key.\n",
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
		"Your cluster contains range partitions that use START EXCLUSIVE or END INCLUSIVE boundaries " +
			"on float, numeric, or text columns. Version 7 does not support these boundaries. " +
			"Recreate the affected tables without them.\n",
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
		"Your cluster contains statement triggers. Version 7 does not support them. " +
			"Replace the affected triggers with row triggers.\n",
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
		"Your cluster contains functions that require libraries missing from the target cluster. " +
			"Add the libraries to the target cluster or remove the affected functions from the source cluster.\n",
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

func checkRemovedExtensions(connection *dbconn.DBConn) (int, error) {
	results := make([]namedObjectResult, 0)
	if queryError := connection.Select(&results, removedExtensionQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains extensions that are absent from version 7 because their functionality " +
			"moved into the server. Drop the affected extensions before running gpbackup.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"extension",
			result.SchemaName,
			"The extension must be dropped before migration.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkArenadataToolkitSchema(connection *dbconn.DBConn) (int, error) {
	results := make([]namedObjectResult, 0)
	if queryError := connection.Select(&results, arenadataToolkitSchemaQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains the arenadata_toolkit schema. " +
			"Exclude this schema from the backup with --exclude-schema arenadata_toolkit.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"schema",
			result.SchemaName,
			"Version 7 provides a different schema definition.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkResourceGroups(connection *dbconn.DBConn) (int, error) {
	results := make([]resourceGroupResult, 0)
	if queryError := connection.Select(&results, resourceGroupQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains resource groups whose settings changed in version 7. " +
			"Run gpbackup with --without-globals and recreate global objects on the target cluster.\n",
	)
	for _, result := range results {
		fmt.Fprintf(
			&output,
			"The cluster contains object %q of type %q. The resource group requires version 7 settings.\n",
			result.ObjectName,
			"resource group",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkSystemObjectDependencies(connection *dbconn.DBConn) (int, error) {
	results := make([]systemObjectDependencyResult, 0)
	if queryError := connection.Select(&results, systemObjectDependencyQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains user objects that reference system relations. " +
			"Review each definition against the version 7 system catalog.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			getRelationKindLabel(result.RelationKind),
			result.SchemaName,
			"The object references %q.",
			result.ReferencedObject,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkDeepPartitionTemplates(connection *dbconn.DBConn) (int, error) {
	results := make([]namedObjectResult, 0)
	if queryError := connection.Select(&results, deepPartitionTemplateQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains subpartition templates deeper than the second partition level. " +
			"Save and remove these templates before backup, then recreate them with version 7 syntax.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"partitioned table",
			result.SchemaName,
			"The table has a deep subpartition template.",
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkIncompatibleStorageOptions(connection *dbconn.DBConn) (int, error) {
	results := make([]configurationSettingResult, 0)
	if queryError := connection.Select(&results, incompatibleStorageOptionQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains gp_default_storage_options assignments with options that are incompatible " +
			"with version 7. Remove the incompatible options before migration.\n",
	)
	for _, result := range results {
		fmt.Fprintf(
			&output,
			"Database setting for database %q and role %q contains option %q in %q.\n",
			result.DatabaseName,
			result.RoleName,
			result.OptionName,
			result.Setting,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkRemovedGUCSettings(connection *dbconn.DBConn) (int, error) {
	results := make([]configurationSettingResult, 0)
	if queryError := connection.Select(&results, removedGUCSettingQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains persistent assignments for settings that were removed from version 7. " +
			"Remove the assignments before migration.\n",
	)
	for _, result := range results {
		fmt.Fprintf(
			&output,
			"Database setting for database %q and role %q contains removed setting %q in %q.\n",
			result.DatabaseName,
			result.RoleName,
			result.GUCName,
			result.Setting,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkDisallowedArrowOperators(connection *dbconn.DBConn) (int, error) {
	results := make([]namedObjectResult, 0)
	if queryError := connection.Select(&results, disallowedArrowOperatorQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString("Your cluster contains user-defined => operators. Drop the operators before migration.\n")
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(&output, result.ObjectName, "operator", result.SchemaName, "")
	}
	logFindingOutput(&output)

	return len(results), nil
}

func checkPartitionOpfamilies(connection *dbconn.DBConn) (int, error) {
	results := make([]partitionOpfamilyResult, 0)
	if queryError := connection.Select(&results, partitionOpfamilyQuery); queryError != nil {
		return 0, queryError
	}
	if len(results) == 0 {
		return 0, nil
	}

	var output strings.Builder
	output.WriteString(
		"Your cluster contains partition keys whose operator families lack support procedure 1. " +
			"Add the support procedure or recreate the affected partitioned tables with supported operator classes.\n",
	)
	writeDatabaseFindingHeader(&output, connection.DBName)
	for _, result := range results {
		writeObjectFinding(
			&output,
			result.ObjectName,
			"partitioned table",
			result.SchemaName,
			"Operator class %q uses operator family %q.",
			result.OperatorClass,
			result.OperatorFamily,
		)
	}
	logFindingOutput(&output)

	return len(results), nil
}
