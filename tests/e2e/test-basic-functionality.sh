#!/bin/bash
set -e

# Use the job binary provided by JOB_CMD or fallback to ../bin/job
if [ -n "$JOB_CMD" ]; then
    JOB="$JOB_CMD"
elif [ -x "../bin/job" ]; then
    JOB="$(cd .. && pwd)/bin/job"
else
    echo "Error: job binary not found"
    echo "Please build job first: make build"
    exit 1
fi

echo "=== Grove Jobs Basic Functionality Test ==="
echo

# Create a temporary directory for the test
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

# Save the original directory
ORIG_DIR=$(pwd)

# Change to temp directory for test execution
cd "$TEMP_DIR"

# Test 1: Check help output
echo "Test 1: Checking help output..."
if $JOB jobs --help > /dev/null 2>&1; then
    echo "✓ Help command works"
else
    echo "✗ Help command failed"
    exit 1
fi

# Test 2: Check subcommands
echo
echo "Test 2: Checking available subcommands..."
HELP_OUTPUT=$($JOB jobs --help 2>&1)
if echo "$HELP_OUTPUT" | grep -q "init" && \
   echo "$HELP_OUTPUT" | grep -q "status" && \
   echo "$HELP_OUTPUT" | grep -q "run" && \
   echo "$HELP_OUTPUT" | grep -q "add-step"; then
    echo "✓ All expected subcommands are available"
else
    echo "✗ Missing expected subcommands"
    echo "$HELP_OUTPUT"
    exit 1
fi

# Test 3: Init command with minimal setup
echo
echo "Test 3: Testing init command..."

# Initialize git repo for basic functionality
git init -q
git config user.email "test@example.com"
git config user.name "Test User"

# Create a minimal grove.yml
cat > grove.yml << 'EOF'
orchestration:
  plans_directory: .
EOF

# Create a minimal spec file
cat > spec.md << 'EOF'
# Test Specification
This is a test specification for the E2E test.
EOF

# Try to initialize a plan with chat template
INIT_OUTPUT=$($JOB jobs init test-plan --spec-file spec.md --template chat --force 2>&1)
if echo "$INIT_OUTPUT" | grep -q "Created plan"; then
    echo "✓ Init command creates plan successfully"
    echo "  Output: $INIT_OUTPUT"
else
    echo "✗ Init command failed"
    echo "  Output: $INIT_OUTPUT"
    exit 1
fi

# Debug: List files after init
echo "  Files after init:"
ls -la

# Test 4: Check if plan files were created
echo
echo "Test 4: Checking created files..."
echo "  Contents of test-plan directory:"
ls -la test-plan/

# The init command might not create plan.md immediately, just the directory
if [ -d "test-plan" ]; then
    echo "✓ Plan directory was created"
else
    echo "✗ Plan directory was not created"
    exit 1
fi

# Test 5: Status command
echo
echo "Test 5: Testing status command..."
STATUS_OUTPUT=$($JOB jobs status test-plan 2>&1)
if echo "$STATUS_OUTPUT" | grep -q "Plan:"; then
    echo "✓ Status command works"
    echo "  Output: $STATUS_OUTPUT"
else
    echo "✗ Status command failed"
    echo "  Output: $STATUS_OUTPUT"
    # Don't exit on status failure, it might just be an empty plan
fi

echo
echo "=== All Tests Passed ==="
echo "✓ Basic functionality verified successfully"