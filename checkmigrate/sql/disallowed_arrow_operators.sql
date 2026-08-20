SELECT namespace_catalog.nspname::text AS schema_name,
       operator_catalog.oprname::text AS object_name
FROM pg_catalog.pg_operator operator_catalog
JOIN pg_catalog.pg_namespace namespace_catalog ON namespace_catalog.oid = operator_catalog.oprnamespace
WHERE operator_catalog.oprname = '=>'
  AND operator_catalog.oid >= 16384
  AND namespace_catalog.nspname !~ '^pg_temp_'
  AND namespace_catalog.nspname !~ '^pg_toast_temp_'
  AND namespace_catalog.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_catalog')
ORDER BY schema_name, object_name;
