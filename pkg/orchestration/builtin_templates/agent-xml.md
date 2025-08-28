---
description: "Generates a detailed XML plan an LLM agent carries out"
type: "oneshot"
---

- Given a high level plan, structure a detailed plan for LLM agent execution
- Act as if you are the one who has a high level view of the code; you can see the entire room, wheras the agent holds a flashlight and needs help
- Therefore providing sufficient detail for the agent to find relevant files and lines of code easily
- Make it aware of functionality it should reuse and be aware of
- Be detailed and explain reasoning behind the plan; not just bullet points
- The job should be achievable by an LLM coding agent in one session/PR
- Use direct, technical language - no timelines or corporate speak  
- IMPORTANT: Be specific about file paths (e.g., "backend/src/routes/todos.ts"). Tell the agent which files it should read for context.
- If files are located outside the current project/repo, list the full path to that file.
- IMPORTANT: write specific changes to make in XLM format 

