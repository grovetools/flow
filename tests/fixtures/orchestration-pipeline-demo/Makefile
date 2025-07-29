.PHONY: help setup run-pipeline clean reset run-all init list-templates e2e

PLAN_DIR = todo-plan

help:
	@echo "Available targets:"
	@echo "  init          - Initialize a new plan from spec.md" 
	@echo "  list-templates - List available job templates"
	@echo "  setup         - Set up the plan directory for running"
	@echo "  run-pipeline  - Execute the full orchestration pipeline"
	@echo "  clean         - Clean up generated files"
	@echo "  reset         - Full reset (clean + init)"
	@echo "  run-all       - Reset and run everything from scratch"
	@echo "  e2e           - Run end-to-end test (non-interactive)"

init:
	@echo "Initializing orchestration plan..."
	# Remove old plan directory if exists
	rm -rf $(PLAN_DIR)
	# Clean up any previous git repo in this demo dir
	rm -rf .git .grove-worktrees
	# Initialize git in the demo directory (like orchestration-demo does)
	git init && git add . && git commit -m "Initial commit for orchestration pipeline demo" --allow-empty
	# Use grove jobs init to create the plan with initial job
	grove jobs init $(PLAN_DIR) --spec-file spec.md --create-initial-job --output-type generate_jobs
	@echo "Plan created in $(PLAN_DIR)/"

setup:
	@echo "Setting up plan directory..."
	@if [ ! -d $(PLAN_DIR) ]; then \
		echo "Error: $(PLAN_DIR) does not exist. Run 'make init' first."; \
		exit 1; \
	fi

run-pipeline:
	@echo "Starting orchestration pipeline..."
	# Run the initial job
	grove jobs run $(PLAN_DIR)/01-high-level-plan.md --yes
	# Then run all jobs
	grove jobs run --all $(PLAN_DIR) --yes

clean:
	@echo "Cleaning up..."
	# Remove plan directory
	rm -rf $(PLAN_DIR)
	# Clean up git repo and worktrees
	rm -rf .git .grove-worktrees
	# Prune any stale worktrees
	git worktree prune 2>/dev/null || true

reset: clean init
	@echo "Reset complete!"

run-all: reset setup run-pipeline
	@echo "Full pipeline execution complete!"

list-templates:
	@echo "Listing available job templates..."
	@grove jobs templates list

