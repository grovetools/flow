.PHONY: init chat-turn transform run-jobs clean demo demo-interactive

# Initialize the chat-based plan
init:
	grove jobs init user-profile-api --spec-file spec.md --template chat

# Add a user turn to the chat (requires manual editing of plan.md)
chat-turn:
	@echo "Please manually add your feedback to user-profile-api/plan.md"
	@echo "Format: "
	@echo "---"
	@echo ""
	@echo '<!-- grove: {"template": "refine-plan-generic"} -->'
	@echo "> Your feedback here"
	@echo ""

# Run the chat to get LLM response
chat:
	cd user-profile-api && grove jobs run plan.md

# Transform the chat into executable jobs
transform:
	cd user-profile-api && grove jobs add-step . --template generate-plan --prompt-file plan.md

# Run all generated jobs
run-jobs:
	cd user-profile-api && grove jobs run --all --yes

# Clean up
clean:
	rm -rf user-profile-api

# Run the full demo with mocked responses (automated)
demo: clean
	@echo "=== Running Orchestration Chat Demo ==="
	./test-chat-pipeline.sh

# Run the interactive demo with step-by-step guidance
demo-interactive: clean
	@echo "=== Running Interactive Orchestration Chat Demo ==="
	./test-chat-pipeline-interactive.sh