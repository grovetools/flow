---
id: generate-docs
title: "Generate Documentation"
status: pending
type: interactive_agent
depends_on:
  - 01-customize-docs.md
---

Implement the documentation plan defined in `01-customize-docs.md`.

Your task is to generate comprehensive documentation based on the agreed-upon plan from the previous chat session. Follow these guidelines:

1. **Review the Plan**: First, review the final documentation plan from the chat job to understand the exact requirements.

2. **Generate Documentation**: For each section defined in the plan:
   - Use the context from {{ if .Vars.rules_file }}`{{ .Vars.rules_file }}`{{ else }}your documentation rules file{{ end }} to understand the codebase
   - Generate documentation content following the specific prompts agreed upon
   - Ensure consistency in tone and style across all sections
   - Include code examples where appropriate

3. **File Organization**: 
   - All documentation must be written to {{ if .Vars.output_dir }}the `{{ .Vars.output_dir }}` directory{{ else }}the documentation directory (typically `docs/`){{ end }}
   - Follow the file structure and naming conventions defined in the plan
   - Do not modify any files outside of the documentation directory

4. **Quality Standards**:
   - Ensure technical accuracy by referencing actual code
   - Use clear, concise language appropriate for the target audience
   - Include practical examples and use cases
   - Maintain consistent formatting throughout

5. **Documentation Format**:
   - Use Markdown format for all documentation files
   - Include proper headings, code blocks, and formatting
   - Add navigation links between related sections where appropriate

Execute the documentation generation plan systematically, creating each section as specified. If the project has `docgen` configured, you may use `docgen generate` command to assist with generation.

Remember: Focus solely on generating documentation. Do not modify source code or any files outside the designated documentation directory.