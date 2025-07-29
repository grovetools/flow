# Orchestration Demo

This demo showcases Grove's job orchestration capabilities, demonstrating how it can decompose high-level specifications into executable tasks and manage their execution.

## Overview

The demo includes:
- A sample feature specification (`spec.md`)
- An orchestration plan with multiple jobs (`myfeat/` directory)
- A mock agent setup for demonstration purposes

## Mock Agent Setup

Since this is a demo, we use a mock grove agent instead of the full Claude-powered agent. The mock agent:
- Runs in a lightweight Alpine Linux container
- Simulates the grove agent behavior
- Creates sample implementation files when processing jobs
- Always completes successfully for demonstration

The mock is implemented via:
- `entrypoint.sh`: Sets up the mock grove binary in the container
- `grove.yml`: Configures the agent to use the mock setup

## Running the Demo

1. **Initialize the orchestration** (if not already done):
   ```bash
   grove jobs init spec.md myfeat
   ```

2. **Check the plan status**:
   ```bash
   grove jobs status myfeat
   ```

3. **Run a specific job**:
   ```bash
   grove jobs run myfeat/implementation.md
   ```

4. **View the execution graph**:
   ```bash
   grove jobs graph myfeat
   ```

## Understanding the Output

When you run a job:
1. The orchestrator starts the mock agent container
2. It sends the job prompt to the mock grove agent via stdin
3. The mock agent simulates processing and creates a sample `implementation.py`
4. The job is marked as completed
5. Logs are saved in `myfeat/.logs/`

## Real vs Mock Agent

In a real deployment:
- The agent would use the actual `grove-claude-agent` image
- Claude would process the prompts and generate real implementations
- The agent would have access to development tools and can make actual code changes

This demo provides a simplified version to showcase the orchestration workflow without requiring Claude API access or the full agent image.