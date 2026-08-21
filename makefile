# --- Default target ---
.DEFAULT_GOAL := help

.PHONY: help hooks lint check-lint-version check-fmt check-header fmt vet test test-integration test-all benchmark tidy

help: ## Show this help
	@echo ""
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-25s\033[0m %s\n", $$1, $$2}'

## 🧹 Linting & Formatting
lint: check-lint-version check-fmt check-header ## Run golangci-lint, including integration-tagged files
	@golangci-lint run ./...
	@golangci-lint run --build-tags=integration ./...

check-lint-version: ## Check the local golangci-lint matches the pinned version
	@want=$$(cat .golangci-version); \
	got=v$$(golangci-lint version --short 2>/dev/null || golangci-lint --version | sed -n 's/.*version \([0-9.]*\).*/\1/p'); \
	if [ "$$got" != "$$want" ]; then \
		echo "golangci-lint is $$got, but CI enforces $$want (.golangci-version)."; \
		echo "A clean run here would not predict CI. Install the pinned version:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$$want"; \
		exit 1; \
	fi

# golangci-lint's formatters block is applied by `golangci-lint fmt`, not
# reported by `golangci-lint run` — so without this a misformatted file passes
# every local check and fails CI's very first step.
check-fmt: ## Fail if any Go file is not gofmt-formatted
	@bad=$$(gofmt -l .); \
	if [ -n "$$bad" ]; then \
		echo "Not gofmt-formatted — run 'make fmt':"; \
		echo "$$bad"; \
		exit 1; \
	fi

# MPL-2.0 is file-level copyleft: the Exhibit A header is what puts a file
# inside the licence at all. .claude/hooks/post-edit.sh prepends it, but only
# when Claude is the editor — a hand-written file is covered by nothing else.
# Tracked files only, so an untracked scratch file does not block a push.
check-header: ## Fail if any Go file is missing the MPL-2.0 Exhibit A header
	@bad=$$(for f in $$(git ls-files '*.go'); do \
		head -1 "$$f" | grep -q "Mozilla Public" || echo "$$f"; \
	done); \
	if [ -n "$$bad" ]; then \
		echo "Missing the MPL-2.0 Exhibit A header (see .claude/hooks/post-edit.sh for the text):"; \
		echo "$$bad"; \
		exit 1; \
	fi

fmt: ## Format all Go files
	@go fmt ./...
	@gofmt -s -w .

vet: ## Run go vet
	@go vet ./...
	@go vet -tags=integration ./...

## 🧪 Testing
test: ## Run unit tests (no Docker required)
	@go test -race -v ./...

test-integration: ## Run integration tests against Tile38 in Docker
	@go test -tags=integration -timeout=15m -v ./...

test-all: test test-integration ## Run unit and integration tests

benchmark: ## Run benchmarks
	@go test -bench=. -benchmem ./...

## 🔨 Development
tidy: ## Download dependencies
	@go mod tidy

hooks: ## Point git at .githooks (pre-push runs make lint + test)
	@git config core.hooksPath .githooks
	@echo "core.hooksPath = .githooks — pre-push runs 'make lint test'; bypass with 'git push --no-verify'."
