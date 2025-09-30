# Supported base images:
# centos: hub.adsw.io/library/gpdb6_regress:latest
# rocky: hub.adsw.io/library/gpdb7_regress:latest
# ubuntu: hub.adsw.io/library/gpdb6_u22:latest
# ubuntu: hub.adsw.io/library/gpdb7_u22:latest
# default image is centos
ARG GPDB_IMAGE=hub.adsw.io/library/gpdb6_regress:latest
FROM $GPDB_IMAGE

COPY . /home/gpadmin/go/src/github.com/greengage-db/gpbackup
