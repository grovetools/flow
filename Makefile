# Makefile for flow

BINARY_NAME=flow
BIN_DIR=bin
VERSION_PKG=github.com/mattsolo1/grove-core/version

# --- Versioning ---
# For dev builds, we construct a version string from git info.
# For release builds, VERSION is passed in by the CI/CD pipeline (e.g., VERSION=v1.2.3)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD)
GIT_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
GIT_DIRTY  ?= $(shell test -n "`git status --porcelain`" && echo "-dirty")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# If VERSION is not set, default to a dev version string
VERSION ?= $(GIT_BRANCH)-$(GIT_COMMIT)$(GIT_DIRTY)

# Go LDFLAGS to inject version info at compile time
LDFLAGS = -ldflags="\
-X '$(VERSION_PKG).Version=$(VERSION)' \
-X '$(VERSION_PKG).Commit=$(GIT_COMMIT)' \
-X '$(VERSION_PKG).Branch=$(GIT_BRANCH)' \
-X '$(VERSION_PKG).BuildDate=$(BUILD_DATE)'"

.PHONY: all build test clean fmt vet lint run check generate-docs dev build-all schema help

all: build

schema:
	@echo "Generating JSON schema..."
	@go generate ./cmd

build: schema
	@mkdir -p $(BIN_DIR)
	@echo "Building $(BINARY_NAME) version $(VERSION)..."
	@go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME) .

test:
	@echo "Running tests..."
	@go test -v ./...

# --- Grove-tend E2E Testing ---
E2E_BINARY_NAME=tend
MOCK_BIN_DIR=tests/e2e/tend/mocks/bin

# Build mock binaries for E2E tests
build-mocks:
	@echo "Building mock binaries..."
	@mkdir -p $(MOCK_BIN_DIR)
	@for mock in llm docker grove-hooks grove tmux nb cx; do \
		echo "  Building mock-$$mock..."; \
		go build -o $(MOCK_BIN_DIR)/mock-$$mock ./tests/e2e/tend/mocks/src/$$mock; \
	done

# Build the custom tend binary for grove-flow E2E tests.
test-tend-build: build-mocks
	@echo "Building E2E test binary $(E2E_BINARY_NAME)..."
	@go build -o $(BIN_DIR)/$(E2E_BINARY_NAME) ./tests/e2e/tend

# Run grove-tend E2E tests.
test-e2e: build test-tend-build
	@echo "Running grove-tend E2E tests..."
	@$(BIN_DIR)/$(E2E_BINARY_NAME) run $(ARGS)


clean:
	@echo "Cleaning..."
	@go clean
	@rm -rf $(BIN_DIR)
	@rm -f $(BINARY_NAME)
	@rm -f coverage.out

fmt:
	@echo "Formatting code..."
	@go fmt ./...

vet:
	@echo "Running go vet..."
	@go vet ./...

lint:
	@echo "Running linter..."
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Run the CLI
run: build
	@$(BIN_DIR)/$(BINARY_NAME) $(ARGS)

# Run all checks
check: fmt vet test

# Generate documentation
generate-docs: build
	@echo "Generating documentation..."
	@docgen generate
	@echo "Synchronizing README.md..."
	@docgen sync-readme

# Development build with race detector
dev:
	@mkdir -p $(BIN_DIR)
	@echo "Building $(BINARY_NAME) version $(VERSION) with race detector..."
	@go build -race $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME) .

# Cross-compilation targets
PLATFORMS ?= darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
DIST_DIR ?= dist

build-all:
	@echo "Building for multiple platforms into $(DIST_DIR)..."
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$$(echo $$platform | cut -d'/' -f1); \
		arch=$$(echo $$platform | cut -d'/' -f2); \
		output_name="$(BINARY_NAME)-$${os}-$${arch}"; \
		echo "  -> Building $${output_name} version $(VERSION)"; \
		GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $(DIST_DIR)/$${output_name} .; \
	done

# Interactive e2e tests
test-orchestration-interactive: build
	@echo "Running orchestration tests in interactive mode..."
	@cd tests/e2e/orchestration-tests && GROVE_TEST_STEP_THROUGH=true FLOW_CMD=$$(cd ../../.. && pwd)/bin/flow ./test-orchestration-e2e.sh

test-reference-prompts-interactive: build
	@echo "Running reference prompts tests in interactive mode..."
	@cd tests/e2e/orchestration-tests && GROVE_TEST_STEP_THROUGH=true FLOW_CMD=$$(cd ../../.. && pwd)/bin/flow ./test-reference-prompts-e2e.sh

test-chat-interactive: build
	@echo "Running chat functionality tests in interactive mode..."
	@cd tests && GROVE_TEST_STEP_THROUGH=true FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-chat-functionality.sh

# Test individual e2e test files
test-chat-run: build
	@echo "Running chat run command tests..."
	@cd tests && FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-chat-run.sh

test-chat-run-interactive: build
	@echo "Running chat run command tests in interactive mode..."
	@cd tests && GROVE_TEST_STEP_THROUGH=true FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-chat-run.sh

test-chat-title-filtering: build
	@echo "Running chat title filtering tests..."
	@cd tests && FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-chat-title-filtering.sh

test-chat-title-filtering-interactive: build
	@echo "Running chat title filtering tests in interactive mode..."
	@cd tests && GROVE_TEST_STEP_THROUGH=true FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-chat-title-filtering.sh

test-launch: build
	@echo "Running launch feature tests..."
	@cd tests && FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-launch.sh

test-launch-interactive: build
	@echo "Running launch feature tests in interactive mode..."
	@cd tests && GROVE_TEST_STEP_THROUGH=true FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-launch-feature.sh

# Show available targets
help:
	@echo "Available targets:"
	@echo "  make build       - Build the binary"
	@echo "  make test        - Run tests"
	@echo "  make test-e2e    - Run end-to-end tests"
	@echo "  make clean       - Clean build artifacts"
	@echo "  make fmt         - Format code"
	@echo "  make vet         - Run go vet"
	@echo "  make lint        - Run linter"
	@echo "  make run ARGS=.. - Run the CLI with arguments"
	@echo "  make check       - Run all checks"
	@echo "  make dev         - Build with race detector"
	@echo "  make help        - Show this help"
	@echo ""
	@echo "Interactive test targets:"
	@echo "  make test-orchestration-interactive    - Run orchestration tests interactively"
	@echo "  make test-reference-prompts-interactive - Run reference prompts tests interactively"
	@echo "  make test-chat-interactive             - Run chat tests interactively"
	@echo ""
	@echo "Individual test targets:"
	@echo "  make test-chat-run                     - Run chat run command tests only"
	@echo "  make test-chat-run-interactive         - Run chat run tests in interactive mode"
	@echo "  make test-launch                       - Run launch feature tests only"
	@echo "  make test-launch-interactive           - Run launch tests in interactive mode"
