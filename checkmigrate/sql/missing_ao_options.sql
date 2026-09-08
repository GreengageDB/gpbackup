SELECT parent_namespace.nspname::text AS parent_schema,
       parent_relation.relname::text AS parent_name,
       child_namespace.nspname::text AS child_schema,
       child_relation.relname::text AS child_name,
       po::text AS parent_option
FROM pg_catalog.pg_partition_rule child_rule
JOIN pg_catalog.pg_partition root_partition
  ON root_partition.oid = child_rule.paroid
LEFT JOIN pg_catalog.pg_partition_rule parent_rule
  ON parent_rule.oid = child_rule.parparentrule
JOIN pg_catalog.pg_class parent_relation
  ON parent_relation.oid = COALESCE(parent_rule.parchildrelid, root_partition.parrelid)
JOIN pg_catalog.pg_namespace parent_namespace
  ON parent_namespace.oid = parent_relation.relnamespace
JOIN pg_catalog.pg_class child_relation
  ON child_relation.oid = child_rule.parchildrelid
JOIN pg_catalog.pg_namespace child_namespace
  ON child_namespace.oid = child_relation.relnamespace
JOIN unnest(parent_relation.reloptions) po ON true
LEFT JOIN unnest(child_relation.reloptions) co
  ON split_part(po, '=', 1) = split_part(co, '=', 1)
WHERE co IS NULL
  AND parent_relation.relstorage IN ('a', 'c')
  AND child_relation.relstorage IN ('a', 'c')
  AND parent_namespace.nspname !~ '^pg_temp_'
  AND parent_namespace.nspname !~ '^pg_toast_temp_'
  AND parent_namespace.nspname NOT IN ('pg_catalog', 'information_schema')
  AND split_part(po, '=', 1) IN ('appendonly', 'appendoptimized', 'orientation',
                                 'compresstype', 'compresslevel', 'blocksize', 'checksum')
ORDER BY parent_schema, parent_name, child_schema, child_name, po;
