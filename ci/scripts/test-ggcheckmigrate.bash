#!/bin/bash

set -euo pipefail

required_variables=(
  GGCHECKMIGRATE_SOURCE_HOST
  GGCHECKMIGRATE_SOURCE_PORT
  GGCHECKMIGRATE_SOURCE_USER
)

for variable_name in "${required_variables[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    echo "${variable_name} must be set" >&2
    exit 1
  fi
done

source_host=${GGCHECKMIGRATE_SOURCE_HOST}
source_port=${GGCHECKMIGRATE_SOURCE_PORT}
source_user=${GGCHECKMIGRATE_SOURCE_USER}

target_host=${GGCHECKMIGRATE_TARGET_HOST:-}
target_port=${GGCHECKMIGRATE_TARGET_PORT:-}
target_user=${GGCHECKMIGRATE_TARGET_USER:-${source_user}}
if [[ -n ${target_host} && -z ${target_port} ]] || [[ -z ${target_host} && -n ${target_port} ]]; then
  echo "GGCHECKMIGRATE_TARGET_HOST and GGCHECKMIGRATE_TARGET_PORT must be set together" >&2
  exit 1
fi

binary_path=${GGCHECKMIGRATE_BINARY:-ggcheckmigrate}
database_name=${GGCHECKMIGRATE_DATABASE:-ggcheckmigrate_test}
enumeration_database_name=${database_name}_enumeration
if [[ ! ${database_name} =~ ^[a-zA-Z_][a-zA-Z_0-9]*$ ]]; then
  echo "GGCHECKMIGRATE_DATABASE must be an unquoted SQL identifier" >&2
  exit 1
fi
output_path=$(mktemp)
cleanup_output() {
  rm -f "${output_path}" "${output_path}.first" "${output_path}.second"
}
trap cleanup_output EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

source_psql=(
  psql
  -X
  -v ON_ERROR_STOP=1
  -v database_name="${database_name}"
  -h "${source_host}"
  -p "${source_port}"
  -U "${source_user}"
)

check_command=(
  "${binary_path}"
  --source-host "${source_host}"
  --source-port "${source_port}"
  --source-user "${source_user}"
  --source-database "${database_name}"
  --debug
)
expected_database_check_count=20
if [[ -n ${target_host} ]]; then
  check_command+=(
    --target-host "${target_host}"
    --target-port "${target_port}"
    --target-user "${target_user}"
  )
  expected_database_check_count=21
fi

fail_with_output() {
  local description=$1
  local expected_code=$2
  local actual_code=$3
  echo "${description} expected exit code ${expected_code} and got ${actual_code}" >&2
  cat "${output_path}" >&2
  exit 1
}

"${source_psql[@]}" postgres -c "DROP DATABASE IF EXISTS ${database_name}"
"${source_psql[@]}" postgres -c "DROP DATABASE IF EXISTS ${enumeration_database_name}"
if [[ $("${source_psql[@]}" postgres -Atc "SELECT count(*) FROM pg_catalog.pg_resgroup WHERE rsgname = 'ggcheckmigrate_fixture_group'") -gt 0 ]]; then
  "${source_psql[@]}" postgres -c "DROP RESOURCE GROUP ggcheckmigrate_fixture_group"
fi
"${source_psql[@]}" postgres -c "CREATE DATABASE ${database_name}"
"${source_psql[@]}" postgres -c "CREATE DATABASE ${enumeration_database_name}"
cleanup_fixture() {
  "${source_psql[@]}" postgres -c "DROP DATABASE IF EXISTS ${database_name}"
  "${source_psql[@]}" postgres -c "DROP DATABASE IF EXISTS ${enumeration_database_name}"
  if [[ $("${source_psql[@]}" postgres -Atc "SELECT count(*) FROM pg_catalog.pg_resgroup WHERE rsgname = 'ggcheckmigrate_fixture_group'") -gt 0 ]]; then
    "${source_psql[@]}" postgres -c "DROP RESOURCE GROUP ggcheckmigrate_fixture_group"
  fi
  cleanup_output
}
trap cleanup_fixture EXIT

