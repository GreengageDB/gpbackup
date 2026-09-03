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
expected_database_check_count=8
if [[ -n ${target_host} ]]; then
  check_command+=(
    --target-host "${target_host}"
    --target-port "${target_port}"
    --target-user "${target_user}"
  )
  expected_database_check_count=9
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

all_database_exit_code=0
all_database_command=(
  "${binary_path}"
  --source-host "${source_host}"
  --source-port "${source_port}"
  --source-user "${source_user}"
  --debug
)
"${all_database_command[@]}" >"${output_path}" 2>&1 || all_database_exit_code=$?
if [[ ${all_database_exit_code} -gt 1 ]]; then
  fail_with_output "The check of all connectable databases" "0 or 1" "${all_database_exit_code}"
fi
for expected_database in postgres template1; do
  if ! grep -Fq "Starting checks for database \"${expected_database}\"" "${output_path}"; then
    echo "The fresh cluster run did not check ${expected_database}" >&2
    cat "${output_path}" >&2
    exit 1
  fi
done

"${source_psql[@]}" postgres -c "CREATE DATABASE ${database_name}"
"${source_psql[@]}" postgres -c "CREATE DATABASE ${enumeration_database_name}"
cleanup_fixture() {
  "${source_psql[@]}" postgres -c "DROP DATABASE IF EXISTS ${database_name}"
  "${source_psql[@]}" postgres -c "DROP DATABASE IF EXISTS ${enumeration_database_name}"
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
if ! grep -Fq 'CheckMigrate completed successfully with exit code 0' "${output_path}"; then
  echo "The successful run did not log exit code 0" >&2
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
if ! grep -Fq 'CheckMigrate completed with exit code 4' "${output_path}"; then
  echo "The parameter failure did not log exit code 4" >&2
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
if ! grep -Fq '[ERROR]' "${output_path}"; then
  echo "The connection failure did not print an error" >&2
  cat "${output_path}" >&2
  exit 1
fi
if ! grep -Fq 'CheckMigrate completed with exit code 5' "${output_path}"; then
  echo "The connection failure did not log exit code 5" >&2
  cat "${output_path}" >&2
  exit 1
fi

"${source_psql[@]}" "${database_name}" <<'SQL'
CREATE SCHEMA ggcheckmigrate_fixture;
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
if ! grep -Fq 'CheckMigrate completed with exit code 1' "${output_path}"; then
  echo "The finding run did not log exit code 1" >&2
  cat "${output_path}" >&2
  exit 1
fi

for expected_text in \
  'removed_operator_view' \
  'removed_function_view' \
  'removed_type_view' \
  'changed_signature_view' \
  'removed_column_view' \
  'removed_relation_view' \
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
  "incompatible storage options"
  "removed GUC settings"
  "views with removed operators"
  "views with removed functions"
  "views with removed types"
  "views with changed function signatures"
  "views with removed catalog columns"
  "views with removed catalog relations"
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

if ! grep -Eq 'completed cluster checks:[[:space:]]+2$' "${output_path}" ||
  ! grep -Eq 'failed cluster checks:[[:space:]]+0$' "${output_path}" ||
  ! grep -Eq "completed database checks:[[:space:]]+${expected_database_check_count}$" "${output_path}" ||
  ! grep -Eq 'failed database checks:[[:space:]]+0$' "${output_path}" ||
  ! grep -Eq 'unavailable database checks:[[:space:]]+0$' "${output_path}"; then
  echo "The finding run did not complete every check" >&2
  cat "${output_path}" >&2
  exit 1
fi

if ! grep -Eq 'findings:[[:space:]]+[1-9][0-9]*$' "${output_path}"; then
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
ALTER DATABASE :"database_name" RESET gp_default_storage_options;
ALTER DATABASE :"database_name" RESET password_hash_algorithm;
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

if ! grep -Eq 'completed cluster checks:[[:space:]]+2$' "${output_path}" ||
  ! grep -Eq 'failed cluster checks:[[:space:]]+0$' "${output_path}" ||
  ! grep -Eq "completed database checks:[[:space:]]+${expected_database_check_count}$" "${output_path}" ||
  ! grep -Eq 'failed database checks:[[:space:]]+0$' "${output_path}" ||
  ! grep -Eq 'unavailable database checks:[[:space:]]+0$' "${output_path}" ||
  ! grep -Eq 'findings:[[:space:]]+0$' "${output_path}"; then
  echo "The clean run did not complete every check with zero findings" >&2
  cat "${output_path}" >&2
  exit 1
fi

echo "All ggcheckmigrate tests passed"
