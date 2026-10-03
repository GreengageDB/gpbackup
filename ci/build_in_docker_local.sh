#!/bin/bash
# FILE:    ci/build_in_docker_local.sh
# CONTEXT: Build gpbackup and package it as a .deb in container
# PURPOSE: Convenience wrapper for local development. Runs
#          ci/build_in_docker.sh in a Greengage developer image for the
#          selected Ubuntu version. gpbackup does not depend on the
#          Greengage version, so the ggdb6 image is used as a build
#          environment for all Ubuntu versions.
#
# USAGE:   ci/build_in_docker_local.sh [UBUNTU_VERSION]
# EXAMPLE: ci/build_in_docker_local.sh 24.04

set -euo pipefail

target_os_version=${1:-22.04}

if [[ "$target_os_version" == "22.04" ]]; then
    os_image=ubuntu
else
    os_image="ubuntu${target_os_version}"
fi

image="ghcr.io/greengagedb/greengage/ggdb6_${os_image}:latest"
SRC=/gpbackup/src

echo "Building gpbackup on Ubuntu ${target_os_version}"

docker run --rm -it \
    -v "./:$SRC" -w "$SRC" \
    "$image" ci/build_in_docker.sh
