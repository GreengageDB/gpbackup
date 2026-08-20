WITH RECURSIVE user_views AS (
    SELECT c.oid,
           n.nspname::text AS schema_name,
           c.relname::text AS object_name,
           c.relkind::text AS relation_kind
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
           dependency.refobjid AS referenced_oid,
           ARRAY[v.oid, dependency.refobjid] AS dependency_path
    FROM user_views v
    JOIN pg_catalog.pg_rewrite rewrite_rule
      ON rewrite_rule.ev_class = v.oid
     AND rewrite_rule.rulename = '_RETURN'
    JOIN pg_catalog.pg_depend dependency
      ON dependency.classid = 'pg_catalog.pg_rewrite'::pg_catalog.regclass
     AND dependency.objid = rewrite_rule.oid
     AND dependency.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
     AND dependency.deptype = 'n'
    WHERE dependency.refobjid <> v.oid
    UNION ALL
    SELECT dependency.oid,
           dependency.schema_name,
           dependency.object_name,
           dependency.relation_kind,
           referenced_dependency.refobjid AS referenced_oid,
           dependency.dependency_path || ARRAY[referenced_dependency.refobjid]
    FROM relation_dependencies dependency
    JOIN pg_catalog.pg_class referenced_relation
      ON referenced_relation.oid = dependency.referenced_oid
     AND referenced_relation.relkind IN ('v', 'm')
    JOIN pg_catalog.pg_rewrite rewrite_rule
      ON rewrite_rule.ev_class = referenced_relation.oid
     AND rewrite_rule.rulename = '_RETURN'
    JOIN pg_catalog.pg_depend referenced_dependency
      ON referenced_dependency.classid = 'pg_catalog.pg_rewrite'::pg_catalog.regclass
     AND referenced_dependency.objid = rewrite_rule.oid
     AND referenced_dependency.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
     AND referenced_dependency.deptype = 'n'
    WHERE NOT referenced_dependency.refobjid = ANY(dependency.dependency_path)
), relation_findings AS (
    SELECT dependency.schema_name,
           dependency.object_name,
           dependency.relation_kind,
           pg_catalog.format('%I.%I', referenced_namespace.nspname, referenced_relation.relname)::text AS referenced_object
    FROM relation_dependencies dependency
    JOIN pg_catalog.pg_class referenced_relation ON referenced_relation.oid = dependency.referenced_oid
    JOIN pg_catalog.pg_namespace referenced_namespace ON referenced_namespace.oid = referenced_relation.relnamespace
    WHERE referenced_namespace.nspname IN ('pg_catalog', 'gp_toolkit')
      AND referenced_relation.relkind IN ('r', 'v', 'm')
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
