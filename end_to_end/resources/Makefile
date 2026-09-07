MODULE = test_extensions

EXTENSION = test_ext1 test_ext2 test_ext3 test_ext4 test_ext5 test_ext6 test_ext_local
DATA = test_ext1--1.0.sql test_ext2--1.0.sql test_ext3--1.0.sql \
       test_ext4--1.0.sql test_ext5--1.0.sql test_ext6--1.0.sql \
       test_ext_local--1.0.sql

PG_CONFIG = pg_config
PGXS := $(shell $(PG_CONFIG) --pgxs)
include $(PGXS)
