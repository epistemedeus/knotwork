# Knotwork developer tooling. Everything runs through mise; `make help` lists targets.
-include .env
export

MISE ?= mise
GO ?= $(MISE) exec -- go
GOLANGCI ?= $(MISE) exec -- golangci-lint
PNPM ?= $(MISE) exec -- pnpm
GIT ?= git

FRONTEND := frontend

.DEFAULT_GOAL := help

.PHONY: help setup hooks build web web-install web-build web-check web-fix run \
	test test-race cover vet lint lint-fix fmt fmt-check tidy tidy-check vuln \
	check ci clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "\nUsage: make <target>\n\n"} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

setup: ## Install tools/deps and enable the committed git hooks
	$(MISE) install
	cd $(FRONTEND) && $(PNPM) install
	$(MAKE) hooks

hooks: ## Point git at .githooks (pre-commit + pre-push)
	$(GIT) config core.hooksPath .githooks
	@echo "git hooks enabled: .githooks"

build: web-build ## Build the embedded web bundle and bin/knotwork
	$(GO) build -o bin/knotwork ./cmd/knotwork

web: web-build ## Alias for web-build

web-install: ## Install frontend dependencies
	cd $(FRONTEND) && $(PNPM) install

web-build: ## Type-check, lint, and bundle the frontend into internal/web/static
	cd $(FRONTEND) && $(PNPM) run build

web-check: ## Type-check and lint the frontend without writing files
	cd $(FRONTEND) && $(PNPM) run check

web-fix: ## Apply Biome fixes and formatting
	cd $(FRONTEND) && $(PNPM) run lint:fix

run: ## Run the daemon with `go run`
	$(GO) run ./cmd/knotwork serve

test: ## Run the Go test suite
	$(GO) test ./...

test-race: ## Run tests with the race detector, shuffled order, no cache
	$(GO) test -race -shuffle=on -count=1 ./...

cover: ## Write coverage.out and print total coverage
	$(GO) test -covermode=atomic -coverprofile=coverage.out ./...
	@$(GO) tool cover -func=coverage.out | tail -n 1

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run golangci-lint
	$(GOLANGCI) run ./...

lint-fix: ## Run golangci-lint and apply safe fixes
	$(GOLANGCI) run --fix ./...

fmt: ## Format Go code (gofumpt + goimports)
	$(GOLANGCI) fmt

fmt-check: ## Fail if Go code is not formatted
	@diff=$$($(GOLANGCI) fmt --diff); \
	if [ -n "$$diff" ]; then \
		echo "$$diff"; \
		echo "Go files need formatting: run 'make fmt'"; \
		exit 1; \
	fi

tidy: ## Tidy go.mod/go.sum
	$(GO) mod tidy

tidy-check: ## Fail if go.mod/go.sum are not tidy
	$(GO) mod tidy -diff

vuln: ## Scan Go dependencies for known vulnerabilities
	$(MISE) exec -- govulncheck ./...

check: fmt-check tidy-check vet lint test web-check ## Fast local gate (fmt, vet, lint, tests, frontend)

ci: check test-race ## Full gate used by GitHub Actions

clean: ## Remove build and coverage output
	rm -rf bin coverage.out
