WITH partitions AS (
    SELECT DISTINCT n.nspname, c.relname, c.oid, p.paratts
    FROM pg_catalog.pg_partition p
    JOIN pg_catalog.pg_class c ON c.oid = p.parrelid
    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
)
SELECT DISTINCT p.nspname::text AS schema_name,
       p.relname::text AS table_name,
       index_relation.relname::text AS index_name
FROM pg_catalog.pg_index i
JOIN partitions p ON p.oid = i.indrelid
JOIN pg_catalog.pg_class index_relation ON index_relation.oid = i.indexrelid
WHERE (i.indisunique OR i.indisprimary)
  AND NOT (p.paratts <@ i.indkey)
  AND p.nspname !~ '^pg_temp_'
  AND p.nspname !~ '^pg_toast_temp_'
  AND p.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY schema_name, table_name, index_name;
