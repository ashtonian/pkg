# Published modules. Each has its own go.mod, tags and releases.
CORE_MODULES := gowalker jsonvalue
CONFIG_MODULES := config config/kongprovider
HTTPX_MODULES := httpx httpx/codec/msgpack httpx/codec/proto httpx/compress/brotli
PARSE_MODULES := parse
TAGGED_MODULES := $(HTTPX_MODULES) $(PARSE_MODULES)
MODULES := $(CORE_MODULES) $(CONFIG_MODULES) $(TAGGED_MODULES)
# Programs that use the published modules; tested, never published.
COOKBOOK := config/examples/cookbook
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2

.PHONY: test test-jsonvalue jsonvalue-release-check test-gowalker gowalker-release-check test-config test-httpx-unit test-httpx-integration test-parse-unit test-parse-integration test-parse-sdk parse-sdk-release-check parse-release-check lint release-check httpx-release-check cookbook-check tool-check ci

## test: every published module from the working tree (go.work), race detector on
test: test-jsonvalue test-gowalker test-config test-httpx-unit test-httpx-integration test-parse-unit test-parse-integration test-parse-sdk

test-jsonvalue:
	go test -race -count=1 ./jsonvalue/...

jsonvalue-release-check:
	bash tools/check-jsonvalue-release.sh

test-gowalker:
	go test -race -count=1 ./gowalker/...

test-config:
	go test -race -count=1 $(foreach m,$(CONFIG_MODULES),./$(m)/...)

# HTTPx uses build tags: an untagged go test would skip all its tests.
test-httpx-unit:
	go test -tags=unit -race -count=1 $(foreach m,$(HTTPX_MODULES),./$(m)/...)

test-httpx-integration:
	go test -tags=integration -race -count=1 $(foreach m,$(HTTPX_MODULES),./$(m)/...)

test-parse-unit:
	go test -tags=unit -race -count=1 ./parse/...

# SQLite always runs. Supply PARSE_POSTGRES_DSN and PARSE_POSTGIS_DSN for SQL parity.
test-parse-integration:
	go test -tags=integration -race -count=1 -timeout=15m ./parse/...

test-parse-sdk:
	cd parse/sdk/ts && npm ci && npm run check && npm run check:compat && npm run lint && npm test && npm run build

parse-sdk-release-check:
	bash tools/check-parse-sdk-release.sh

## lint: go vet and staticcheck over every module and the cookbook
lint:
	@set -e; for m in $(CORE_MODULES) $(CONFIG_MODULES); do \
		echo "lint $$m"; (cd $$m && go vet ./... && go run $(STATICCHECK) ./...); \
	done
	@set -e; for m in $(TAGGED_MODULES); do \
		for tag in unit integration; do \
			echo "lint $$m ($$tag)"; \
			(cd $$m && go vet -tags=$$tag ./... && go run $(STATICCHECK) -tags=$$tag ./...); \
		done; \
	done
	@echo "lint $(COOKBOOK)"; cd $(COOKBOOK) && GOWORK=off go vet ./... && GOWORK=off go run $(STATICCHECK) ./...

## release-check: each published module as a consumer gets it — outside the
## workspace, its dependencies at the versions its go.mod requires
release-check:
	@set -e; for m in $(CORE_MODULES) $(CONFIG_MODULES); do \
		echo "release-check $$m"; \
		(cd $$m && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test -race -count=1 ./...); \
	done
	$(MAKE) jsonvalue-release-check gowalker-release-check httpx-release-check parse-release-check

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
	cd $(COOKBOOK) && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test -race -count=1 ./...

## tool-check: release tooling is a development module, never published
tool-check:
	cd tools/modarchive && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test -race -count=1 ./... && GOWORK=off go run $(STATICCHECK) ./...

## ci: everything CI runs
ci: lint test release-check parse-sdk-release-check cookbook-check tool-check
