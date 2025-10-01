# Recipes and Templates

Grove Flow uses recipes for creating multi-job plans from a scaffold, and job templates for creating individual jobs from a blueprint.

## Job Templates

A job template is a Markdown file containing frontmatter and a prompt body. It serves as a blueprint for creating new, individual jobs.

### Using a Job Template

The `flow plan add --template` command creates a new job file using the frontmatter and body from a specified template file.

```bash
# Add a new job using the 'code-review' template
flow plan add --title "Review API Endpoints" --template code-review
```

Flags like `--prompt` or `--source-files` provide additional content to the job being created from the template.

### Listing Available Templates

The `flow plan templates list` command displays all available job templates from built-in, user-global (`~/.config/grove/job-templates/`), and project-specific (`.grove/job-templates/`) locations.

```bash
flow plan templates list
```

```text
NAME                   SOURCE     DESCRIPTION
agent-run              [Built-in] Generates a plan an LLM agent carries out
api-design             [Built-in] Design a RESTful or GraphQL API
...
my-custom-template     [User]     A custom template for team-specific tasks
```

### Creating Custom Job Templates

Job templates are located in two directories, with project-specific templates overriding user-global ones:

1.  **Project-specific**: `.grove/job-templates/` within a project root.
2.  **User-global**: `~/.config/grove/job-templates/`.

Each template is a Markdown file. The frontmatter defines default settings for the job, and the body serves as the base prompt.

**Example: `.grove/job-templates/test-strategy.md`**

```yaml
---
description: "Create a test strategy"
type: "oneshot"
model: "gemini-2.5-pro"
---

Develop a testing strategy for this feature. Analyze existing test patterns and consider the balance between unit, integration, and e2e tests.
```

### Built-in Job Templates

Grove Flow includes several built-in templates.

| Template Name            | Description                                        |
| ------------------------ | -------------------------------------------------- |
| `agent-run`              | Generates a plan an LLM agent carries out.         |
| `agent-xml`              | Generates a detailed XML plan an LLM agent carries out. |
| `api-design`             | Designs a RESTful or GraphQL API.                  |
| `architecture-overview`  | Generates an architecture overview of a codebase.  |
| `chat`                   | A template for conversational chat jobs.           |
| `code-review`            | Performs a code review.                            |
| `deployment-runbook`     | Creates a production deployment runbook.           |
| `documentation`          | Updates project documentation.                     |
| `generate-plan`          | Generates a multi-step plan from a specification.  |
| `incident-postmortem`    | Analyzes an incident and creates action items.     |
| `learning-guide`         | Creates a learning guide for a codebase.           |
| `learning-lang`          | Creates a language learning guide from code.       |
| `migration-plan`         | Plans a technology or database migration.          |
| `performance-analysis`   | Analyzes and suggests optimizations for performance.|
| `refactoring-plan`       | Plans a large-scale refactoring effort.            |
| `refine-plan-generic`    | A generic template for refining a plan.            |
| `security-audit`         | Conducts a security audit of a codebase.           |
| `tech-debt-assessment`   | Assesses and prioritizes technical debt.           |
| `test-strategy`          | Creates a test strategy.                           |

## Plan Recipes

Plan recipes are directories containing a set of job files that define a multi-job workflow. They are used to scaffold new plans.

### Using a Plan Recipe

The `flow plan init --recipe` command creates a new directory and populates it with the job files defined in the specified recipe.

```bash
# Initialize a plan using the standard-feature recipe
flow plan init new-auth-feature --recipe standard-feature
```

This command creates the `plans/new-auth-feature` directory and populates it with jobs like `01-spec.md` and `02-implement.md` from the recipe.

### Built-in Recipes

Grove Flow provides several built-in recipes:

-   **`standard-feature`**: A workflow for feature development: Spec -> Implement -> Git Status Checks -> Review.
-   **`chat-workflow`**: A workflow that starts with a `chat` job for exploration, followed by implementation and review.
-   **`chat`**: A minimal plan containing only a single `chat` job. This is the default recipe for `flow plan init`.
-   **`docgen-customize`**: A workflow for generating documentation: first a `chat` job to define the structure, then an `agent` job to write the content.

### Creating Custom Recipes

User-defined recipes can be created by adding directories to `~/.config/grove/recipes/`. Each subdirectory is a recipe containing the `.md` job files that compose the plan.

**Example Structure:**

```
~/.config/grove/recipes/
└── my-release-workflow/
    ├── 01-update-changelog.md
    ├── 02-run-tests.md
    └── 03-tag-release.md
```

This structure enables running `flow plan init my-project-release --recipe my-release-workflow`.

## Templating in Recipes

Recipe files can contain template variables using Go's template syntax.

### Standard Variables

-   `{{ .PlanName }}`: This variable is replaced with the name of the plan being initialized.

**Example: `01-spec.md` from the `standard-feature` recipe**

```yaml
---
id: spec
title: "Specification for {{ .PlanName }}"
status: pending
type: oneshot
---

Define the specification for the "{{ .PlanName }}" feature.
```

When running `flow plan init new-login-flow --recipe standard-feature`, `{{ .PlanName }}` is replaced with `new-login-flow`.

### Custom Variables

The `--recipe-vars` flag passes key-value pairs to a recipe. These are accessible via the `{{ .Vars.<key> }}` syntax.

-   **Passing Variables**:
    -   Multiple flags: `--recipe-vars key1=val1 --recipe-vars key2=val2`
    -   Comma-delimited: `--recipe-vars "key1=val1,key2=val2"`

**Example: A recipe job `01-chat.md` using a custom `model` variable:**

```yaml
---
title: "Chat about {{ .PlanName }}"
type: chat
{{ if .Vars.model }}model: "{{ .Vars.model }}"{{ end }}
---

Let's discuss the plan.
```

**Command to pass the variable:**

```bash
flow plan init my-plan --recipe my-chat-recipe --recipe-vars model=gemini-2.5-pro
```

Default variables can be set for a recipe in `grove.yml`. CLI flags override `grove.yml` settings.

```yaml
# grove.yml
flow:
  recipes:
    docgen-customize:
      vars:
        model: "gemini-2.5-pro"
        rules_file: "docs/docs.rules"
```

## Dynamic Recipe Loading

Recipes can be loaded by executing an external command defined by `get_recipe_cmd` in `grove.yml`.

```yaml
# grove.yml
flow:
  recipes:
    get_recipe_cmd: "my-cli-tool recipes --json"
```

The command must output a JSON object where each key is a recipe name and the value is an object containing `description` and a `jobs` map.

**Example JSON Output:**

```json
{
  "company-microservice": {
    "description": "Standard microservice template.",
    "jobs": {
      "01-setup.md": "---\ntitle: Setup {{ .PlanName }}\n---\nSetup the service.",
      "02-deploy.md": "---\ntitle: Deploy {{ .PlanName }}\n---\nDeploy the service."
    }
  }
}
```

When a recipe name exists in multiple sources, the order of precedence is: **User** > **Dynamic** > **Built-in**.