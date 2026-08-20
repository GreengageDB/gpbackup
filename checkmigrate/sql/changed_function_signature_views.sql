SELECT namespace_catalog.nspname::text AS schema_name,
       relation_catalog.relname::text AS object_name,
       relation_catalog.relkind::text AS relation_kind
FROM pg_catalog.pg_class relation_catalog
JOIN pg_catalog.pg_namespace namespace_catalog ON namespace_catalog.oid = relation_catalog.relnamespace
WHERE relation_catalog.relkind IN ('v', 'm')
  AND relation_catalog.oid >= 16384
  AND pg_temp.view_has_changed_function_signatures(relation_catalog.oid)
  AND namespace_catalog.nspname !~ '^pg_temp_'
  AND namespace_catalog.nspname !~ '^pg_toast_temp_'
  AND namespace_catalog.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_catalog')
ORDER BY schema_name, object_name;
