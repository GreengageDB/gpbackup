SELECT n.nspname::text AS schema_name,
       n.nspname::text AS object_name
FROM pg_catalog.pg_namespace n
WHERE n.nspname = 'arenadata_toolkit';
