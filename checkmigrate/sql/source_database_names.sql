SELECT datname::text AS database_name
FROM pg_catalog.pg_database
WHERE datallowconn
ORDER BY datname;
