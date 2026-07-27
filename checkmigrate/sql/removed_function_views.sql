SELECT n.nspname::text AS schema_name, c.relname::text AS object_name, c.relkind::text AS relation_kind
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm')
  AND c.oid >= 16384
  AND __ggcheckmigrate_tmp.view_has_removed_functions(c.oid)
ORDER BY n.nspname, c.relname;