run_check() {
  "${check_command[@]}"
}

clean_exit_code=0
run_check >"${output_path}" 2>&1 || clean_exit_code=$?
if [[ ${clean_exit_code} -ne 0 ]]; then
  fail_with_output "The check of an empty database" 0 "${clean_exit_code}"
fi

all_database_exit_code=0
all_database_command=(
  "${binary_path}"
  --source-host "${source_host}"
  --source-port "${source_port}"
  --source-user "${source_user}"
)
"${all_database_command[@]}" >"${output_path}" 2>&1 || all_database_exit_code=$?
if [[ ${all_database_exit_code} -gt 1 ]]; then
  fail_with_output "The check of all connectable databases" "0 or 1" "${all_database_exit_code}"
fi
if ! grep -Eq 'Summary contains ([2-9]|[1-9][0-9]+) enumerated databases' "${output_path}"; then
  echo "The all-database summary does not include multiple databases" >&2
  cat "${output_path}" >&2
  exit 1
fi

parameter_exit_code=0
"${binary_path}" \
  --source-host "${source_host}" \
  --source-port "${source_port}" \
  --target-host target.example.com \
  >"${output_path}" 2>&1 || parameter_exit_code=$?
if [[ ${parameter_exit_code} -ne 4 ]]; then
  fail_with_output "The check with a target host and no target port" 4 "${parameter_exit_code}"
fi
if ! grep -Fq 'both -H and -P options must be provided to check the target cluster' "${output_path}"; then
  echo "The parameter failure does not explain the invalid target options" >&2
  cat "${output_path}" >&2
  exit 1
fi

execution_exit_code=0
"${binary_path}" \
  --source-host "${source_host}" \
  --source-port 1 \
  --source-user "${source_user}" \
  >"${output_path}" 2>&1 || execution_exit_code=$?
if [[ ${execution_exit_code} -ne 5 ]]; then
  fail_with_output "The check against an unreachable source port" 5 "${execution_exit_code}"
fi

