package orchestration

// InitialPlanTemplate is the template for the initial planning job.
const InitialPlanTemplate = `---
id: {{ .ID }}
title: "Create High-Level Implementation Plan"
status: pending
type: oneshot
prompt_source:
  - spec.md
output:
  type: generate_jobs
---

You are a senior software architect AI responsible for creating detailed, multi-step implementation plans.
You are operating within the Grove orchestration framework.

Your task is to analyze the feature request in spec.md and generate a comprehensive execution plan with detailed instructions for each job.

**IMPORTANT: Create 3-5 focused jobs (context updates will be added automatically):**
- Each job should be completable in one session
- Split large features into multiple agent jobs if needed
- Always end with a oneshot review job
- Focus on single-responsibility principle for each job

**CRITICAL: YOUR OUTPUT MUST BE A JSON OBJECT**

Generate a JSON object with a "jobs" array containing the job definitions.
Do NOT include any preamble, explanation, or commentary. Output only valid JSON.

**JOB DEFINITION FORMAT:**
Each job in the JSON array must have:
- title: Simple kebab-case title (e.g., "setup-backend", "implement-api", "run-tests")
- type: one of "oneshot", "agent", or "shell"
- depends_on: array of job titles this job depends on (use exact titles)
- prompt: detailed instructions for the job (be specific about what to implement) - defaults to the plan directory name if not specified

**IMPORTANT PROMPT GUIDELINES:**
- Each job should be achievable by an LLM coding agent in one session/PR
- Use direct, technical language - no timelines or corporate speak  
- Be concise - use bullet points or short sentences, not numbered lists
- Do NOT include code blocks or shell commands - just describe what to create
- Be specific about file paths (e.g., "backend/src/routes/todos.ts")
- Break large features into focused, single-responsibility jobs
- Avoid explaining how to do things - just state what needs to be done
- Keep job titles simple and in kebab-case to ensure clean dependency graphs

**REQUIRED JOB TYPES:**
- agent: For writing code (creates files, implements features)
- shell: For running commands (context updates will be added automatically)
- oneshot: For planning, review, or analysis tasks

**CRITICAL RULES:**
1. EVERY job MUST include "status: pending" in the frontmatter
2. Use clear dependencies to ensure proper execution order
3. First job should have empty depends_on: []

**EXAMPLE OF EXPECTED JSON OUTPUT:**

{
  "jobs": [
    {
      "title": "setup-project",
      "type": "agent",
      "depends_on": [],
      "prompt": "Set up monorepo structure:\n- Create backend/ and frontend/ directories\n- backend/package.json: Express, TypeScript, Jest dependencies. Scripts: dev (nodemon), build (tsc), test (jest)\n- backend/tsconfig.json: target ES2020, module commonjs, outDir ./dist\n- backend/src/ with subdirs: routes/, models/, middleware/, utils/\n- frontend/: Use create-react-app with TypeScript template\n- Root .gitignore: node_modules, dist, build, .env\n- Root README.md with setup instructions for both apps"
    },
    {
      "title": "implement-backend",
      "type": "agent",
      "depends_on": ["setup-project"],
      "prompt": "Implement TODO app core features:\n\nBackend:\n- backend/src/models/todo.ts: interface Todo { id: number; text: string; completed: boolean }\n- backend/src/store/todoStore.ts: class with private todos array, methods: getAll(), add(text), toggle(id), delete(id)\n- backend/src/app.ts: Express setup with cors, json middleware, error handler\n- backend/src/routes/todos.ts: Router with endpoints GET /api/todos, POST /api/todos (validate text), PUT /api/todos/:id, DELETE /api/todos/:id\n- backend/src/middleware/logger.ts: Simple request logger\n- backend/tests/todos.test.ts: Test each endpoint with supertest\n\nFrontend:\n- frontend/src/api/todoApi.ts: Functions wrapping fetch() for each endpoint\n- frontend/src/components/TodoList.tsx, TodoItem.tsx, AddTodo.tsx\n- frontend/src/App.tsx: Main component managing state with useState\n- frontend/src/App.css: Basic styling"
    },
    {
      "title": "review-implementation",
      "type": "oneshot",
      "depends_on": ["implement-backend"],
      "prompt": "Review TODO app against spec.md requirements:\n- Check backend/src/routes/todos.ts has all CRUD endpoints\n- Verify backend/src/store/todoStore.ts maintains state correctly\n- Confirm frontend can list, add, complete, delete todos\n- Test error cases: invalid input, non-existent IDs\n- Verify TypeScript types are used throughout\n- Check backend/tests coverage of endpoints\n- Ensure README.md has install and run instructions\nOutput concise list of any missing requirements or bugs found."
    }
  ]
}

**NOW GENERATE YOUR ACTUAL PLAN STARTING HERE:**
`

