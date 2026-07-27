package checkmigrate

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/GreengageDB/gp-common-go-libs/dbconn"
	"github.com/GreengageDB/gp-common-go-libs/gplog"
	"github.com/GreengageDB/gpbackup/utils"
)

//go:embed sql/migration_check_setup.sql
var migrationCheckSetupQuery string

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

type namedObjectResult struct {
	SchemaName string `db:"schema_name"`
	ObjectName string `db:"object_name"`
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
	LibraryName string `db:"library_name"`
}

func runMigrationChecks(sourceConnection *dbconn.DBConn, targetConnection *dbconn.DBConn) (hasFindings bool, returnError error) {
	if sourceConnection == nil {
		return false, fmt.Errorf("The source connection is not initialized")
	}

	beginError := sourceConnection.Begin()
	if len(sourceConnection.Tx) > 0 && sourceConnection.Tx[0] != nil {
		defer func() {
			rollbackError := sourceConnection.Rollback()
			if rollbackError == nil {
				return
			}
			if returnError != nil {
				returnError = fmt.Errorf("%v. The source transaction rollback failed with %w", returnError, rollbackError)
				return
			}

			returnError = fmt.Errorf("The source transaction rollback failed with %w", rollbackError)
		}()
	}
	if beginError != nil {
		return false, beginError
	}

	if targetConnection != nil {
		hasCheckFindings, checkError := checkRequiredLibraries(sourceConnection, targetConnection)
		if checkError != nil {
			return false, checkError
		}
		hasFindings = hasFindings || hasCheckFindings
	}

	_, returnError = sourceConnection.Exec(migrationCheckSetupQuery)
	if returnError != nil {
		return false, returnError
	}

	hasCheckFindings, returnError := checkMultiColumnListPartitions(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkPlpython2DependentFunctions(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkViewsWithRemovedOperators(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkViewsWithRemovedFunctions(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkViewsWithRemovedTypes(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkRemovedDataTypes(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkMissingAOOptions(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkRestrictedExecuteOnFunctions(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkIncompletePartitionIndexes(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkIncompatibleRangePartitions(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	hasCheckFindings, returnError = checkStatementTriggers(sourceConnection)
	if returnError != nil {
		return false, returnError
	}
	hasFindings = hasFindings || hasCheckFindings

	return hasFindings, nil
}

func checkMultiColumnListPartitions(connection *dbconn.DBConn) (bool, error) {
	results := make([]namedObjectResult, 0)
	if err := connection.Select(&results, multiColumnListPartitionQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your installation contains partitioned tables with a LIST partition key containing multiple columns, which is not supported anymore. Consider modifying the partition key to use a single column or dropping the tables.\n")
	for _, result := range results {
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ObjectName, "partitioned table", result.SchemaName, "The LIST partition key contains multiple columns")
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkPlpython2DependentFunctions(connection *dbconn.DBConn) (bool, error) {
	results := make([]namedObjectResult, 0)
	if err := connection.Select(&results, plpython2DependentFunctionQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your installation contains plpython functions which rely on Python 2. These functions must be updated to use Python 3 or dropped before migration.\n")
	for _, result := range results {
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ObjectName, "function", result.SchemaName, "The function depends on plpython2")
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkViewsWithRemovedOperators(connection *dbconn.DBConn) (bool, error) {
	results := make([]viewResult, 0)
	if err := connection.Select(&results, removedOperatorViewQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your installation contains views using removed operators. These operators are no longer present in the target version. These views must be updated to use supported operators or removed before migration.\n")
	for _, result := range results {
		objectType := "view"
		if result.RelationKind == "m" {
			objectType = "materialized view"
		}
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ObjectName, objectType, result.SchemaName, "The view uses a removed operator")
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkViewsWithRemovedFunctions(connection *dbconn.DBConn) (bool, error) {
	results := make([]viewResult, 0)
	if err := connection.Select(&results, removedFunctionViewQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your installation contains views using removed functions. These functions are no longer present in the target version. These views must be updated to use supported functions or removed before migration.\n")
	for _, result := range results {
		objectType := "view"
		if result.RelationKind == "m" {
			objectType = "materialized view"
		}
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ObjectName, objectType, result.SchemaName, "The view uses a removed function")
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkViewsWithRemovedTypes(connection *dbconn.DBConn) (bool, error) {
	results := make([]viewResult, 0)
	if err := connection.Select(&results, removedTypeViewQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your installation contains views using removed types. These types are no longer present in the target version. These views must be updated to use supported types or removed before migration.\n")
	for _, result := range results {
		objectType := "view"
		if result.RelationKind == "m" {
			objectType = "materialized view"
		}
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ObjectName, objectType, result.SchemaName, "The view uses a removed type")
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkRemovedDataTypes(connection *dbconn.DBConn) (bool, error) {
	results := make([]removedDataTypeResult, 0)
	if err := connection.Select(&results, removedDataTypeQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your cluster contains user columns that depend on the removed abstime, reltime, tinterval, or unknown data types. Convert each column to a supported type or drop the affected column.\n")
	for _, result := range results {
		detail := fmt.Sprintf("Relation %q contains the affected column", result.ObjectName)
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ColumnName, "column", result.SchemaName, detail)
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkMissingAOOptions(connection *dbconn.DBConn) (bool, error) {
	results := make([]missingAOOptionResult, 0)
	if err := connection.Select(&results, missingAOOptionQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your cluster contains partitioned tables with child partitions, which do not have the parent table's settings defined.\nIn version 7, they will be inherited from the parent table instead of being taken by default.\nYou can recreate following tables with defined setting.\nList of partitioned tables, partitions, and settings with the specified problem:\n")
	for _, result := range results {
		detail := fmt.Sprintf("Parent table %q in schema %q defines option %q", result.ParentName, result.ParentSchema, result.ParentOption)
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ChildName, "partition", result.ChildSchema, detail)
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkRestrictedExecuteOnFunctions(connection *dbconn.DBConn) (bool, error) {
	results := make([]namedObjectResult, 0)
	if err := connection.Select(&results, restrictedExecuteOnFunctionQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your cluster contains not set-returning functions with MASTER, ALL SEGMENTS or INITPLAN EXECUTE ON.\nYou need to make the function set-returning or change EXECUTE ON to ANY.\nList of functions with the specified problem:\n")
	for _, result := range results {
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.ObjectName, "function", result.SchemaName, "The function uses a restricted EXECUTE ON location")
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkIncompletePartitionIndexes(connection *dbconn.DBConn) (bool, error) {
	results := make([]incompletePartitionIndexResult, 0)
	if err := connection.Select(&results, incompletePartitionIndexQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your cluster contains partitioned tables with unique indexes, which do not have all partition keys.\nIn version 7, unique index on partitioned table must include all partitioning keys.\nYou can recreate following indexes with all partitioning keys.\nList of partitioned tables and indexes with the specified problem:\n")
	for _, result := range results {
		detail := fmt.Sprintf("Partitioned table %q owns the index", result.TableName)
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.IndexName, "index", result.SchemaName, detail)
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkIncompatibleRangePartitions(connection *dbconn.DBConn) (bool, error) {
	results := make([]incompatibleRangePartitionResult, 0)
	if err := connection.Select(&results, incompatibleRangePartitionQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("In version 7, range partitions don't support `START EXCLUSIVE` or `END INCLUSIVE` for columns with float, numeric, or text types.\nYou can recreate following tables without `START EXCLUSIVE` and `END INCLUSIVE`.\nList of partitioned tables with the specified problem:\n")
	for _, result := range results {
		detail := fmt.Sprintf("Parent table %q in schema %q uses key type %q", result.TableName, result.ParentSchema, result.TypeName)
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.PartitionName, "partition", result.PartitionSchema, detail)
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkStatementTriggers(connection *dbconn.DBConn) (bool, error) {
	results := make([]statementTriggerResult, 0)
	if err := connection.Select(&results, statementTriggerQuery); err != nil {
		return false, err
	}
	if len(results) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("In version 7, statements triggers are not supported.\nYou can use row triggers.\nList of triggers with the specified problem:\n")
	for _, result := range results {
		detail := fmt.Sprintf("Table %q owns the trigger", result.TableName)
		fmt.Fprintf(&output, "Database %q contains object %q of type %q in schema %q. The detail is %q.\n", connection.DBName, result.TriggerName, "trigger", result.SchemaName, detail)
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}

func checkRequiredLibraries(sourceConnection *dbconn.DBConn, targetConnection *dbconn.DBConn) (bool, error) {
	results := make([]requiredLibraryResult, 0)
	if err := sourceConnection.Select(&results, requiredLibraryQuery); err != nil {
		return false, err
	}

	missingLibraries := make([]string, 0)
	for _, result := range results {
		loadQuery := fmt.Sprintf("LOAD '%s'", utils.EscapeSingleQuotes(result.LibraryName))
		if _, err := targetConnection.Exec(loadQuery); err != nil {
			missingLibraries = append(missingLibraries, result.LibraryName)
		}
	}
	if len(missingLibraries) == 0 {
		return false, nil
	}

	var output strings.Builder
	output.WriteString("Your cluster references loadable libraries that are missing from the new cluster.\nYou can add these libraries to the new installation, or remove the functions using them from the old installation.\nA list of problems libraries are:\n")
	for _, libraryName := range missingLibraries {
		fmt.Fprintf(&output, "Database %q requires object %q of type %q. The detail is %q.\n", sourceConnection.DBName, libraryName, "library", "The target cluster could not load the library")
	}
	gplog.Custom(gplog.LOGERROR, gplog.LOGERROR, "%s", strings.TrimSpace(output.String()))

	return true, nil
}
