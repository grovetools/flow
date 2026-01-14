---
id: implement
title: "Implement {{ .PlanName }}"
status: pending
type: headless_agent
depends_on:
  - 03-generate-plan.md
---

Implement the "{{ .PlanName }}" feature based on the implementation plan in `03-generate-plan.md`.

Please create the necessary files and write the code to fulfill all requirements.

Don't completely take `03-generate-plan` as truth, also refer to original spec and verify the generated plan is good.

`01-cx.md` might contain implementation discussion too but the chat log could be too large, and you'll need to grep.

Use `cx list` to see files that could be relevant to the work, but feel free to go beyond if necessary.

When done with the feature, invoke the `cx-builder` skill to curate the context for the next phase, which will be to update existing _test.go or e2e tests, or add new ones to test what was developed here.

Feel free to add additional instructions to the prompt for the next `05-spec-tests.md`.

Be sure to provide relevant examples using existing high quality tests, adding them to the context.