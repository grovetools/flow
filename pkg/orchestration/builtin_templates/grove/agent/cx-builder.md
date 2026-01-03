---
description: "An interactive session to curate context for a new feature."
type: interactive_agent
---
You are now in an interactive session to build the context for this feature plan.

**Finding other plan files:** If you need to locate other job files in this plan (such as a specification file), use `flow plan status --json` to get the full paths to all jobs in the current plan.

**Your goal:** Use the `cx` (context) tool to define the set of files that will be provided to subsequent planning and implementation jobs.

## Workflow

### 1. Understand the feature requirements
- Read any specification or planning files to understand what code will be involved
- Identify which packages, modules, or subsystems are relevant

### 2. Assess current context
```bash
cx stats        # Shows files, tokens, size, and distribution
cx rules list   # Shows available preset rule configurations
cx list         # Lists all files currently in context
```

### 3. Decide on context strategy

**Should tests be included?**
- **Exclude tests** (`dev-no-tests`) when:
  - Writing new features or fixing bugs in implementation code
  - The spec focuses on adding functionality, not testing
  - You need to understand existing test patterns first

- **Include tests** (`dev-with-tests`) when:
  - The spec explicitly mentions test behavior or test failures
  - Fixing test-related issues
  - Understanding how existing features are tested
  - **When including tests, also add grove-tend with tests** (`dev-with-tests`) for test framework context and examples

**For focused features** (specific subsystem or package):
- Include only the relevant packages/directories
- Exclude CLI/UI layers if working on core logic
- Exclude documentation and tooling unless needed

**For cross-cutting features** (touches many areas):
- Use broader patterns with appropriate test exclusion
- Consider using preset rules like `dev-no-tests` or `dev-with-tests`

### 4. Edit context rules
Edit `.grove/rules` to control what's included. Common patterns:
```
# Include specific packages
pkg/orchestration/*.go
pkg/exec/*.go

# Include everything with exclusions
*.go
!*_test.go
!tests/

# Exclude entire directories
!cmd/
!tools/
```

### 4a. Consider cross-repository dependencies

**Many features touch code in other grove-ecosystem repositories.** Check if your feature involves:
- **grove-core**: Logging, workspace management, CLI utilities, configuration
- **grove-context**: Context building, file discovery, rule processing
- **grove-gemini**: LLM client, API interactions

**Adding external repositories:**
```
# Include all non-test files from another repo
@a:grove-ecosystem:grove-core::dev-no-tests

# Include with tests (and add grove-tend with its tests as examples)
@a:grove-ecosystem:grove-core::dev-with-tests
@a:grove-ecosystem:grove-tend::dev-with-tests

# Include specific files matching a pattern (more focused)
@a:grove-ecosystem:grove-core @grep: "logger"
@a:grove-ecosystem:grove-core @grep: "workspace"

# Combine multiple repositories
@a:grove-ecosystem:grove-core @grep: "logger"
@a:grove-ecosystem:grove-gemini @grep: "client"
```

**Common cross-repo patterns:**
- **Logging issues**: Add `@a:grove-ecosystem:grove-core @grep: "logger"`
- **Workspace/worktree**: Add `@a:grove-ecosystem:grove-core @grep: "workspace"`
- **Context building**: Add `@a:grove-ecosystem:grove-context::dev-no-tests`
- **LLM interactions**: Add `@a:grove-ecosystem:grove-gemini @grep: "client"`
- **Test-related work**: Use `dev-with-tests` + add `@a:grove-ecosystem:grove-tend::dev-with-tests` (includes test examples)

### 5. Regenerate and verify
```bash
cx generate     # Regenerate context with new rules
cx stats        # Verify the changes (token count, file count)
cx list | grep <key-pattern>  # Confirm critical files are present
```

### 6. Optimize if needed
- Aim for focused context: only what's needed for the feature
- Typical range: 40-100 files, 50k-150k tokens for focused features
- If over 200k tokens, look for opportunities to prune further

## Key Commands Reference

- `cx stats` - View context statistics (files, tokens, size)
- `cx list` - List all files in current context
- `cx rules list` - Show available preset rule configurations
- `cx edit` - Open `.grove/rules` in your editor
- `cx generate` - Regenerate context after editing rules
- `cx diff <preset>` - Compare current context with a preset
