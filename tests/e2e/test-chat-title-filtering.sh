#!/bin/bash
# Test chat title filtering functionality
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
    echo "  1. Run directly in your terminal: GROVE_TEST_STEP_THROUGH=true ./test-chat-title-filtering.sh"
    echo "  2. Use make: make test-chat-title-filtering-interactive"
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
        echo "  File contents:"
        cat "$file"
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

assert_output_contains() {
    local output="$1"
    local pattern="$2"
    local message="$3"
    
    if echo "$output" | grep -q "$pattern"; then
        log_success "$message"
    else
        log_error "$message"
        echo "  Expected to find: $pattern"
        echo "  In output: $output"
    fi
}

create_mock_llm() {
    local response="$1"
    # Create a mock response file
    echo "$response" > "$TEST_DIR/mock_response.txt"
    export GROVE_MOCK_LLM_RESPONSE_FILE="$TEST_DIR/mock_response.txt"
}

pause_if_interactive() {
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}[PAUSED]${NC} $1"
        echo "Press Enter to continue..."
        read -r
    fi
}

show_chat_list() {
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Current chat list:${NC}"
        (cd "$TEST_DIR" && "$FLOW_CMD" chat list)
        echo ""
    fi
}

# Script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Use the flow binary - default to the built binary
if [ -z "$FLOW_CMD" ]; then
    PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
    FLOW_CMD="$PROJECT_ROOT/bin/flow"
fi

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
header "Setting up test environment for chat title filtering"

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

# Create multiple chat directories with different states
mkdir -p "$CHAT_DIR/testing-situation"
mkdir -p "$CHAT_DIR/agent-session-archiving"
mkdir -p "$CHAT_DIR/build-and-dev-process"
mkdir -p "$CHAT_DIR/completed-chat"

# Create initial content for each chat (with user turns to make them runnable)
echo -e "Testing situation discussion.\n\n---\n\n> Please help with testing" > "$CHAT_DIR/testing-situation/plan.md"
echo -e "Agent session archiving ideas.\n\n---\n\n> How should we archive sessions?" > "$CHAT_DIR/agent-session-archiving/plan.md"
echo -e "Build and dev process improvements.\n\n---\n\n> Suggest improvements" > "$CHAT_DIR/build-and-dev-process/plan.md"
echo -e "This chat is already done.\n\n---\n\n> Done" > "$CHAT_DIR/completed-chat/plan.md"

# Initialize them as chats
pause_if_interactive "About to initialize all chats"
(cd "$TEST_DIR" && "$FLOW_CMD" chat -s "$CHAT_DIR/testing-situation/plan.md" --title "testing-situation")
(cd "$TEST_DIR" && "$FLOW_CMD" chat -s "$CHAT_DIR/agent-session-archiving/plan.md" --title "agent-session-archiving")
(cd "$TEST_DIR" && "$FLOW_CMD" chat -s "$CHAT_DIR/build-and-dev-process/plan.md" --title "build-and-dev-process")
(cd "$TEST_DIR" && "$FLOW_CMD" chat -s "$CHAT_DIR/completed-chat/plan.md" --title "completed-chat")

# Manually mark the completed-chat as completed by editing its frontmatter
sed -i.bak 's/status: pending_user/status: completed/' "$CHAT_DIR/completed-chat/plan.md"

show_chat_list

# --- Test 1: Run specific chat by title ---
header "Test 1: Running a specific chat by title"
pause_if_interactive "About to run only 'testing-situation' chat"

# Setup mock for the 'llm' command
create_mock_llm "Response for testing-situation chat."

# Run only the testing-situation chat
output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run testing-situation 2>&1)
assert_output_contains "$output" "Found 1 runnable chat(s)" "Should find exactly 1 runnable chat"
assert_output_contains "$output" "Running Chat: testing-situation" "Should run testing-situation chat"

# Verify only the specified chat received the response
assert_contains "$CHAT_DIR/testing-situation/plan.md" "Response for testing-situation" "testing-situation should have response"
refute_contains "$CHAT_DIR/agent-session-archiving/plan.md" "Response for testing-situation" "agent-session-archiving should not have response"
refute_contains "$CHAT_DIR/build-and-dev-process/plan.md" "Response for testing-situation" "build-and-dev-process should not have response"

