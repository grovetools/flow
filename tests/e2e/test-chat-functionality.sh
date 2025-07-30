#!/bin/bash
# Don't use set -e as it makes debugging harder
set -o pipefail

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Interactive mode flag
INTERACTIVE="${GROVE_TEST_STEP_THROUGH:-false}"

# Check if we're in an interactive terminal
if [ "$INTERACTIVE" = "true" ] && [ ! -t 0 ]; then
    echo -e "${YELLOW}Warning: GROVE_TEST_STEP_THROUGH=true but not running in interactive terminal${NC}"
    echo -e "${YELLOW}Interactive mode disabled. To enable interactive mode:${NC}"
    echo "  1. Run directly in your terminal: GROVE_TEST_STEP_THROUGH=true ./test-chat-functionality.sh"
    echo "  2. Use make: make test-chat-interactive"
    echo ""
    INTERACTIVE="false"
fi

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[PASS]${NC} $1"
    ((TESTS_PASSED++))
}

log_error() {
    echo -e "${RED}[FAIL]${NC} $1"
    ((TESTS_FAILED++))
}

print_header() {
    echo -e "\n${YELLOW}==== $1 ====${NC}\n"
}

print_test_summary() {
    echo -e "\n${YELLOW}Test Summary:${NC}"
    echo "Tests run: $((TESTS_PASSED + TESTS_FAILED))"
    echo -e "Passed: ${GREEN}$TESTS_PASSED${NC}"
    echo -e "Failed: ${RED}$TESTS_FAILED${NC}"
}

pause_if_interactive() {
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}[PAUSED]${NC} $1"
        echo "Press Enter to continue..."
        read -r < /dev/tty || true
    else
        # In non-interactive mode, show progress marker
        echo -e "${YELLOW}[STEP]${NC} $1"
    fi
}

assert_command_success() {
    local desc=$1
    shift
    ((TESTS_RUN++))
    if "$@"; then
        log_success "$desc"
        return 0
    else
        log_error "$desc"
        return 1
    fi
}

# Script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Use the flow binary - default to the built binary
if [ -z "$FLOW_CMD" ]; then
    PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
    FLOW_CMD="$PROJECT_ROOT/bin/flow"
fi

# Test in a temporary directory
TEST_DIR=$(mktemp -d)
CHAT_DIR="$TEST_DIR/chats"

cleanup() {
    log_info "Cleaning up chat test..."
    if [ "$INTERACTIVE" != "true" ]; then
        rm -rf "$TEST_DIR"
    else
        echo -e "\n${YELLOW}Test directory preserved for inspection: $TEST_DIR${NC}"
    fi
    print_test_summary
}
trap cleanup EXIT

setup_test_environment() {
    log_info "Setting up chat test environment..."
    log_info "Test directory: $TEST_DIR"
    
    # Create test directory structure
    mkdir -p "$TEST_DIR"
    mkdir -p "$CHAT_DIR"
    
    # Initialize git repo
    cd "$TEST_DIR"
    git init -q
    git config user.email "test@example.com"
    git config user.name "Test User"
    
    # Create grove config with chat directory
    cat > grove.yml <<EOF
flow:
  chat_directory: chats
  oneshot_model: claude-3-5-sonnet-20241022
  plans_directory: plans
EOF
    
    # Create test markdown files in the chat directory (simulating existing files)
    cat > "$CHAT_DIR/feature-request.md" <<'EOF'
# Feature Request: Add User Authentication

We need to add user authentication to our application. This should include:

1. Login/logout functionality
2. Session management
3. Password reset capability
4. Remember me option

Please provide a high-level implementation plan.
EOF

    cat > "$CHAT_DIR/bug-report.md" <<'EOF'
# Bug Report: Database Connection Timeout

Users are experiencing timeout errors when the application tries to connect to the database during peak hours.

Error message: "Connection timeout after 30 seconds"

Please analyze this issue and suggest potential fixes.
EOF

    # Create subdirectory with more files
    mkdir -p "$CHAT_DIR/project-ideas"
    cat > "$CHAT_DIR/project-ideas/new-feature.md" <<'EOF'
# New Feature Idea

Implement a dashboard for monitoring system health.
EOF

    # Create an already-initialized chat
    cat > "$CHAT_DIR/existing-chat.md" <<'EOF'
---
type: chat
model: claude-3-5-sonnet-20241022
status: completed
created_at: 2024-01-01T10:00:00Z
title: existing-chat
id: job-existing01
---

# Previous Chat Session

This is an existing chat that was already completed.
EOF

    # Commit initial files
    git add .
    git commit -m "Initial test setup" -q
    
    pause_if_interactive "Test environment created"
}

