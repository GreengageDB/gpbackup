SELECT n.nspname::text AS schema_name,
       e.extname::text AS object_name
FROM pg_catalog.pg_extension e
JOIN pg_catalog.pg_namespace n ON n.oid = e.extnamespace
WHERE e.extname IN (
    'gp_parallel_retrieve_cursor',
    'gp_array_agg',
    'gp_percentile_agg'
)
ORDER BY n.nspname, e.extname;
