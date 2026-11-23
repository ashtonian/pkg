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
PARSE_EXAMPLE := parse/examples/server
# Staticcheck runs on Go 1.27.4 or later whatever toolchain vets. Earlier 1.27
# releases write export data with a type's generic methods moved last, so its
# facts for every method declared after a generic one belong to another method
# (golang/go#82106), and SA4023 can panic on them.
STATICCHECK := GOTOOLCHAIN=go1.27.4 go run honnef.co/go/tools/cmd/staticcheck@v0.8.2
# golangci-lint runs the shared baseline in .golangci.yml on the same Go release.
# gocritic type-checks the baseline's ruleguard rules, tools/gorules/rules.go,
# through go/build, which finds their DSL import only in a workspace holding a
# module that requires it. go.work holds the published modules alone, so
# golangci-lint runs under LINT_WORK: go.work plus tools/gorules.
# go fix's fixers come with the toolchain, so the check pins the same release.
# Three fixers are off. embedlit flattens literals of embedded structs, which
# the tests of promoted fields spell out on purpose. omitzero deletes omitempty
# from struct-typed fields, where only encoding/json ignores it: config's JSON
# Schema and parse's patch decoding read it, and their tests pin that. stditerators
# turns Value.Field(i) walks into Value.Fields(), which also builds each field's
# StructField: about ten times the cost of the walk it replaces.
GOFIX := GOTOOLCHAIN=go1.27.4 go fix -embedlit=false -omitzero=false -stditerators=false
GOFMT = $(shell GOTOOLCHAIN=go1.27.4 go env GOROOT)/bin/gofmt
GOLANGCI_LINT_VERSION := v2.15.0
LINT_WORK := .lint.work
# Empty: golangci-lint reports only the issues on lines changed since the merge
# base with origin/main (.golangci.yml). 1: every issue.
LINT_ALL ?=
GOLANGCI_LINT := GOTOOLCHAIN=go1.27.4 GOWORK=$(CURDIR)/$(LINT_WORK) GO111MODULE=on go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run$(if $(LINT_ALL), --new-from-merge-base=)
# modernc.org/sqlite is C translated to Go. Race and checkptr instrumentation of
# it made the SQLite-backed parse suites about 30 times slower while checking
# third-party code, so it builds uninstrumented; this repository's code keeps both.
RACE := -race -gcflags='modernc.org/...=-race=false -d=checkptr=0'
# Every published module's release check; CI passes only the changed ones.
RELEASE_CHECKS := gitx-release-check config-release-check kongprovider-release-check native-release-check jsonvalue-release-check gowalker-release-check httpx-release-check parse-release-check
MODULE_PATH := github.com/ashtonian/pkg
# The packages the test and lint targets cover: "all", or the import paths CI's
# change scope reaches (go-ci-actions scope's go-args).
PACKAGES ?= all
# $(call module-packages,<module dir>): its covered packages: the import paths
# under it, less those of the workspace modules nested inside it.
module-packages = $(filter-out $(foreach n,$(filter $(1)/%,$(MODULES)),$(MODULE_PATH)/$(n) $(MODULE_PATH)/$(n)/%),$(filter $(MODULE_PATH)/$(1) $(MODULE_PATH)/$(1)/%,$(PACKAGES)))
# $(call packages,<module dirs>,<build tags>): those modules' covered packages
# under the tags, as go command arguments from the repository root. A ./...
# pattern skips a package whose files the tags all exclude, but go refuses one
# named explicitly, so an explicit list keeps only the packages with a file.
packages = $(strip $(if $(filter all,$(PACKAGES)),$(foreach m,$(1),./$(m)/...),$(call buildable,$(foreach m,$(1),$(call module-packages,$(m))),$(2))))
# $(call buildable,<packages>,<build tags>): the import paths among them with a
# Go file under the tags, and their path/... patterns as they are.
buildable = $(call buildable-paths,$(filter-out %/...,$(1)),$(2)) $(filter %/...,$(1))
buildable-paths = $(if $(strip $(1)),$(shell go list -e -tags='$(2)' -f '{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' $(1)))
# $(call scoped,<command and flags>,<module dirs>[,<build tags>]): run it with
# the tags over their covered packages; a change that reaches none of them runs
# nothing.
scoped = $(call scoped-over,$(1)$(if $(3), -tags=$(3)),$(2),$(call packages,$(2),$(3)))
scoped-over = $(if $(3),$(1) $(3),@echo "no covered packages in $(strip $(2))")
# $(call lint-dirs,<packages>): golangci-lint takes directories, not import
# paths: ./ patterns as they are, and each import path's directory.
lint-dirs = $(strip $(filter ./%,$(1)) $(if $(filter-out ./%,$(1)),$(shell go list -e -f '{{.Dir}}' $(filter-out ./%,$(1)))))
UNTAGGED_MODULES := $(CORE_MODULES) $(CONFIG_MODULES)
LINT_TAGS := unit,integration

