# Conversational Workflows (Chats)

Chat jobs provide a conversational interface for exploring problems, brainstorming solutions, and iterating on ideas with an LLM. They serve as the starting point for structured work in Grove Flow.

## Understanding Chat Jobs

Chat jobs are typically the first job created when promoting a note from `grove-notebook` to a plan. They serve as an exploration phase where you can:

- Discuss the problem with an LLM
- Explore different approaches
- Get a detailed implementation plan
- Refine requirements through conversation

Each LLM response is tagged with a unique ID, making it easy to extract specific parts of the conversation into structured implementation jobs.

## The Modern Chat Workflow

The typical workflow with chat jobs:

1. **Promote a note from grove-notebook** - Creates a plan with an initial `01-chat.md` job
2. **Describe the problem** - Edit the chat file and run it with `flow chat run`
3. **Iterate with the LLM** - Continue the conversation until you have a clear plan
4. **Extract to structured jobs** - Use the TUI's `x` and `i` keys to create implementation jobs

### Chat vs. Other Job Types

| Feature       | `chat`                                         | `oneshot`                    | `interactive_agent`              |
|---------------|------------------------------------------------|------------------------------|----------------------------------|
| **Purpose**   | Exploration, ideation, planning                | Single-shot code/doc generation | Implementation, multi-step coding |
| **Interaction**| Multi-turn conversation                       | Single request/response      | Interactive coding session       |
| **Output**    | Conversation appended to file                  | Output appended to file      | Code changes in worktree         |
| **Use Case**  | Problem exploration, getting detailed plans    | Code review, documentation   | Feature implementation           |

Chats are not for execution - they're for thinking. To execute code or make changes, extract the chat content into `oneshot` or `interactive_agent` jobs.

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

**Important**: Chat jobs produce text, not code changes. To execute code or make changes to your codebase, you must extract the chat content into implementation jobs.

There is **no `flow chat launch`** command. The path from chat to execution is:

1. Complete your chat conversation
2. Use the plan status TUI to extract implementation jobs:
   - Press `x` to create an XML plan job (oneshot)
   - Press `i` to create an interactive agent job
3. Run the extracted jobs with `r`

This workflow ensures that you first think through the problem in a chat, then structure the work into proper execution jobs with dependencies.

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

### Example Workflow: From Idea to Implementation

The modern workflow using `grove-notebook` and the TUI:

1. **Start in grove-notebook:**
   ```bash
   nb tui
   # Navigate to an issue
   # Press 'P' to promote to plan (creates plan with initial chat job)
   ```

2. **Open the plan and edit the chat:**
   ```bash
   flow plan open auth-refactor
   # The TUI opens showing 01-chat.md
   # Exit TUI, edit the chat file to describe the problem
   ```

3. **Run the chat to get LLM's plan:**
   ```bash
   flow chat run
   # Or press 'r' in the status TUI
   ```

4. **Extract implementation jobs in the TUI:**
   - Open `flow plan status -t`
   - Select the completed chat job
   - Press `x` to create XML plan extraction job
   - Select the XML plan job
   - Press `i` to create interactive agent job
   - Press `r` to run the jobs

5. **Monitor execution:**
   ```bash
   hooks b  # View running agent session
   ```

This workflow keeps you in the TUI and automatically manages dependencies, making it much faster than the CLI-based approach.