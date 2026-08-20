CREATE OR REPLACE FUNCTION pg_temp.get_removed_columns(oid)
RETURNS text
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.get_removed_tables(oid)
RETURNS text
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
