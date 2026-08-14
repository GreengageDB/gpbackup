WITH RECURSIVE system_objects AS (
    SELECT c.relname::text AS object_name,
           pg_catalog.format('%I.%I', n.nspname, c.relname)::text AS referenced_object
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname IN ('pg_catalog', 'gp_toolkit')
      AND c.relkind IN ('r', 'v', 'm')
), user_views AS (
    SELECT c.oid,
           n.nspname::text AS schema_name,
           c.relname::text AS object_name,
           c.relkind::text AS relation_kind,
           pg_catalog.pg_get_viewdef(c.oid, true)::text AS definition
    FROM pg_catalog.pg_class c
    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind IN ('v', 'm')
      AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'gp_toolkit')
      AND n.nspname !~ '^pg_temp_'
      AND n.nspname !~ '^pg_toast_temp_'
), relation_dependencies AS (
    SELECT v.oid,
           v.schema_name,
           v.object_name,
           v.relation_kind,
           s.referenced_object
    FROM user_views v
    JOIN system_objects s
      ON v.definition ~* (
          '(^|[^A-Za-z_0-9$])' ||
          pg_catalog.regexp_replace(s.object_name, '([\[\]().*+?^$|{}\\-])', '\\\1', 'g') ||
          '([^A-Za-z_0-9$]|$)'
      )
    UNION
    SELECT v.oid,
           v.schema_name,
           v.object_name,
           v.relation_kind,
           dependency.referenced_object
    FROM user_views v
    JOIN relation_dependencies dependency
      ON v.definition ~* (
          '(^|[^A-Za-z_0-9$])' ||
          pg_catalog.regexp_replace(dependency.object_name, '([\[\]().*+?^$|{}\\-])', '\\\1', 'g') ||
          '([^A-Za-z_0-9$]|$)'
      )
), function_dependencies AS (
    SELECT p.oid,
           n.nspname::text AS schema_name,
           p.proname::text AS object_name,
           'f'::text AS relation_kind,
           pg_catalog.format('%I.%I', referenced_namespace.nspname, referenced_relation.relname)::text AS referenced_object
    FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
    JOIN pg_catalog.pg_language l ON l.oid = p.prolang
    CROSS JOIN LATERAL pg_catalog.regexp_matches(
        p.prosrc,
        '(pg_catalog|gp_toolkit)\.([A-Za-z_][A-Za-z_0-9]*)',
        'gi'
    ) AS reference
    JOIN pg_catalog.pg_namespace referenced_namespace
      ON pg_catalog.lower(referenced_namespace.nspname) = pg_catalog.lower(reference[1])
    JOIN pg_catalog.pg_class referenced_relation
      ON referenced_relation.relnamespace = referenced_namespace.oid
     AND pg_catalog.lower(referenced_relation.relname) = pg_catalog.lower(reference[2])
    WHERE p.oid >= 16384
      AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'gp_toolkit')
      AND n.nspname !~ '^pg_temp_'
      AND n.nspname !~ '^pg_toast_temp_'
      AND l.lanname NOT IN ('c', 'internal')
)
SELECT DISTINCT schema_name, object_name, relation_kind, referenced_object
FROM relation_dependencies
UNION
SELECT DISTINCT schema_name, object_name, relation_kind, referenced_object
FROM function_dependencies
ORDER BY schema_name, object_name, referenced_object;
