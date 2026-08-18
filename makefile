# --- Default target ---
.DEFAULT_GOAL := help

.PHONY: help lint check-lint-version fmt vet test test-integration test-all benchmark tidy

help: ## Show this help
	@echo ""
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-25s\033[0m %s\n", $$1, $$2}'

## 🧹 Linting & Formatting
lint: check-lint-version ## Run golangci-lint, including integration-tagged files
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
