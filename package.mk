#!/usr/bin/make -f
# package.mk
#---------------------------------------------------------------------
# Packaging targets with changelog generation
#---------------------------------------------------------------------

SHELL := /bin/bash

.DEFAULT_GOAL := pkg

#---------------------------------------------------------------------
# Metadata
#---------------------------------------------------------------------

DATE_RFC       := $(shell date -R)
DISTRO_CODENAME:= $(shell lsb_release -sc)

MAINTAINER     := $(shell grep '^Maintainer:' debian/control | sed 's/Maintainer: //')
PACKAGE_SOURCE := $(shell grep '^Source:' debian/control | awk '{print $$2}')
PACKAGE_DEBIAN := $(shell grep '^Package:' debian/control | head -1 | awk '{print $$2}')

# Resolve the package version from an explicit override, git, or .version.
RAW_VERSION := $(or $(GPBACKUP_VERSION),\
                     $(shell git describe --tags 2>/dev/null),\
                     $(shell grep -v '$$Format:' .version 2>/dev/null))

ifeq ($(strip $(RAW_VERSION)),)
$(warning No version resolved from GPBACKUP_VERSION, git, or .version; \
using 0.0.0+unknown - this package should not be released)
RAW_VERSION := 0.0.0+unknown
endif

# Convert git describe versions to valid Debian versions.
# Matches <version>-<commits>-<hash> and converts it to
# <version>+dev.<commits>.<hash>.
#
# Sanitization:
#   - removes a common leading "v"
#   - replaces unsupported characters with "."
#   - collapses repeated dots
#   - removes separators at the beginning and end
PACKAGE_VERSION := $(shell printf '%s' '$(RAW_VERSION)' | \
	perl -pe '\
		s/^v//; \
		s/^(.*)-([0-9]+)-(g[0-9a-f]+)$$/\1+dev.\2.\3/; \
		s/[^0-9A-Za-z.+~-]+/./g; \
		s/[.]{2,}/./g; \
		s/^[.+~-]+//; \
		s/[.+~-]+$$//; \
	')

IS_RELEASE  := $(if $(findstring +dev,$(PACKAGE_VERSION)),no,yes)
BUILD_TYPE  := $(if $(filter yes,$(IS_RELEASE)),Release build,Development build)

DEB_PREREQS := debian/changelog
DEBUILD_CMD := debuild --preserve-env -us -uc -b

PACKAGE_DIR := $(or $(strip $(DEB_PACKAGES)),Package/$(PACKAGE_DEBIAN)_$(PACKAGE_VERSION))

#---------------------------------------------------------------------
# Diagnostics
#---------------------------------------------------------------------

version-info:
	@echo "PACKAGE_VERSION: $(PACKAGE_VERSION)"
	@echo "PACKAGE_DEBIAN:  $(PACKAGE_DEBIAN)"
	@echo "DISTRO_CODENAME: $(DISTRO_CODENAME)"
	@echo "IS_RELEASE:      $(IS_RELEASE)"
	@echo "BUILD_TYPE:      $(BUILD_TYPE)"

#---------------------------------------------------------------------
# Changelog generation
#---------------------------------------------------------------------

changelog: debian/changelog
debian/changelog: debian/control
	@echo "$(PACKAGE_SOURCE) ($(PACKAGE_VERSION)) $(DISTRO_CODENAME); urgency=low" > $@
	@echo "" >> $@
	@echo "  * $(BUILD_TYPE)" >> $@
	@echo "" >> $@
	@echo " -- $(MAINTAINER)  $(DATE_RFC)" >> $@

#---------------------------------------------------------------------
# Packaging
#---------------------------------------------------------------------

pkg: pkg-deb

LOCK_DIR := .debuilder.lock

pkg-deb: $(DEB_PREREQS)
	@mkdir "$(LOCK_DIR)" 2>/dev/null || { \
		echo "ERROR: another Debian package build is already running"; \
		exit 1; \
	}; \
	set -e; \
	trap 'rmdir "$(LOCK_DIR)"' EXIT; \
	echo "Building $(PACKAGE_DEBIAN) $(PACKAGE_VERSION)"; \
	$(DEBUILD_CMD); \
	rm -rf $(PACKAGE_DIR); \
	mkdir -p $(PACKAGE_DIR); \
	mv ../$(PACKAGE_DEBIAN){,-dbgsym}_$(PACKAGE_VERSION)_*.*deb $(PACKAGE_DIR)/; \
	mv ../$(PACKAGE_SOURCE)_$(PACKAGE_VERSION)_*.{build,buildinfo,changes} $(PACKAGE_DIR)/

help:
	@echo "Usage:"
	@echo "  make -f package.mk [target]"
	@echo ""
	@echo "Targets:"
	@echo "  pkg            Build Debian package (default)"
	@echo "  pkg-deb        Build Debian package"
	@echo "  changelog      Generate debian/changelog"
	@echo "  version-info   Show package version and build metadata"
	@echo "  help           Show this help"

.PHONY: help pkg pkg-deb changelog debian/changelog version-info
