# Conversational Workflows (Chats) Documentation

Generate documentation for chat jobs, emphasizing their role as exploration phase before structured implementation.

## Outline

### Understanding Chat Jobs
- Chat jobs as conversational exploration interface
- Typical first job when promoting from grove-notebook
- Purpose: exploration, brainstorming, getting detailed plans
- Each LLM response tagged with unique ID for extraction

### The Modern Chat Workflow
Typical flow:
1. Promote note from grove-notebook (creates plan with chat job)
2. Describe problem and run with `flow chat run`
3. Iterate conversation until clear plan emerges
4. Extract to structured jobs using TUI (`x` and `i` keys)

### Chat vs. Other Job Types
Comparison table showing:
- chat: Exploration and planning
- oneshot: Single-shot generation
- interactive_agent: Implementation

Key point: **Chats produce text, not code changes**

### Starting and Managing Chats

#### Initialization
- Automatic creation from grove-notebook promotion
- Manual initialization with `flow chat -s`
- Frontmatter structure

#### Continuing Conversations
- Running with `flow chat run`
- Listing chats with `flow chat list`
- Filtering by status

#### Context in Chat Sessions
- Automatic context from `.grove/rules`
- Grove-context integration
- Best practices

### From Conversation to Execution

**Critical: There is no `flow chat launch`**

Path from chat to execution:
1. Complete chat conversation
2. Use TUI to extract implementation jobs (`x` for XML plan, `i` for agent)
3. Run extracted jobs with `r`

Emphasize: Think first in chat, then structure into execution jobs

### Extracting Plans from Chats

#### Listing Extractable Blocks
- Each LLM response has unique ID
- `flow plan extract list` to view blocks

#### Creating Jobs from Blocks
- Using block IDs with `flow plan extract`
- `source_block` references
- Extracting entire chat with `all` keyword

### Chat Configuration
- Location determined by `notebooks` or `flow.chat_directory`
- Model configuration
- Default settings

### Use Cases and Examples

#### Example Workflow: From Idea to Implementation
Using grove-notebook and TUI:
1. Start in `nb tui`, promote note
2. Edit and run chat job
3. Extract jobs in TUI
4. Monitor execution

Show modern TUI-based approach vs old CLI-heavy workflow.

Include use cases:
- Brainstorming approaches
- Problem exploration
- Prototyping ideas
- Documentation drafting
