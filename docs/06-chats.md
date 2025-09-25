# Conversational Workflows (Chats)

Grove Flow's chat functionality provides a flexible environment for exploratory conversations with LLMs, ideation, and iterative problem-solving. Unlike structured plans with discrete jobs, chats offer a free-form conversational approach that can later be converted into actionable plans.

## The Chat Workflow

### Purpose of Chats

Chats serve several key purposes in the Grove Flow ecosystem:

- **Ideation and Brainstorming**: Explore ideas without committing to a structured plan
- **Problem Exploration**: Understand complex issues through conversation
- **Requirements Gathering**: Refine understanding through iterative discussion
- **Rapid Prototyping**: Quickly test concepts and approaches
- **Decision Making**: Work through trade-offs and alternatives

### Chat vs. Plan Approach

**When to use Chats**:
- Early-stage problem exploration
- Unclear requirements or approach
- Need for back-and-forth refinement
- Brainstorming and creative work
- Learning and understanding complex topics

**When to use Plans**:
- Clear, actionable tasks
- Well-defined deliverables
- Need for structured workflow
- Multiple team members involved
- Repeatable processes

### Workflow Progression

The typical workflow progression:
```
Chat (Explore) → Extract (Structure) → Plan (Execute) → Implement
```

## Starting and Managing Chats

### Creating New Chats

Start a new chat session by creating a markdown file with Grove Flow frontmatter:

```bash
# Create a new chat from scratch
flow chat --spec-file ./chats/feature-exploration.md --title "Explore User Authentication"

# Create with specific model
flow chat --spec-file ./chats/api-design.md --title "API Architecture Discussion" --model claude-4-opus
```

### Chat File Structure

Chats are markdown files with YAML frontmatter:

```markdown
---
id: user-auth-exploration
title: "User Authentication Exploration"
status: pending_user
type: chat
model: claude-4-sonnet
created: 2024-09-24T10:30:00Z
---

I'm exploring different approaches for implementing user authentication in our application.

Let's discuss the trade-offs between different authentication methods:
- JWT tokens
- Session-based auth
- OAuth integration
- Multi-factor authentication

What would you recommend for a modern web application with both web and mobile clients?
```

### Listing and Browsing Chats

View all available chats:

```bash
# List all chats
flow chat list

# Filter by status
flow chat list --status pending_user
flow chat list --status completed

# JSON output for scripting
flow chat list --json
```

Example output:
```
TITLE                          STATUS        MODEL           CREATED
User Authentication Exploration  pending_user  claude-4-sonnet  2024-09-24
API Design Discussion           completed     claude-4-opus    2024-09-23
Database Schema Planning        running       gemini-2.5-pro   2024-09-24
```

### Running Chat Sessions

Execute chat sessions to get LLM responses:

```bash
# Run all pending chats
flow chat run

# Run specific chat
flow chat run ./chats/user-auth-exploration.md
```

Chat execution:
- Processes the conversation context
- Generates LLM responses
- Appends responses to the chat file
- Updates status to reflect progress

## Context in Chat Sessions

### Automatic Context Integration

Grove Flow automatically provides context to chat sessions:

1. **Project Context**: Information from `grove.yml` configuration
2. **Documentation Context**: Content from `docs.rules` files
3. **Codebase Context**: Relevant source files and project structure
4. **Previous Conversation**: Full chat history for continuity

### docs.rules Context Files

Create `docs.rules` files to define automatic context:

```bash
# Example docs.rules content
src/**/*.go
docs/api.md
README.md
config/database.yml

# Exclude patterns
!src/vendor/
!**/*_test.go
```

Grove Flow uses these patterns to:
- Include relevant files in LLM context
- Provide current codebase state
- Maintain consistency across conversations
- Reduce need for manual context management

### Manual Context Addition

Add specific context within chat conversations:

```markdown
---
id: performance-analysis
title: "Performance Analysis Chat"
status: pending_user
type: chat
---

I want to discuss performance optimization for our API.

Context files I want you to consider:
- `src/api/handlers.go` - Main request handlers
- `src/database/queries.go` - Database query logic
- `config/performance.yml` - Current performance settings

Looking at the current implementation, where do you see the biggest bottlenecks?
```

