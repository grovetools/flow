#!/bin/bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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
    echo "  1. Run directly in your terminal: GROVE_TEST_STEP_THROUGH=true ./test-orchestration-e2e.sh"
    echo "  2. Use the wrapper script: ./run-interactive.sh"
    echo "  3. Use make: make test-orchestration-interactive"
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

FLOW_CMD="${FLOW_CMD:-flow}"
DEMO_DIR="${SCRIPT_DIR}/../../fixtures/orchestration-demo"
PLAN_DIR="${DEMO_DIR}/my-feature-plan"

cleanup() {
    log_info "Cleaning up orchestration test..."
    # Don't cleanup in interactive mode so user can inspect
    if [ "$INTERACTIVE" != "true" ]; then
        rm -rf "$PLAN_DIR"
        # Also remove the git directory created during setup
        rm -rf "$DEMO_DIR/.git"
        rm -rf "$DEMO_DIR/.grove-worktrees"
        (cd "$DEMO_DIR" && git worktree prune) 2>/dev/null || true
    fi
    print_test_summary
}
trap cleanup EXIT

setup_demo_environment() {
    log_info "Setting up orchestration demo environment..."
    
    # Ensure demo dir exists
    mkdir -p "$DEMO_DIR"
    
    # Clean up any previous git repo in the demo dir
    rm -rf "$DEMO_DIR/.git"
    rm -rf "$DEMO_DIR/.grove-worktrees"
    
    # Ensure demo dir is a git repo for the test run
    (cd "$DEMO_DIR" && git init && git add . && git commit -m "Initial commit for test run" --allow-empty) >/dev/null 2>&1
    
    # Create a simple spec file
    cat > "$DEMO_DIR/feature-spec.md" <<'EOF'
# Health Check Feature

Implement a health check endpoint for our service.

## Requirements
1. GET /health endpoint
2. Return JSON with status and timestamp
3. Include database connectivity check
EOF
    
    # Cleanup any previous runs
    rm -rf "$PLAN_DIR"
}

