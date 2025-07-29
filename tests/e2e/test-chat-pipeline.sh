#!/bin/bash
set -e

# Find grove binary or use go run
if command -v grove &> /dev/null; then
    GROVE="grove"
elif [ -x "../../grove" ]; then
    GROVE="$(cd ../.. && pwd)/grove"
elif [ -x "../../../grove" ]; then
    GROVE="$(cd ../../.. && pwd)/grove"
elif [ -f "../../cmd/grove/main.go" ]; then
    GROVE="go run $(cd ../.. && pwd)/cmd/grove/main.go"
else
    echo "Error: grove binary not found and cannot find main.go"
    echo "Please build grove first: go build -o grove cmd/grove/main.go"
    exit 1
fi

echo "=== Grove Orchestration Chat Demo ==="
echo

# Clean up any previous runs
rm -rf user-profile-api

# Step 1: Initialize with chat template
echo "Step 1: Initializing chat-based plan..."
$GROVE jobs init ./user-profile-api --spec-file spec.md --template chat

# Step 2: Run initial plan generation
echo -e "\nStep 2: Generating initial plan..."
export GROVE_MOCK_LLM_RESPONSE_FILE="$(pwd)/mocks/initial-plan-response.md"
cd user-profile-api
$GROVE jobs run plan.md --yes
cd ..

# Step 3: Add user feedback
echo -e "\nStep 3: Adding user feedback..."
cat >> user-profile-api/plan.md << 'EOF'

---

<!-- grove: {"template": "refine-plan-generic"} -->
> Can you provide more detail on the database schema? I'm particularly concerned about:
> - How we'll handle user roles and permissions
> - The relationship between users and their preferences
> - Indexes for performance on common queries

EOF

# Step 4: Run refinement
echo -e "\nStep 4: Refining plan based on feedback..."
export GROVE_MOCK_LLM_RESPONSE_FILE="$(pwd)/mocks/refine-schema-response.md"
cd user-profile-api
$GROVE jobs run plan.md --yes
cd ..

# Step 5: Add final user approval
echo -e "\nStep 5: Adding final approval..."
cat >> user-profile-api/plan.md << 'EOF'

---

> This looks great! I like the addition of the roles table and the indexing strategy. 
> Please proceed with generating the implementation jobs.

EOF

# Step 6: Generate jobs from chat
echo -e "\nStep 6: Transforming chat into executable jobs..."
export GROVE_MOCK_LLM_RESPONSE_FILE="$(pwd)/mocks/generate-jobs-response.json"
cd user-profile-api
$GROVE jobs add-step . --title "Generate Implementation Jobs" --template generate-plan --prompt-file plan.md
$GROVE jobs run 01-generate-implementation-jobs.md --yes
cd ..

# Step 7: Show final status
echo -e "\nStep 7: Final plan status..."
cd user-profile-api
$GROVE jobs status .
cd ..

echo -e "\n=== Demo Complete ==="

# Verify results
if [ -d "user-profile-api" ]; then
    echo "✓ Demo completed successfully"
    echo
    echo "Files created:"
    ls -la user-profile-api/
    echo
    echo "The chat-based planning process has:"
    echo "1. Created an initial plan from the spec"
    echo "2. Refined it based on user feedback about database schema"
    echo "3. Generated executable job files"
    echo
    echo "You can now run 'cd user-profile-api && $GROVE jobs run --all' to execute the implementation."
else
    echo "✗ Demo failed - no output directory created"
    exit 1
fi