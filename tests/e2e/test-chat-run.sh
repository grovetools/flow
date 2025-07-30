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
    echo "  1. Run directly in your terminal: GROVE_TEST_STEP_THROUGH=true ./test-chat-run.sh"
    echo "  2. Use make: make test-chat-run-interactive"
    echo ""
    INTERACTIVE="false"
fi

# Helper functions
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

header() {
    print_header "$1"
}

assert_contains() {
    local file="$1"
    local pattern="$2"
    local message="$3"
    
    if grep -q "$pattern" "$file"; then
        log_success "$message"
    else
        log_error "$message"
        echo "  Expected to find: $pattern"
        echo "  In file: $file"
    fi
}

refute_contains() {
    local file="$1"
    local pattern="$2"
    local message="$3"
    
    if ! grep -q "$pattern" "$file"; then
        log_success "$message"
    else
        log_error "$message"
        echo "  Expected NOT to find: $pattern"
        echo "  In file: $file"
    fi
}

assert_equal() {
    local actual="$1"
    local expected="$2"
    local message="$3"
    
    if [ "$actual" = "$expected" ]; then
        log_success "$message"
    else
        log_error "$message"
        echo "  Expected: $expected"
        echo "  Actual: $actual"
    fi
}

create_mock_llm() {
    local response="$1"
    # Create a mock response file
    echo "$response" > "$TEST_DIR/mock_response.txt"
    export GROVE_MOCK_LLM_RESPONSE_FILE="$TEST_DIR/mock_response.txt"
}

success() {
    echo -e "\n${GREEN}$1${NC}\n"
}

pause_if_interactive() {
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}[PAUSED]${NC} $1"
        echo "Press Enter to continue..."
        read -r
    fi
}

show_chat_status() {
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Current chat files:${NC}"
        echo "--- chat1/plan.md ---"
        tail -20 "$CHAT_DIR/chat1/plan.md"
        echo -e "\n--- chat2/plan.md ---"
        tail -20 "$CHAT_DIR/chat2/plan.md"
        echo ""
    fi
}

# Setup test directory
TEST_DIR=$(mktemp -d)

# Cleanup function
cleanup() {
    if [ "$INTERACTIVE" != "true" ]; then
        rm -rf "$TEST_DIR"
    else
        echo -e "\n${YELLOW}Test directory preserved at: $TEST_DIR${NC}"
    fi
}
trap cleanup EXIT

# --- Test Setup ---
header "Setting up test environment for 'flow chat run'"

if [ "$INTERACTIVE" = "true" ]; then
    log_info "Running in interactive mode. Press Enter at each pause point."
fi

# Create a directory for our chats
CHAT_DIR="$TEST_DIR/my-chats"
mkdir -p "$CHAT_DIR"

# Create a grove.yml pointing to our chat directory
cat > "$TEST_DIR/grove.yml" <<EOF
flow:
  chat_directory: "$CHAT_DIR"
EOF

# Initialize a git repository for the test
(cd "$TEST_DIR" && git init -q && git config user.email "test@example.com" && git config user.name "Test User")

# Create two chat directories with plan.md files. We add a user turn to make them runnable.
mkdir -p "$CHAT_DIR/chat1"
mkdir -p "$CHAT_DIR/chat2"
echo -e "Initial idea for chat 1.\n\n---\n\n> Go" > "$CHAT_DIR/chat1/plan.md"
echo -e "Initial idea for chat 2.\n\n---\n\n> Go" > "$CHAT_DIR/chat2/plan.md"

# Initialize them as chats
# NOTE: The flow command must be run from within the test directory
# so it can find the grove.yml file.
(cd "$TEST_DIR" && "$FLOW_CMD" chat -s "$CHAT_DIR/chat1/plan.md" --title "Chat One")
(cd "$TEST_DIR" && "$FLOW_CMD" chat -s "$CHAT_DIR/chat2/plan.md" --title "Chat Two")

# Setup mock for the 'llm' command
create_mock_llm "This is the first mocked LLM response."

# --- Test Execution ---

header "Running 'flow chat run' for the first time"
pause_if_interactive "About to run 'flow chat run' - both chats should be executed"
# Both chats have a user turn, so both should run.
(cd "$TEST_DIR" && "$FLOW_CMD" chat run)
show_chat_status

# --- Assertions ---

assert_contains "$CHAT_DIR/chat1/plan.md" "first mocked LLM response" "Chat 1 should receive the first LLM response."
assert_contains "$CHAT_DIR/chat2/plan.md" "first mocked LLM response" "Chat 2 should receive the first LLM response."

# After running, both chats should be waiting for user input (last turn is LLM).
# Running 'flow chat run' again should do nothing.
header "Running 'flow chat run' again, expecting no actions"
pause_if_interactive "About to run 'flow chat run' again - should find no runnable chats"
output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run 2>&1)
if echo "$output" | grep -q "No runnable chats found"; then
    log_success "Command correctly found no runnable chats"
else
    log_error "Command should find no runnable chats"
    echo "Output was: $output"
fi

# Now, add a user turn to chat1/plan.md to make it runnable again
header "Adding user input to chat1/plan.md"
pause_if_interactive "About to add user input to chat1 - this will make it runnable again"
echo -e "\n---\n\n> Thanks! Now can you elaborate on that?" >> "$CHAT_DIR/chat1/plan.md"
show_chat_status

# Update the mock for the second response
create_mock_llm "This is the SECOND mocked response for elaboration."

# Run again. Only chat1 should be executed.
header "Running 'flow chat run' a third time"
pause_if_interactive "About to run 'flow chat run' - only chat1 should be executed"
(cd "$TEST_DIR" && "$FLOW_CMD" chat run)
show_chat_status

# --- Final Assertions ---

assert_contains "$CHAT_DIR/chat1/plan.md" "SECOND mocked response" "Chat 1 should receive the second LLM response."
refute_contains "$CHAT_DIR/chat2/plan.md" "SECOND mocked response" "Chat 2 should not have been modified."

# Verify chat2/plan.md still only has the first response
count=$(grep -c "mocked LLM response" "$CHAT_DIR/chat2/plan.md")
assert_equal "$count" "1" "Chat 2 should only have one LLM response."

success "Chat run e2e test passed!"