// AgentJobTemplate is the template for agent jobs.
const AgentJobTemplate = `---
id: {{ .ID }}
title: "{{ .Title }}"
status: pending
type: {{ .Type }}
plan_type: {{ .PlanType }}{{ if .DependsOn }}
depends_on:{{ range .DependsOn }}
  - {{ . }}{{ end }}{{ end }}{{ if .PromptSource }}
prompt_source:{{ range .PromptSource }}
  - {{ . }}{{ end }}{{ end }}{{ if .Repository }}
repository: {{ .Repository }}{{ end }}{{ if .Branch }}
branch: {{ .Branch }}{{ end }}{{ if .Worktree }}
worktree: {{ .Worktree }}{{ end }}{{ if .AgentContinue }}
agent_continue: true{{ end }}
output:
  type: {{ .OutputType }}
---

{{ .Prompt }}
`

// InitialJobContent is the template for the initial job file.
const InitialJobContent = `---
id: %s
title: "Create High-Level Implementation Plan"
status: pending
type: oneshot%s
prompt_source:
  - spec.md
output:
  type: generate_jobs
---

You are a senior software architect AI responsible for creating detailed, multi-step implementation plans.
You are operating within the Grove orchestration framework.

Your task is to analyze the feature request in spec.md and generate a comprehensive execution plan with detailed instructions for each job.

**IMPORTANT: Create 3-5 focused jobs (context updates will be added automatically):**
- Each job should be completable in one session
- Split large features into multiple agent jobs if needed
- Always end with a oneshot review job
- Focus on single-responsibility principle for each job

**CRITICAL: YOUR OUTPUT MUST BE A JSON OBJECT**

Generate a JSON object with a "jobs" array containing the job definitions.
Do NOT include any preamble, explanation, or commentary. Output only valid JSON.

**JOB DEFINITION FORMAT:**
Each job in the JSON array must have:
- title: Simple kebab-case title (e.g., "setup-backend", "implement-api", "run-tests")
- type: one of "oneshot", "agent", or "shell"
- depends_on: array of job titles this job depends on (use exact titles)
- prompt: detailed instructions for the job (be specific about what to implement) - defaults to the plan directory name if not specified

**IMPORTANT PROMPT GUIDELINES:**
- Each job should be achievable by an LLM coding agent in one session/PR
- Use direct, technical language - no timelines or corporate speak  
- Be concise - use bullet points or short sentences, not numbered lists
- Do NOT include code blocks or shell commands - just describe what to create
- Be specific about file paths (e.g., "backend/src/routes/todos.ts")
- Break large features into focused, single-responsibility jobs
- Avoid explaining how to do things - just state what needs to be done
- Keep job titles simple and in kebab-case to ensure clean dependency graphs

**REQUIRED JOB TYPES:**
- agent: For writing code (creates files, implements features)
- shell: For running commands (context updates will be added automatically)
- oneshot: For planning, review, or analysis tasks

**CRITICAL RULES:**
1. EVERY job MUST include "status: pending" in the frontmatter
2. Use clear dependencies to ensure proper execution order
3. First job should have empty depends_on: []

**EXAMPLE OF EXPECTED JSON OUTPUT:**

{
  "jobs": [
    {
      "title": "setup-project",
      "type": "agent",
      "depends_on": [],
      "prompt": "Set up monorepo structure:\n- Create backend/ and frontend/ directories\n- backend/package.json: Express, TypeScript, Jest dependencies. Scripts: dev (nodemon), build (tsc), test (jest)\n- backend/tsconfig.json: target ES2020, module commonjs, outDir ./dist\n- backend/src/ with subdirs: routes/, models/, middleware/, utils/\n- frontend/: Use create-react-app with TypeScript template\n- Root .gitignore: node_modules, dist, build, .env\n- Root README.md with setup instructions for both apps"
    },
    {
      "title": "implement-backend",
      "type": "agent",
      "depends_on": ["setup-project"],
      "prompt": "Implement TODO app core features:\n\nBackend:\n- backend/src/models/todo.ts: interface Todo { id: number; text: string; completed: boolean }\n- backend/src/store/todoStore.ts: class with private todos array, methods: getAll(), add(text), toggle(id), delete(id)\n- backend/src/app.ts: Express setup with cors, json middleware, error handler\n- backend/src/routes/todos.ts: Router with endpoints GET /api/todos, POST /api/todos (validate text), PUT /api/todos/:id, DELETE /api/todos/:id\n- backend/src/middleware/logger.ts: Simple request logger\n- backend/tests/todos.test.ts: Test each endpoint with supertest\n\nFrontend:\n- frontend/src/api/todoApi.ts: Functions wrapping fetch() for each endpoint\n- frontend/src/components/TodoList.tsx, TodoItem.tsx, AddTodo.tsx\n- frontend/src/App.tsx: Main component managing state with useState\n- frontend/src/App.css: Basic styling"
    },
    {
      "title": "review-implementation",
      "type": "oneshot",
      "depends_on": ["implement-backend"],
      "prompt": "Review TODO app against spec.md requirements:\n- Check backend/src/routes/todos.ts has all CRUD endpoints\n- Verify backend/src/store/todoStore.ts maintains state correctly\n- Confirm frontend can list, add, complete, delete todos\n- Test error cases: invalid input, non-existent IDs\n- Verify TypeScript types are used throughout\n- Check backend/tests coverage of endpoints\n- Ensure README.md has install and run instructions\nOutput concise list of any missing requirements or bugs found."
    }
  ]
}

**NOW GENERATE YOUR ACTUAL PLAN STARTING HERE:**
`

