SELECT p.schemaname::text AS parent_schema,
       p.tablename::text AS parent_name,
       p.partitionschemaname::text AS child_schema,
       p.partitiontablename::text AS child_name,
       po::text AS parent_option
FROM pg_catalog.pg_partitions p
JOIN pg_catalog.pg_namespace parent_namespace ON parent_namespace.nspname = p.schemaname
JOIN pg_catalog.pg_class parent_relation ON parent_relation.relnamespace = parent_namespace.oid AND parent_relation.relname = p.tablename
JOIN pg_catalog.pg_namespace child_namespace ON child_namespace.nspname = p.partitionschemaname
JOIN pg_catalog.pg_class child_relation ON child_relation.relnamespace = child_namespace.oid AND child_relation.relname = p.partitiontablename
JOIN unnest(parent_relation.reloptions) po ON true
LEFT JOIN unnest(child_relation.reloptions) co ON split_part(po, '=', 1) = split_part(co, '=', 1)
WHERE co IS NULL
ORDER BY p.schemaname, p.tablename, p.partitionschemaname, p.partitiontablename, po;
