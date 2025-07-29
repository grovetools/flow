# Grove Orchestration Demo

This demo showcases Grove's AI-driven orchestration feature by implementing a simple health check endpoint.

## Prerequisites
- Grove CLI installed
- Git initialized in this directory

## Quick Start

```bash
# Initialize git if needed
git init && git add . && git commit -m "Initial commit"

# Run the demo
make demo
```

## Manual Steps

1. Initialize the orchestration plan:
   ```bash
   grove jobs init spec.md my-feature-plan
   ```

2. Check the plan status:
   ```bash
   grove jobs status my-feature-plan
   ```

3. Run the initial planning job:
   ```bash
   grove jobs run my-feature-plan/01-high-level-plan.md
   ```

4. Run all remaining jobs:
   ```bash
   grove jobs run --all my-feature-plan
   ```

## What Happens

1. The AI reads the specification and creates a high-level plan
2. The plan generates specific implementation jobs
3. Agent jobs run in isolated git worktrees
4. The final result is a working health endpoint

## Cleanup

```bash
make clean
```