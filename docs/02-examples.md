This document provides a series of practical examples to demonstrate the capabilities of Grove Flow, from simple, single-job plans to more complex, multi-step workflows.

## Example 1: Basic Plan Execution

This example shows the simplest workflow: creating a plan, adding a single job, and running it. This pattern is useful for straightforward, self-contained tasks.

1.  **Initialize the Plan**

    First, create a new plan and an associated Git worktree. The `--with-worktree` flag automatically creates a branch named `new-feature-endpoint` and sets it as the default for this plan, providing an isolated environment for the AI agent to work in.

    ```bash
    flow plan init new-feature-endpoint --with-worktree
    ```

    This command creates the `plans/new-feature-endpoint` directory, adds a `.grove-plan.yml` configuration file, and sets `new-feature-endpoint` as the active plan.

2.  **Add an Agent Job**

    Next, add a job to the plan. Since the plan is active, you don't need to specify the directory. We'll add an `agent` job, which is designed for code generation tasks.

    ```bash
    flow plan add --title "Implement User Endpoint" --type agent \
      -p "Create a new Go API endpoint for users with basic CRUD operations. The endpoint should be at /api/v1/users and handle GET, POST, PUT, and DELETE requests."
    ```

    This creates the file `plans/new-feature-endpoint/01-implement-user-endpoint.md`. The frontmatter defines the job's properties, and the body contains the prompt for the LLM.

3.  **Check the Status**

    You can view the plan's status at any time. The interactive TUI (`-t`) is a convenient way to visualize the jobs and their states.

    ```bash
    flow plan status -t
    ```

    The TUI will display the "Implement User Endpoint" job with a `pending` status.

4.  **Run the Plan**

    Finally, execute the next available job in the plan.

    ```bash
    flow plan run
    ```

    `grove-flow` will find the pending job, spin up the AI agent in the `new-feature-endpoint` worktree, and instruct it to begin implementing the API endpoint based on your prompt.

## Example 2: Multi-Job Feature Workflow

Grove Flow excels at orchestrating multi-step workflows with dependencies. This example demonstrates a common development pattern: creating a specification, implementing the feature, and then writing tests.

1.  **Initialize the Plan**

    Start by creating a plan for a new user authentication feature.

    ```bash
    flow plan init user-auth-feature --with-worktree
    ```

2.  **Add a Specification Job**

    The first step is a `oneshot` job for the LLM to write a detailed technical specification. This job has no dependencies.

    ```bash
    flow plan add --title "Define Authentication Spec" --type oneshot \
      -p "Write a detailed specification for a JWT-based authentication system. Include the data models for users, API endpoints for login/logout/refresh, and security considerations."
    ```

    This creates `01-define-authentication-spec.md`.

3.  **Add an Implementation Job**

    Next, add an `agent` job to implement the feature. This job depends on the completion of the specification job. The `-d` flag points to the filename of the dependency.

    ```bash
    flow plan add --title "Implement Auth Logic" --type agent \
      -d "01-define-authentication-spec.md" \
      -p "Implement the authentication logic based on the approved specification in the previous step."
    ```

    This creates `02-implement-auth-logic.md`. `grove-flow` will not run this job until `01-define-authentication-spec.md` is complete.

4.  **Add a Testing Job**

    Finally, add another `agent` job to write tests, which depends on the implementation being finished.

    ```bash
    flow plan add --title "Write Unit Tests" --type agent \
      -d "02-implement-auth-logic.md" \
      -p "Write comprehensive unit and integration tests for the authentication system."
    ```

    This creates `03-write-unit-tests.md`.

5.  **Run the Workflow**

    Now, running the plan will execute the jobs in the correct sequence.

    ```bash
    flow plan run --all
    ```

    `grove-flow` will first run the specification job. Once it's complete, it will run the implementation job. Finally, after the implementation is done, it will run the testing job, ensuring a logical and orderly workflow. You can visualize this dependency chain with `flow plan graph`.

## Example 3: Interactive Chat-to-Plan Workflow

Often, development starts with an idea rather than a formal plan. The `chat` and `extract` commands provide a fluid way to move from an unstructured conversation to an executable plan.

1.  **Start a Chat**

    Begin by creating a markdown file to capture an idea and initializing it as a chat.

    ```bash
    # Create the file
    echo "# Idea: Refactor the logging system" > logging-refactor.md

    # Initialize it as a runnable chat job
    flow chat -s logging-refactor.md
    ```

2.  **Iterate with the LLM**

    Run the chat to get the LLM's input. The LLM might respond with a high-level plan, code snippets, or clarifying questions. Each response from the LLM is automatically assigned a unique block ID.

    ```bash
    # Get the first response from the LLM
    flow chat run "Idea: Refactor the logging system"
    ```

    After a few back-and-forth turns, your `logging-refactor.md` file might look like this:

    ```markdown
    ---
    title: Idea: Refactor the logging system
    type: chat
    ---
    # Idea: Refactor the logging system

    <!-- grove: {"id": "a1b2c3"} -->
    ## LLM Response
    Okay, here is a high-level plan for the logging refactor:
    1.  Introduce a structured logging library (e.g., Logrus).
    2.  Define standardized log levels (DEBUG, INFO, WARN, ERROR).
    3.  Create a centralized logging configuration.

    <!-- grove: {"template": "chat"} -->
    That sounds good. Can you create a more detailed technical plan for step 1?

    <!-- grove: {"id": "d4e5f6"} -->
    ## LLM Response
    Certainly. For step 1, we will:
    - Add `github.com/sirupsen/logrus` to `go.mod`.
    - Create a new package `internal/logging`.
    - Implement a `NewLogger()` function that returns a configured logger instance.
    ```

3.  **Extract the Plan**

    The LLM's second response (`id: d4e5f6`) is a solid, actionable step. You can extract it into a formal plan.

    ```bash
    # First, create a new plan to extract the job into
    flow plan init logging-refactor-plan --with-worktree

    # Now, extract the block from the chat into the new plan
    flow plan extract d4e5f6 --title "Implement Structured Logger" \
      --file ../logging-refactor.md
    ```

    This command finds the content associated with block ID `d4e5f6` in your chat file and creates a new, runnable job (`01-implement-structured-logger.md`) inside `plans/logging-refactor-plan`. This new job is now part of a formal workflow and can be executed with `flow plan run`.