#!/bin/bash -l
# FILE:    ci/build_in_docker.sh
# CONTEXT: Build gpbackup and package it as a .deb inside a
#          Greengage developer image.
# PURPOSE: Requires root privileges and the Greengage developer image
#          layout, which already provides the Debian packaging toolchain.
#          Installs Go and runs the package build as an unprivileged user.

set -eo pipefail

GO_VERSION=${GO_VERSION:-1.26.4}

function assert_root() {
    if [ "$(id -u)" -ne 0 ]; then
        echo "FATAL: build must run as root"
        exit 1
    fi
}

function assert_developer_image() {
    if [ ! -f /home/gpadmin/gpdb_src/VERSION ]; then
        echo "FATAL: not a Greengage developer image"
        exit 1
    fi
}

# The developer image has no Go toolchain
function install_go() {
    echo -n "Installing Go ${GO_VERSION}... "
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
        | tar -C /usr/local -xz
    echo "Done"
}

function prepare_build_user() {
    local uid user

    read -r uid < <(stat -c '%u' "$PWD")

    if [ "$uid" -eq 0 ]; then
        echo "FATAL: source directory is owned by root"
        exit 1
    fi

    user="$(getent passwd "$uid" | cut -d: -f1 || true)"

    if [ -z "$user" ]; then
        user="build-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
        useradd --uid "$uid" --create-home --user-group \
            --shell /bin/bash "$user"
    fi

    BUILD_USER="$user"
}

function run_build() {
    chown "$BUILD_USER" ..

    sudo --preserve-env=GPBACKUP_VERSION,DEB_PACKAGES,GOPROXY \
        --user "$BUILD_USER" -- \
        make -f package.mk
}

function _main() {
    assert_root
    assert_developer_image
    install_go
    prepare_build_user

    time run_build
}

_main "$@"
