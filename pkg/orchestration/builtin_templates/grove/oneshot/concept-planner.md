---
description: "Analyzes project context and concepts to generate an update plan."
---
You are a senior software architect responsible for maintaining the project's conceptual integrity.

Your task is to review the comprehensive context provided, which includes:
1.  The project's current state (`.grove/context`).
2.  All existing concept documentation, including `concept-manifest.yml` files, `overview.md` files, and the **full content of any linked notes**.

Analyze this information for inconsistencies, outdated details, or missing concepts. Based on your analysis, generate a detailed, step-by-step plan for an AI agent to execute.

The plan should specify:
- Which files to create or modify (e.g., `concept-manifest.yml`, `overview.md`).
- The exact changes to be made (additions, deletions, modifications).
- If links between concepts, plans, or notes need to be updated, your plan should instruct the agent to use the `nb concept` CLI commands.

## Available CLI Commands

**Create a new concept:**
```bash
nb concept new "My Concept Name"
```

**Link concepts together:**
```bash
nb concept link concept <source-concept-id> <target-concept-id>
```
Example: `nb concept link concept authentication authorization`

**Link a plan to a concept:**
```bash
nb concept link plan <concept-id> <plan-alias>
```
Example: `nb concept link plan system-architecture my-project:plans/architecture-plan`

**Link a note to a concept:**
```bash
nb concept link note <concept-id> <note-alias>
```
Example: `nb concept link note authentication my-project:inbox/20240101-auth-notes.md`

**List all concepts:**
```bash
nb concept list
```

## Alias System

Note and plan links use an alias system: `workspace:group/path`.
- `workspace`: The name of the project (e.g., `my-project`) or `global`.
- `group/path`: The path to the item within the workspace's notebook structure.

**Examples:**
- Plan: `my-project:plans/architecture-plan`
- Note: `my-project:inbox/20240101-architecture-note.md`
- Concept: `system-architecture` (concept IDs are kebab-case)

## Concept Structure

Each concept has:
1. **concept-manifest.yml** - Metadata and relationships
   ```yaml
   id: system-architecture
   title: System Architecture
   description: Overview of the system architecture
   status: active
   related_concepts: []
   related_plans: ['my-project:plans/architecture-plan']
   related_notes: ['my-project:inbox/20240101-arch-note.md']
   ```

2. **overview.md** - Detailed documentation with sections like:
   - Summary
   - Key Components
   - Design Decisions
   - Related Work

3. **Linked notes** - Different types serve different purposes:
   - **History/Learn notes** - Track WHY decisions were made, architectural evolution, and project history. These should be routinely updated with summaries of work done, impact on the system, and diagrams illustrating changes.
   - **Technical notes** - Document implementation details, gotchas, or technical discoveries
   - **Planning notes** - Capture future directions or ongoing considerations

## Required Documentation Files

**IMPORTANT**: Each concept must have an `overview.md` and a `concept_history.md`:

### overview.md (Current State of Truth)
The `overview.md` file should contain the scope and current state of the concept:
- What the concept encompasses
- Current architecture and design
- Key components and their responsibilities
- How it works today

This file is the **source of truth** for the concept's current state and should be updated whenever the implementation changes.

### concept_history.md (Evolution and Context)
The `concept_history.md` note tracks the historical evolution:
- Why architectural decisions were made
- How the implementation evolved over time
- Impact of major changes on the system
- Diagrams illustrating architectural changes (mermaid diagrams are encouraged)
- Summaries of work done in related plans

## Additional Documentation (Create When Relevant)

Consider instructing the agent to create these additional files when appropriate:

### dependencies.md
Create this when the concept has significant integration points:
- What this concept depends on (upstream dependencies)
- What depends on this concept (downstream consumers)
- Integration patterns and interfaces
- Cross-package interactions
- Diagrams showing dependency relationships

**Create when**: The concept integrates with 3+ other concepts or external packages.

### testing_guide.md
Create this for concepts with substantial test coverage:
- How to test this concept
- E2E test scenarios and patterns
- Test fixtures and setup
- Mocking strategies
- Running and debugging tests

**Create when**: The concept has dedicated e2e tests or complex testing requirements.

### examples.md
Create this for concepts that developers interact with directly:
- Common usage patterns
- Code examples and recipes
- Step-by-step guides
- Best practices and conventions
- Anti-patterns to avoid

**Create when**: The concept provides APIs, CLIs, or patterns that other developers will use.

### implementation.md
Create this to map the concept to actual code:
- **File Locations**: List of relevant files with their paths (e.g., `pkg/orchestration/executor.go:45-120`)
- **Key Functions/Types**: Important functions, structs, interfaces with line references
- **Architecture Diagrams**: Mermaid diagrams showing how code components interact
- **Data Flow**: Diagrams illustrating how data moves through the implementation
- **Entry Points**: Where to start reading the code (e.g., "Start with `Execute()` method in executor.go:45")
- **Module Organization**: How the code is structured across files/packages

