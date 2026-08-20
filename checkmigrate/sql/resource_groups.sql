SELECT rsgname::text AS object_name
FROM pg_catalog.pg_resgroup
WHERE rsgname NOT IN ('admin_group', 'default_group')
ORDER BY rsgname;
