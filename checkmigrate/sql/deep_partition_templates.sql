SELECT schemaname::text AS schema_name,
       tablename::text AS object_name
FROM pg_catalog.pg_partition_templates
WHERE partitionlevel > 1
GROUP BY schemaname, tablename
ORDER BY schemaname, tablename;
