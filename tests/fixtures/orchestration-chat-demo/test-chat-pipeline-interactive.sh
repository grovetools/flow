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

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to display a step and wait for user
step() {
    echo
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${GREEN}STEP $1:${NC} $2"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo
    if [ -n "$3" ]; then
        echo -e "${YELLOW}$3${NC}"
        echo
    fi
    read -p "Press Enter to continue..."
}

# Function to show file content
show_file() {
    echo
    echo -e "${BLUE}File: $1${NC}"
    echo "────────────────────────────────────────────────────────────────────────────────"
    if [ -f "$1" ]; then
        head -n 50 "$1"
        if [ $(wc -l < "$1") -gt 50 ]; then
            echo
            echo "... (truncated, showing first 50 lines)"
        fi
    else
        echo "(File not found)"
    fi
    echo "────────────────────────────────────────────────────────────────────────────────"
    echo
}

echo -e "${GREEN}╔════════════════════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║              Grove Orchestration Chat Demo - Interactive Mode                  ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════════════════════════╝${NC}"
echo
echo "This demo shows Grove's conversational plan refinement feature."
echo "You'll see how to iteratively develop implementation plans through chat."
echo

# Clean up any previous runs
rm -rf user-profile-api

step 1 "Initialize Chat-Based Plan" \
    "We'll create a new plan using the 'chat' template.\nThis creates a single plan.md file instead of traditional job files."

echo "Running: $GROVE jobs init user-profile-api --spec-file spec.md --template chat"
$GROVE jobs init user-profile-api --spec-file spec.md --template chat

show_file "user-profile-api/plan.md"

step 2 "Generate Initial Plan" \
    "Now we'll run the chat job to generate the initial plan.\nThe LLM will read the spec and create a comprehensive plan."

export GROVE_MOCK_LLM_RESPONSE_FILE="../mocks/initial-plan-response.md"
cd user-profile-api
echo "Running: $GROVE jobs run plan.md"
$GROVE jobs run plan.md
cd ..

show_file "user-profile-api/plan.md"

step 3 "Add User Feedback" \
    "Now we'll add user feedback to refine the plan.\nNotice the directive comment that specifies which template to use."

cat >> user-profile-api/plan.md << 'EOF'

---

<!-- grove: {"template": "refine-plan-generic"} -->
> Can you provide more detail on the database schema? I'm particularly concerned about:
> - How we'll handle user roles and permissions
> - The relationship between users and their preferences
> - Indexes for performance on common queries

EOF

echo "Added user feedback to plan.md:"
tail -n 10 user-profile-api/plan.md

step 4 "Run Refinement" \
    "The LLM will now respond to our feedback using the 'refine-plan-generic' template.\nThis template can handle various types of refinement questions."

export GROVE_MOCK_LLM_RESPONSE_FILE="../mocks/refine-schema-response.md"
cd user-profile-api
echo "Running: $GROVE jobs run plan.md"
$GROVE jobs run plan.md
cd ..

echo "Showing the database schema response (first 100 lines):"
echo "────────────────────────────────────────────────────────────────────────────────"
tail -n +150 user-profile-api/plan.md | head -n 100
echo "────────────────────────────────────────────────────────────────────────────────"

step 5 "Add Final Approval" \
    "Let's add our approval and prepare to generate executable jobs."

cat >> user-profile-api/plan.md << 'EOF'

---

> This looks great! I like the addition of the roles table and the indexing strategy. 
> Please proceed with generating the implementation jobs.

EOF

echo "Added approval to plan.md"

step 6 "Create Job Generation Task" \
    "Now we'll create a new job that will transform our chat into executable jobs.\nThis uses the 'generate-plan' template."

cd user-profile-api
echo "Running: $GROVE jobs add-step . --title 'Generate Implementation Jobs' --template generate-plan --prompt-file plan.md"
$GROVE jobs add-step . --title "Generate Implementation Jobs" --template generate-plan --prompt-file plan.md
cd ..

show_file "user-profile-api/01-generate-implementation-jobs.md"

step 7 "Generate Executable Jobs" \
    "This step transforms our conversational plan into concrete job files."

export GROVE_MOCK_LLM_RESPONSE_FILE="../mocks/generate-jobs-response.json"
cd user-profile-api
echo "Running: $GROVE jobs run 01-generate-implementation-jobs.md"
$GROVE jobs run 01-generate-implementation-jobs.md || true  # Allow failure for generate_jobs in mock mode
cd ..

step 8 "View Final Plan Status" \
    "Let's see the complete plan with all generated jobs."

cd user-profile-api
echo "Running: $GROVE jobs status ."
$GROVE jobs status .
cd ..

# List generated job files
echo
echo -e "${BLUE}Generated job files:${NC}"
ls -la user-profile-api/*.md 2>/dev/null | grep -v plan.md || echo "No additional jobs generated (mock mode)"

echo
echo -e "${GREEN}╔════════════════════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║                            Demo Complete!                                      ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════════════════════════════════════╝${NC}"
echo
echo "Key Concepts Demonstrated:"
echo "1. Chat-based planning with plan.md files"
echo "2. Directives to guide the conversation"
echo "3. Specialized templates for different refinement needs"
echo "4. Transformation from chat to executable jobs"
echo
echo "Next Steps:"
echo "- Try adding your own feedback to the plan.md file"
echo "- Experiment with different directive templates"
echo "- Run '$GROVE jobs run --all' to execute the implementation (requires real LLM)"
echo