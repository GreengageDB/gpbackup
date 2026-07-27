SELECT n.nspname::text AS schema_name, p.proname::text AS object_name
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
JOIN pg_catalog.pg_language l ON l.oid = p.prolang
JOIN pg_catalog.pg_pltemplate t ON t.tmplname = l.lanname
WHERE t.tmpllibrary = '$libdir/plpython2'
ORDER BY n.nspname, p.proname, p.oid;