.PHONY: test test-native-unit test-native-integration native-release-check test-gitx test-jsonvalue jsonvalue-release-check test-gowalker gowalker-release-check test-config test-httpx-unit test-httpx-integration test-parse-unit test-parse-integration parse-postgres-up parse-postgres-down test-parse-sdk parse-sdk-release-check parse-sdk-registry-check parse-release-check lint release-check gitx-release-check config-release-check kongprovider-release-check httpx-release-check cookbook-check parse-example-check cookbook-integration cookbook-postgres-up cookbook-postgres-down tool-check release-please-check ci

## test: every published module from the working tree (go.work), race detector on.
## -count=1 only where a test depends on state the test cache cannot see:
## processes it starts, the installed git, real servers.
test: test-native-unit test-native-integration test-gitx test-jsonvalue test-gowalker test-config test-httpx-unit test-httpx-integration test-parse-race-all test-parse-integration test-parse-sdk

test-native-unit:
	$(call scoped,go test $(RACE) -count=1,$(NATIVE_MODULES),unit)

test-native-integration:
	$(call scoped,go test $(RACE) -count=1 -timeout=2m,$(NATIVE_MODULES),integration)

native-release-check:
	bash tools/check-native-release.sh

test-gitx:
	$(call scoped,go test $(RACE) -count=1,gitx)

test-jsonvalue:
	$(call scoped,go test $(RACE),jsonvalue)

jsonvalue-release-check:
	bash tools/check-jsonvalue-release.sh

test-gowalker:
	$(call scoped,go test $(RACE),gowalker)

test-config:
	$(call scoped,go test $(RACE),$(CONFIG_MODULES))

# HTTPx uses build tags: an untagged go test would skip all its tests.
test-httpx-unit:
	$(call scoped,go test $(RACE),$(HTTPX_MODULES),unit)

test-httpx-integration:
	$(call scoped,go test $(RACE) -count=1,$(HTTPX_MODULES),integration)

.PHONY: test-parse-race test-parse-race-all lint-cookbook fix

## test-parse-unit: parse unit tests and fuzz seeds, without the race detector.
test-parse-unit:
	$(call scoped,go test,$(PARSE_MODULES),unit)

## test-parse-race: under the race detector, the parse tests that run its code
## on several goroutines (files tagged concurrency). Parse starts no goroutines
## of its own, so only these can race; the rest would cost a race build of
## parse's large test package for no coverage.
test-parse-race:
	$(call scoped,go test $(RACE),$(PARSE_MODULES),concurrency)

## test-parse-race-all: every parse unit test and fuzz seed under the race detector.
test-parse-race-all:
	$(call scoped,go test $(RACE),$(PARSE_MODULES),unit)

# SQLite always runs. Supply PARSE_POSTGRES_DSN and PARSE_POSTGIS_DSN for SQL parity.
test-parse-integration:
	$(call scoped,go test $(RACE) -count=1 -timeout=15m,$(PARSE_MODULES),integration)

