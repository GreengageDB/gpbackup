WITH RECURSIVE system_relations AS (
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
), direct_relation_findings AS (
    SELECT v.oid,
           v.schema_name,
           v.object_name,
           v.relation_kind,
           relation.referenced_object
    FROM user_views v
    JOIN system_relations relation
      ON v.definition ~* (
          '(^|[^A-Za-z_0-9$])' || relation.object_name || '([^A-Za-z_0-9$]|$)'
      )
), relation_findings AS (
    SELECT oid,
           schema_name,
           object_name,
           relation_kind,
           referenced_object
    FROM direct_relation_findings
    UNION
    SELECT dependent_view.oid,
           dependent_view.schema_name,
           dependent_view.object_name,
           dependent_view.relation_kind,
           dependency.referenced_object
    FROM relation_findings dependency
    JOIN pg_catalog.pg_depend catalog_dependency
      ON catalog_dependency.classid = 'pg_catalog.pg_rewrite'::pg_catalog.regclass
     AND catalog_dependency.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
     AND catalog_dependency.refobjid = dependency.oid
     AND catalog_dependency.deptype = 'n'
    JOIN pg_catalog.pg_rewrite rewrite_rule
      ON rewrite_rule.oid = catalog_dependency.objid
     AND rewrite_rule.rulename = '_RETURN'
    JOIN user_views dependent_view ON dependent_view.oid = rewrite_rule.ev_class
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
    WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'gp_toolkit')
      AND n.nspname !~ '^pg_temp_'
      AND n.nspname !~ '^pg_toast_temp_'
      AND l.lanname NOT IN ('c', 'internal')
)
SELECT DISTINCT schema_name, object_name, relation_kind, referenced_object
FROM relation_findings
UNION
SELECT DISTINCT schema_name, object_name, relation_kind, referenced_object
FROM function_dependencies
ORDER BY schema_name, object_name, referenced_object;
