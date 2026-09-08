SELECT n.nspname::text AS schema_name,
       p.proname::text AS object_name,
       pg_catalog.pg_get_function_identity_arguments(p.oid)::text AS identity_arguments
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
JOIN pg_catalog.pg_language l ON l.oid = p.prolang
JOIN pg_catalog.pg_proc handler ON handler.oid = l.lanplcallfoid
WHERE handler.probin = '$libdir/plpython2'
  AND n.nspname !~ '^pg_temp_'
  AND n.nspname !~ '^pg_toast_temp_'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY n.nspname, p.proname, p.oid;
