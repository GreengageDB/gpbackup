# gpbackup Debian Packaging

## Overview

`.deb` packages are built by the top-level `package.mk` (via `debuild`),
which hands off to `debian/rules` for the actual `dh` sequence. This is a
**single-package** source tree — one binary package plus the automatically
generated `-dbgsym` companion:

| Package | Contents |
| --- | --- |
| `gpbackup` | `gpbackup`, `gprestore`, `gpbackup_helper` binaries in `/opt/greengagedb/gpbackup/bin` |
| `gpbackup-dbgsym` | Debug symbols, produced by `dh_strip` (`.ddeb`) |

gpbackup does not link against Greengage, so unlike extensions such as
diskquota there is one package for both Greengage 6 and 7
(`Depends: greengage6 | greengage7`). It is built in the
Greengage developer image `ggdb6_<os>`, used only as a build environment
with the packaging toolchain; Go is installed by `ci/build_in_docker.sh`.

## Requirements / Environment Variables

| Variable | Used by | Required? | Default | Purpose |
| --- | --- | --- | --- | --- |
| `DEB_PACKAGES` | `package.mk` | No | `Package/<package>_<version>` | Overrides the final output directory (used by CI) |
| `GPBACKUP_VERSION` | `package.mk` | No | — | Explicit version override, takes priority over git and `.version` |
| `GO_VERSION` | `ci/build_in_docker.sh` | No | `1.26.4` | Go toolchain downloaded from go.dev into `/usr/local/go` |

`go` must be in `PATH` or in `/usr/local/go/bin` (`debuild` resets `PATH`,
so `debian/rules` prepends that directory).

## Build Flow

```text
make -f package.mk pkg
                    ↓
debian/changelog (single entry; package name/maintainer
                   read from debian/control)
                    ↓
debuild --preserve-env -us -uc -b
                    ↓
debian/rules: dh sequence
                    ↓
override_dh_auto_build
   (go build -tags <bin> -o debian/build/<bin> for gpbackup,
    gprestore and gpbackup_helper,
    version = DEB_VERSION from debian/changelog)
                    ↓
override_dh_auto_install
   (install into debian/tmp/opt/greengagedb/gpbackup/bin)
                    ↓
dh_install (filters debian/tmp through debian/install)
                    ↓
dh_strip → .ddeb, dh_installchangelogs, dh_lintian, dh_builddeb
                    ↓
rm -rf $(PACKAGE_DIR) && mkdir -p $(PACKAGE_DIR)
                    ↓
mv of exact filenames:
   <package>_<version>_*.<deb|ddeb>
   <source>_<version>_*.<build|buildinfo|changes>
   → $(PACKAGE_DIR)/  (default: ./Package/gpbackup_$(PACKAGE_VERSION))
```

The top-level `Makefile` is not used by the package build: it is meant
for development (`make build` installs into `$GOPATH/bin`), so
`debian/rules` overrides `dh_auto_clean`/`dh_auto_build`/`dh_auto_install`
and calls `go` directly.

## package.mk Targets

| Target | Description |
| --- | --- |
| `help` | Prints usage and the list of targets |
| `version-info` | Prints the resolved package version and build metadata |
| `changelog` / `debian/changelog` | Generates a single-entry changelog using the resolved package version |
| `pkg` | Alias for `pkg-deb` — the entry point used by `ci/build_in_docker.sh` |
| `pkg-deb` | Runs `debuild --preserve-env -us -uc -b` (binary-only, unsigned), then moves the results into `$(PACKAGE_DIR)` |

A `.debuilder.lock` directory (created with `mkdir`, atomic) guards
against concurrent `pkg-deb` runs in the same working tree.

### Version

The package version is resolved from `GPBACKUP_VERSION`, then
`git describe --tags`, then `.version`. If none of them yields a value,
`0.0.0+unknown` is used (with a warning).

Git development versions in the form `<version>-<commits>-<hash>` are
converted to `<version>+dev.<commits>.<hash>` for Debian packaging.
The same version is embedded into all three binaries (`gpbackup --version`,
`gprestore --version`, `gpbackup_helper --version`); gpbackup requires
`gpbackup_helper` on the segments to report exactly the same version.

## Key Files

| File | Purpose |
| --- | --- |
| `package.mk` | Version, changelog generation, packaging targets |
| `ci/build_in_docker.sh` | Installs Go and runs the build inside a Greengage developer image |
| `ci/build_in_docker_local.sh` | Wrapper for local development |
| `.version` | Fallback package version, populated at `git archive` time via `.gitattributes` |
| `debian/control` | Package metadata |
| `debian/rules` | Debhelper overrides |
| `debian/install` | Install manifest; the path is fixed and must match `GPBACKUP_HOME` in `debian/rules` and `debian/postinst` |
| `debian/postinst` | Symlinks the binaries into `bin/` of every installed `/opt/greengagedb/greengage*` (gpbackup runs `$GPHOME/bin/gpbackup_helper` on segments) |
| `debian/postrm` | Removes those symlinks |
| `debian/lintian-overrides` | Suppressed lintian warnings (`dir-or-file-in-opt`) |
| `debian/copyright` | Debian copyright file (Apache-2.0) |
| `debian/compat` | Debhelper compat level (13) |

Generated (do not commit, `.gitignore`d): `debian/changelog`,
`debian/build/`.

## Usage

### Local build in a container (recommended)

```bash
ci/build_in_docker_local.sh          # Ubuntu 22.04
ci/build_in_docker_local.sh 24.04
```

`ci/build_in_docker.sh` must run as root inside the container; it runs
the package build as the owner of the mounted source tree, avoiding
root-owned build artifacts. It is not intended for direct execution on
a host system.

### Local build on a host

```bash
make -f package.mk pkg
make -f package.mk version-info
```
