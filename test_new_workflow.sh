#!/bin/bash
# Test script for the new flow plan init workflow

set -e

echo "Testing new flow plan init workflow..."

# Create a test directory
TEST_DIR="/tmp/grove-test-$$"
mkdir -p "$TEST_DIR"
cd "$TEST_DIR"

# Create a test markdown file to extract from
cat > test_issue.md << 'EOF'
---
title: Implement User Authentication
priority: high
---

# User Authentication Feature

We need to implement a user authentication system with the following requirements:

1. JWT-based authentication
2. User registration endpoint
3. Login endpoint
4. Password reset functionality
5. Session management

Please implement this feature following our standard patterns and include proper tests.
EOF

echo "Created test issue file: test_issue.md"
echo "Content:"
cat test_issue.md
echo ""

# Test the new command (dry run - won't actually launch tmux)
echo "Testing command: flow plan init auth-feature --with-worktree --extract-all-from test_issue.md"
echo "(Note: This is a dry run - tmux session won't actually launch)"

# Since we can't actually run flow from here, just show what would happen
echo ""
echo "Expected behavior:"
echo "1. Creates plan directory: auth-feature"
echo "2. Creates .grove-plan.yml with worktree: auth-feature"
echo "3. Extracts content from test_issue.md to new job: 01-test_issue.md"
echo "4. Would launch tmux session: <repo>__auth-feature (if --open-session was added)"

# Clean up
rm -rf "$TEST_DIR"

echo ""
echo "Test script complete. To actually test the workflow:"
echo "1. Build the flow command: go build ./cmd/flow"
echo "2. Run: ./flow plan init myfeature --with-worktree --extract-all-from myissue.md --open-session"