SET LOCAL track_counts TO off;
CREATE OR REPLACE FUNCTION pg_temp.view_has_removed_operators(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.view_has_removed_functions(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.view_has_removed_types(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.view_has_changed_function_signatures(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.get_removed_columns(oid)
RETURNS text
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.get_removed_tables(oid)
RETURNS text
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.data_type_checks(base_oids regtype[])
RETURNS TABLE (nspname name, relname name, attname name)
AS $function$
DECLARE
    result_oids regtype[];
    dependent_oids regtype[];
BEGIN
    dependent_oids := base_oids;
    result_oids := base_oids;

    WHILE array_length(dependent_oids, 1) IS NOT NULL LOOP
        dependent_oids := ARRAY(
            SELECT dependent_type.oid
            FROM (
                SELECT t.oid
                FROM pg_catalog.pg_type t, unnest(dependent_oids) AS x(oid)
                WHERE t.typbasetype = x.oid AND t.typtype = 'd'
                  AND NOT (t.oid = ANY(result_oids))
                UNION
                SELECT t.oid
                FROM pg_catalog.pg_type t, unnest(dependent_oids) AS x(oid)
                WHERE t.typelem = x.oid AND t.typtype = 'b'
                  AND NOT (t.oid = ANY(result_oids))
                UNION
                SELECT t.oid
                FROM pg_catalog.pg_type t
                JOIN pg_catalog.pg_class c ON t.oid = c.reltype
                JOIN pg_catalog.pg_attribute a ON c.oid = a.attrelid
                WHERE t.typtype = 'c'
                  AND NOT a.attisdropped
                  AND a.atttypid = ANY(dependent_oids)
                  AND NOT (t.oid = ANY(result_oids))
                UNION
                SELECT t.oid
                FROM pg_catalog.pg_type t, pg_catalog.pg_range r, unnest(dependent_oids) AS x(oid)
                WHERE t.typtype = 'r'
                  AND r.rngtypid = t.oid
                  AND r.rngsubtype = x.oid
                  AND NOT (t.oid = ANY(result_oids))
            ) AS dependent_type
        );
        result_oids := result_oids || dependent_oids;
    END LOOP;

    RETURN QUERY
    SELECT n.nspname, c.relname, a.attname
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON c.relnamespace = n.oid
    JOIN pg_catalog.pg_attribute a ON c.oid = a.attrelid
    WHERE NOT a.attisdropped
      AND a.atttypid = ANY(result_oids)
      AND c.relkind IN ('r', 'm', 'i')
      AND n.nspname !~ '^pg_temp_'
      AND n.nspname !~ '^pg_toast_temp_'
      AND n.nspname NOT IN ('pg_catalog', 'information_schema');
END;
$function$ LANGUAGE plpgsql;
