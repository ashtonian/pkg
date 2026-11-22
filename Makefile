# Published modules. Each has its own go.mod, tags and releases.
CORE_MODULES := gowalker jsonvalue gitx
CONFIG_MODULES := config config/kongprovider
HTTPX_MODULES := httpx httpx/oauth2 httpx/codec/msgpack httpx/codec/proto httpx/compress/brotli
NATIVE_MODULES := process state onepassword
PARSE_MODULES := parse
TAGGED_MODULES := $(HTTPX_MODULES) $(PARSE_MODULES) $(NATIVE_MODULES)
MODULES := $(CORE_MODULES) $(CONFIG_MODULES) $(TAGGED_MODULES)
# Programs that use the published modules; tested, never published.
COOKBOOK := config/examples/cookbook
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
# modernc.org/sqlite is C translated to Go. Race and checkptr instrumentation of
# it made the SQLite-backed parse suites about 30 times slower while checking
# third-party code, so it builds uninstrumented; this repository's code keeps both.
RACE := -race -gcflags='modernc.org/...=-race=false -d=checkptr=0'
# Every published module's release check; CI passes only the changed ones.
RELEASE_CHECKS := gitx-release-check config-release-check kongprovider-release-check native-release-check jsonvalue-release-check gowalker-release-check httpx-release-check parse-release-check
# The packages the test and lint targets cover: "all", or CI's list of the
# packages a change reaches, each <module dir>:<package dir> (tools/ci_scope.py).
PACKAGES ?= all
# $(call packages,<module dirs>): those modules' covered packages, as go
# command arguments from the repository root.
packages = $(strip $(foreach m,$(1),$(if $(filter all,$(PACKAGES)),./$(m)/...,$(patsubst $(m):%,./%,$(filter $(m):%,$(PACKAGES))))))
# $(call scoped,<go subcommand and flags>,<module dirs>): run it over their
# covered packages; a change that reaches none of them runs nothing.
scoped = $(if $(call packages,$(2)),go $(1) $(call packages,$(2)),@echo "no covered packages in $(strip $(2))")
UNTAGGED_MODULES := $(CORE_MODULES) $(CONFIG_MODULES)
LINT_TAGS := -tags=unit,integration

.PHONY: test test-native-unit test-native-integration native-release-check test-gitx test-jsonvalue jsonvalue-release-check test-gowalker gowalker-release-check test-config test-httpx-unit test-httpx-integration test-parse-unit test-parse-integration test-parse-sdk parse-sdk-release-check parse-sdk-registry-check parse-release-check lint release-check gitx-release-check config-release-check kongprovider-release-check httpx-release-check cookbook-check tool-check release-please-check ci

## test: every published module from the working tree (go.work), race detector on.
## -count=1 only where a test depends on state the test cache cannot see:
## processes it starts, the installed git, real servers.
test: test-native-unit test-native-integration test-gitx test-jsonvalue test-gowalker test-config test-httpx-unit test-httpx-integration test-parse-race-all test-parse-integration test-parse-sdk

test-native-unit:
	$(call scoped,test -tags=unit $(RACE) -count=1,$(NATIVE_MODULES))

test-native-integration:
	$(call scoped,test -tags=integration $(RACE) -count=1 -timeout=2m,$(NATIVE_MODULES))

native-release-check:
	bash tools/check-native-release.sh

test-gitx:
	$(call scoped,test $(RACE) -count=1,gitx)

test-jsonvalue:
	$(call scoped,test $(RACE),jsonvalue)

jsonvalue-release-check:
	bash tools/check-jsonvalue-release.sh

test-gowalker:
	$(call scoped,test $(RACE),gowalker)

test-config:
	$(call scoped,test $(RACE),$(CONFIG_MODULES))

# HTTPx uses build tags: an untagged go test would skip all its tests.
test-httpx-unit:
	$(call scoped,test -tags=unit $(RACE),$(HTTPX_MODULES))

