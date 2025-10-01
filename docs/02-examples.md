This document provides examples for using `grove-flow` for single- and multi-step development workflows.

## Example 1: Basic Plan Execution

This example covers the process of creating a plan, adding a single job, and running it.

1.  **Initialize the Plan**

    Create a new plan directory and an associated Git worktree. The `--worktree` flag creates a Git worktree and branch named after the plan (`new-feature-endpoint`), which isolates file changes.

    ```bash
    flow plan init new-feature-endpoint --worktree
    ```

    This command creates the `plans/new-feature-endpoint` directory, adds a `.grove-plan.yml` configuration file, and sets `new-feature-endpoint` as the active plan.

2.  **Add an Agent Job**

    Add a job to the active plan. The `agent` job type is for code generation tasks.

    ```bash
    flow plan add --title "Implement User Endpoint" --type agent \
      -p "Create a new Go API endpoint at /api/v1/users for basic CRUD operations."
    ```

    This creates the file `plans/new-feature-endpoint/01-implement-user-endpoint.md` containing the job's configuration and prompt.

3.  **Check the Status**

    View the plan's status using the interactive terminal UI (`-t`).

    ```bash
    flow plan status -t
    ```

    The TUI displays the "Implement User Endpoint" job with a `pending` status.

4.  **Run the Plan**

    Execute the next available job in the plan.

    ```bash
    flow plan run
    ```

    `grove-flow` finds the pending job and executes it in the `new-feature-endpoint` worktree.

## Example 2: Multi-Job Feature Workflow

`grove-flow` orchestrates multi-step workflows with dependencies. This example shows a development pattern of specification, implementation, and testing.

1.  **Initialize the Plan**

    Create a plan for a new user authentication feature.

    ```bash
    flow plan init user-auth-feature --worktree
    ```

2.  **Add a Specification Job**

    The first step is a `oneshot` job for an LLM to write a technical specification. This job has no dependencies.

    ```bash
    flow plan add --title "Define Authentication Spec" --type oneshot \
      -p "Write a technical specification for a JWT-based authentication system, including data models and API endpoints."
    ```

    This creates `01-define-authentication-spec.md`.

3.  **Add an Implementation Job**

    Next, add an `agent` job to implement the feature. The `-d` flag specifies that this job depends on the completion of the specification job.

    ```bash
    flow plan add --title "Implement Auth Logic" --type agent \
      -d "01-define-authentication-spec.md" \
      -p "Implement the authentication logic based on the specification from the previous step."
    ```

    This creates `02-implement-auth-logic.md`. `grove-flow` will not run this job until `01-define-authentication-spec.md` is complete.

4.  **Add a Testing Job**

    Add another `agent` job to write tests, which depends on the implementation.

    ```bash
    flow plan add --title "Write Unit Tests" --type agent \
      -d "02-implement-auth-logic.md" \
      -p "Write unit and integration tests for the authentication system."
    ```

    This creates `03-write-unit-tests.md`.

5.  **Run the Workflow**

    Execute all jobs in the plan according to their dependency order.

    ```bash
    flow plan run --all
    ```

    `grove-flow` runs the specification job first. Once it completes, it runs the implementation job, followed by the testing job.

## Example 3: Chat-to-Plan Workflow

The `chat` and `extract` commands are used to convert an unstructured conversation into an executable plan.

1.  **Start a Chat**

    Create a Markdown file for an idea and initialize it as a chat job. This adds the required frontmatter to the file.

    ```bash
    # Create the directory for chat files
    mkdir -p chats

    # Create the file
    echo "# Idea: Refactor the logging system" > chats/logging-refactor.md

    # Initialize it as a runnable chat job
    flow chat -s chats/logging-refactor.md
    ```

2.  **Iterate with the LLM**

    Run the chat to get a response from the LLM. Each LLM response is automatically assigned a unique block ID.

    ```bash
    # Get the first response from the LLM
    flow chat run "Idea: Refactor the logging system"
    ```

    After several turns, `chats/logging-refactor.md` might contain:

    ```markdown
    ---
    title: Idea: Refactor the logging system
    type: chat
    ---
    # Idea: Refactor the logging system

    <!-- grove: {"id": "a1b2c3"} -->
    ## LLM Response
    A high-level plan for the logging refactor:
    1.  Introduce a structured logging library.
    2.  Define standardized log levels.
    3.  Create a centralized logging configuration.

    <!-- grove: {"template": "chat"} -->
    That sounds good. Can you create a more detailed technical plan for step 1?

    <!-- grove: {"id": "d4e5f6"} -->
    ## LLM Response
    For step 1, we will:
    - Add a logging library to `go.mod`.
    - Create a new package `internal/logging`.
    - Implement a `NewLogger()` function.
    ```

3.  **Extract a Job from the Chat**

    The LLM's second response (ID `d4e5f6`) can be extracted into a formal plan.

    ```bash
    # First, create a new plan to hold the extracted job
    flow plan init logging-refactor-plan --with-worktree

    # Now, extract the block from the chat into the active plan
    flow plan extract d4e5f6 --title "Implement Structured Logger" \
      --file chats/logging-refactor.md
    ```

    This command finds the content of block `d4e5f6` and creates a new job file (`01-implement-structured-logger.md`) in the `logging-refactor-plan`. This job is now part of a formal workflow and can be executed with `flow plan run`.