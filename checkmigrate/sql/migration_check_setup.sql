CREATE OR REPLACE FUNCTION pg_temp.view_has_removed_operators(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.view_has_removed_functions(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.view_has_removed_types(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
CREATE OR REPLACE FUNCTION pg_temp.view_has_changed_function_signatures(oid)
RETURNS boolean
AS '$libdir/pg_upgrade_support'
LANGUAGE C STRICT;
