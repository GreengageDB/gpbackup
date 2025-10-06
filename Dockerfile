# Supported base images:
# ggdb6: ghcr.io/greengagedb/greengage/ggdb6_ubuntu:latest
# ggdb7: ghcr.io/greengagedb/greengage/ggdb7_ubuntu:latest
ARG GGDB_IMAGE=ghcr.io/greengagedb/greengage/ggdb6_ubuntu:latest
FROM $GGDB_IMAGE

COPY . /home/gpadmin/go/src/github.com/GreengageDB/gpbackup
