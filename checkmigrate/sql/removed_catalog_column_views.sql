SELECT schema_name,
       object_name,
       relation_kind,
       removed_columns
FROM (
    SELECT namespace_catalog.nspname::text AS schema_name,
           relation_catalog.relname::text AS object_name,
           relation_catalog.relkind::text AS relation_kind,
           pg_temp.get_removed_columns(relation_catalog.oid)::text AS removed_columns
    FROM pg_catalog.pg_class relation_catalog
    JOIN pg_catalog.pg_namespace namespace_catalog ON namespace_catalog.oid = relation_catalog.relnamespace
    WHERE relation_catalog.oid >= 16384
      AND relation_catalog.relkind IN ('v', 'm')
      AND namespace_catalog.nspname !~ '^pg_temp_'
      AND namespace_catalog.nspname !~ '^pg_toast_temp_'
      AND namespace_catalog.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_catalog')
) views
WHERE removed_columns <> ''
ORDER BY schema_name, object_name;
