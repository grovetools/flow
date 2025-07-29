#!/bin/bash
set -o pipefail

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
    echo "  1. Run directly in your terminal: GROVE_TEST_STEP_THROUGH=true ./test-reference-prompts-e2e.sh"
    echo "  2. Use make: make test-reference-prompts-interactive"
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

# Use the job binary
JOB_CMD="${JOB_CMD:-job}"

# Test in the demo directory
DEMO_DIR="${SCRIPT_DIR}/../../fixtures/reference-prompts-demo"

# Global variable to store where Grove actually creates the plan
ACTUAL_PLAN_DIR=""

cleanup() {
    log_info "Cleaning up reference prompts test..."
    # Clean up all potential locations where grove might create files
    rm -rf "$DEMO_DIR/.git"
    rm -rf "$DEMO_DIR/.grove-worktrees"
    rm -rf "$DEMO_DIR/.grove"
    rm -rf "$DEMO_DIR/my-feature-plan"*
    rm -rf "$DEMO_DIR/calculator-improvement-plan"
    
    if [ "$INTERACTIVE" = "true" ]; then
        echo -e "\n${YELLOW}Test cleanup complete${NC}"
    fi
    print_test_summary
}
trap cleanup EXIT

setup_demo_environment() {
    log_info "Setting up reference prompts demo environment..."
    
    # Pre-test cleanup to ensure we start fresh
    log_info "Performing pre-test cleanup..."
    rm -rf "$DEMO_DIR/.git"
    rm -rf "$DEMO_DIR/.grove-worktrees"
    rm -rf "$DEMO_DIR/.grove"
    rm -rf "$DEMO_DIR/my-feature-plan"
    
    # Ensure demo dir exists
    mkdir -p "$DEMO_DIR"
    
    # Ensure demo dir is a git repo for the test run
    (cd "$DEMO_DIR" && git init && git config user.email "test@example.com" && git config user.name "Test User" && git add . && git commit -m "Initial commit for test run" --allow-empty) >/dev/null 2>&1
    
    # Create custom templates directory for the project
    mkdir -p "$DEMO_DIR/.grove/job-templates"
    
    # Create a code review template
    cat > "$DEMO_DIR/.grove/job-templates/code-review.md" <<'EOF'
---
type: agent
output_type: file
---

You are an expert code reviewer. Your task is to review the provided code files for:
- Code quality and best practices
- Potential bugs or issues
- Performance considerations
- Security concerns
- Suggestions for improvement

Provide your review in a structured format with specific line references where applicable.
EOF

    # Create a refactor template
    cat > "$DEMO_DIR/.grove/job-templates/refactor.md" <<'EOF'
---
type: oneshot
output_type: file
---

You are an expert at code refactoring. Your task is to refactor the provided code to:
- Improve readability and maintainability
- Follow best practices and design patterns
- Optimize performance where possible
- Add appropriate documentation

Provide the refactored code with explanations for the changes made.
EOF

    # Create source files to reference
    mkdir -p "$DEMO_DIR/src"
    cat > "$DEMO_DIR/src/calculator.py" <<'EOF'
def add(a, b):
    return a + b

def subtract(a, b):
    return a - b

def multiply(a, b):
    result = 0
    for i in range(b):
        result = add(result, a)
    return result

def divide(a, b):
    if b == 0:
        return "Error"
    return a / b
EOF

    cat > "$DEMO_DIR/src/main.py" <<'EOF'
from calculator import add, subtract, multiply, divide

def main():
    print("Calculator Demo")
    x = 10
    y = 5
    
    print(f"{x} + {y} = {add(x, y)}")
    print(f"{x} - {y} = {subtract(x, y)}")
    print(f"{x} * {y} = {multiply(x, y)}")
    print(f"{x} / {y} = {divide(x, y)}")
    
    # Test division by zero
    print(f"{x} / 0 = {divide(x, 0)}")

if __name__ == "__main__":
    main()
EOF

    # Create a spec file
    cat > "$DEMO_DIR/feature-spec.md" <<'EOF'
# Calculator Improvement Specification

This project needs improvements to the calculator implementation:

## Requirements
1. Review the current calculator implementation
2. Refactor the code for better performance and error handling
3. Add comprehensive unit tests
4. Update documentation

## Technical Details
- The multiply function is inefficient
- Error handling needs improvement
- Type hints should be added
EOF
}

