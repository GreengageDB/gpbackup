/* end_to_end/resources/test_ext_local--1.0.sql */
\echo Use "CREATE EXTENSION test_ext_local" to load this file. \quit

-- No DISTRIBUTED BY: gg_local forces entry (coordinator-only) policy automatically.
CREATE TABLE test_local_cfg (id int, val text, note text, data bytea);
SELECT pg_catalog.pg_extension_config_dump('test_local_cfg', '');

-- Second config table, filtered via extcondition. Row 4 is seeded by the
-- script itself and excluded by the filter -- must not be duplicated.
CREATE TABLE test_local_cfg_filtered (id int, active bool);
SELECT pg_catalog.pg_extension_config_dump('test_local_cfg_filtered', 'WHERE active');
INSERT INTO test_local_cfg_filtered VALUES (4, false);

CREATE TABLE test_local_cfg_quoted_cols (
	id int,
	"comma,name" text,
	"paren(name)" text,
	"quote'name" text,
	"dot.and space" text,
	"unicode_名前" text
);
SELECT pg_catalog.pg_extension_config_dump('test_local_cfg_quoted_cols', '');
