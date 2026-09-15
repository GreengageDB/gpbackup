SELECT DISTINCT n.nspname::text AS schema_name, c.relname::text AS object_name
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_partition p ON p.parrelid = c.oid
WHERE p.parkind = 'l'
  AND p.parnatts > 1
  AND n.nspname NOT LIKE 'pg_temp_%'
  AND n.nspname NOT LIKE 'pg_toast%'
  AND n.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_aoseg', 'pg_bitmapindex', 'pg_catalog')
ORDER BY schema_name, object_name;