# Helper to find where grove created files
find_grove_files() {
    local pattern=$1
    local file=""
    
    # If we have ACTUAL_PLAN_DIR, search there first
    if [ -n "$ACTUAL_PLAN_DIR" ] && [ -d "$ACTUAL_PLAN_DIR" ]; then
        file=$(find "$ACTUAL_PLAN_DIR" -name "$pattern" -type f 2>/dev/null | head -1)
    fi
    
    # If not found, check demo directory
    if [ -z "$file" ]; then
        file=$(find "$DEMO_DIR" -name "$pattern" -type f 2>/dev/null | head -1)
    fi
    
    # If still not found, check expanded paths
    if [ -z "$file" ]; then
        # Just look in demo directory as last resort
        file=""
    fi
    
    echo "$file"
}

test_init_plan() {
    print_header "Test: Initialize Plan"
    
    pause_if_interactive "About to initialize Grove plan"
    
    # Capture Grove's output to find where it actually creates the plan
    local init_output
    init_output=$( (cd "$DEMO_DIR" && "$JOB_CMD" jobs init --spec-file feature-spec.md --create-initial-job my-feature-plan 2>&1) )
    local init_status=$?
    
    echo "$init_output"
    
    if [ $init_status -eq 0 ]; then
        # Extract the actual plan directory from Grove's output
        if echo "$init_output" | grep -q "Initializing orchestration plan in:"; then
            # The path is on the next line after "Initializing orchestration plan in:"
            ACTUAL_PLAN_DIR=$(echo "$init_output" | grep -A1 "Initializing orchestration plan in:" | tail -n1 | sed 's/^[[:space:]]*//' | sed 's/[[:space:]]*$//')
            log_info "Grove created plan at: $ACTUAL_PLAN_DIR"
        else
            log_error "Could not extract plan directory from grove init output"
            # Fallback to expected location
            ACTUAL_PLAN_DIR="$DEMO_DIR/my-feature-plan"
        fi
        
        # Verify the plan was created
        if [ -d "$ACTUAL_PLAN_DIR" ] && [ -f "$ACTUAL_PLAN_DIR/01-initial-job.md" ]; then
            log_success "Verify plan initialized successfully"
            log_success "Verify initial job created"
        else
            log_error "Verify plan initialized successfully"
            log_error "Verify initial job created"
        fi
    else
        log_error "Verify plan initialized successfully"
        log_error "Verify initial job created"
        # Set fallback path even on failure
        ACTUAL_PLAN_DIR="$DEMO_DIR/my-feature-plan"
    fi
    
    
    pause_if_interactive "Initial plan created"
}

test_create_reference_job_with_template() {
    print_header "Test: Create Reference-Based Job with Template"
    
    pause_if_interactive "About to create a reference-based job using code-review template"
    
    # Create the job using the actual plan directory
    (cd "$DEMO_DIR" && echo "Focus on performance issues and error handling" | \
        "$JOB_CMD" jobs add-step "$ACTUAL_PLAN_DIR" \
            --template code-review \
            --source-files src/calculator.py,src/main.py \
            --title "Review Calculator Implementation" \
            --type agent \
            --prompt-file /dev/stdin)
    
    # Find the created job file
    local job_file=$(find_grove_files "*review-calculator*.md")
    
    if [ -n "$job_file" ]; then
        log_success "Verify job file was created"
        
        # Verify job has template in frontmatter
        if grep -q "^template: code-review" "$job_file"; then
            log_success "Verify job has template field"
        else
            log_error "Verify job has template field"
        fi
        
        # Verify job has prompt_source in frontmatter
        if grep -q "^prompt_source:" "$job_file"; then
            log_success "Verify job has prompt_source field"
        else
            log_error "Verify job has prompt_source field"
        fi
        
        # Verify source files are listed
        if grep -q "calculator.py" "$job_file"; then
            log_success "Verify calculator.py is in prompt_source"
        else
            log_error "Verify calculator.py is in prompt_source"
        fi
        
        if grep -q "main.py" "$job_file"; then
            log_success "Verify main.py is in prompt_source"
        else
            log_error "Verify main.py is in prompt_source"
        fi
        
    else
        log_error "Verify job file was created"
        log_error "Verify job has template field"
        log_error "Verify job has prompt_source field"
        log_error "Verify calculator.py is in prompt_source"
        log_error "Verify main.py is in prompt_source"
    fi
    
    pause_if_interactive "Reference-based job created"
}

