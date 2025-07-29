#!/bin/bash
# Quick verification that the demo is set up correctly

echo "Verifying orchestration chat demo setup..."

# Check required files
required_files=(
    "spec.md"
    "Makefile"
    "test-chat-pipeline.sh"
    "test-chat-pipeline-interactive.sh"
    "mocks/initial-plan-response.md"
    "mocks/refine-schema-response.md"
    "mocks/generate-jobs-response.json"
    "README.md"
)

all_good=true
for file in "${required_files[@]}"; do
    if [ -f "$file" ]; then
        echo "✓ $file"
    else
        echo "✗ $file is missing"
        all_good=false
    fi
done

# Check grove binary
if command -v grove &> /dev/null; then
    echo "✓ grove is in PATH"
elif [ -x "../../grove" ]; then
    echo "✓ grove binary found at ../../grove"
else
    echo "✗ grove binary not found"
    all_good=false
fi

# Check templates
echo ""
echo "Checking for required templates..."
if [ -f "../../internal/orchestration/builtin_templates/chat.md" ]; then
    echo "✓ chat template"
else
    echo "✗ chat template missing"
    all_good=false
fi

if [ -f "../../internal/orchestration/builtin_templates/refine-plan-generic.md" ]; then
    echo "✓ refine-plan-generic template"
else
    echo "✗ refine-plan-generic template missing"
    all_good=false
fi

if [ -f "../../internal/orchestration/builtin_templates/generate-plan.md" ]; then
    echo "✓ generate-plan template"
else
    echo "✗ generate-plan template missing"
    all_good=false
fi

echo ""
if $all_good; then
    echo "✅ All demo files are in place!"
    echo ""
    echo "You can now run:"
    echo "  make demo              # Automated demo"
    echo "  make demo-interactive  # Interactive demo with step-by-step guidance"
else
    echo "❌ Some files are missing. Please check the setup."
    exit 1
fi