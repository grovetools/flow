#!/bin/bash
set -uo pipefail

# Test script for Grove orchestration pipeline demo
# Updated to work with new CLI changes:
# - grove jobs init now defaults to no initial job (use --create-initial-job to create one)
# - spec file is now optional with --spec-file flag
# - output type can be specified during init

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
INTERACTIVE="${GROVE_TEST_STEP_THROUGH:-true}"

# Check if we're in an interactive terminal
if [ "$INTERACTIVE" = "true" ] && [ ! -t 0 ]; then
    echo -e "${YELLOW}Warning: GROVE_TEST_STEP_THROUGH=true but not running in interactive terminal${NC}"
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
        echo -e "${YELLOW}[STEP]${NC} $1"
    fi
}

# Set up Grove command
GROVE="${GROVE:-grove}"
DEMO_DIR="${SCRIPT_DIR}"
PLAN_DIR="${DEMO_DIR}"

cleanup() {
    log_info "Cleaning up pipeline demo..."
    if [ "$INTERACTIVE" != "true" ]; then
        rm -f 01-high-level-plan.md 0[2-9]-*.md [1-9][0-9]-*.md
        rm -rf .grove
        rm -rf .logs
        rm -rf .grove-worktrees
    fi
    print_test_summary
}
trap cleanup EXIT

setup_demo_environment() {
    log_info "Setting up pipeline demo environment..."
    
    # Clean up any previous runs
    rm -f 01-high-level-plan.md 0[2-9]-*.md [1-9][0-9]-*.md
    rm -rf .logs
    
    # Ensure demo dir is a git repo
    if [ ! -d .git ]; then
        git init && git add . && git commit -m "Initial commit for pipeline demo" --allow-empty >/dev/null 2>&1
    fi
    
    # Initialize context
    if [ ! -f .grovectx ]; then
        echo "*.md" > .grovectx
        echo "grove.yml" >> .grovectx
    fi
    
    "$GROVE" cx update
    "$GROVE" cx generate
    
    # Create initial job file if it doesn't exist
    if [ ! -f 01-high-level-plan.md ]; then
        log_info "Creating initial plan generation job..."
        # Initialize a temporary directory for the job
        TEMP_DIR="test-run-$$"
        "$GROVE" jobs init "$TEMP_DIR" --spec-file spec.md --create-initial-job --output-type generate_jobs
        # Copy the generated job to the current directory
        cp "$TEMP_DIR/01-high-level-plan.md" .
        # Clean up
        rm -rf "$TEMP_DIR"
    fi
    
    log_success "Environment setup complete"
}

test_initial_plan_generation() {
    print_header "Testing Initial Plan Generation"
    
    pause_if_interactive "About to run the initial planning job that will generate other jobs"
    
    # Run the initial job
    if ! "$GROVE" jobs run "$PLAN_DIR/01-high-level-plan.md" --yes; then
        log_error "Initial plan generation failed"
        return 1
    fi
    
    # Check if new job files were created
    local job_count=$(ls -1 0[2-9]-*.md 2>/dev/null | wc -l)
    if [ "$job_count" -eq 0 ]; then
        log_error "No job files were generated"
        return 1
    fi
    
    log_success "Generated $job_count new job files"
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Generated jobs:${NC}"
        ls -la 0[2-9]-*.md [1-9][0-9]-*.md 2>/dev/null || true
        pause_if_interactive "Review the generated job files"
    fi
}

test_job_status() {
    print_header "Testing Job Status"
    
    local output=$("$GROVE" jobs status "$PLAN_DIR" 2>&1)
    
    # Check for expected output
    if ! echo "$output" | grep -q "01-high-level-plan.md"; then
        log_error "Status output missing initial job"
        return 1
    fi
    
    # Should show the initial job as completed
    if ! echo "$output" | grep -i "01-high-level-plan.md.*completed"; then
        log_error "Initial job should be marked as completed"
        return 1
    fi
    
    log_success "Status command shows correct job states"
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Current job status:${NC}"
        "$GROVE" jobs status "$PLAN_DIR"
        pause_if_interactive "Review the job status"
    fi
}