# The PostgreSQL integration server: one PostGIS server serves both DSN
# variables, as in CI. postgis/postgis publishes amd64 images only, so arm64
# hosts run it emulated.
PARSE_PG_IMAGE ?= postgis/postgis:18-3.6
PARSE_PG_NAME ?= parse-postgres
PARSE_PG_PORT ?= 55432
PARSE_PG_READY_SECONDS ?= 180
PARSE_PG_DSN = postgres://parse:parse@127.0.0.1:$(PARSE_PG_PORT)/parse?sslmode=disable

## parse-postgres-up: start the PostgreSQL integration server and print the
## exports that point the parse tests at it: eval "$(make -s parse-postgres-up)".
## A server that stops while starting, or is not ready in time, is removed
## after its log is printed (tools/postgres-up.sh).
parse-postgres-up:
	@bash tools/postgres-up.sh --name '$(PARSE_PG_NAME)' --image '$(PARSE_PG_IMAGE)' --platform linux/amd64 \
		--port '$(PARSE_PG_PORT)' --user parse --ready-seconds '$(PARSE_PG_READY_SECONDS)'
	@echo "export PARSE_POSTGRES_DSN='$(PARSE_PG_DSN)'"
	@echo "export PARSE_POSTGIS_DSN='$(PARSE_PG_DSN)'"

## parse-postgres-down: stop and remove the PostgreSQL integration server.
parse-postgres-down:
	docker rm -f $(PARSE_PG_NAME)

test-parse-sdk:
	node tools/check-pnpm.mjs
	cd parse/sdk/ts && pnpm install --frozen-lockfile && pnpm run check && pnpm run check:compat && pnpm run lint && pnpm test && pnpm run build

parse-sdk-release-check:
	bash tools/check-parse-sdk-release.sh

## lint: gofmt over every tracked Go file, then go vet, go fix -diff, staticcheck
## and golangci-lint over every published module's covered packages. Tagged
## modules take both tags in one pass: no file requires either tag's absence.
## golangci-lint reports only new issues unless LINT_ALL=1; gofmt and go fix
## report every one. make fix applies gofmt's and go fix's.
lint: $(LINT_WORK)
	@unformatted="$$(git ls-files '*.go' | xargs $(GOFMT) -l)" || exit 1; \
	if [ -n "$$unformatted" ]; then printf 'gofmt: run make fix\n%s\n' "$$unformatted"; exit 1; fi
	$(call scoped,go vet,$(UNTAGGED_MODULES))
	$(call scoped,$(GOFIX) -diff,$(UNTAGGED_MODULES))
	$(call scoped,$(STATICCHECK),$(UNTAGGED_MODULES))
	$(call scoped-over,$(GOLANGCI_LINT),$(UNTAGGED_MODULES),$(call lint-dirs,$(call packages,$(UNTAGGED_MODULES))))
	$(call scoped,go vet,$(TAGGED_MODULES),$(LINT_TAGS))
	$(call scoped,$(GOFIX) -diff,$(TAGGED_MODULES),$(LINT_TAGS))
	$(call scoped,$(STATICCHECK),$(TAGGED_MODULES),$(LINT_TAGS))
	$(call scoped-over,$(GOLANGCI_LINT) --build-tags=$(LINT_TAGS),$(TAGGED_MODULES),$(call lint-dirs,$(call packages,$(TAGGED_MODULES),$(LINT_TAGS))))

# The lint workspace: go.work and its sums, plus the ruleguard rules' module.
$(LINT_WORK): go.work go.work.sum tools/gorules/go.mod
	cp go.work $@ && cp go.work.sum $@.sum && go work edit -use=./tools/gorules $@

## fix: apply gofmt to every tracked Go file and go fix's fixers, as lint checks
## them, to every published module's covered packages.
fix:
	git ls-files '*.go' | xargs $(GOFMT) -w
	$(call scoped,$(GOFIX),$(UNTAGGED_MODULES))
	$(call scoped,$(GOFIX),$(TAGGED_MODULES),$(LINT_TAGS))