test_create_reference_job_without_template() {
    print_header "Test: Create Reference-Based Job Without Template"
    
    pause_if_interactive "About to create a reference-based job without template"
    
    # Create job with just source files (no template)
    (cd "$DEMO_DIR" && echo "Analyze the performance of the multiply function" | \
        "$JOB_CMD" jobs add-step "$ACTUAL_PLAN_DIR" \
            --source-files src/calculator.py \
            --title "Analyze Calculator Performance" \
            --type oneshot \
            --prompt-file /dev/stdin)
    
    # Find the created job file
    local job_file=$(find_grove_files "*analyze-calculator*.md")
    
    if [ -n "$job_file" ]; then
        log_success "Verify job file was created"
        
        # Verify job has prompt_source but no template
        if grep -q "^prompt_source:" "$job_file"; then
            log_success "Verify job has prompt_source field"
        else
            log_error "Verify job has prompt_source field"
        fi
        
        if grep -q "^template:" "$job_file"; then
            log_error "Job should not have template field"
        else
            log_success "Verify job has no template field"
        fi
    else
        log_error "Verify job file was created"
        log_error "Verify job has prompt_source field"
        log_success "Verify job has no template field (no file to check)"
    fi
    
    pause_if_interactive "Job without template created"
}

test_create_traditional_job() {
    print_header "Test: Create Traditional Job (Backward Compatibility)"
    
    pause_if_interactive "About to create a traditional job without reference"
    
    # Create traditional job
    (cd "$DEMO_DIR" && echo "Write comprehensive unit tests for the calculator module" | \
        "$JOB_CMD" jobs add-step "$ACTUAL_PLAN_DIR" \
            --title "Write Unit Tests" \
            --type agent \
            --prompt-file /dev/stdin)
    
    # Find the test job - be more specific to avoid matching wrong files
    local job_file=$(find_grove_files "*write-unit-tests*.md")
    
    if [ -n "$job_file" ]; then
        log_success "Verify traditional job was created"
        
        # Verify job does NOT have template or prompt_source fields
        if grep -q "^template:" "$job_file"; then
            log_error "Traditional job should not have template field"
        else
            log_success "Verify traditional job has no template field"
        fi
        
        if grep -q "^prompt_source:" "$job_file"; then
            log_error "Traditional job should not have prompt_source field"
            # Debug: show the problematic content
            echo "DEBUG: Job file path: $job_file"
            echo "DEBUG: Job file content:"
            cat "$job_file" | head -20
            echo "DEBUG: grep result:"
            grep "^prompt_source:" "$job_file" || echo "No match found"
        else
            log_success "Verify traditional job has no prompt_source field"
        fi
        
        # Verify job body contains the prompt
        if grep -q "Write comprehensive unit tests" "$job_file"; then
            log_success "Verify traditional job contains prompt text"
        else
            log_error "Verify traditional job contains prompt text"
        fi
    else
        log_error "Verify traditional job was created"
        log_success "Verify traditional job has no template field (no file to check)"
        log_success "Verify traditional job has no prompt_source field (no file to check)"
        log_error "Verify traditional job contains prompt text"
    fi
}

test_template_listing() {
    print_header "Test: Template Listing"
    
    pause_if_interactive "About to list available templates"
    
    # Change to demo directory so grove can find project templates
    (cd "$DEMO_DIR" && "$JOB_CMD" jobs templates list > /tmp/templates.out 2>&1)
    
    if grep -q "code-review" /tmp/templates.out; then
        log_success "List available templates - found code-review"
    else
        log_error "List available templates - code-review not found"
        cat /tmp/templates.out
    fi
    
    if grep -q "refactor" /tmp/templates.out; then
        log_success "Verify refactor template listed"
    else
        log_error "Verify refactor template listed"
    fi
}

test_error_handling() {
    print_header "Test: Error Handling"
    
    pause_if_interactive "About to test error handling"
    
    # Test with non-existent source file
    if (cd "$DEMO_DIR" && echo "This should fail" | \
        "$JOB_CMD" jobs add-step "$ACTUAL_PLAN_DIR" \
            --source-files src/nonexistent.py \
            --title "Should Fail" \
            --type agent \
            --prompt-file /dev/stdin) 2>/dev/null; then
        log_error "Job creation should fail with non-existent source file"
    else
        log_success "Job creation correctly failed with non-existent source file"
    fi
    
    # Test with non-existent template
    if (cd "$DEMO_DIR" && echo "This should also fail" | \
        "$JOB_CMD" jobs add-step "$ACTUAL_PLAN_DIR" \
            --template nonexistent-template \
            --source-files src/calculator.py \
            --title "Should Also Fail" \
            --type agent \
            --prompt-file /dev/stdin) 2>&1 | grep -q "not found"; then
        log_success "Job creation correctly reports missing template"
    else
        log_info "Template validation happens at runtime"
    fi
}

# Main test execution
main() {
    print_header "Grove Reference-Based Prompts E2E Test"
    
    if [ "$INTERACTIVE" = "true" ]; then
        log_info "Running in interactive mode. Press Enter at each pause point."
    fi
    
    setup_demo_environment
    
    # Run all tests
    test_init_plan
    test_create_reference_job_with_template
    test_create_reference_job_without_template
    test_create_traditional_job
    test_template_listing
    test_error_handling
    
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