# --- Test 2: Run multiple specific chats by title ---
header "Test 2: Running multiple chats by title"
pause_if_interactive "About to run 'agent-session-archiving' and 'build-and-dev-process' chats"

# Setup mock for the next responses
create_mock_llm "Response for multiple chats test."

# Run two specific chats
output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run agent-session-archiving build-and-dev-process 2>&1)
assert_output_contains "$output" "Found 2 runnable chat(s)" "Should find exactly 2 runnable chats"
assert_output_contains "$output" "Running Chat: agent-session-archiving" "Should run agent-session-archiving"
assert_output_contains "$output" "Running Chat: build-and-dev-process" "Should run build-and-dev-process"

# Verify both specified chats received responses
assert_contains "$CHAT_DIR/agent-session-archiving/plan.md" "Response for multiple chats" "agent-session-archiving should have response"
assert_contains "$CHAT_DIR/build-and-dev-process/plan.md" "Response for multiple chats" "build-and-dev-process should have response"

# --- Test 3: Attempt to run completed chat ---
header "Test 3: Attempting to run a completed chat"
pause_if_interactive "About to try running 'completed-chat' which is already completed"

output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run completed-chat 2>&1)
assert_output_contains "$output" "No runnable chats found with the specified title(s): completed-chat" "Should not find completed chat as runnable"
assert_output_contains "$output" "Available chats:" "Should show available chats"
assert_output_contains "$output" "completed-chat (status: completed)" "Should show completed chat status"

# --- Test 4: Non-existent chat title ---
header "Test 4: Running with non-existent chat title"
pause_if_interactive "About to try running 'non-existent-chat'"

output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run non-existent-chat 2>&1)
assert_output_contains "$output" "No runnable chats found with the specified title(s): non-existent-chat" "Should report non-existent chat"
assert_output_contains "$output" "Available chats:" "Should show available chats list"

# --- Test 5: Mixed runnable and non-runnable titles ---
header "Test 5: Mixed runnable and non-runnable titles"
pause_if_interactive "About to run mix of completed and non-existent chats with one valid chat"

# First add a user turn to testing-situation to make it runnable again
echo -e "\n---\n\n> Another question for testing" >> "$CHAT_DIR/testing-situation/plan.md"

create_mock_llm "Response for mixed test."

output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run completed-chat testing-situation non-existent 2>&1)
assert_output_contains "$output" "Found 1 runnable chat(s)" "Should find only 1 runnable chat from the mix"
assert_output_contains "$output" "Running Chat: testing-situation" "Should run the valid runnable chat"

# --- Test 6: Running all chats (no title filter) ---
header "Test 6: Running all runnable chats (no title specified)"
pause_if_interactive "About to run all runnable chats"

# Add user turns to make more chats runnable
echo -e "\n---\n\n> Follow-up question" >> "$CHAT_DIR/agent-session-archiving/plan.md"
echo -e "\n---\n\n> Another improvement?" >> "$CHAT_DIR/build-and-dev-process/plan.md"

create_mock_llm "Response for all chats."

output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run 2>&1)
assert_output_contains "$output" "Found 2 runnable chat(s)" "Should find all runnable chats"

# --- Test 7: Legacy file path support ---
header "Test 7: Legacy file path support"
pause_if_interactive "About to test legacy file path argument"

# Add a user turn to a specific chat
echo -e "\n---\n\n> Legacy path test" >> "$CHAT_DIR/testing-situation/plan.md"

create_mock_llm "Response for legacy path test."

# Test with full file path (legacy behavior)
output=$(cd "$TEST_DIR" && "$FLOW_CMD" chat run "$CHAT_DIR/testing-situation/plan.md" 2>&1)
assert_output_contains "$output" "Found 1 runnable chat(s)" "Should work with file path"
assert_output_contains "$output" "Running Chat: testing-situation" "Should run the specified file"

show_chat_list

# --- Summary ---
echo -e "\n${YELLOW}=== Test Summary ===${NC}"
echo "Tests run: $((TESTS_PASSED + TESTS_FAILED))"
echo -e "Tests passed: ${GREEN}$TESTS_PASSED${NC}"
echo -e "Tests failed: ${RED}$TESTS_FAILED${NC}"

if [ $TESTS_FAILED -eq 0 ]; then
    echo -e "\n${GREEN}All tests passed!${NC}"
    exit 0
else
    echo -e "\n${RED}Some tests failed!${NC}"
    exit 1
fi