# Conversational Workflows (Chats) Documentation

Generate comprehensive documentation for the chat functionality in Grove Flow, focusing on interactive conversations with LLMs.

## Content to Cover:

### The Chat Workflow
- Explain the purpose of `flow chat` for ideation and exploration
- How chats differ from structured plans:
  - Exploratory vs. structured execution
  - Back-and-forth conversation vs. discrete jobs
  - When to use each approach

### Starting and Managing Chats
- Using `flow chat` to start a new conversation
- Chat session management and persistence
- Navigating chat history
- Resuming previous conversations

### Context in Chat Sessions
- How context is provided to the LLM during chats:
  - Automatic context from `docs.rules`
  - Manual context addition
  - Context windows and management
- Best practices for effective context usage

### From Chat to Action
- Using `flow chat launch` to jump into interactive coding sessions:
  - How it integrates with your development environment
  - Working with generated code
  - Iterating on solutions
  
### Extracting Plans from Chats
- Detail the `flow plan extract` command:
  - Converting chat content into structured plan jobs
  - How extraction analyzes conversation flow
  - Customizing extraction behavior
  - Best practices for chat-to-plan workflows

### Chat Configuration
- Chat-specific settings in `grove.yml`
- Model selection for chats
- Chat directory organization
- Storage and retrieval of chat sessions

### Use Cases and Examples
- Brainstorming and ideation workflows
- Problem exploration before planning
- Quick prototyping sessions
- Documentation and explanation tasks
- Interactive debugging sessions

Include practical examples showing the progression from initial chat to final implementation.