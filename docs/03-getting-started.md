# Getting Started with Grove Flow

Grove Flow is a CLI tool for orchestrating multi-step tasks using LLMs, defined in Markdown files. It allows you to define, run, and manage workflows as a series of jobs, leveraging isolated worktrees for safe, reproducible execution.

## Installation

`grove-flow` is part of the Grove ecosystem. To install it, follow the general Grove installation instructions using `grove get`:

```bash
grove get github.com/mattsolo1/grove-flow
```

Ensure the `flow` binary is in your system's PATH. Use `grove list` to view all Grove ecosystem binaries.

## First Steps: Tutorial

Let's walk through the process of creating and running a simple plan:

1. **Initialize a new plan:**
   ```bash
   flow plan init my-first-plan
   ```
   This command creates a new directory named `my-first-plan` for your orchestration plan.

2. **Add your first job:**
   ```bash
   flow plan add my-first-plan \
     --title "Scaffold API" \
     --type agent \
     -p "Create a new Go API endpoint for users with basic CRUD operations."
   ```
   This creates a new job file (`01-scaffold-api.md`) inside your plan directory.

3. **Check the plan status:**
   ```bash
   flow plan status -t
   ```
   This opens an interactive terminal UI (TUI) showing the status of all jobs in your plan.  It shows key information: what is running or blocked, where the job files are, and what their current status is.
   You must have a TTY to use the `-t` flag.

4. **Run the job:**
   ```bash
   flow plan run
   ```
   This command runs the next available job in your plan.  The agent will start up in its isolated worktree (if configured) and begin implementing the feature.

**Troubleshooting Tip:** If you encounter any errors, check that:
- The `flow` binary is in your PATH (`grove list`)
- You are in a Git repository.

## Quick Example: Adding a New Feature

Here's a complete example of creating a plan to add a "user profile" feature to a web application:

1.  **Initialize the plan:**
    ```bash
    flow plan init user-profile-api
    ```

2.  **Add a job to design the API:**
    ```bash
    flow plan add user-profile-api \
      -t agent \
      --title "API Design" \
      -p "Design the REST API endpoints for user profile management (GET, POST, PUT, DELETE)."
    ```

3.  **Add a job to implement the backend:**
    ```bash
    flow plan add user-profile-api \
      -t agent \
      --title "Backend Implementation" \
      -d 01-api-design.md \
      -p "Implement the API endpoints in Go, using the design from 01-api-design.md."
    ```

4.  **Run the plan:**
    ```bash
    flow plan run
    ```
    As the jobs run, `flow` will create new worktrees, generate code, and track the progress of each step.

## Next Steps

Congratulations, you have created a simple plan and ran it! To extend this base understanding, you can try:

-   **Exploring different job types:** Experiment with oneshot, shell, and chat jobs.
-   **Using worktrees:** Run agent jobs in isolated worktrees.
-   **Using recipes:**  Automate common workflows with flow plan recipes.