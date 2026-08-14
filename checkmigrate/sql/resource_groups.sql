SELECT rsgname::text AS object_name
FROM pg_catalog.pg_resgroup
WHERE oid >= 16384
ORDER BY rsgname;
