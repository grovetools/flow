---
id: "{{ .PlanName }}-code-review-{{ .Vars.uuid }}"
title: "Code Review for {{ .PlanName }}"
status: pending
type: oneshot
depends_on:
  - 06-impl-tests.md
prepend_dependencies: true
git_changes: true
---

Review the code changes provided in the `<git_changes>` context block for the "{{ .PlanName }}" feature.

**Instructions for Review:**

1. **Analyze the Changes**: Carefully examine the committed, staged, uncommitted, and new files.
2. **Identify Issues**: Look for bugs, logical errors, performance issues, or deviations from the original specification.
3. **Code Style & Best Practices**: Ensure the code adheres to project standards and best practices.
4. **Test Coverage**: Verify that appropriate tests are included.
5. **Provide Feedback**: Write a concise, actionable summary of your findings. If there are issues, provide specific suggestions for improvement. If the implementation is good, approve it.
