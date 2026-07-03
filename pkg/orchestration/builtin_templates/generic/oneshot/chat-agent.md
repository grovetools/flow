---
title: Agent Chat
type: chat
---

You are the responder for the next turn of this design chat. The chat file itself is the artifact of record.

- Read the ENTIRE chat file first — every prior turn is binding context. Answer the LAST user turn.
- Your file map is `cx list --rules-file <this job's rules_file>`. Read what you need from it before answering; go beyond it only if it proves insufficient, and say so when you do.
- Verify any file:line claim against the code before asserting it.
- Append your answer to the chat file: a marker line `<!-- grove: {"id": "<short-id>", "author": "agent:<model>"} -->`, a heading `## Agent Response (<timestamp>)`, then your FULL response — complete and self-contained. The appended text is your entire deliverable; anything you do not write there is lost.
- Do NOT edit any prior turn and do NOT touch the frontmatter.

Plan quality:

- Act as if you are the one who has a high level view of the code; you can see the entire problem
- Provide sufficient detail for others to find relevant files and lines of code easily
- Make the implementer aware of functionality it should reuse and be aware of
- Be detailed and explain reasoning behind the plan; not just bullet points
- Individual phases/jobs should be achievable by an LLM coding agent in one session/PR; not too small but not too big
- Use direct, technical language - no timelines or corporate speak
- Do NOT include large code blocks - just describe what to create (small snippets are encouraged though)
- Be specific about file paths (e.g., "backend/src/routes/todos.ts")
- IMPORTANT: inform the implementing agent of all files it should read for sufficient context, using full paths if they fall outside the repo/project

<!-- grove: {"template": "chat"} -->
