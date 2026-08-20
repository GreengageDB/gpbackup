WITH settings AS (
    SELECT database_catalog.datname,
           role_catalog.rolname,
           setting_value.setting,
           pg_catalog.lower(pg_catalog.substr(setting_value.setting, 1, pg_catalog.strpos(setting_value.setting, '=') - 1)) AS guc_name,
           pg_catalog.substr(setting_value.setting, pg_catalog.strpos(setting_value.setting, '=') + 1) AS guc_value
    FROM pg_catalog.pg_db_role_setting setting_catalog
    LEFT JOIN pg_catalog.pg_database database_catalog ON database_catalog.oid = setting_catalog.setdatabase
    LEFT JOIN pg_catalog.pg_roles role_catalog ON role_catalog.oid = setting_catalog.setrole
    CROSS JOIN pg_catalog.unnest(setting_catalog.setconfig) AS setting_value(setting)
    WHERE pg_catalog.strpos(setting_value.setting, '=') > 0
), options AS (
    SELECT datname,
           rolname,
           setting,
           pg_catalog.lower(pg_catalog.btrim(pg_catalog.split_part(option_value, '=', 1))) AS option_name
    FROM settings
    CROSS JOIN pg_catalog.regexp_split_to_table(guc_value, ',') AS option(option_value)
    WHERE guc_name = 'gp_default_storage_options'
      AND pg_catalog.btrim(option_value) <> ''
)
SELECT pg_catalog.coalesce(datname, '<none>')::text AS database_name,
       pg_catalog.coalesce(rolname, '<none>')::text AS role_name,
       setting::text,
       option_name::text
FROM options
WHERE option_name NOT IN ('blocksize', 'compresstype', 'compresslevel', 'checksum')
ORDER BY database_name, role_name, option_name;