"${source_psql[@]}" "${database_name}" <<'SQL'
CREATE SCHEMA ggcheckmigrate_fixture;
CREATE SCHEMA arenadata_toolkit;
CREATE EXTENSION plpython2u;
CREATE EXTENSION gp_array_agg;
CREATE VIEW ggcheckmigrate_fixture.catalog_view AS SELECT relname FROM pg_catalog.pg_class;
CREATE VIEW ggcheckmigrate_fixture.transitive_catalog_view AS SELECT * FROM ggcheckmigrate_fixture.catalog_view;
CREATE VIEW ggcheckmigrate_fixture.removed_operator_view AS
SELECT '1 2'::pg_catalog.int2vector = '1 2'::pg_catalog.int2vector AS matched;
CREATE VIEW ggcheckmigrate_fixture.removed_function_view AS
SELECT pg_catalog.int2vectoreq(
  '1 2'::pg_catalog.int2vector,
  '1 2'::pg_catalog.int2vector
) AS matched;
CREATE VIEW ggcheckmigrate_fixture.removed_type_view AS
SELECT '2000-01-01 00:00:00'::pg_catalog.abstime AS old_time;
CREATE VIEW ggcheckmigrate_fixture.changed_signature_view AS
SELECT pg_catalog.to_regclass('pg_class') AS relation_oid;
CREATE VIEW ggcheckmigrate_fixture.removed_column_view AS
SELECT relhasoids FROM pg_catalog.pg_class;
CREATE VIEW ggcheckmigrate_fixture.removed_relation_view AS
SELECT * FROM pg_catalog.pg_partition;
CREATE TABLE ggcheckmigrate_fixture.rule_table (id integer) DISTRIBUTED BY (id);
CREATE RULE unrelated_catalog_rule AS ON INSERT TO ggcheckmigrate_fixture.rule_table
DO ALSO SELECT count(*) FROM pg_catalog.pg_class;
CREATE VIEW ggcheckmigrate_fixture.rule_table_view AS SELECT * FROM ggcheckmigrate_fixture.rule_table;
CREATE FUNCTION ggcheckmigrate_fixture.catalog_function() RETURNS bigint
LANGUAGE SQL AS 'SELECT count(*) FROM pg_catalog.pg_class';
CREATE FUNCTION ggcheckmigrate_fixture.fixture_plpython2(value integer)
RETURNS integer
AS 'return args[0]'
LANGUAGE plpython2u;
CREATE FUNCTION ggcheckmigrate_fixture.restricted_execute(integer, integer)
RETURNS integer
AS 'SELECT $1 + $2'
LANGUAGE SQL WINDOW
EXECUTE ON ALL SEGMENTS;
CREATE FUNCTION ggcheckmigrate_fixture.missing_library()
RETURNS text
AS '$libdir/gp_check_functions', 'get_tablespace_version_directory_name'
LANGUAGE C;
CREATE FUNCTION ggcheckmigrate_fixture.arrow_operator(integer, integer) RETURNS boolean
LANGUAGE SQL IMMUTABLE AS 'SELECT $1 = $2';
CREATE OPERATOR ggcheckmigrate_fixture.=> (
  LEFTARG = integer,
  RIGHTARG = integer,
  PROCEDURE = ggcheckmigrate_fixture.arrow_operator
);
ALTER DATABASE :"database_name" SET gp_default_storage_options TO 'appendonly=true,compresstype=zlib';
ALTER DATABASE :"database_name" SET password_hash_algorithm TO 'md5';
CREATE RESOURCE GROUP ggcheckmigrate_fixture_group WITH (CPU_RATE_LIMIT=1, MEMORY_LIMIT=1, CONCURRENCY=1);
CREATE TABLE ggcheckmigrate_fixture.multi_list (id integer, key_a text, key_b integer)
DISTRIBUTED BY (id)
PARTITION BY LIST (key_a, key_b) (
  PARTITION p1 VALUES (('a', 1)),
  DEFAULT PARTITION other
);
CREATE TABLE ggcheckmigrate_fixture.incomplete_index (id integer NOT NULL, partition_key integer)
DISTRIBUTED BY (id)
PARTITION BY RANGE (partition_key) (START (1) END (3) EVERY (1));
CREATE UNIQUE INDEX incomplete_index_unique ON ggcheckmigrate_fixture.incomplete_index (id);
CREATE TABLE ggcheckmigrate_fixture.removed_data_types (
  abstime_column pg_catalog.abstime,
  reltime_column pg_catalog.reltime,
  tinterval_column pg_catalog.tinterval,
  unknown_column pg_catalog.unknown
)
DISTRIBUTED RANDOMLY;
CREATE TABLE ggcheckmigrate_fixture.ao_missing_options (id integer, partition_key integer)
WITH (appendonly=true, compresstype=zlib, compresslevel=5, blocksize=65536)
DISTRIBUTED BY (id)
PARTITION BY RANGE (partition_key) (
  PARTITION ao_child START (0) END (10) WITH (appendonly=true)
);
CREATE TABLE ggcheckmigrate_fixture.bad_range (id integer, partition_key numeric)
DISTRIBUTED BY (id)
PARTITION BY RANGE (partition_key) (
  PARTITION p1 START (0) EXCLUSIVE END (10)
);
CREATE TABLE ggcheckmigrate_fixture.deep_templates (
  id integer,
  partition_date date,
  region integer,
  department text,
  category text
)
WITH (appendonly=true, orientation=column)
DISTRIBUTED BY (id)
PARTITION BY RANGE (partition_date)
SUBPARTITION BY LIST (region)
SUBPARTITION TEMPLATE (
  SUBPARTITION l VALUES (1),
  SUBPARTITION r VALUES (2)
)
SUBPARTITION BY LIST (department)
SUBPARTITION TEMPLATE (
  SUBPARTITION e VALUES ('engineering'),
  SUBPARTITION q VALUES ('quality')
)
SUBPARTITION BY LIST (category)
SUBPARTITION TEMPLATE (
  SUBPARTITION p VALUES ('primary'),
  SUBPARTITION s VALUES ('secondary')
)
(
  START (DATE '2020-01-01') END (DATE '2022-01-01') EVERY (INTERVAL '1 year')
);
CREATE TABLE ggcheckmigrate_fixture.ao_with_heap_child (id integer, partition_key integer)
WITH (appendonly=true, compresstype=zlib)
DISTRIBUTED BY (id)
PARTITION BY RANGE (partition_key) (
  PARTITION heap_child START (1) END (2) WITH (appendonly=false)
);
CREATE TABLE ggcheckmigrate_fixture.statement_trigger_table (id integer) DISTRIBUTED BY (id);
CREATE FUNCTION ggcheckmigrate_fixture.statement_trigger_fn() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END; $$;
CREATE TRIGGER statement_trigger AFTER INSERT ON ggcheckmigrate_fixture.statement_trigger_table
FOR EACH STATEMENT EXECUTE PROCEDURE ggcheckmigrate_fixture.statement_trigger_fn();
CREATE TYPE ggcheckmigrate_fixture.partition_key_type AS (value integer);
CREATE FUNCTION ggcheckmigrate_fixture.partition_key_less_than(
  ggcheckmigrate_fixture.partition_key_type,
  ggcheckmigrate_fixture.partition_key_type
)
RETURNS boolean
AS 'SELECT $1.value < $2.value'
LANGUAGE SQL IMMUTABLE RETURNS NULL ON NULL INPUT;
CREATE FUNCTION ggcheckmigrate_fixture.partition_key_equal(
  ggcheckmigrate_fixture.partition_key_type,
  ggcheckmigrate_fixture.partition_key_type
)
RETURNS boolean
AS 'SELECT $1.value = $2.value'
LANGUAGE SQL IMMUTABLE RETURNS NULL ON NULL INPUT;
CREATE OPERATOR ggcheckmigrate_fixture.< (
  LEFTARG = ggcheckmigrate_fixture.partition_key_type,
  RIGHTARG = ggcheckmigrate_fixture.partition_key_type,
  PROCEDURE = ggcheckmigrate_fixture.partition_key_less_than
);
CREATE OPERATOR ggcheckmigrate_fixture.= (
  LEFTARG = ggcheckmigrate_fixture.partition_key_type,
  RIGHTARG = ggcheckmigrate_fixture.partition_key_type,
  PROCEDURE = ggcheckmigrate_fixture.partition_key_equal
);
CREATE OPERATOR CLASS ggcheckmigrate_fixture.partition_key_ops
DEFAULT FOR TYPE ggcheckmigrate_fixture.partition_key_type
USING btree AS
  OPERATOR 1 ggcheckmigrate_fixture.<,
  OPERATOR 3 ggcheckmigrate_fixture.=;
