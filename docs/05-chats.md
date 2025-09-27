# Conversational Workflows with `flow chat`

Grove Flow includes a dedicated set of commands for managing conversational workflows with LLMs. The `flow chat` command provides an interactive, exploratory environment for ideation, problem-solving, and prototyping before committing to a structured, multi-step plan.

## The Chat Workflow

Chats are distinct from plans and serve a different purpose. While plans are structured for automated, sequential execution of discrete jobs, chats are designed for interactive, back-and-forth conversation.

| Feature               | `flow plan`                                         | `flow chat`                                           |
| --------------------- | --------------------------------------------------- | ----------------------------------------------------- |
| **Purpose**           | Structured execution, automation, complex workflows | Exploration, ideation, refinement, problem-solving    |
| **Structure**         | A DAG of interdependent jobs in separate files      | A single Markdown file capturing a linear conversation |
| **Execution Model**   | Orchestrator runs jobs based on dependencies        | User and LLM take turns responding in the same file   |
| **When to Use**       | When the steps of a task are reasonably well-defined | When exploring a problem or refining an idea          |

The typical workflow involves starting with a `chat` to explore a concept and then using `flow plan extract` to convert valuable parts of the conversation into an executable `plan`.

## Starting and Managing Chats

Conversations are managed as simple Markdown files in the directory specified by `flow.chat_directory` in your `grove.yml` configuration.

### Starting a New Chat

You can turn any Markdown file into a chat job by initializing it. This adds the necessary frontmatter to the file.

```bash
# Create a new markdown file for your idea
touch chats/new-api-idea.md

# Initialize it as a chat job
flow chat -s chats/new-api-idea.md
```

This command will add frontmatter like this to `chats/new-api-idea.md`:

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

Let's design an API for user profiles.
```

### Continuing a Conversation

To have the LLM respond to your latest message, use `flow chat run`. This command scans the chat directory for any files where the last turn was from a "user" and generates the next LLM response.

```bash
# Run all chats that are waiting for an LLM response
flow chat run

# Run a specific chat by its title
flow chat run new-api-idea
```

After running, the file will be updated with the LLM's response, ready for your next turn.

### Listing Chats

To see all available chats and their current status, use `flow chat list`.

```bash
flow chat list
```

**Example Output:**

```
TITLE             STATUS        MODEL             FILE
new-api-idea      pending_user  gemini-2.5-pro    new-api-idea.md
old-feature       completed     gemini-2.0-flash  old-feature.md
```

## Context in Chat Sessions

Chat sessions automatically leverage the same context mechanisms as other Grove tools. When you run a chat, Grove Flow gathers context based on rules defined in your project's `.grove/rules` file. This ensures the LLM has relevant information from your codebase to inform its responses, making the conversation technically grounded and context-aware.

## From Chat to Action

A key feature of the chat workflow is the ability to seamlessly transition from conversation to execution. Once you have refined an idea or received a useful code snippet, you can extract it into a formal plan.

While `chat` itself is for conversation, you can create an `interactive_agent` job from a chat and then launch it into a dedicated `tmux` session for hands-on development.

## Extracting Plans from Chats

The `flow plan extract` command is the bridge between conversational chats and executable plans. It allows you to select specific LLM responses (or the entire conversation) and create a new job from them.

### Listing Extractable Blocks

First, you can list all the extractable blocks within a chat file. Each LLM response is automatically assigned a unique ID.

```bash
flow plan extract list --file chats/new-api-idea.md
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

### Extracting Blocks into a New Job

Using the block IDs, you can create a new job in a plan.

```bash
# First, ensure a plan exists
flow plan init api-implementation --with-worktree

# Extract a block into the new plan
flow plan add api-implementation --title "Implement API Schema" --type agent --extract-from chats/new-api-idea.md --extract e5f6g7h8
```

This creates a new agent job in the `api-implementation` plan containing the database schema design from the chat, ready to be implemented by an agent.

You can also extract the entire body of a chat file using the `all` keyword:

```bash
flow plan extract all --file chats/new-api-idea.md --title "Full API Discussion"
```

## Chat Configuration

Chat behavior is configured in your project's `grove.yml` file under the `flow` key.

```yaml
# .grove/config.yml or grove.yml
flow:
  # Directory where chat markdown files are stored
  chat_directory: ./chats
  
  # Default model for oneshot and chat jobs
  oneshot_model: gemini-2.5-pro
```

You can override the model for a specific chat by setting the `model` key in the chat file's frontmatter.

## Use Cases and Examples

Chats excel in exploratory and iterative scenarios.

*   **Brainstorming**: Start a chat to explore different approaches to a new feature.
*   **Problem Exploration**: Work through a complex bug with an LLM, providing logs and code snippets.
*   **Prototyping**: Ask the LLM to generate boilerplate code or a proof-of-concept.
*   **Documentation**: Have a conversation to draft an outline for new documentation.

### Example Workflow: From Idea to Plan

1.  **Start a chat:**
    ```bash
    echo "# Idea: Refactor auth service" > chats/auth-refactor.md
    flow chat -s chats/auth-refactor.md
    ```

2.  **Add your initial prompt to the file and run the chat:**
    ```bash
    # (Edit chats/auth-refactor.md to add details)
    flow chat run auth-refactor
    ```

3.  **After a few turns, you have a solid plan from the LLM. List the blocks:**
    ```bash
    flow plan extract list --file chats/auth-refactor.md
    # Output shows block ID: a1b2c3
    ```

4.  **Create a new plan and extract the final LLM response into it:**
    ```bash
    flow plan init refactor-auth-service --with-worktree
    flow plan add refactor-auth-service --title "Refactor Authentication" --type agent --extract-from chats/auth-refactor.md --extract a1b2c3
    ```

5.  **Run the new, structured plan:**
    ```bash
    flow plan run
    ```