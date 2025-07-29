# Orchestration E2E Tests

This directory contains end-to-end tests for the Grove orchestration system. The tests follow the repository convention of being located under `demos/tests/`.

## Running Tests

### Standard Mode
```bash
./test-orchestration-e2e.sh
```

### Interactive Mode
The interactive mode allows you to step through the test execution, pausing at key points to inspect the state.

#### Method 1: Direct execution in terminal
```bash
GROVE_TEST_STEP_THROUGH=true ./test-orchestration-e2e.sh
```

#### Method 2: Using the wrapper script
```bash
./run-interactive.sh
```

#### Method 3: Using Make (from project root)
```bash
make test-orchestration-interactive
```

## Interactive Mode Requirements

The interactive mode requires:
1. Running in an interactive terminal (TTY)
2. The `GROVE_TEST_STEP_THROUGH=true` environment variable

If you see the warning "not running in interactive terminal", try:
- Running directly in your terminal (not through a pipe or script)
- Using the `run-interactive.sh` wrapper which uses `script` to create a PTY
- SSHing into the machine with TTY allocation (`ssh -t`)

## Mock Agent vs. Real Agent Testing

This test suite supports two primary modes of operation, controlled by the `grove.yml` configuration in the `demos/orchestration-demo` directory.

### Mock Agent Mode (Default for Local Demo)

-   **Purpose:** To test the orchestration logic (`jobs` commands, dependency resolution, worktree creation) without requiring a live LLM or API keys.
-   **Mechanism:** The `grove.yml` is configured to use a simple Alpine image that runs `entrypoint.sh`. This script creates a *mock* `grove` binary inside the container (`mock-grove-agent.sh`).
-   **Behavior:** When a job runs, this mock agent simulates a successful execution by creating placeholder files (`implementation.py`, `test_implementation.py`) instead of making real code changes. This allows the E2E test to verify the complete orchestration flow.

### Real Agent Mode (For Integration Testing)

-   **Purpose:** To perform a true end-to-end test with a live LLM agent.
-   **Mechanism:** To enable this mode, modify `demos/orchestration-demo/grove.yml` to use the actual `grove-claude-agent` image:

    ```yaml
    agent:
        enabled: true
        image: grove-claude-agent:latest # Use the real agent image
        # ... add necessary args like --dangerously-skip-permissions
    ```
-   **Behavior:** The orchestrator will start a real agent container, which will require valid Claude credentials to be available. The agent will then process the job prompts and attempt to make real code changes.

## Test Coverage

The E2E test covers:
- Plan initialization (`grove jobs init`)
- Job status viewing (`grove jobs status`)
- Oneshot job execution
- Agent job execution with worktrees
- Dependency resolution
- Parallel job execution
- Add-step command
- Graph visualization
- Worktree cleanup
- Full workflow execution

## Troubleshooting

### Binary Format Error
If you see "cannot execute binary file: Exec format error":
1. The grove binary was compiled for a different architecture
2. Rebuild it: `cd ../../.. && go build -o grove cmd/grove/main.go`

### Interactive Mode Not Working
If pauses are skipped:
1. Check you're in an interactive terminal: `[ -t 0 ] && echo "Interactive" || echo "Non-interactive"`
2. Use the wrapper script: `./run-interactive.sh`
3. Run directly (not through make or pipes)