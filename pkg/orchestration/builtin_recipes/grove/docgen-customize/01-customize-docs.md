---
id: customize-docs
title: "Customize Documentation Plan"
status: pending_user
type: chat
template: chat{{ if .Vars.model }}
model: "{{ .Vars.model }}"{{ end }}
---

Let's create a custom documentation plan for your project.

I will now act as a technical writer to help you create comprehensive documentation for your project.

First, I will analyze your project's codebase using the context provided by {{ if .Vars.rules_file }}the rules in `{{ .Vars.rules_file }}`{{ else }}your documentation rules file (typically `docs.rules` or `docs/docs.rules`){{ end }}. Based on this analysis, I will propose a documentation structure with appropriate sections such as:

- **Overview**: High-level description of the project
- **Installation**: How to set up and install the project
- **Getting Started**: Quick start guide for new users
- **API Reference**: Detailed API documentation
- **Configuration**: Configuration options and settings
- **Examples**: Code examples and use cases
- **Architecture**: System design and architecture details
- **Contributing**: Guidelines for contributors
- **Troubleshooting**: Common issues and solutions

Please review my proposed documentation structure. You can:
1. Approve the proposed sections
2. Suggest changes or modifications
3. Add new sections specific to your project needs
4. Define specific prompts for generating each section

Once we finalize the documentation plan, the next job will execute it by generating all the documentation files in {{ if .Vars.output_dir }}the `{{ .Vars.output_dir }}` directory{{ else }}your documentation directory (typically `docs/`){{ end }}.

The final output of this chat will be a detailed plan including:
- The exact documentation sections to generate
- The specific prompts for each section
- The file structure and naming conventions
- Any special instructions for the documentation generation