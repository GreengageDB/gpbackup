SELECT n.nspname::text AS schema_name, c.relname::text AS object_name, c.relkind::text AS relation_kind
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm')
  AND n.nspname !~ '^pg_temp_'
  AND n.nspname !~ '^pg_toast_temp_'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND pg_temp.view_has_removed_operators(c.oid)
ORDER BY n.nspname, c.relname;