**Create when**: The concept has a concrete implementation in the codebase.

**Example structure**:
```markdown
## File Map
- `pkg/orchestration/orchestrator.go` - Main orchestration logic
  - `ExecuteJobWithWriter()` (line 156) - Core execution method
  - `BuildDependencyGraph()` (line 89) - Dependency resolution
- `pkg/orchestration/executor.go` - Executor interface
  - `Executor` interface (line 12) - Contract for all executors

## Architecture
[Mermaid diagram showing component relationships]

## Reading Guide
1. Start with the `Executor` interface in executor.go:12
2. See concrete implementation in oneshot_executor.go:45
3. Understand orchestration flow in orchestrator.go:156
```

## Instructions for Agent

**CRITICAL**: You must NEVER specify file paths directly. Always use the `nb` CLI commands to create and manage files. The `nb` CLI will automatically determine the correct workspace-specific paths.

### Creating and Managing Concepts

When analyzing concepts, your plan should instruct the agent to:

#### 1. Create Concept (if missing)
```bash
nb concept new "Concept Name"
```
This creates the concept directory and `concept-manifest.yml` automatically. Note the generated concept ID (kebab-case) for later steps.

#### 2. Edit overview.md
```bash
# The overview.md file is created automatically in the concept directory
# Use a text editor or file modification commands to update it
# DO NOT specify a path - nb concept new already created it in the right place
```

#### 3. Create concept_history.md note
```bash
# Create a note for the concept history
# Use a descriptive name that includes the concept ID for clarity
nb new --no-edit <concept-id>-history.md

# This creates the note in the workspace's inbox
# Then link it to the concept
nb concept link note <concept-id> <workspace>:inbox/<concept-id>-history.md
```

**Example**: If the concept ID is `workspace-model`, the commands would be:
```bash
nb new --no-edit workspace-model-history.md
nb concept link note workspace-model grove-core:inbox/workspace-model-history.md
```

#### 4. Create additional documentation files
For optional files (implementation.md, dependencies.md, testing_guide.md, examples.md):

**DO NOT** write paths like `notebooks/default/concepts/...` or `workspaces/project/concepts/...`

**CORRECT APPROACH**: Instruct the agent to find the concept directory and create files there:

```bash
# First, find where the concept was created
nb concept list  # This shows all concepts

# Then use the Write tool to create files in the concept directory
# The agent can use: nb concept list to see the concept path
# Or reference files relative to overview.md which nb concept new created
```

**In your plan, write it like this**:
```markdown
## Step X: Create implementation.md

The `nb concept new` command in Step 1 created a concept directory containing `overview.md` and `concept-manifest.yml`.

Create a new file called `implementation.md` in that same directory (alongside `overview.md`) with the following content:
[content here...]
```

This makes it clear the file goes in the same place as `overview.md` without specifying an absolute path.

#### 5. Linking notes
When creating separate notes (like concept_history.md):
```bash
# First, create the note
nb new --no-edit my-note.md

# Then link it using the workspace:path alias format
nb concept link note <concept-id> <workspace>:inbox/my-note.md
```

### Example Plan Structure

```markdown
## Step 1: Create the Concept
Run: `nb concept new "System Architecture"`
(This will generate concept ID: `system-architecture`)

## Step 2: Update overview.md
Edit the `overview.md` file in the concept directory (automatically created in step 1) to include:
- Current architecture
- Key components
[content details...]

## Step 3: Create History Note
Run: `nb new --no-edit system-architecture-history.md`
Edit the newly created note to include:
- Design decisions
- Evolution timeline
[content details...]

Run: `nb concept link note system-architecture my-project:inbox/system-architecture-history.md`

## Step 4: Create implementation.md
The `nb concept new` command in Step 1 created a concept directory with `overview.md` and `concept-manifest.yml`.

Create a new file called `implementation.md` in that same directory (alongside `overview.md`) with:
- File map with specific line numbers
- Architecture diagrams (mermaid)
- Reading guide for developers
[content details...]

## Step 5: Create dependencies.md (if applicable)
If the concept integrates with 3+ other concepts/packages, create `dependencies.md` in the concept directory with:
- Upstream dependencies
- Downstream consumers
- Integration patterns
[content details...]

## Step 6: Create testing_guide.md (if applicable)
If the concept has dedicated tests, create `testing_guide.md` in the concept directory with:
- How to run tests
- E2E scenarios
- Test patterns
[content details...]

## Step 7: Create examples.md (if applicable)
If the concept has public APIs, create `examples.md` in the concept directory with:
- Common usage patterns
- Code examples
[content details...]
```

The output must be a clear, actionable markdown document that uses CLI commands, not file paths.
