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
target_user=${GGCHECKMIGRATE_TARGET_USER:-}
if [[ -n ${target_host} && -z ${target_port} ]] || [[ -z ${target_host} && -n ${target_port} ]]; then
  echo "GGCHECKMIGRATE_TARGET_HOST and GGCHECKMIGRATE_TARGET_PORT must be set together" >&2
  exit 1
fi

binary_path=${GGCHECKMIGRATE_BINARY:-ggcheckmigrate}
database_name=${GGCHECKMIGRATE_DATABASE:-ggcheckmigrate_test}
if [[ ! ${database_name} =~ ^[a-zA-Z_][a-zA-Z_0-9]*$ ]]; then
  echo "GGCHECKMIGRATE_DATABASE must be an unquoted SQL identifier" >&2
  exit 1
fi
output_path=$(mktemp)
trap 'rm -f "${output_path}" "${output_path}.first" "${output_path}.second"' EXIT INT TERM

source_psql=(
  psql
  -X
  -v ON_ERROR_STOP=1
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
)
if [[ -n ${target_host} ]]; then
  check_command+=(--target-host "${target_host}" --target-port "${target_port}")
  if [[ -n ${target_user} ]]; then
    check_command+=(--target-user "${target_user}")
  fi
else
  echo "The target cluster variables are unset, so the required library check is skipped"
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
"${source_psql[@]}" postgres -c "CREATE DATABASE ${database_name}"
trap '"${source_psql[@]}" postgres -c "DROP DATABASE IF EXISTS ${database_name}"; rm -f "${output_path}" "${output_path}.first" "${output_path}.second"' EXIT INT TERM

run_check() {
  "${check_command[@]}"
}

clean_exit_code=0
run_check >"${output_path}" 2>&1 || clean_exit_code=$?
if [[ ${clean_exit_code} -ne 0 ]]; then
  fail_with_output "The check of an empty database" 0 "${clean_exit_code}"
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
CREATE VIEW ggcheckmigrate_fixture.catalog_view AS SELECT relname FROM pg_catalog.pg_class;
CREATE FUNCTION ggcheckmigrate_fixture.catalog_function() RETURNS bigint
LANGUAGE SQL AS 'SELECT count(*) FROM pg_catalog.pg_class';
CREATE TABLE ggcheckmigrate_fixture.multi_list (id integer, key_a text, key_b integer)
DISTRIBUTED BY (id)
PARTITION BY LIST (key_a, key_b) (
  PARTITION p1 VALUES (('a', 1)),
  DEFAULT PARTITION other
);
CREATE TABLE ggcheckmigrate_fixture.statement_trigger_table (id integer) DISTRIBUTED BY (id);
CREATE FUNCTION ggcheckmigrate_fixture.statement_trigger_fn() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END; $$;
CREATE TRIGGER statement_trigger AFTER INSERT ON ggcheckmigrate_fixture.statement_trigger_table
FOR EACH STATEMENT EXECUTE PROCEDURE ggcheckmigrate_fixture.statement_trigger_fn();
SQL

finding_exit_code=0
run_check >"${output_path}" 2>&1 || finding_exit_code=$?
if [[ ${finding_exit_code} -ne 1 ]]; then
  fail_with_output "The check of a database with known problems" 1 "${finding_exit_code}"
fi

for expected_text in \
  'multi_list' \
  'arenadata_toolkit' \
  'catalog_view' \
  'catalog_function' \
  'statement_trigger'; do
  if ! grep -Fq "${expected_text}" "${output_path}"; then
    echo "The report does not name ${expected_text}" >&2
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
