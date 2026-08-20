SELECT schemaname::text AS schema_name,
       tablename::text AS object_name
FROM pg_catalog.pg_partition_templates
WHERE partitionlevel > 1
  AND schemaname !~ '^pg_temp_'
  AND schemaname !~ '^pg_toast_temp_'
  AND schemaname NOT IN ('pg_catalog', 'information_schema')
GROUP BY schemaname, tablename
ORDER BY schemaname, tablename;
