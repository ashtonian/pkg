# Published modules. Each has its own go.mod, tags and releases.
MODULES := config config/kongprovider
# Programs that use the published modules; tested, never published.
COOKBOOK := config/examples/cookbook
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2

.PHONY: test lint release-check cookbook-check ci

## test: every published module from the working tree (go.work), race detector on
test:
	go test -race -count=1 $(foreach m,$(MODULES),./$(m)/...)

## lint: go vet and staticcheck over every module and the cookbook
lint:
	@set -e; for m in $(MODULES); do \
		echo "lint $$m"; (cd $$m && go vet ./... && go run $(STATICCHECK) ./...); \
	done
	@echo "lint $(COOKBOOK)"; cd $(COOKBOOK) && GOWORK=off go vet ./... && GOWORK=off go run $(STATICCHECK) ./...

## release-check: each published module as a consumer gets it — outside the
## workspace, its dependencies at the versions its go.mod requires
release-check:
	@set -e; for m in $(MODULES); do \
		echo "release-check $$m"; \
		(cd $$m && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test -race -count=1 ./...); \
	done

## cookbook-check: build and test the cookbook against the working tree
cookbook-check:
	cd $(COOKBOOK) && GOWORK=off go mod tidy -diff && GOWORK=off go vet ./... && GOWORK=off go test -race -count=1 ./...

## ci: everything CI runs
ci: lint test release-check cookbook-check