## lint-cookbook: go vet, go fix -diff and staticcheck over the cookbook, outside the workspace.
lint-cookbook:
	cd $(COOKBOOK) && GOWORK=off go vet ./... && GOWORK=off $(GOFIX) -diff ./... && GOWORK=off $(STATICCHECK) ./...

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

## cookbook-integration: the cookbook's database tests, the settings-store
## recipe's on SQLite and on the PostgreSQL server COOKBOOK_POSTGRES_DSN names.
## Its PostgreSQL legs skip without the variable, unless COOKBOOK_REQUIRE_PG=1.
cookbook-integration:
	cd $(COOKBOOK) && GOWORK=off go test $(RACE) -count=1 ./internal/pgtest/... ./settingsstore/...

# The cookbook's PostgreSQL server, as CI's cookbook-postgres job provisions it.
COOKBOOK_PG_IMAGE ?= postgres:19
COOKBOOK_PG_NAME ?= cookbook-postgres
COOKBOOK_PG_PORT ?= 55433
COOKBOOK_PG_READY_SECONDS ?= 60
COOKBOOK_PG_DSN = postgres://cookbook:cookbook@127.0.0.1:$(COOKBOOK_PG_PORT)/cookbook?sslmode=disable

## cookbook-postgres-up: start the cookbook's PostgreSQL server and print the
## export that points its tests at it: eval "$(make -s cookbook-postgres-up)".
cookbook-postgres-up:
	@bash tools/postgres-up.sh --name '$(COOKBOOK_PG_NAME)' --image '$(COOKBOOK_PG_IMAGE)' \
		--port '$(COOKBOOK_PG_PORT)' --user cookbook --ready-seconds '$(COOKBOOK_PG_READY_SECONDS)'
	@echo "export COOKBOOK_POSTGRES_DSN='$(COOKBOOK_PG_DSN)'"

## cookbook-postgres-down: stop and remove the cookbook's PostgreSQL server.
cookbook-postgres-down:
	docker rm -f $(COOKBOOK_PG_NAME)

## parse-example-check: vet, staticcheck and test the Parse server example, a
## module outside the workspace, against the Parse working tree
parse-example-check:
	cd $(PARSE_EXAMPLE) && GOWORK=off go mod tidy -diff && GOWORK=off go vet -tags=unit,integration ./... && GOWORK=off $(GOFIX) -diff -tags=unit,integration ./... && GOWORK=off $(STATICCHECK) -tags=unit,integration ./... && GOWORK=off go test $(RACE) -tags=unit,integration -count=1 ./...

## parse-sdk-registry-check: bounded registry availability and integrity checks
parse-sdk-registry-check:
	node --test tools/check-parse-sdk-registry.test.mjs

## tool-check: release tooling is a development module, never published
tool-check:
	cd tools/modarchive && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off $(GOFIX) -diff ./... && GOWORK=off go test $(RACE) -count=1 ./... && GOWORK=off $(STATICCHECK) ./...

## docs-check: every module's Markdown, in the workspace or not, against its code: Go identifiers,
## relative links and anchors, code maps (tools/doccheck)
docs-check:
	cd tools/doccheck && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off $(GOFIX) -diff ./... && GOWORK=off go test $(RACE) -count=1 ./... && GOWORK=off $(STATICCHECK) ./... && GOWORK=off go run . -root ../..

## release-please-check: the release workflow's runner (first-parent notes and their release-please wiring),
## the release-notes check of a pull request's commits, and the repository's pnpm contract (tools/check-pnpm.mjs)
release-please-check:
	node tools/check-pnpm.mjs
	node --test tools/check-pnpm.test.mjs
	cd tools/release-please && pnpm install --frozen-lockfile && node --test

## ci: everything CI runs
ci: lint lint-cookbook test release-check parse-sdk-release-check parse-sdk-registry-check cookbook-check parse-example-check tool-check release-please-check docs-check