### Context Best Practices

- **Be specific**: Reference particular files, functions, or concepts
- **Provide current state**: Include recent changes or issues
- **Set scope**: Define what aspects to focus on
- **Update regularly**: Keep context current as projects evolve

## Interactive Development Sessions

### Launching Chat-Based Development

Transform chats into interactive coding sessions:

```bash
# Launch by chat title
flow chat launch "User Authentication Exploration"

# Launch by file path
flow chat launch ./chats/feature-discussion.md

# Launch in host environment (not container)
flow chat launch api-design --host
```

### Development Session Features

Chat launch creates:

1. **Tmux Session**: Dedicated development environment
2. **Pre-loaded Context**: Chat content available to the agent
3. **Interactive Agent**: Full development tool access
4. **Working Directory**: Appropriate project location
5. **Session Persistence**: Continue work across multiple sessions

### Session Workflow

Typical chat-to-development workflow:

1. **Explore in Chat**: Discuss approaches and gather requirements
2. **Launch Session**: Convert chat into interactive development
3. **Implement**: Use agent tools to write code and make changes
4. **Iterate**: Return to chat for refinement and adjustments
5. **Extract Plan**: Structure the work into a formal plan

### Session Management

```bash
# List active chat sessions
tmux list-sessions | grep grove-chat

# Attach to existing session
tmux attach -t grove-chat-user-auth

# Kill session when done
tmux kill-session -t grove-chat-user-auth
```

## Extracting Plans from Chats

### Understanding Extraction

The extraction process analyzes chat conversations and creates structured plans with discrete jobs. Grove Flow identifies:

- **Key decisions** made during discussion
- **Action items** that emerged from conversation
- **Dependencies** between different tasks
- **Deliverables** mentioned in the chat

### Basic Extraction

Extract entire chat conversations:

```bash
# Extract all chat content into a new plan job
flow plan extract all --file ./chats/feature-discussion.md --title "Feature Implementation Plan"

# Extract with dependencies
flow plan extract all --file ./chats/api-design.md --title "API Implementation" --depends-on "01-spec.md"
```

### Selective Block Extraction

Extract specific parts of conversations:

```bash
# List available blocks in a chat
flow plan extract list --file ./chats/complex-discussion.md

# Extract specific blocks by ID
flow plan extract f3b9a2 a1c2d4 --title "Database Schema Design" --file ./chats/complex-discussion.md
```

Example block listing output:
```json
[
  {
    "id": "f3b9a2",
    "type": "user",
    "timestamp": "2024-09-24T10:15:00Z",
    "preview": "Let's discuss the database schema for user management..."
  },
  {
    "id": "a1c2d4", 
    "type": "assistant",
    "timestamp": "2024-09-24T10:16:30Z",
    "preview": "I'd recommend a normalized approach with separate tables..."
  }
]
```

### Extraction Customization

Control extraction behavior:

```bash
# Extract with specific output type
flow plan extract all --file chat.md --title "Implementation" --output commit

# Extract with specific model for the new job
flow plan extract all --file chat.md --title "Analysis" --model claude-4-opus

# Extract with worktree specification
flow plan extract all --file chat.md --title "Feature Work" --worktree feature-branch
```

### Best Practices for Extraction

**Prepare Chats for Extraction**:
- Summarize key decisions at the end of chat sessions
- Use clear section headers to organize discussions
- Mark action items explicitly in conversations
- Document assumptions and constraints

**Structure Extracted Plans**:
- Create logical job boundaries from chat flow
- Maintain dependencies identified in discussions
- Use appropriate job types for different tasks
- Include context and rationale in job prompts

## Chat Configuration

### Chat-Specific Settings

Configure chat behavior in `grove.yml`:

```yaml
flow:
  chat_directory: ./chats          # Where chat files are stored
  chat_model: claude-4-sonnet      # Default model for chats
  oneshot_model: claude-3-haiku    # Model for simple responses
```

### Chat Directory Organization

Organize chats for easy management:

