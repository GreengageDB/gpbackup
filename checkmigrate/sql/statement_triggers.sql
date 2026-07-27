SELECT n.nspname::text AS schema_name,
       c.relname::text AS table_name,
       t.tgname::text AS trigger_name
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE (t.tgtype & 1) = 0
  AND NOT t.tgisinternal
ORDER BY n.nspname, c.relname, t.tgname;
