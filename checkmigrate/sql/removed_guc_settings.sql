SELECT pg_catalog.coalesce(database_catalog.datname, '<none>')::text AS database_name,
       pg_catalog.coalesce(role_catalog.rolname, '<none>')::text AS role_name,
       pg_catalog.lower(pg_catalog.split_part(setting_value.setting, '=', 1))::text AS guc_name,
       setting_value.setting::text
FROM pg_catalog.pg_db_role_setting setting_catalog
LEFT JOIN pg_catalog.pg_database database_catalog ON database_catalog.oid = setting_catalog.setdatabase
LEFT JOIN pg_catalog.pg_roles role_catalog ON role_catalog.oid = setting_catalog.setrole
CROSS JOIN pg_catalog.unnest(setting_catalog.setconfig) AS setting_value(setting)
WHERE pg_catalog.lower(pg_catalog.split_part(setting_value.setting, '=', 1)) IN (
    'gp_autostats_on_change_ratio_threshold',
    'gp_enable_exchange_default_partition',
    'gp_enable_groupext_distinct_gather',
    'gp_enable_groupext_distinct_pruning',
    'gp_enable_sort_distinct',
    'gp_gpperfmon_send_interval',
    'gp_keep_partition_children_locks',
    'gp_log_resqueue_priority_sleep_time',
    'gp_resource_group_enable_recalculate_query_mem',
    'gp_use_synchronize_seqscans_catalog_vacuum_full',
    'gpperfmon_log_alert_level',
    'memory_spill_ratio',
    'password_hash_algorithm',
    'debug_assertions'
)
ORDER BY database_name, role_name, guc_name;
