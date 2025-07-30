# Makefile for flow

BINARY_NAME=flow
INSTALL_PATH=/usr/local/bin
BIN_DIR=bin

.PHONY: all build install uninstall test test-e2e clean fmt vet lint run

all: build

build:
	@mkdir -p $(BIN_DIR)
	@echo "Building $(BINARY_NAME)..."
	@go build -o $(BIN_DIR)/$(BINARY_NAME) .

install: build
	@echo "Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	@sudo cp $(BIN_DIR)/$(BINARY_NAME) $(INSTALL_PATH)/
	@echo "Installed successfully!"

uninstall:
	@echo "Uninstalling $(BINARY_NAME)..."
	@sudo rm -f $(INSTALL_PATH)/$(BINARY_NAME)
	@echo "Uninstalled successfully!"

test:
	@echo "Running tests..."
	@go test -v ./...

test-e2e: build
	@echo "Running jobs end-to-end tests..."
	@chmod +x tests/e2e/*.sh
	@chmod +x tests/e2e/orchestration-tests/*.sh
	@# Run basic functionality test
	@cd tests && FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-basic-functionality.sh
	@echo ""
	@echo "Running advanced orchestration tests..."
	@cd tests/e2e/orchestration-tests && FLOW_CMD=$$(cd ../../.. && pwd)/bin/flow ./test-orchestration-e2e.sh
	@echo ""
	@echo "Running reference prompts tests..."
	@cd tests/e2e/orchestration-tests && FLOW_CMD=$$(cd ../../.. && pwd)/bin/flow ./test-reference-prompts-e2e.sh
	@echo ""
	@echo "Running chat functionality tests..."
	@cd tests && FLOW_CMD=$$(cd .. && pwd)/bin/flow ./e2e/test-chat-functionality.sh
	@echo ""
	@echo "Note: Full pipeline test (test-chat-pipeline.sh) is disabled due to git worktree"
	@echo "      limitations when running in temporary directories. To run it manually:"
	@echo "      cd tests && ./e2e/test-chat-pipeline.sh"

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

# Development build with race detector
dev:
	@mkdir -p $(BIN_DIR)
	@echo "Building $(BINARY_NAME) with race detector..."
	@go build -race -o $(BIN_DIR)/$(BINARY_NAME) .

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

# Show available targets
help:
	@echo "Available targets:"
	@echo "  make build       - Build the binary"
	@echo "  make install     - Build and install to $(INSTALL_PATH)"
	@echo "  make uninstall   - Remove from $(INSTALL_PATH)"
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