test_dependency_graph() {
    print_header "Testing Dependency Graph"
    
    # Test ASCII output
    local output=$("$GROVE" jobs graph "$PLAN_DIR" -f ascii 2>&1)
    
    if ! echo "$output" | grep -q "Job Dependency Graph"; then
        log_error "Graph output missing ASCII header"
        return 1
    fi
    
    log_success "Dependency graph generation works"
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Dependency Graph (ASCII):${NC}"
        "$GROVE" jobs graph "$PLAN_DIR" -f ascii
        echo -e "\n${YELLOW}Dependency Graph (Mermaid):${NC}"
        "$GROVE" jobs graph "$PLAN_DIR" -f mermaid
        pause_if_interactive "Review the dependency graph showing job relationships"
    fi
}

test_shell_job_execution() {
    print_header "Testing Shell Job Execution"
    
    # Find a shell job (should be one that updates context)
    local shell_job=$(grep -l "type: shell" 0[2-9]-*.md 2>/dev/null | head -1)
    
    if [ -z "$shell_job" ]; then
        log_error "No shell jobs found in generated plan"
        return 1
    fi
    
    pause_if_interactive "About to run shell job: $shell_job"
    
    if ! "$GROVE" jobs run "$shell_job" --yes; then
        log_error "Shell job execution failed"
        return 1
    fi
    
    # Check if the job file was updated with output
    if ! grep -q "Output" "$shell_job"; then
        log_error "Shell job output not appended to job file"
        return 1
    fi
    
    log_success "Shell job executed successfully"
}

test_run_next_jobs() {
    print_header "Testing Run Next Command"
    
    pause_if_interactive "About to run all currently runnable jobs"
    
    # Run next runnable jobs
    if "$GROVE" jobs run --next "$PLAN_DIR" --yes; then
        log_success "Run next command executed"
    else
        # This might fail if no jobs are runnable, which is okay
        log_info "No runnable jobs found (this is okay)"
    fi
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Updated job status:${NC}"
        "$GROVE" jobs status "$PLAN_DIR"
        pause_if_interactive "Review updated job status"
    fi
}

test_full_pipeline() {
    print_header "Testing Full Pipeline Execution"
    
    pause_if_interactive "About to run the entire pipeline (this may take a while)"
    
    # Run all remaining jobs
    if [ "$INTERACTIVE" = "true" ]; then
        # In interactive mode, run step by step
        while true; do
            local runnable=$("$GROVE" jobs status "$PLAN_DIR" 2>&1 | grep -c "pending")
            if [ "$runnable" -eq 0 ]; then
                break
            fi
            
            echo -e "\n${YELLOW}Running next batch of jobs...${NC}"
            "$GROVE" jobs run --next "$PLAN_DIR" --yes || true
            
            echo -e "\n${YELLOW}Current status:${NC}"
            "$GROVE" jobs status "$PLAN_DIR"
            
            pause_if_interactive "Jobs executed. Review status before continuing"
        done
    else
        # In non-interactive mode, run all at once
        "$GROVE" jobs run --all "$PLAN_DIR" --yes || true
    fi
    
    # Check final status
    local completed=$("$GROVE" jobs status "$PLAN_DIR" 2>&1 | grep -c "completed")
    local total=$("$GROVE" jobs status "$PLAN_DIR" 2>&1 | grep -E "^\s*[0-9]+-" | wc -l)
    
    log_info "Completed $completed out of $total jobs"
    
    if [ "$completed" -gt 1 ]; then
        log_success "Pipeline execution completed"
    else
        log_error "Pipeline execution incomplete"
    fi
}

main() {
    print_header "Grove Orchestration Pipeline Demo"
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "${YELLOW}Running in interactive mode - will pause at key points${NC}"
        echo -e "${YELLOW}This demo will show:${NC}"
        echo "  1. Dynamic job generation from a spec"
        echo "  2. Shell jobs for context updates"
        echo "  3. Complex dependency management"
        echo "  4. Parallel job execution"
        echo ""
    fi
    
    setup_demo_environment
    
    # Run tests in order
    test_initial_plan_generation
    test_job_status
    test_dependency_graph
    test_shell_job_execution
    test_run_next_jobs
    test_full_pipeline
    
    if [ $TESTS_FAILED -eq 0 ]; then
        log_success "All pipeline tests passed!"
        
        if [ "$INTERACTIVE" = "true" ]; then
            echo -e "\n${YELLOW}Demo complete!${NC}"
            echo "The generated jobs created a TODO application based on the spec."
            echo "You can explore the generated job files and their outputs."
            pause_if_interactive "Demo finished"
        fi
        
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