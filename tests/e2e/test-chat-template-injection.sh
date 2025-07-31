#!/bin/bash
# Test that template directives are automatically added after LLM responses
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

# Setup test directory
TEST_DIR=$(mktemp -d)
trap "rm -rf $TEST_DIR" EXIT

# --- Test Setup ---
print_header "Setting up test environment for template injection"

# Create a directory for our chat
CHAT_DIR="$TEST_DIR/my-chats"
mkdir -p "$CHAT_DIR"

# Create a grove.yml pointing to our chat directory
cat > "$TEST_DIR/grove.yml" <<EOF
flow:
  chat_directory: "$CHAT_DIR"
EOF

# Initialize a git repository for the test
(cd "$TEST_DIR" && git init -q && git config user.email "test@example.com" && git config user.name "Test User")

# Create a chat directory with initial content
mkdir -p "$CHAT_DIR/test-chat"
cat > "$CHAT_DIR/test-chat/plan.md" <<'EOF'
Initial idea for testing template injection.

---

> Please help me design a simple API
EOF

# Initialize it as a chat
(cd "$TEST_DIR" && "$FLOW_CMD" chat -s "$CHAT_DIR/test-chat/plan.md" --title "Test Chat")

# Setup mock for the 'llm' command
echo "Here's my design for a simple API with GET and POST endpoints." > "$TEST_DIR/mock_response.txt"
export GROVE_MOCK_LLM_RESPONSE_FILE="$TEST_DIR/mock_response.txt"

# --- Test Execution ---
print_header "Running 'flow chat run' to get LLM response"
(cd "$TEST_DIR" && "$FLOW_CMD" chat run)

# --- Assertions ---
print_header "Verifying template directive was automatically added"

# Check that the LLM response was added
assert_contains "$CHAT_DIR/test-chat/plan.md" "Here's my design for a simple API" "LLM response should be added to chat"

# Check that the template directive was automatically added after the LLM response
assert_contains "$CHAT_DIR/test-chat/plan.md" '<!-- grove: {"template": "chat"} -->' "Template directive should be automatically added after LLM response"

# Verify the template directive appears AFTER the LLM response, not before
# Debug: show the actual content
echo "=== Debug: File content after LLM response ==="
tail -10 "$CHAT_DIR/test-chat/plan.md"
echo "=== End debug ==="

# Check position more carefully - the directive should be after the response content
if grep -A3 "Here's my design for a simple API" "$CHAT_DIR/test-chat/plan.md" | grep -q '<!-- grove: {"template": "chat"} -->'; then
    log_success "Template directive appears after LLM response (correct position)"
else
    log_error "Template directive not in correct position relative to LLM response"
fi

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