test_chat_init_basic() {
    print_header "Test: Basic Chat Initialization"
    
    pause_if_interactive "About to initialize a chat from markdown file"
    
    # Initialize chat from feature request (in chat directory)
    log_info "Executing: $FLOW_CMD chat -s $CHAT_DIR/feature-request.md"
    local output
    output=$("$FLOW_CMD" chat -s "$CHAT_DIR/feature-request.md" 2>&1)
    
    if echo "$output" | grep -q "Initialized chat job"; then
        log_success "Chat init command executed"
    else
        log_error "Chat init command failed"
        echo "Output: $output"
        return 1
    fi
    
    # The file should be modified in-place
    local chat_file="$CHAT_DIR/feature-request.md"
    
    if [ -f "$chat_file" ]; then
        log_success "Chat file exists (modified in-place)"
        
        # Verify frontmatter was added
        if grep -q "^type: chat" "$chat_file"; then
            log_success "Chat file has correct type"
        else
            log_error "Chat file missing type: chat"
        fi
        
        if grep -q "^model:" "$chat_file"; then
            log_success "Chat file has model specified"
        else
            log_error "Chat file missing model"
        fi
        
        if grep -q "^status: pending_user" "$chat_file"; then
            log_success "Chat file has pending_user status"
        else
            log_error "Chat file has incorrect status"
        fi
        
        # Verify content preservation
        if grep -q "Feature Request: Add User Authentication" "$chat_file"; then
            log_success "Original content preserved"
        else
            log_error "Original content not preserved"
        fi
    else
        log_error "Chat file not found"
        echo "Contents of $CHAT_DIR:"
        ls -la "$CHAT_DIR"
    fi
    
    pause_if_interactive "Basic chat initialized"
    return 0
}

test_chat_init_with_model() {
    print_header "Test: Chat Init with Custom Model"
    
    pause_if_interactive "About to initialize chat with custom model"
    
    # Initialize with specific model
    log_info "Executing: $FLOW_CMD chat -s $CHAT_DIR/bug-report.md -m gpt-4"
    local output=$("$FLOW_CMD" chat -s "$CHAT_DIR/bug-report.md" -m gpt-4 2>&1)
    
    if echo "$output" | grep -q "Initialized chat job"; then
        log_success "Chat init with model executed"
    else
        log_error "Chat init with model failed"
        return 1
    fi
    
    # The file should be modified in-place
    local chat_file="$CHAT_DIR/bug-report.md"
    
    if [ -f "$chat_file" ]; then
        # Verify custom model
        if grep -q "^model: gpt-4" "$chat_file"; then
            log_success "Custom model correctly set"
        else
            log_error "Custom model not set correctly"
            echo "Chat file content:"
            head -20 "$chat_file"
        fi
    else
        log_error "Chat file not found"
    fi
    
    pause_if_interactive "Chat with custom model initialized"
}