```
chats/
├── features/
│   ├── user-auth.md
│   ├── payment-integration.md
│   └── mobile-app.md
├── architecture/
│   ├── database-design.md
│   ├── api-structure.md
│   └── deployment-strategy.md
└── investigations/
    ├── performance-analysis.md
    ├── security-review.md
    └── tech-debt-assessment.md
```

### Model Selection for Chats

Choose appropriate models based on chat purpose:

- **claude-4-opus**: Complex architectural discussions
- **claude-4-sonnet**: General feature exploration
- **claude-3-haiku**: Quick questions and clarifications
- **gemini-2.5-pro**: Cost-effective lengthy discussions

### Storage and Retrieval

Chat sessions are persistent:
- **File-based storage**: Chats stored as markdown files
- **Version control friendly**: Plain text format works with Git
- **Searchable**: Use standard text search tools
- **Portable**: Easy to share and backup

## Use Cases and Examples

### Brainstorming and Ideation

**Scenario**: Exploring new feature concepts

```markdown
---
title: "Mobile App Feature Brainstorming"
type: chat
model: claude-4-sonnet
---

I want to brainstorm features for our mobile app's next release. 
Our users are primarily professionals who need quick access to data.

Current pain points:
- Slow loading on mobile networks
- Difficult navigation on small screens  
- Limited offline functionality

What creative solutions could address these issues while adding value?
```

### Problem Exploration

**Scenario**: Understanding complex technical issues

```markdown
---
title: "Database Performance Investigation"
type: chat
model: claude-4-opus
---

We're experiencing slow query performance on our main database.
Looking at the current schema and recent changes, help me understand:

1. What could be causing the slowdown?
2. What diagnostic steps should we take?
3. What are our options for optimization?

Recent changes include:
- Added full-text search capabilities
- Increased user activity by 3x
- Added several new indexes
```

### Requirements Refinement

**Scenario**: Clarifying project requirements through discussion

```markdown
---
title: "Payment System Requirements"
type: chat
model: claude-4-sonnet
---

I need to refine the requirements for our payment processing system.

Initial requirements:
- Support credit cards and PayPal
- Handle subscriptions and one-time payments
- PCI compliance
- International currency support

Let's walk through the edge cases and technical considerations...
```

### Decision Making

**Scenario**: Working through architectural decisions

```markdown
---
title: "Microservices vs Monolith Decision"
type: chat
model: claude-4-opus
---

We need to decide on architecture for our new service platform.

Context:
- Team of 12 developers
- Expected 10x traffic growth over 2 years
- Need to support multiple client types
- Limited DevOps resources currently

Help me think through the trade-offs between microservices and monolithic architecture...
```

## Advanced Chat Features

### Multi-Session Conversations

Resume and extend conversations across multiple sessions:

```bash
# Continue previous conversation
flow chat run existing-chat.md

# Launch development session from ongoing chat
flow chat launch existing-chat.md
```

### Chat Templates

Use templates for common chat patterns:

```yaml
# Template: architecture-discussion
---
title: "{{ .Title }} Architecture Discussion"
type: chat
model: claude-4-opus
---

I want to discuss the architecture for {{ .Title }}.

Context:
- Current system: {{ .CurrentSystem }}
- Scale requirements: {{ .Scale }}
- Team size: {{ .TeamSize }}
- Timeline: {{ .Timeline }}

Key considerations:
1. Scalability requirements
2. Development team structure
3. Technology constraints
4. Integration requirements

Let's explore different approaches and their trade-offs...
```

### Integration with Development Workflow

**Chat-Driven Development Pattern**:

1. **Explore**: Start with open-ended chat discussion
2. **Clarify**: Use conversation to refine understanding
3. **Structure**: Extract key insights into plan jobs
4. **Implement**: Execute structured plan
5. **Review**: Return to chat for evaluation and iteration

This pattern combines the flexibility of conversation with the structure of plans, enabling both exploration and execution within a unified workflow.

Chats provide the exploratory foundation for Grove Flow workflows, enabling you to think through problems, gather requirements, and refine approaches before committing to structured implementation plans.