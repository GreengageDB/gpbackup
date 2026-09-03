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

//go:embed sql/source_database_names.sql
var sourceDatabaseNamesQuery string

//go:embed sql/removed_operator_views.sql
var removedOperatorViewQuery string

//go:embed sql/removed_function_views.sql
var removedFunctionViewQuery string

//go:embed sql/removed_type_views.sql
var removedTypeViewQuery string

//go:embed sql/required_libraries.sql
var requiredLibraryQuery string

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

type viewResult struct {
	SchemaName   string `db:"schema_name"`
	ObjectName   string `db:"object_name"`
	RelationKind string `db:"relation_kind"`
}

type requiredLibraryResult struct {
	SchemaName        string `db:"schema_name"`
	ObjectName        string `db:"object_name"`
	IdentityArguments string `db:"identity_arguments"`
	LibraryName       string `db:"library_name"`
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
	{name: "incompatible storage options", doRunCheck: checkIncompatibleStorageOptions},
	{name: "removed GUC settings", doRunCheck: checkRemovedGUCSettings},
}

// Database checks inspect catalogs whose contents are scoped to the current database.
var databaseChecks = []migrationCheck{
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
	{name: "disallowed arrow operators", doRunCheck: checkDisallowedArrowOperators},
	{name: "partition operator families", doRunCheck: checkPartitionOpfamilies},
}

const (
	migrationSupportCapability  = "migration support functions"
	catalogSupportCapability    = "catalog support functions"
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
		{capability: catalogSupportCapability, query: migrationCheckSetupCatalogQuery},
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
		summary.unavailableCheckCount += checkSummary.unavailableCheckCount
		summary.findingCount += checkSummary.findingCount
		if executionError != nil {
			return summary, executionError
		}
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

	checks := append([]migrationCheck(nil), databaseChecks...)
	if targetConnection != nil {
		checks = append(checks, migrationCheck{
			name: "required libraries",
			doRunCheck: func(connection *dbconn.DBConn) (int, error) {
				return checkRequiredLibraries(connection, targetConnection)
			},
		})
	}

	return runMigrationCheckPlan(sourceConnection, checks, availableCapabilities)
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