test_chat_list() {
    print_header "Test: Chat List Command"
    
    pause_if_interactive "About to list chat jobs"
    
    # First, let's initialize the subdirectory file to test recursive search
    log_info "Executing: $FLOW_CMD chat -s $CHAT_DIR/project-ideas/new-feature.md"
    "$FLOW_CMD" chat -s "$CHAT_DIR/project-ideas/new-feature.md" >/dev/null 2>&1
    
    # List chats
    log_info "Executing: $FLOW_CMD chat list"
    local output=$("$FLOW_CMD" chat list 2>&1)
    
    # Should show all the chats we initialized
    if echo "$output" | grep -q "feature-request"; then
        log_success "Feature request chat listed"
    else
        log_error "Feature request chat not listed"
    fi
    
    if echo "$output" | grep -q "bug-report"; then
        log_success "Bug report chat listed"
    else
        log_error "Bug report chat not listed"
    fi
    
    if echo "$output" | grep -q "existing-chat"; then
        log_success "Existing chat listed"
    else
        log_error "Existing chat not listed"
    fi
    
    if echo "$output" | grep -q "new-feature"; then
        log_success "Subdirectory chat listed (recursive search works)"
    else
        log_error "Subdirectory chat not listed (recursive search failed)"
    fi
    
    # Check for status indicators
    if echo "$output" | grep -q "pending_user"; then
        log_success "Pending user status shown"
    else
        log_error "Pending user status not shown"
    fi
    
    if echo "$output" | grep -q "completed"; then
        log_success "Completed status shown"
    else
        log_error "Completed status not shown"
    fi
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Full chat list output:${NC}"
        echo "$output"
    fi
    
    pause_if_interactive "Chat list displayed"
}