CREATE TABLE ggcheckmigrate_fixture.partition_opfamily_table (
  id integer,
  partition_key ggcheckmigrate_fixture.partition_key_type
)
DISTRIBUTED BY (id)
PARTITION BY LIST (partition_key) (
  PARTITION p1 VALUES ('(1)')
);
SQL

finding_exit_code=0
run_check >"${output_path}" 2>&1 || finding_exit_code=$?
if [[ ${finding_exit_code} -ne 1 ]]; then
  fail_with_output "The check of a database with known problems" 1 "${finding_exit_code}"
fi

for expected_text in \
  'multi_list' \
  'fixture_plpython2' \
  'removed_operator_view' \
  'removed_function_view' \
  'removed_type_view' \
  'changed_signature_view' \
  'removed_column_view' \
  'removed_relation_view' \
  'removed_data_types' \
  'ao_missing_options' \
  'restricted_execute' \
  'incomplete_index_unique' \
  'bad_range' \
  'arenadata_toolkit' \
  'gp_array_agg' \
  'catalog_view' \
  'transitive_catalog_view' \
  'catalog_function' \
  'statement_trigger' \
  'deep_templates' \
  'ggcheckmigrate_fixture_group' \
  'gp_default_storage_options' \
  'password_hash_algorithm' \
  'partition_opfamily_table' \
  '=>' ; do
  if ! grep -Fq "${expected_text}" "${output_path}"; then
    echo "The report does not name ${expected_text}" >&2
    cat "${output_path}" >&2
    exit 1
  fi
done

