# CI Workflows

## build_and_unit_test.yml

Builds the binaries and runs the unit tests, then runs the regression
tests against Greengage 6 and 7 in the developer image.

## Build and package DEB ([build_and_package.yml](build_and_package.yml))

Builds `gpbackup` and packages it as a `.deb`/`.ddeb`.

### What it does

1. **Build in Docker** — runs [ci/build_in_docker.sh](../../ci/build_in_docker.sh)
   inside the Greengage developer image
   (`ghcr.io/greengagedb/greengage/ggdb6_<os>`), which already provides the
   Debian packaging toolchain. gpbackup does not depend on the Greengage
   version, so the ggdb6 image is used for all OS versions. The script
   installs Go, then packages gpbackup with
   `make -f package.mk pkg` (see [package.mk](../../package.mk)).
   The output directory is set via `DEB_PACKAGES`, which is also the
   artifact name — no post-build rename step.
2. **Upload artifacts** — uploads the contents of the `DEB_PACKAGES`
   directory as a GitHub Actions artifact.
3. **Test install** — installs the package into a clean `<os>:<version>`
   image with the Greengage repository enabled via the shared
   [`tests/install/deb`](https://github.com/greengagedb/greengage-ci) action
   and runs `--version` for gpbackup, gprestore and gpbackup_helper.

### Artifacts

Name                                                 | Contents
---------------------------------------------------- | --------
`deb-packages-gpbackup-ubuntu<version>`    | `.deb`, `.ddeb`, `.build`, `.buildinfo`, `.changes`

### Triggers

Event          | Branches / refs
-------------- | ---------------
`push`         | `master`, tags
`pull_request` | all branches

## release.yml

Uploads the `.deb`/`.ddeb` packages built by `build_and_package.yml` to a
GitHub Release.

### What it does

1. Waits for the `Build and package DEB` workflow run for the
   released tag's commit to complete successfully
2. Downloads its `deb-packages-gpbackup-ubuntu<version>` artifact (falling
   back to the Actions cache if the artifact has expired)
3. Renames each package to include the OS revision suffix and uploads it to
   the release

Uses `greengagedb/greengage-ci/.github/actions/upload-pkgs-to-release`.

### Triggers

Event      | Types
---------- | --------
`release`  | `released`
