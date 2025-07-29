.PHONY: demo demo-quick test test-interactive test-ci clean setup help

GROVE ?= ../../grove
PLAN_DIR = my-feature-plan

setup:
	@if [ ! -d .git ]; then \
		git init && git add . && git commit -m "Initial commit"; \
	fi

demo: clean setup
	@echo "=== Grove Orchestration Demo ==="
	@echo "Initializing orchestration plan..."
	$(GROVE) jobs init spec.md $(PLAN_DIR)
	@echo "\nShowing initial status..."
	$(GROVE) jobs status $(PLAN_DIR)
	@echo "\nRunning planning job..."
	$(GROVE) jobs run $(PLAN_DIR)/01-high-level-plan.md
	@echo "\nShowing updated status..."
	$(GROVE) jobs status $(PLAN_DIR)
	@echo "\nRunning all jobs..."
	$(GROVE) jobs run --all $(PLAN_DIR)
	@echo "\nDemo complete! Check source-code/main.go for changes."

demo-quick: clean setup
	@echo "=== Quick Demo (non-interactive) ==="
	$(GROVE) jobs init spec.md $(PLAN_DIR)
	$(GROVE) jobs run --all --yes $(PLAN_DIR)

test: clean setup
	../tests/orchestration/test-orchestration-e2e.sh

test-interactive: clean setup
	@echo "=== Running E2E Test in Interactive Mode ==="
	@echo "Test will pause at key points for inspection"
	@echo
	GROVE_TEST_STEP_THROUGH=true ../tests/orchestration/test-orchestration-e2e.sh

test-ci: clean setup
	@echo "=== Running E2E Test in CI Mode ==="
	@echo "Test will run without pauses"
	@echo
	GROVE_TEST_STEP_THROUGH=false ../tests/orchestration/test-orchestration-e2e.sh

clean:
	rm -rf $(PLAN_DIR)
	rm -rf *-worktrees
	git worktree prune

help:
	@echo "Grove Orchestration Demo - Available Commands:"
	@echo
	@echo "  make demo              - Run the full orchestration demo"
	@echo "  make demo-quick        - Run demo without confirmations (--yes flag)"
	@echo "  make test              - Run E2E test (non-interactive by default)"
	@echo "  make test-interactive  - Run E2E test with pauses for inspection"
	@echo "  make test-ci           - Run E2E test in CI mode (no pauses)"
	@echo "  make clean             - Remove all generated files and worktrees"
	@echo "  make setup             - Initialize git repository if needed"
	@echo "  make help              - Show this help message"
	@echo
	@echo "Examples:"
	@echo "  make test-interactive  # Step through the test with breakpoints"
	@echo "  make test-ci          # Run quickly for automated testing"
	@echo "  make demo             # See the orchestration feature in action"