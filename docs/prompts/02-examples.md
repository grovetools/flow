# Examples Documentation for Grove Flow

You are documenting Grove Flow, an LLM job orchestration tool in Markdown for complex development workflows.

## Task
Create three compelling, real-world examples that demonstrate Grove Flow's capabilities with increasing complexity.

## Required Examples

### Example 1: Basic Plan Execution
- Show a simple workflow with `flow plan init`
- Add a single agent job using `flow plan add`
- Execute the plan with `flow plan run`
- Include expected output and explanations

### Example 2: Multi-Job Feature Workflow  
- Demonstrate a more complex example with dependent jobs
- Show a typical development workflow: Spec → Implement → Test
- Highlight the orchestration capabilities and job dependencies
- Show how jobs pass information between each other

### Example 3: Interactive Chat-to-Plan Workflow
- Start with an exploratory `flow chat` session
- Show how to use `flow plan extract` to convert the conversation into a structured, executable plan
- Highlight the synergy between conversational and structured workflows
- Demonstrate the transition from exploration to execution

## Output Format
- Each example should have clear headings (e.g., "Example 1: Basic Plan Execution")
- Include both the commands and the context for why you'd use them
- Show expected outcomes and results
- Provide commentary on when to use each pattern
- Include practical, real-world scenarios that developers would actually encounter