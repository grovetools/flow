# Using prepend_dependencies Feature

## Overview

The `prepend_dependencies` feature allows you to inline dependency content directly into the prompt body instead of uploading it as separate files. This gives dependency content higher priority in the LLM's attention.

---

## Method 1: CLI Flag

Add the `--prepend-dependencies` flag when creating a job:

```bash
flow plan add myplan \
  --title "Implement Features" \
  --type oneshot \
  --depends-on 01-spec.md \
  --prepend-dependencies \
  -p "Implement the features from the spec"
```

This will create a job with `prepend_dependencies: true` in its frontmatter.

---

## Method 2: Plan-Level Default

Set it as a default for all jobs in a plan by adding it to `.grove-plan.yml`:

```yaml
# .grove-plan.yml

# Default model for jobs in this plan
model: gemini-2.5-pro

# Default worktree for agent jobs
worktree: my-feature

# Inline dependency content into prompt body by default
prepend_dependencies: true
```

Now all jobs created in this plan will have `prepend_dependencies: true` by default.

You can set this using the config command:

```bash
flow plan config myplan --set prepend_dependencies=true
```

---

## Method 3: Job Frontmatter

Manually add it to a job's frontmatter:

```markdown
---
id: implement
title: Implement Features
type: oneshot
model: gemini-2.0-flash-exp
prepend_dependencies: true
depends_on:
  - 01-spec.md
  - 02-design.md
---

Implement the features described in the dependencies.
```

---

## Behavior

### WITHOUT prepend_dependencies (default):

```
📎 Adding 1 dependency as separate file:
     • 01-spec.md (uploaded as file attachment)

📤  Uploading 2 files for request...
  ✅  context (1.5s)
  ✅  01-spec.md (1.0s)
```

**Result**: Dependency uploaded as a separate file attachment.

### WITH prepend_dependencies: true:

```
🔗 prepend_dependencies enabled - inlining dependency content into prompt body
   Prepending 1 dependency to prompt:
     • 01-spec.md (inlined, not uploaded as file)

📤  Uploading 1 files for request...
  ✅  context (1.5s)
```

**Result**: Dependency content prepended to prompt body, not uploaded separately.

---

## Priority Order

When determining if prepend_dependencies should be enabled:

1. **CLI Flag** (`--prepend-dependencies`) - highest priority
2. **Job Frontmatter** (`prepend_dependencies: true` in the .md file)
3. **Plan Config** (`.grove-plan.yml` setting)
4. **Default** (`false`)

---

## Use Cases

### ✅ Good use cases for prepend_dependencies:

- **Specifications**: When implementing features based on a detailed spec
- **Critical Context**: When dependencies contain must-read information
- **Small Dependencies**: When dependency files are reasonably sized
- **Priority Content**: When you want to ensure the LLM focuses on dependency content first

### ❌ When NOT to use prepend_dependencies:

- **Large Dependencies**: Files with lots of output that would bloat the prompt
- **Reference Material**: Content that's useful but not critical
- **Many Dependencies**: When you have 5+ dependency files
- **Binary/Media Files**: These should always be separate attachments

---

## Examples

### Example 1: Feature Implementation Plan

```bash
# Initialize plan with prepend_dependencies as default
flow plan init feature-auth --set prepend_dependencies=true

# Add spec job (no dependencies)
flow plan add feature-auth \
  --title "Create Specification" \
  --type oneshot \
  -p "Create a spec for user authentication"

# Add implementation job (will use plan default)
flow plan add feature-auth \
  --title "Implement Auth" \
  --type oneshot \
  --depends-on 01-create-specification.md \
  -p "Implement the authentication feature"
```

### Example 2: Override Plan Default

```bash
# Plan has prepend_dependencies: true by default
# But for this specific job, we want normal file upload
flow plan add feature-auth \
  --title "Generate Tests" \
  --type oneshot \
  --depends-on 02-implement-auth.md \
  -p "Generate unit tests"
  # Note: no --prepend-dependencies flag, will use plan default

# To override the plan default, manually edit the job frontmatter to set:
# prepend_dependencies: false
```

---

## Verification

Check if a job has prepend_dependencies enabled:

```bash
# View job file
cat plans/myplan/02-implement.md

# Look for in frontmatter:
# prepend_dependencies: true
```

Or run the job and watch for the logging:

```
🔗 prepend_dependencies enabled - inlining dependency content into prompt body
   Prepending 1 dependency to prompt:
     • 01-spec.md (inlined, not uploaded as file)
```

---

## Tips

1. **Start with plan-level default** if most jobs in a plan need this behavior
2. **Use CLI flag** for one-off jobs that need different behavior
3. **Monitor prompt sizes** - inlining large dependencies can bloat prompts
4. **Check file upload counts** - should be 1 less per inlined dependency
5. **Use for sequential workflows** - great for spec → implementation → testing flows
