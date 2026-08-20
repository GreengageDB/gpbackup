#!/bin/bash

set -euo pipefail

source_image=${GGCHECKMIGRATE_SOURCE_IMAGE:-gpbackup:checkmigrate-source}
target_image=${GGCHECKMIGRATE_TARGET_IMAGE:-gpbackup:checkmigrate-target}
source_container=ggcheckmigrate-source
target_container=ggcheckmigrate-target
network_name=ggcheckmigrate

cleanup_containers() {
  docker rm -f "${source_container}" "${target_container}" >/dev/null 2>&1 || true
  docker network rm "${network_name}" >/dev/null 2>&1 || true
}
trap cleanup_containers EXIT

docker network create "${network_name}"
docker run --rm -d \
  --name "${source_container}" \
  --hostname "${source_container}" \
  --network "${network_name}" \
  --sysctl 'kernel.sem=500 1024000 200 4096' \
  "${source_image}" \
  sleep infinity
docker run --rm -d \
  --name "${target_container}" \
  --hostname "${target_container}" \
  --network "${network_name}" \
  --sysctl 'kernel.sem=500 1024000 200 4096' \
  "${target_image}" \
  sleep infinity

docker exec "${source_container}" bash -lc '
  . /etc/os-release
  export TEST_OS=${ID}
  source /home/gpadmin/gpdb_src/concourse/scripts/common.bash
  install_and_configure_gpdb
  /home/gpadmin/gpdb_src/concourse/scripts/setup_gpadmin_user.bash
  make_cluster
'
docker exec "${target_container}" bash -lc '
  . /etc/os-release
  export TEST_OS=${ID}
  source /home/gpadmin/gpdb_src/concourse/scripts/common.bash
  install_and_configure_gpdb
  /home/gpadmin/gpdb_src/concourse/scripts/setup_gpadmin_user.bash
  make_cluster
'

source_address=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${source_container}")
docker exec \
  --user gpadmin \
  --env GGCHECKMIGRATE_SOURCE_ADDRESS="${source_address}" \
  "${target_container}" \
  bash -lc '
    source /home/gpadmin/gpdb_src/gpAux/gpdemo/gpdemo-env.sh
    printf "host all all %s/32 trust\n" "${GGCHECKMIGRATE_SOURCE_ADDRESS}" >> "${COORDINATOR_DATA_DIRECTORY}/pg_hba.conf"
    gpstop -u
  '

docker exec "${source_container}" bash -lc '
  wget https://golang.org/dl/go1.20.5.linux-amd64.tar.gz -q -O - | tar -C /opt -xz
'
docker exec --user gpadmin "${source_container}" bash -lc '
  source /home/gpadmin/gpdb_src/gpAux/gpdemo/gpdemo-env.sh
  PATH=/opt/go/bin:${PATH} GOPATH=/home/gpadmin/go make depend build -C /home/gpadmin/go/src/github.com/GreengageDB/gpbackup
'

source_port=$(docker exec --user gpadmin "${source_container}" bash -lc '
  source /home/gpadmin/gpdb_src/gpAux/gpdemo/gpdemo-env.sh
  printf "%s" "${PGPORT}"
')
target_port=$(docker exec --user gpadmin "${target_container}" bash -lc '
  source /home/gpadmin/gpdb_src/gpAux/gpdemo/gpdemo-env.sh
  printf "%s" "${PGPORT}"
')

docker exec \
  --user gpadmin \
  --env GGCHECKMIGRATE_SOURCE_HOST=127.0.0.1 \
  --env GGCHECKMIGRATE_SOURCE_PORT="${source_port}" \
  --env GGCHECKMIGRATE_SOURCE_USER=gpadmin \
  --env GGCHECKMIGRATE_TARGET_HOST="${target_container}" \
  --env GGCHECKMIGRATE_TARGET_PORT="${target_port}" \
  --env GGCHECKMIGRATE_TARGET_USER=gpadmin \
  --env GGCHECKMIGRATE_BINARY=/home/gpadmin/go/bin/ggcheckmigrate \
  "${source_container}" \
  bash -lc '
    source /home/gpadmin/gpdb_src/gpAux/gpdemo/gpdemo-env.sh
    /home/gpadmin/go/src/github.com/GreengageDB/gpbackup/ci/scripts/test-ggcheckmigrate.bash
  '
