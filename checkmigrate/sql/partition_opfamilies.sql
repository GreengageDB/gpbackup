SELECT relation_namespace.nspname::text AS schema_name,
       relation_catalog.relname::text AS object_name,
       operator_class.opcname::text AS operator_class,
       operator_family.opfname::text AS operator_family
FROM pg_catalog.pg_partition partition_catalog
JOIN pg_catalog.pg_class relation_catalog ON relation_catalog.oid = partition_catalog.parrelid
JOIN pg_catalog.pg_namespace relation_namespace ON relation_namespace.oid = relation_catalog.relnamespace
JOIN pg_catalog.pg_opclass operator_class ON operator_class.oid = ANY(partition_catalog.parclass)
JOIN pg_catalog.pg_opfamily operator_family ON operator_family.oid = operator_class.opcfamily
WHERE NOT EXISTS (
    SELECT 1
    FROM pg_catalog.pg_amproc support_procedure
    WHERE support_procedure.amprocnum = 1
      AND support_procedure.amprocfamily = operator_family.oid
      AND support_procedure.amproclefttype = operator_class.opcintype
      AND support_procedure.amprocrighttype = operator_class.opcintype
)
  AND relation_namespace.nspname !~ '^pg_temp_'
  AND relation_namespace.nspname !~ '^pg_toast_temp_'
  AND relation_namespace.nspname NOT IN ('gp_toolkit', 'information_schema', 'pg_catalog')
ORDER BY schema_name, object_name, operator_class, operator_family;
