# Makefile for the ergo.services/actor repository.
# One repository, three independent modules: every target runs per module, because
# `go ./...` does not cross a module boundary.

GO ?= go
MODULES ?= health leader metrics
COVER ?= coverage.out

.PHONY: help all audit test test-race cover vet fmt fmt-fix build tidy clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

all: audit test ## Run the audit, then the full test suite

audit: vet fmt build ## Static checks only: vet + gofmt + build (no tests)

test: clean ## Run every test verbosely with a freshly cleared cache
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) test -v ./...) || exit 1; \
	done

test-race: clean ## Run every test under the race detector
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) test -race ./...) || exit 1; \
	done

cover: ## Report per-module statement coverage
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) test -coverprofile=$(COVER) ./... && $(GO) tool cover -func=$(COVER) | tail -1) || exit 1; \
	done

vet: ## Run go vet over every module
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) vet ./...) || exit 1; \
	done

fmt: ## Report gofmt drift (fails if any file needs formatting)
	@files=$$(gofmt -l $(MODULES)); \
	if [ -n "$$files" ]; then \
		echo "gofmt: the following files are not formatted:"; \
		echo "$$files"; \
		exit 1; \
	fi; \
	echo "gofmt: clean"

fmt-fix: ## Reformat every file in place with gofmt
	gofmt -w $(MODULES)

build: ## Compile all packages (non-test code) in every module
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) build ./...) || exit 1; \
	done

tidy: ## Verify go.mod/go.sum are tidy in every module
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && $(GO) mod tidy -diff) || exit 1; \
	done

clean: ## Clear the test cache and drop coverage profiles
	@$(GO) clean -testcache
	@rm -f $(addsuffix /$(COVER),$(MODULES))
