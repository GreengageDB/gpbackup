SELECT n.nspname::text AS schema_name,
       p.proname::text AS object_name,
       pg_catalog.pg_get_function_identity_arguments(p.oid)::text AS identity_arguments,
       p.probin::text AS library_name
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
JOIN pg_catalog.pg_language l ON l.oid = p.prolang
WHERE l.lanname = 'c'
  AND p.oid >= 16384
  AND p.probin IS NOT NULL
  AND p.probin NOT IN ('', '-', '$libdir/plpython2')
  AND n.nspname NOT LIKE 'pg_temp_%'
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_aoseg', 'pg_bitmapindex', 'pg_catalog')
ORDER BY p.probin, n.nspname, p.proname, p.oid;
