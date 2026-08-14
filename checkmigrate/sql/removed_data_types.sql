SELECT nspname::text AS schema_name, relname::text AS object_name, attname::text AS column_name
FROM pg_temp.data_type_checks(
    ARRAY[
        'pg_catalog.abstime'::regtype,
        'pg_catalog.unknown'::regtype,
        'pg_catalog.reltime'::regtype,
        'pg_catalog.tinterval'::regtype
    ]
)
ORDER BY nspname, relname, attname;
