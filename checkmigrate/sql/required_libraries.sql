SELECT DISTINCT probin::text AS library_name
FROM pg_catalog.pg_proc
WHERE prolang = 13
  AND probin IS NOT NULL
  AND probin <> '$libdir/plpython2'
  AND oid >= 16384
ORDER BY probin;