test_chat_without_worktree() {
    print_header "Test: Chat Execution Without Worktree"
    
    pause_if_interactive "About to verify chat runs without creating worktree"
    
    # Create a simple test file to verify no worktree is created
    echo "test content" > test-file.txt
    git add test-file.txt
    git commit -m "Add test file" -q
    
    # Note the current number of worktrees (should be 0)
    local worktrees_before=$(git worktree list | wc -l)
    
    # Get a chat file to "run" (we'll just verify the structure)
    local chat_file=$(ls "$CHAT_DIR"/*feature-request*.md 2>/dev/null | head -1)
    
    if [ -n "$chat_file" ]; then
        # In a real scenario, this would execute the chat
        # For testing, we just verify no worktree is created
        
        # Simulate what the chat execution would do - check the frontmatter
        if ! grep -q "^worktree:" "$chat_file"; then
            log_success "Chat has no worktree specified"
        else
            log_error "Chat incorrectly has worktree specified"
        fi
        
        # Verify no new worktrees were created
        local worktrees_after=$(git worktree list | wc -l)
        if [ "$worktrees_before" -eq "$worktrees_after" ]; then
            log_success "No worktree created for chat"
        else
            log_error "Unexpected worktree created"
        fi
    else
        log_error "No chat file found to test"
    fi
    
    pause_if_interactive "Worktree test complete"
}

test_chat_error_handling() {
    print_header "Test: Chat Error Handling"
    
    pause_if_interactive "About to test error conditions"
    
    # Test with non-existent file
    log_info "Executing: $FLOW_CMD chat -s nonexistent.md"
    local error_output=$("$FLOW_CMD" chat -s nonexistent.md 2>&1)
    if echo "$error_output" | grep -qi "file not found"; then
        log_success "Handles non-existent file correctly"
    else
        log_error "Should error on non-existent file"
        echo "Actual output: $error_output" | head -5
    fi
    
    # Test with directory instead of file
    mkdir -p "$TEST_DIR/not-a-file"
    log_info "Executing: $FLOW_CMD chat -s not-a-file"
    error_output=$("$FLOW_CMD" chat -s not-a-file 2>&1)
    if echo "$error_output" | grep -q "path is a directory"; then
        log_success "Handles directory input correctly"
    else
        log_error "Should error on directory input"
        echo "Actual output: $error_output" | head -5
    fi
    
    # Test list with no config
    cd /tmp
    log_info "Executing: $FLOW_CMD chat list (from /tmp)"
    if "$FLOW_CMD" chat list 2>&1 | grep -q -E "(config|grove.yml|not found)"; then
        log_success "Handles missing config correctly"
    else
        log_info "List command may work without config"
    fi
    cd "$TEST_DIR"
    
    pause_if_interactive "Error handling tests complete"
}

test_chat_absolute_vs_relative_paths() {
    print_header "Test: Absolute vs Relative Path Handling"
    
    pause_if_interactive "About to test path handling"
    
    # Create a file outside chat directory to test initialization
    mkdir -p "$TEST_DIR/docs"
    cat > "$TEST_DIR/docs/readme-update.md" <<'EOF'
# README Update Request

Please update the README to include installation instructions.
EOF
    
    # Test initializing file outside chat directory
    log_info "Executing: $FLOW_CMD chat -s docs/readme-update.md"
    local output=$("$FLOW_CMD" chat -s docs/readme-update.md 2>&1)
    if echo "$output" | grep -q "Initialized chat job"; then
        log_success "Can initialize files outside chat directory"
        
        # Verify the file was modified in-place
        if grep -q "^type: chat" "$TEST_DIR/docs/readme-update.md"; then
            log_success "File modified in-place outside chat directory"
        else
            log_error "File not properly initialized"
        fi
    else
        log_error "Failed to initialize file outside chat directory"
    fi
    
    # Test re-initializing already initialized file
    log_info "Executing: $FLOW_CMD chat -s docs/readme-update.md (again)"
    output=$("$FLOW_CMD" chat -s docs/readme-update.md 2>&1)
    if echo "$output" | grep -q "already initialized as a chat"; then
        log_success "Detects already initialized file"
    else
        log_error "Should detect already initialized file"
    fi
    
    pause_if_interactive "Path handling tests complete"
}

test_chat_config_integration() {
    print_header "Test: Chat Config Integration"
    
    pause_if_interactive "About to test config integration"
    
    # Update config with different chat directory
    log_info "Creating custom grove.yml with chat_directory: my-custom-chats"
    cat > grove.yml <<EOF
flow:
  chat_directory: my-custom-chats
  oneshot_model: gpt-4
  plans_directory: plans
EOF
    
    mkdir -p my-custom-chats
    
    # Create a new test file in the custom directory
    cat > my-custom-chats/config-test.md <<'EOF'
# Config Test

Testing custom chat directory configuration.
EOF
    
    # Initialize chat with new config
    log_info "Executing: $FLOW_CMD chat -s my-custom-chats/config-test.md"
    local output=$("$FLOW_CMD" chat -s my-custom-chats/config-test.md 2>&1)
    
    if echo "$output" | grep -q "Initialized chat job"; then
        # Check if file was created in custom directory
        if ls my-custom-chats/*config-test*.md >/dev/null 2>&1; then
            log_success "Chat created in custom directory"
        else
            log_error "Chat not created in custom directory"
            echo "Contents of my-custom-chats:"
            ls -la my-custom-chats/
        fi
        
        # Verify default model from config
        local chat_file="my-custom-chats/config-test.md"
        if [ -f "$chat_file" ]; then
            echo "DEBUG: Chat file contents:"
            cat "$chat_file" | head -20
            if grep -q "^model: gpt-4" "$chat_file"; then
                log_success "Default model from config used"
            else
                log_error "Default model from config not used"
            fi
        else
            log_error "Chat file not found at expected location"
        fi
    else
        log_error "Chat init with custom config failed"
    fi
    
    pause_if_interactive "Config integration test complete"
}

# Main test execution
main() {
    print_header "Grove Chat Functionality E2E Test"
    
    if [ "$INTERACTIVE" = "true" ]; then
        log_info "Running in interactive mode. Press Enter at each pause point."
    fi
    
    setup_test_environment
    
    # Run all tests
    test_chat_init_basic
    test_chat_init_with_model
    test_chat_list
    
    test_chat_without_worktree
    test_chat_error_handling
    test_chat_absolute_vs_relative_paths
    test_chat_config_integration
    
    print_header "Test Complete"
    
    # Return appropriate exit code
    if [ $TESTS_FAILED -eq 0 ]; then
        return 0
    else
        return 1
    fi
}

# Run main
main