test-httpx-integration:
	$(call scoped,test -tags=integration $(RACE) -count=1,$(HTTPX_MODULES))

.PHONY: test-parse-race test-parse-race-all lint-cookbook

## test-parse-unit: parse unit tests and fuzz seeds, without the race detector.
test-parse-unit:
	$(call scoped,test -tags=unit,$(PARSE_MODULES))

## test-parse-race: under the race detector, the parse tests that run its code
## on several goroutines (files tagged concurrency). Parse starts no goroutines
## of its own, so only these can race; the rest would cost a race build of
## parse's large test package for no coverage.
test-parse-race:
	$(call scoped,test -tags=concurrency $(RACE),$(PARSE_MODULES))

## test-parse-race-all: every parse unit test and fuzz seed under the race detector.
test-parse-race-all:
	$(call scoped,test -tags=unit $(RACE),$(PARSE_MODULES))

# SQLite always runs. Supply PARSE_POSTGRES_DSN and PARSE_POSTGIS_DSN for SQL parity.
test-parse-integration:
	$(call scoped,test -tags=integration $(RACE) -count=1 -timeout=15m,$(PARSE_MODULES))

test-parse-sdk:
	node tools/check-parse-sdk-pnpm.mjs
	cd parse/sdk/ts && pnpm install --frozen-lockfile && pnpm run check && pnpm run check:compat && pnpm run lint && pnpm test && pnpm run build

parse-sdk-release-check:
	bash tools/check-parse-sdk-release.sh

## lint: go vet and staticcheck over every published module's covered
## packages. Tagged modules take both tags in one pass: no file requires either
## tag's absence.
lint:
	$(call scoped,vet,$(UNTAGGED_MODULES))
	$(call scoped,run $(STATICCHECK),$(UNTAGGED_MODULES))
	$(call scoped,vet $(LINT_TAGS),$(TAGGED_MODULES))
	$(call scoped,run $(STATICCHECK) $(LINT_TAGS),$(TAGGED_MODULES))

## lint-cookbook: go vet and staticcheck over the cookbook, outside the workspace.
lint-cookbook:
	cd $(COOKBOOK) && GOWORK=off go vet ./... && GOWORK=off go run $(STATICCHECK) ./...

## release-check: each published module as a consumer gets it — outside the
## workspace, its dependencies at the versions its go.mod requires. The test
## targets run these tests under the race detector; these run them once without
## it, against the archive and its required versions.
release-check: $(RELEASE_CHECKS)

# gitx and config publish no archive check, so their standalone build is it.
define standalone-check
cd $(1) && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...
endef

gitx-release-check:
	$(call standalone-check,gitx)

config-release-check:
	$(call standalone-check,config)

kongprovider-release-check:
	$(call standalone-check,config/kongprovider)

parse-release-check:
	bash tools/check-parse-release.sh

## gowalker-release-check: download, test and run the quick start from a module ZIP
gowalker-release-check:
	bash tools/check-gowalker-release.sh

## httpx-release-check: module ZIPs in fresh consumers, plus Gin and benchmarks
## This also works before the HTTPx modules' first public tags exist.
httpx-release-check:
	bash tools/check-httpx-release.sh

## cookbook-check: build and test the cookbook against the working tree
cookbook-check:
	cd $(COOKBOOK) && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test $(RACE) -count=1 ./...

## parse-sdk-registry-check: bounded registry availability and integrity checks
parse-sdk-registry-check:
	node --test tools/check-parse-sdk-registry.test.mjs

## tool-check: release tooling is a development module, never published
tool-check:
	cd tools/modarchive && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test $(RACE) -count=1 ./... && GOWORK=off go run $(STATICCHECK) ./...

## release-please-check: the release workflow's runner: first-parent notes and their release-please wiring
release-please-check:
	cd tools/release-please && pnpm install --frozen-lockfile && node --test

## ci: everything CI runs
ci: lint lint-cookbook test release-check parse-sdk-release-check parse-sdk-registry-check cookbook-check tool-check release-please-check
