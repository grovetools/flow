# Recipes and Templates

Grove Flow includes features for creating reusable jobs and plans. Job templates define individual jobs, while plan recipes define multi-job workflows.

## Job Templates

A job template is a Markdown file containing frontmatter and a prompt body. It functions as a definition for creating new, individual jobs within a plan.

### Using a Job Template

The `flow plan add` command creates a new job from a template using the `--template` flag.

```bash
# Add a new job using the 'code-review' template
flow plan add --title "Review API Endpoints" --template code-review
```

Command-line flags such as `--prompt` or `--source-files` append additional content or context to the job being created from the template.

### Listing Available Templates

The `flow plan templates list` command displays all available job templates. It searches for templates in the following order of precedence:

1.  Project-specific (`.grove/job-templates/`)
2.  User-global (`~/.config/grove/job-templates/`)
3.  Built-in

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

Job templates are Markdown files located in specific directories. Templates in a project's `.grove/job-templates/` directory override user-global templates with the same name.

1.  **Project-specific**: `.grove/job-templates/`
2.  **User-global**: `~/.config/grove/job-templates/`

The frontmatter of the template file defines default settings for the job, and the body serves as the base prompt.

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

Grove Flow includes the following built-in templates.

| Template Name            | Description                                        |
| ------------------------ | -------------------------------------------------- |
| `agent-run`              | Generates a plan for an LLM agent to execute.      |
| `agent-xml`              | Generates a detailed XML plan for an LLM agent.    |
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

A plan recipe is a directory containing a set of job files that define a multi-job workflow. Recipes are used to create new plans with a predefined structure.

### Using a Plan Recipe

The `flow plan init --recipe` command creates a new plan directory and populates it with copies of the job files from the specified recipe.

```bash
# Initialize a plan using the standard-feature recipe
flow plan init new-auth-feature --recipe standard-feature
```

This command creates the `plans/new-auth-feature` directory and adds the jobs defined in the `standard-feature` recipe, such as `01-spec.md` and `02-implement.md`.

### Built-in Recipes

Grove Flow provides several built-in recipes:

-   **`standard-feature`**: A workflow for feature development: Spec -> Implement -> Git Status Checks -> Review.
-   **`chat-workflow`**: A workflow that starts with a `chat` job for exploration, followed by implementation and review.
-   **`chat`**: A minimal plan containing only a single `chat` job. This is the default recipe for `flow plan init`.
-   **`docgen-customize`**: A workflow for generating documentation: first a `chat` job to define the structure, then an `agent` job to write the content.

### Creating Custom Recipes

User-defined recipes are directories located in `~/.config/grove/recipes/`. Each subdirectory within this location is treated as a recipe, and its `.md` job files are used to compose the new plan.

**Example Directory Structure:**

```
~/.config/grove/recipes/
└── my-release-workflow/
    ├── 01-update-changelog.md
    ├── 02-run-tests.md
    └── 03-tag-release.md
```

This structure enables the command `flow plan init my-project-release --recipe my-release-workflow`.

## Templating in Recipes

Recipe files can contain template variables using Go's `text/template` syntax.

### Standard Variables

-   `{{ .PlanName }}`: This variable is replaced with the name of the plan provided in the `flow plan init` command.

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

When running `flow plan init new-login-flow --recipe standard-feature`, the `{{ .PlanName }}` variable is replaced with `new-login-flow`.

### Custom Variables

The `--recipe-vars` flag is used to pass key-value pairs to a recipe. These variables are accessible in templates via the `{{ .Vars.<key> }}` syntax.

-   **Passing Variables**:
    -   Multiple flags: `--recipe-vars key1=val1 --recipe-vars key2=val2`
    -   Comma-delimited: `--recipe-vars "key1=val1,key2=val2"`

**Example: A recipe job `01-chat.md` that uses a custom `model` variable:**

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

Default variables for a recipe can be defined in `grove.yml`. Variables passed via the CLI override those set in `grove.yml`.

**Example: `grove.yml` with default recipe variables**

```yaml
flow:
  recipes:
    docgen-customize:
      vars:
        model: "gemini-2.5-pro"
        rules_file: "docs/docs.rules"
```

## Dynamic Recipe Loading

Recipes can be loaded by executing an external command. The command is configured via the `get_recipe_cmd` key under the `recipes` section in `grove.yml`.

```yaml
# grove.yml
flow:
  recipes:
    get_recipe_cmd: "my-cli-tool recipes --json"
```

The specified command must output a JSON object to standard output. Each key in the object is a recipe name. The value is an object containing a `description` string and a `jobs` map, where keys are filenames and values are the file contents.

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