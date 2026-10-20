SELECT n.nspname::text AS schema_name, c.relname::text AS object_name, c.relkind::text AS relation_kind
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm')
  AND c.oid >= 16384
  AND n.nspname NOT LIKE 'pg_temp_%'
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_aoseg', 'pg_bitmapindex', 'pg_catalog')
  AND n.nspname::pg_catalog.text <> ALL($1::pg_catalog.text[])
  AND c.oid NOT IN (SELECT objid FROM pg_catalog.pg_depend WHERE deptype = 'e')
  AND pg_temp.view_has_removed_functions(c.oid)
ORDER BY n.nspname, c.relname;