if [[ -n ${target_host} ]] && ! grep -Fq 'missing_library' "${output_path}"; then
  echo "The report does not name missing_library" >&2
  cat "${output_path}" >&2
  exit 1
fi

expected_checks=(
  "resource groups"
  "incompatible storage options"
  "removed GUC settings"
  "multi-column LIST partitions"
  "PL/Python 2 functions"
  "views with removed operators"
  "views with removed functions"
  "views with removed types"
  "views with changed function signatures"
  "views with removed catalog columns"
  "views with removed catalog relations"
  "removed data types"
  "missing AO options"
  "restricted EXECUTE ON functions"
  "incomplete partition indexes"
  "incompatible range partitions"
  "statement triggers"
  "removed extensions"
  "arenadata_toolkit schema"
  "system object dependencies"
  "deep partition templates"
  "disallowed arrow operators"
  "partition operator families"
)
if [[ -n ${target_host} ]]; then
  expected_checks+=("required libraries")
fi

for expected_check in "${expected_checks[@]}"; do
  if ! grep -Eq "completed check \"${expected_check}\" with [1-9][0-9]* findings" "${output_path}"; then
    echo "The finding run did not report a finding for ${expected_check}" >&2
    cat "${output_path}" >&2
    exit 1
  fi
done

if ! grep -Fq "3 completed cluster checks, 0 failed cluster checks, ${expected_database_check_count} completed database checks, 0 failed database checks, 0 unavailable database checks" "${output_path}"; then
  echo "The finding run did not complete every check" >&2
  cat "${output_path}" >&2
  exit 1
fi

for unexpected_text in \
  'rule_table_view' \
  'heap_child'; do
  if grep -Fq "${unexpected_text}" "${output_path}"; then
    echo "The report unexpectedly named ${unexpected_text}" >&2
    cat "${output_path}" >&2
    exit 1
  fi
done

if ! grep -Eq 'and [1-9][0-9]* findings' "${output_path}"; then
  echo "The summary does not report a positive finding count" >&2
  cat "${output_path}" >&2
  exit 1
fi

run_check >"${output_path}.first" 2>&1 &
first_pid=$!
run_check >"${output_path}.second" 2>&1 &
second_pid=$!
first_exit_code=0
wait "${first_pid}" || first_exit_code=$?
second_exit_code=0
wait "${second_pid}" || second_exit_code=$?
if [[ ${first_exit_code} -ne 1 || ${second_exit_code} -ne 1 ]]; then
  echo "The concurrent checks expected exit code 1 and got ${first_exit_code} and ${second_exit_code}" >&2
  cat "${output_path}.first" "${output_path}.second" >&2
  exit 1
fi

"${source_psql[@]}" "${database_name}" <<'SQL'
DROP SCHEMA ggcheckmigrate_fixture CASCADE;
DROP SCHEMA arenadata_toolkit CASCADE;
DROP EXTENSION gp_array_agg;
DROP EXTENSION plpython2u;
ALTER DATABASE :"database_name" RESET gp_default_storage_options;
ALTER DATABASE :"database_name" RESET password_hash_algorithm;
DROP RESOURCE GROUP ggcheckmigrate_fixture_group;
SQL

post_cleanup_exit_code=0
run_check >"${output_path}" 2>&1 || post_cleanup_exit_code=$?
if [[ ${post_cleanup_exit_code} -ne 0 ]]; then
  fail_with_output "The check after fixture cleanup" 0 "${post_cleanup_exit_code}"
fi

for expected_check in "${expected_checks[@]}"; do
  if ! grep -Fq "completed check \"${expected_check}\" with 0 findings" "${output_path}"; then
    echo "The clean run did not complete ${expected_check} with zero findings" >&2
    cat "${output_path}" >&2
    exit 1
  fi
done

if ! grep -Fq "3 completed cluster checks, 0 failed cluster checks, ${expected_database_check_count} completed database checks, 0 failed database checks, 0 unavailable database checks, and 0 findings" "${output_path}"; then
  echo "The clean run did not complete every check with zero findings" >&2
  cat "${output_path}" >&2
  exit 1
fi
