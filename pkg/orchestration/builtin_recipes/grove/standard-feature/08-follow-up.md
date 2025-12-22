---
id: "{{ .PlanName }}-follow-up-{{ .Vars.uuid }}"
title: "Follow-up & Tweaks"
status: pending
type: interactive_agent
depends_on:
  - 02-spec.md
  - 03-generate-plan.md
  - 04-implement.md
  - 05-spec-tests.md
  - 06-impl-tests.md
  - 07-review.md
---
Read all previous job files in this plan to understand the full context:
- 02-spec.md: The feature specification
- 03-generate-plan.md: The implementation plan
- 04-implement.md: The implementation job
- 05-spec-tests.md: The test specification
- 06-impl-tests.md: The e2e test implementation
- 07-review.md: The code review feedback

After reviewing all the plan files, critically evaluate whether the specification goals have been met:

1. **Review the original specification** from 02-spec.md - What were the core requirements, user stories, and acceptance criteria?

2. **Examine the reviewer's assessment** from 07-review.md - What issues, concerns, or gaps did they identify?

3. **Conduct your own analysis** - Review the implementation and form your own independent assessment of whether the spec goals have been achieved.

4. **Synthesize your findings** - Combine the reviewer's evaluation with your own critical analysis to identify:
   - Any unmet requirements or acceptance criteria
   - Quality issues or technical debt
   - Missing edge cases or error handling
   - Opportunities for improvement

5. **Present your assessment to the user** - Share your analysis and ask if they would like to address any gaps, implement reviewer suggestions, or make any final tweaks or additions to the implementation.
