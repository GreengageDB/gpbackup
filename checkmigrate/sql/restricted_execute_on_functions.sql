SELECT n.nspname::text AS schema_name, p.proname::text AS object_name
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_language l ON l.oid = p.prolang
JOIN pg_catalog.pg_type t ON t.oid = p.prorettype
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE NOT p.proretset
  AND p.proexeclocation IN ('s', 'm', 'i')
  AND l.lanname <> 'internal'
  AND t.typname <> 'record'
ORDER BY n.nspname, p.proname, p.oid;
