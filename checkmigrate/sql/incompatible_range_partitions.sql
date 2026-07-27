SELECT n.nspname::text AS parent_schema,
       c.relname::text AS table_name,
       t.typname::text AS type_name,
       child_namespace.nspname::text AS partition_schema,
       child_relation.relname::text AS partition_name
FROM pg_catalog.pg_partition p
JOIN pg_catalog.pg_partition_rule partition_rule ON p.oid = partition_rule.paroid
JOIN pg_catalog.pg_class c ON c.oid = p.parrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(p.paratts)
JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
JOIN pg_catalog.pg_class child_relation ON child_relation.oid = partition_rule.parchildrelid
JOIN pg_catalog.pg_namespace child_namespace ON child_namespace.oid = child_relation.relnamespace
WHERE t.typname IN ('text', 'float8', 'float4', 'numeric')
  AND (NOT partition_rule.parrangestartincl OR partition_rule.parrangeendincl)
ORDER BY n.nspname, c.relname, t.typname, child_namespace.nspname, child_relation.relname;
