# Recipes and Templates

Grove Flow uses recipes and templates to automate common workflows and promote reusability. Recipes scaffold entire multi-job plans, while job templates provide blueprints for individual jobs.

## Job Templates

Job templates are reusable definitions for a single job. They allow you to quickly add standardized jobs to any plan without rewriting the same prompt and configuration.

### Using a Job Template

To use a template, use the `--template` flag with the `flow plan add` command. The template's default settings and prompt will be used to create the new job file.

```bash
# Add a new job using the 'code-review' template
flow plan add --title "Review API Endpoints" --template code-review
```

Any additional flags, such as `--prompt` or `--source-files`, will supplement the template's content.

### Listing Available Templates

To see all available job templates, including built-in, user-defined, and project-specific ones, use the `templates list` command:

```bash
flow plan templates list
```

```text
NAME                   SOURCE     DESCRIPTION
agent-run                [Built-in] Generates a plan an LLM agent carries out
api-design               [Built-in] Design a RESTful or GraphQL API
architecture-overview    [Built-in] Generate an architecture overview
chat                     [Built-in] 
code-review              [Built-in] Perform a thorough code review
...
my-custom-template       [User]     A custom template for team-specific tasks
```

### Creating Custom Job Templates

You can create your own job templates in two locations:

1.  **Project-specific**: Create a `.grove/job-templates/` directory in your project root. Any `.md` file in this directory is available as a template.
2.  **User-global**: Create a `~/.config/grove/job-templates/` directory. Templates here are available across all your projects.

A template is a standard Markdown job file. The frontmatter defines default settings, and the body serves as the base prompt.

**Example: `.grove/job-templates/test-strategy.md`**

```yaml
---
description: "Create a comprehensive test strategy"
type: "oneshot"
model: "gemini-2.5-pro"
---

Develop a comprehensive testing strategy for this feature. Analyze the existing test patterns and consider the balance between different test levels (unit, integration, e2e).
```

### Built-in Job Templates

Grove Flow includes several built-in templates for common development tasks:

| Template Name            | Description                                        |
| ------------------------ | -------------------------------------------------- |
| `agent-run`              | Generates a detailed plan for an agent to execute. |
| `api-design`             | Designs a RESTful or GraphQL API.                  |
| `architecture-overview`  | Generates an architecture overview of a codebase.  |
| `code-review`            | Performs a thorough code review.                   |
| `deployment-runbook`     | Creates a production deployment runbook.           |
| `documentation`          | Updates project documentation.                     |
| `generate-plan`          | Generates a multi-step plan from a specification.  |
| `incident-postmortem`    | Analyzes an incident and creates action items.     |
| `learning-guide`         | Creates a learning guide for a codebase.           |
| `refactoring-plan`       | Plans a large-scale refactoring effort.            |
| `security-audit`         | Conducts a security audit of a codebase.           |
| `test-strategy`          | Creates a comprehensive test strategy.             |

## Plan Recipes

Plan recipes are complete, multi-job plan scaffolds. They are used with `flow plan init` to create a new plan directory pre-populated with a sequence of dependent jobs, providing a ready-to-run workflow for common tasks.

### Using a Plan Recipe

To initialize a plan from a recipe, use the `--recipe` flag with the `init` command.

```bash
# Initialize a new plan for a feature using the standard-feature recipe
flow plan init new-auth-feature --recipe standard-feature
```

This command creates the `plans/new-auth-feature` directory and populates it with the jobs defined in the `standard-feature` recipe, such as `01-spec.md`, `02-implement.md`, and so on.

### Built-in Recipes

Grove Flow provides several built-in recipes:

-   **`standard-feature`**: A typical feature development workflow: Spec -> Implement -> Git Status Checks -> Review.
-   **`chat-workflow`**: A workflow that starts with an open-ended `chat` job for exploration, followed by implementation and review.
-   **`chat`**: A minimal plan containing only a single `chat` job. This is the default recipe.
-   **`docgen-customize`**: A two-step workflow for generating project documentation: first, a chat job to plan the documentation structure, followed by an agent job to write the documentation.

### Creating Custom Recipes

You can create globally available recipes by adding them to `~/.config/grove/recipes/`. Each recipe is a directory containing the Markdown job files that make up the plan.

**Example Structure:**

```
~/.config/grove/recipes/
└── my-release-workflow/
    ├── 01-update-changelog.md
    ├── 02-run-tests.md
    └── 03-tag-release.md
```

With this structure, you can run `flow plan init my-project-release --recipe my-release-workflow`.

### Template Variables in Recipes

Recipe files can be parameterized using Go's template syntax. This allows you to create dynamic and context-aware plans.

#### Standard Variables

-   `{{ .PlanName }}`: Automatically substituted with the name of the plan being initialized.

**Example: `01-spec.md` from the `standard-feature` recipe**

```yaml
---
id: spec
title: "Specification for {{ .PlanName }}"
status: pending
type: oneshot
---

Define the detailed specification for the "{{ .PlanName }}" feature.
```

When you run `flow plan init new-login-flow --recipe standard-feature`, `{{ .PlanName }}` becomes `new-login-flow`.

#### Custom Variables

You can define and pass your own variables using the `--recipe-vars` flag.

-   **Syntax**: `{{ .Vars.<variable_name> }}`
-   **Passing Variables**:
    -   Multiple flags: `--recipe-vars key1=val1 --recipe-vars key2=val2`
    -   Comma-delimited: `--recipe-vars "key1=val1,key2=val2"`

**Example: A recipe job `01-chat.md` that uses a custom `model` variable:**

```yaml
---
title: "Chat about {{ .PlanName }}"
type: chat
status: pending_user
{{ if .Vars.model }}model: "{{ .Vars.model }}"{{ end }}
---

Let's discuss the plan.
```

**Command to pass the variable:**

```bash
flow plan init my-plan --recipe my-chat-recipe --recipe-vars model=gemini-2.5-pro
```

You can also set default variables for recipes in your `grove.yml` file:

```yaml
# .grove/config.yml or grove.yml
flow:
  recipes:
    docgen-customize:
      vars:
        model: "gemini-2.5-pro"
        rules_file: "docs/docs.rules"
```

CLI flags will always override defaults set in `grove.yml`.

### Dynamic Recipe Loading

For advanced use cases, Grove Flow can load recipes dynamically from an external command. This is configured in `grove.yml` with the `get_recipe_cmd` setting.

```yaml
# grove.yml
flow:
  recipes:
    # This command must output a JSON object where keys are recipe names
    get_recipe_cmd: "my-cli-tool recipes --json"
```

The specified command must output a JSON object where each key is a recipe name and the value is an object containing `description` and a `jobs` map.

**Example JSON Output from `get_recipe_cmd`:**

```json
{
  "company-microservice": {
    "description": "Standard microservice template for our company.",
    "jobs": {
      "01-setup.md": "---\ntitle: Setup {{ .PlanName }}\n---\nSetup the service.",
      "02-deploy.md": "---\ntitle: Deploy {{ .PlanName }}\n---\nDeploy the service."
    }
  }
}
```

When listing or initializing recipes, Grove Flow will execute this command to discover available recipes.

**Precedence Order**: When a recipe name exists in multiple sources, the order of precedence is: **User** > **Dynamic** > **Built-in**.