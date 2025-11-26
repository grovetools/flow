# Conversational Workflows (Chats)

Grove Flow includes `flow chat` commands for managing conversational workflows with LLMs. This provides an environment for exploration and problem-solving before creating a structured, multi-step plan.

## The Chat Workflow

Chats are distinct from plans. Plans consist of interdependent jobs designed for structured execution, while chats are single Markdown files designed for interactive, back-and-forth conversation.

| Feature       | `flow plan`                                    | `flow chat`                                        |
|---------------|------------------------------------------------|----------------------------------------------------|
| **Purpose**   | Structured execution, automation               | Exploration, ideation, refinement, problem-solving |
| **Structure** | A directory of interdependent job files (a DAG) | A single Markdown file capturing a linear conversation |
| **Execution** | Orchestrator runs jobs based on dependencies   | User and LLM respond sequentially in the same file   |

A common workflow is to start with a `chat` to explore a concept, then use `flow plan extract` to convert parts of the conversation into an executable `plan`.

## Starting and Managing Chats

Conversations are managed as Markdown files. Their location is determined by the `notebooks` configuration in `grove.yml`, falling back to the `flow.chat_directory` setting for backward compatibility.

### Initializing a Chat

Any Markdown file can be converted into a chat job. The initialization command adds the necessary YAML frontmatter to the file.

```bash
# Create a markdown file
touch ./chats/new-api-idea.md

# Initialize it as a chat job
flow chat -s ./chats/new-api-idea.md
```

This command adds the following frontmatter to `chats/new-api-idea.md`:

```yaml
---
id: job-a1b2c3d4
title: new-api-idea
type: chat
model: gemini-2.5-pro # Inherited from config
status: pending_user
updated_at: "2025-09-26T10:00:00Z"
aliases: []
tags: []
---

# My API Idea
```

You can override the default title and model during initialization:

```bash
flow chat -s ./chats/new-api-idea.md --title "API Design" --model "claude-4-sonnet"
```

### Continuing a Conversation

To have the LLM respond to your latest message, use `flow chat run`. This command scans for chat files where the last turn was from a "user" and generates the next LLM response, appending it to the file.

```bash
# Run all chats that are waiting for an LLM response
flow chat run

# Run a specific chat by its title
flow chat run "API Design"
```

After running, the file is updated with the LLM's response, ready for your next input.

### Listing Chats

To see all available chats and their current status, use `flow chat list`.

```bash
flow chat list
```

**Example Output:**

```
TITLE             STATUS        MODEL             FILE
API Design        pending_user  claude-4-sonnet   new-api-idea.md
old-feature       completed     gemini-2.0-flash  old-feature.md
```

You can filter by status:
```bash
flow chat list --status pending_user
```

## Context in Chat Sessions

When `flow chat run` is executed, it uses `grove-context` to gather file context based on rules defined in your project's `.grove/rules` file. This context is included in the request to the LLM, providing it with relevant information from your codebase.

## From Conversation to Execution

`chat` jobs are for conversation; their output is text. To execute code or run commands, content from a chat must be extracted into a `plan` containing an `agent` or `shell` job. An `interactive_agent` job created from a chat can then be run, which will launch a `tmux` session for development.

There is no `flow chat launch` command. The workflow involves extracting content into a plan first.

## Extracting Plans from Chats

The `flow plan extract` command converts parts of a conversation into new jobs within a plan.

### Listing Extractable Blocks

Each LLM response in a chat file is tagged with a unique ID inside a `<!-- grove: ... -->` comment. You can list all extractable blocks within a file.

```bash
flow plan extract list --file ./chats/new-api-idea.md
```

**Example Output:**

```
Found 2 extractable blocks in chats/new-api-idea.md:

ID: a1b2c3d4
Type: llm
Line: 10
Preview: Here is a proposed API structure...
---
ID: e5f6g7h8
Type: llm
Line: 45
Preview: The database schema could look like this...
---
```

### Creating a Job from a Block

Using a block ID, you can create a new job in a plan directory. The new job does not contain the extracted content directly; instead, it contains a `source_block` reference. The content is resolved and injected into the prompt when the job is executed.

```bash
# First, ensure a plan exists
flow plan init api-implementation --worktree

# Extract a block into the new plan
flow plan extract e5f6g7h8 --file ./chats/new-api-idea.md --title "Implement API Schema"
```

This creates a new chat job in the `api-implementation` plan. The job file will contain a reference like `source_block: chats/new-api-idea.md#e5f6g7h8`.

You can also extract the entire body of a chat file using the `all` keyword:

```bash
flow plan extract all --file ./chats/new-api-idea.md --title "Full API Discussion"
```

This creates a job with a `source_block: chats/new-api-idea.md` reference.

## Chat Configuration

Chat behavior is configured in your project's `grove.yml` file under the `flow` key.

```yaml
# .grove/config.yml or grove.yml
flow:
  # Directory where chat markdown files are stored.
  # This is superseded by the `notebooks` configuration if present.
  chat_directory: ./chats
  
  # Default model for oneshot and chat jobs
  oneshot_model: gemini-2.5-pro
```

You can override the model for a specific chat by setting the `model` key in the chat file's frontmatter.

## Use Cases and Examples

Chats are suitable for exploratory and iterative scenarios.

*   **Brainstorming**: Explore different approaches to a new feature.
*   **Problem Exploration**: Work through a bug with an LLM, providing logs and code snippets.
*   **Prototyping**: Ask an LLM to generate boilerplate code or a proof-of-concept.
*   **Documentation**: Draft an outline for new documentation.

### Example Workflow: From Idea to Plan

1.  **Start a chat:**
    ```bash
    echo "# Idea: Refactor auth service" > chats/auth-refactor.md
    flow chat -s chats/auth-refactor.md
    ```

2.  **Add an initial prompt to the file, then run the chat to get an LLM response:**
    ```bash
    # (Edit chats/auth-refactor.md to add details)
    flow chat run auth-refactor
    ```

3.  **After a few turns, you have a solid plan from the LLM. List the extractable blocks:**
    ```bash
    flow plan extract list --file chats/auth-refactor.md
    # Output shows block ID: a1b2c3
    ```

4.  **Create a new plan and extract the final LLM response into an `interactive_agent` job:**
    ```bash
    flow plan init refactor-auth-service --worktree
    flow plan add refactor-auth-service -t interactive_agent --title "Refactor Auth Service"
    # This creates a new job, e.g., 01-refactor-auth-service.md

    # Now, extract the chat content into that job.
    # We can do this by setting the source_block property. For now, this is a manual edit.
    # An alternative is to use `plan extract` and then change the job type.
    flow plan extract a1b2c3 --file ./chats/auth-refactor.md --title "Implement Auth Refactor"
    # This creates a new chat job in the plan. Manually change `type: chat` to `type: interactive_agent`.
    ```

5.  **Run the new agent job to start a coding session:**
    ```bash
    flow plan run refactor-auth-service/01-implement-auth-refactor.md
    ```