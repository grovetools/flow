# Makefile for building E2E test mocks

# Find all main.go files in the source directories
MOCK_SOURCES := $(wildcard mocks/src/*/main.go)

# Generate the target binary paths based on the source paths
# e.g., mocks/src/llm/main.go becomes mocks/bin/mock-llm
MOCK_BINARIES := $(patsubst mocks/src/%/main.go,mocks/bin/mock-%,$(MOCK_SOURCES))

# Default target
all: build-mocks

.PHONY: build-mocks clean
build-mocks: $(MOCK_BINARIES)

# Rule to build a mock binary from its source
# Creates the bin directory if it doesn't exist
mocks/bin/mock-%: mocks/src/%/main.go
	@mkdir -p $(dir $@)
	@echo "Building mock for '$*'..."
	@go build -o $@ $<

# Clean up compiled mocks
clean:
	@echo "Cleaning up mock binaries..."
	@rm -f mocks/bin/*

# Add this to your main project's .gitignore:
# tests/e2e/tend/mocks/bin/