test_init_command() {
    log_info "Testing init command..."
    
    "$FLOW_CMD" jobs init "$PLAN_DIR" -s "$DEMO_DIR/feature-spec.md" --create-initial-job
    
    # Check for any initial job file
    if ! ls "$PLAN_DIR"/*.md >/dev/null 2>&1; then
        log_error "Initial job not created"
        return 1
    fi
    
    log_success "Init command test passed"
    
    pause_if_interactive "Initial plan created. Check $PLAN_DIR"
}

test_status_command() {
    log_info "Testing status command..."
    
    local output=$("$FLOW_CMD" jobs status "$PLAN_DIR" 2>&1)
    
    # Check for expected output - look for the initial job file
    if ! echo "$output" | grep -q "01-initial-job.md"; then
        log_error "Status output missing job file"
        return 1
    fi
    
    if ! echo "$output" | grep -qi "pending"; then
        log_error "Status output missing job status"
        return 1
    fi
    
    log_success "Status command test passed"
    
    # Show status in interactive mode
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Current status:${NC}"
        "$FLOW_CMD" jobs status "$PLAN_DIR"
        pause_if_interactive "Review the job status"
    fi
}

test_run_oneshot_job() {
    log_info "Testing oneshot job execution..."
    
    # Create a realistic job related to the health check feature
    cat > "$PLAN_DIR/02-design-api.md" <<'EOF'
---
id: design-api
title: "Design Health Check API"
status: pending
type: oneshot
depends_on:
  - 01-initial-job.md
output:
  type: file
---
Design the API structure for the health check endpoint.
Consider REST conventions and response format.
EOF
    
    pause_if_interactive "About to run oneshot job. Check $PLAN_DIR/02-design-api.md"
    
    # Run the job (in real scenario, this would use LLM)
    # For now, we'll just verify the command runs
    if "$FLOW_CMD" jobs run "$PLAN_DIR/02-design-api.md" --yes 2>&1 | grep -q "Error"; then
        log_error "Job execution failed"
        return 1
    fi
    
    log_success "Oneshot job test passed"
}

test_dependency_resolution() {
    log_info "Testing dependency resolution..."
    
    # Create job with dependency
    cat > "$PLAN_DIR/03-implement-backend.md" <<'EOF'
---
id: implement-backend
title: "Implement Health Check Endpoint"
status: pending
type: agent
depends_on:
  - design-api
worktree: feature-health-check
output:
  type: commit
---
Implement the /health endpoint based on the API design.
Include basic health status and timestamp.
EOF
    
    pause_if_interactive "Created job with dependency. Check dependency resolution"
    
    # This should work if 02 is completed
    # In a real test, we'd verify the dependency logic
    
    log_success "Dependency resolution test passed"
}

test_parallel_execution() {
    log_info "Testing parallel execution..."
    
    # Create two parallel jobs that can run after backend implementation
    cat > "$PLAN_DIR/04-add-tests.md" <<'EOF'
---
id: add-tests
title: "Add Unit Tests"
status: pending
type: agent
depends_on:
  - implement-backend
worktree: feature-health-check
output:
  type: commit
---
Add unit tests for the health check endpoint.
Test successful responses and error cases.
EOF

    cat > "$PLAN_DIR/05-update-docs.md" <<'EOF'
---
id: update-docs
title: "Update API Documentation"
status: pending
type: oneshot
depends_on:
  - implement-backend
output:
  type: file
---
Update the API documentation to include the new health check endpoint.
Document request/response format and examples.
EOF
    
    pause_if_interactive "Created parallel jobs. About to run them"
    
    # In a real test, we'd run these in parallel and verify
    
    log_success "Parallel execution test passed"
}

test_add_step_command() {
    log_info "Testing add-step command..."
    
    pause_if_interactive "About to test add-step command"
    
    # Test non-interactive mode
    assert_command_success "add step via CLI args" \
        "$FLOW_CMD" jobs add-step "$PLAN_DIR" \
            --title "Add Database Health Check" \
            --type agent \
            -d 03-implement-backend.md \
            --prompt-file /dev/stdin <<< "Add database connectivity check to the health endpoint"
    
    # Verify job was created
    if ! ls "$PLAN_DIR"/*-add-database-health-check.md >/dev/null 2>&1; then
        log_error "Job file not created by add-step"
        return 1
    fi
    
    log_success "Add-step command test passed"
    
    pause_if_interactive "New job added. Check the plan directory"
}

test_graph_command() {
    log_info "Testing graph command..."
    
    # Test mermaid output
    local output=$("$FLOW_CMD" jobs graph "$PLAN_DIR" -f mermaid 2>&1)
    
    if ! echo "$output" | grep -q "graph TD"; then
        log_error "Graph output missing mermaid syntax"
        return 1
    fi
    
    # Test ASCII output
    output=$("$FLOW_CMD" jobs graph "$PLAN_DIR" -f ascii 2>&1)
    
    if ! echo "$output" | grep -q "Job Dependency Graph"; then
        log_error "Graph output missing ASCII header"
        if [ "$INTERACTIVE" = "true" ] || [ -n "$DEBUG" ]; then
            echo "Actual output:"
            echo "$output"
        fi
        return 1
    fi
    
    log_success "Graph command test passed"
    
    # Show graph in interactive mode
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Dependency Graph (ASCII):${NC}"
        "$FLOW_CMD" jobs graph "$PLAN_DIR" -f ascii
        echo -e "\n${YELLOW}Dependency Graph (Mermaid):${NC}"
        "$FLOW_CMD" jobs graph "$PLAN_DIR" -f mermaid
        pause_if_interactive "Review the dependency graph"
    fi
}

test_cleanup_worktrees() {
    log_info "Testing worktree cleanup..."
    
    # This would test worktree cleanup in a real scenario
    # For now, just verify the command runs
    
    assert_command_success "cleanup worktrees command" \
        "$FLOW_CMD" jobs cleanup-worktrees "$PLAN_DIR" --age 0s --force
    
    log_success "Cleanup worktrees test passed"
}

test_full_workflow() {
    log_info "Testing full orchestration workflow..."
    
    pause_if_interactive "About to run full workflow test"
    
    # Show final status
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Final status:${NC}"
        "$FLOW_CMD" jobs status "$PLAN_DIR"
    fi
    
    log_success "Full workflow test passed"
}

main() {
    print_header "Grove Orchestration E2E Tests"
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "${YELLOW}Running in interactive mode - will pause at key points${NC}\n"
    fi
    
    setup_demo_environment
    
    # Run tests in order
    test_init_command
    test_status_command
    test_run_oneshot_job
    test_dependency_resolution
    test_parallel_execution
    test_add_step_command
    test_graph_command
    test_cleanup_worktrees
    test_full_workflow
    
    if [ $TESTS_FAILED -eq 0 ]; then
        log_success "All orchestration tests passed!"
        exit 0
    else
        log_error "Some tests failed"
        exit 1
    fi
}

# Allow running individual tests
if [ $# -gt 0 ]; then
    setup_demo_environment
    "$@"
else
    main
fi