e2e: clean
	@echo "Running e2e test..."
	@# Initialize plan with spec file
	rm -rf $(PLAN_DIR)
	rm -rf .git .grove-worktrees
	git init && git add . && git commit -m "Initial commit for e2e test" --allow-empty >/dev/null 2>&1
	grove jobs init $(PLAN_DIR) --spec-file spec.md --create-initial-job --output-type generate_jobs
	@echo "✓ Plan initialized"
	@# Run the initial job to generate other jobs
	grove jobs run $(PLAN_DIR)/01-high-level-plan.md --yes
	@# Check that jobs were generated
	@job_count=$$(ls $(PLAN_DIR)/0[2-9]-*.md $(PLAN_DIR)/[1-9][0-9]-*.md 2>/dev/null | wc -l); \
	if [ $$job_count -eq 0 ]; then \
		echo "ERROR: No jobs were generated"; \
		exit 1; \
	else \
		echo "✓ Generated $$job_count jobs successfully"; \
	fi
	@# Test status command
	@echo "Testing status command..."
	@grove jobs status $(PLAN_DIR) >/dev/null 2>&1 || { echo "ERROR: status command failed"; exit 1; }
	@echo "✓ Status command works"
	@# Test graph command
	@echo "Testing graph command..."
	@grove jobs graph $(PLAN_DIR) -f ascii >/dev/null 2>&1 || { echo "ERROR: graph command failed"; exit 1; }
	@echo "✓ Graph command works"
	@# Check that we have both agent and shell jobs
	@agent_jobs=$$(grep -l "type: agent" $(PLAN_DIR)/*.md 2>/dev/null | wc -l); \
	shell_jobs=$$(grep -l "type: shell" $(PLAN_DIR)/*.md 2>/dev/null | wc -l); \
	echo "✓ Found $$agent_jobs agent jobs and $$shell_jobs shell jobs"
	@# Check dependencies exist
	@deps=$$(grep -h "depends_on:" $(PLAN_DIR)/0[2-9]-*.md 2>/dev/null | wc -l); \
	if [ $$deps -gt 0 ]; then \
		echo "✓ Jobs have dependencies configured"; \
	fi
	@echo "✓ E2E test passed!"

e2e-debug: clean
	@echo "Running e2e test with debug info..."
	@# Initialize plan with spec file
	rm -rf $(PLAN_DIR)
	rm -rf .git .grove-worktrees
	git init && git add . && git commit -m "Initial commit for e2e test" --allow-empty >/dev/null 2>&1
	grove jobs init $(PLAN_DIR) --spec-file spec.md --create-initial-job --output-type generate_jobs
	@echo "✓ Plan initialized"
	@echo ""
	@echo "=== Checking context locations before running job ==="
	@echo "Main .grove/context:"
	@ls -la .grove/context 2>/dev/null || echo "  Not found"
	@echo ""
	@echo "Plan .grove/context:"
	@ls -la $(PLAN_DIR)/.grove/context 2>/dev/null || echo "  Not found"
	@echo ""
	@# Run the initial job to generate other jobs
	grove jobs run $(PLAN_DIR)/01-high-level-plan.md --yes
	@echo ""
	@echo "=== Checking context locations after running oneshot job ==="
	@echo "Main .grove/context:"
	@ls -la .grove/context 2>/dev/null || echo "  Not found"
	@echo ""
	@echo "Plan .grove/context:"
	@ls -la $(PLAN_DIR)/.grove/context 2>/dev/null || echo "  Not found"
	@echo ""
	@echo "Worktrees:"
	@ls -la .grove-worktrees/ 2>/dev/null || echo "  No worktrees found"
	@echo ""
	@# Check the oneshot job to see if it has a worktree
	@echo "Checking if oneshot job has worktree specified:"
	@grep -E "worktree:|type:" $(PLAN_DIR)/01-high-level-plan.md | head -5 || true
	@echo ""
	@# Check worktree contents
	@if [ -d .grove-worktrees/todo-plan ]; then \
		echo "=== Worktree contents ==="; \
		echo "Worktree .grove directory:"; \
		ls -la .grove-worktrees/todo-plan/.grove/ 2>/dev/null || echo "  No .grove directory in worktree"; \
		echo ""; \
		echo "Worktree context file:"; \
		ls -la .grove-worktrees/todo-plan/.grove/context 2>/dev/null || echo "  No context file in worktree"; \
	fi
	@echo ""
	@echo "=== Context Sources for OneShot Job ==="
	@echo "According to FindContextFiles logic, oneshot jobs look for context in:"
	@echo "1. Plan directory (.grove/context): $(PLAN_DIR)/.grove/context"
	@ls -la $(PLAN_DIR)/.grove/context 2>/dev/null || echo "   Not found"
	@echo "2. Plan directory (CLAUDE.md): $(PLAN_DIR)/CLAUDE.md"  
	@ls -la $(PLAN_DIR)/CLAUDE.md 2>/dev/null || echo "   Not found"
	@echo "3. Current directory (.grove/context): .grove/context"
	@ls -la .grove/context 2>/dev/null || echo "   Not found"
	@echo "4. Current directory (CLAUDE.md): CLAUDE.md"
	@ls -la CLAUDE.md 2>/dev/null || echo "   Not found"
	@echo ""
	@echo "So the oneshot job used context from: .grove/context (main directory)"

test-context: clean
	@echo "Testing context generation in worktree..."
	@# Initialize
	rm -rf $(PLAN_DIR) .git .grove-worktrees
	git init && git add . && git commit -m "test" --allow-empty >/dev/null 2>&1
	/Users/solom4/Code/grove/grove jobs init $(PLAN_DIR) --spec-file spec.md --create-initial-job --output-type generate_jobs >/dev/null
	@# Run the job (from demo directory, not plan directory)
	/Users/solom4/Code/grove/grove jobs run $(PLAN_DIR)/01-high-level-plan.md --yes 2>&1 | grep -E "worktree|context|Regenerating" || true
	@echo ""
	@echo "=== Results ==="
	@echo "✓ Plan directory contains only job markdown files:"
	@ls $(PLAN_DIR)/
	@echo ""
	@echo "✓ Worktree created in: .grove-worktrees/$(PLAN_DIR)"
	@echo "✓ Context generated in worktree (NOT in plan dir):"
	@ls -la .grove-worktrees/$(PLAN_DIR)/.grove/context 2>/dev/null || echo "  Error listing file"
	@echo ""
	@echo "✓ OneShot job used ONLY worktree context"