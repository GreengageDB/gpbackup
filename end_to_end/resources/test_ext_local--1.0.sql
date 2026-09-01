/* end_to_end/resources/test_ext_local--1.0.sql */
\echo Use "CREATE EXTENSION test_ext_local" to load this file. \quit

-- No DISTRIBUTED BY: gg_local forces entry (coordinator-only) policy automatically.
CREATE TABLE test_local_cfg (id int, val text, note text, data bytea);
SELECT pg_catalog.pg_extension_config_dump('test_local_cfg', '');

-- Second config table, registered with a non-empty extcondition filter --
-- only rows matching the WHERE clause should be backed up/restored.
CREATE TABLE test_local_cfg_filtered (id int, active bool);
SELECT pg_catalog.pg_extension_config_dump('test_local_cfg_filtered', 'WHERE active');
