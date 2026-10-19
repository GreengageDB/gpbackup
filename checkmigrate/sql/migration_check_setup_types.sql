CREATE OR REPLACE FUNCTION pg_temp.data_type_checks(base_oids regtype[])
RETURNS TABLE (nspname name, relname name, attname name, root_nspname name)
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
    SELECT n.nspname, c.relname, a.attname, COALESCE(root_namespace.nspname, n.nspname)
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON c.relnamespace = n.oid
    JOIN pg_catalog.pg_attribute a ON c.oid = a.attrelid
    LEFT JOIN pg_catalog.pg_index i ON i.indexrelid = c.oid
    LEFT JOIN pg_catalog.pg_partition_rule child_rule
      ON child_rule.parchildrelid = COALESCE(i.indrelid, c.oid)
    LEFT JOIN pg_catalog.pg_partition root_partition
      ON root_partition.oid = child_rule.paroid
    LEFT JOIN pg_catalog.pg_class root_relation
      ON root_relation.oid = root_partition.parrelid
    LEFT JOIN pg_catalog.pg_namespace root_namespace
      ON root_namespace.oid = root_relation.relnamespace
    WHERE NOT a.attisdropped
      AND a.atttypid = ANY(result_oids)
      AND c.relkind IN ('r', 'm', 'i')
      AND n.nspname NOT LIKE 'pg_temp_%'
      AND n.nspname NOT LIKE 'pg_toast%'
      AND n.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_aoseg', 'pg_bitmapindex', 'pg_catalog')
      AND c.oid NOT IN (SELECT objid FROM pg_catalog.pg_depend WHERE deptype = 'e');
END;
$function$ LANGUAGE plpgsql;
