# Recipes and Templates

Grove Flow provides powerful automation and reusability features through recipes and templates, enabling you to standardize workflows and quickly bootstrap common project patterns.

## Job Templates

### What are Job Templates?

Job templates are reusable job definitions that provide pre-configured prompts, frontmatter, and best practices for common tasks. They serve as building blocks for creating standardized, high-quality jobs without writing everything from scratch.

### Using Templates in Job Creation

Apply templates when adding jobs to plans:

```bash
# Add job using a template
flow plan add --template code-review --title "Review authentication module"

# Add with dependencies
flow plan add \
  --template api-design \
  --title "User API Design" \
  --depends-on "01-spec.md"

# Interactive template selection
flow plan add  # TUI will offer template options
```

### Listing Available Templates

See all available templates:

```bash
# List all templates
flow plan templates list

# JSON output for scripting
flow plan templates list --json
```

### Template Structure and Components

Templates consist of:

1. **YAML Frontmatter**: Default job configuration
2. **Description**: Clear explanation of the template's purpose
3. **Pre-defined Prompts**: Professional, comprehensive prompts
4. **Type Specification**: Appropriate job type for the task
5. **Best Practice Patterns**: Proven approaches embedded in prompts

Example template structure:
```yaml
---
description: "Perform a thorough code review"
type: "oneshot"
model: "claude-4-sonnet"
output:
  type: "file"
---

## Base Prompt (Code Review)

You're performing a thorough code review of the changes in this branch.

Take a comprehensive look at the code quality, functionality, security, 
performance, and documentation. Consider how well the changes meet their 
intended purpose, handle edge cases, and follow best practices.

Share your insights about what works well and what could be improved.
```

## Built-in Templates

Grove Flow includes professionally crafted templates for common development tasks:

### Development Templates

**agent-run**: Generates a plan an LLM agent carries out
- **Purpose**: Create autonomous execution plans
- **Use Case**: Complex multi-step automation tasks
- **Type**: `interactive_agent`

**agent-xml**: Generates detailed XML plans for LLM agents
- **Purpose**: Structured, detailed execution plans
- **Use Case**: Complex workflows requiring precise specification
- **Type**: `interactive_agent`

**code-review**: Perform thorough code reviews
- **Purpose**: Comprehensive code quality assessment
- **Use Case**: Pre-merge reviews, quality audits
- **Type**: `oneshot`

**refactoring-plan**: Plan large-scale refactoring efforts
- **Purpose**: Structure complex refactoring projects
- **Use Case**: Technical debt reduction, architecture improvements
- **Type**: `oneshot`

### Architecture and Design Templates

**api-design**: Design RESTful or GraphQL APIs
- **Purpose**: Create comprehensive API specifications
- **Use Case**: New API development, API redesign
- **Type**: `oneshot`

**architecture-overview**: Generate architecture documentation
- **Purpose**: Document system architecture and design decisions
- **Use Case**: Architecture reviews, onboarding documentation
- **Type**: `oneshot`

**migration-plan**: Plan technology or database migrations
- **Purpose**: Structure complex migration projects
- **Use Case**: Technology upgrades, database migrations
- **Type**: `oneshot`

### Operations and Deployment Templates

**deployment-runbook**: Create production deployment runbooks
- **Purpose**: Document deployment procedures and troubleshooting
- **Use Case**: Production deployments, operational procedures
- **Type**: `oneshot`

**incident-postmortem**: Analyze incidents and create action items
- **Purpose**: Learn from incidents and prevent recurrence
- **Use Case**: Post-incident analysis, process improvement
- **Type**: `oneshot`

**security-audit**: Conduct security audits
- **Purpose**: Assess security posture and identify vulnerabilities
- **Use Case**: Security reviews, compliance assessments
- **Type**: `oneshot`

### Quality and Testing Templates

**test-strategy**: Create comprehensive test strategies
- **Purpose**: Design testing approaches for projects
- **Use Case**: Test planning, quality assurance strategy
- **Type**: `oneshot`

**performance-analysis**: Analyze and optimize performance
- **Purpose**: Identify and address performance issues
- **Use Case**: Performance optimization, bottleneck analysis
- **Type**: `oneshot`

**tech-debt-assessment**: Assess and prioritize technical debt
- **Purpose**: Evaluate and plan technical debt reduction
- **Use Case**: Code quality improvement, maintenance planning
- **Type**: `oneshot`

### Documentation Templates

**documentation**: Update and create documentation
- **Purpose**: Generate and maintain project documentation
- **Use Case**: Documentation updates, new documentation creation
- **Type**: `oneshot`

**learning-guide**: Create learning guides for codebases
- **Purpose**: Help new team members understand the codebase
- **Use Case**: Onboarding, knowledge transfer
- **Type**: `oneshot`

### Planning Templates

**generate-plan**: Create multi-step plans from specifications
- **Purpose**: Convert requirements into structured execution plans
- **Use Case**: Project planning, workflow automation
- **Type**: `oneshot`

**initial-plan**: Create initial plans for new apps or features
- **Purpose**: Bootstrap new development projects
- **Use Case**: New feature development, application bootstrapping
- **Type**: `oneshot`

### Template Usage Examples

```bash
# Create API documentation
flow plan add --template api-design --title "User Management API"

# Plan a major refactoring
flow plan add \
  --template refactoring-plan \
  --title "Refactor Authentication System"

# Conduct security review
flow plan add --template security-audit --title "Q3 Security Audit"

# Create deployment guide
flow plan add \
  --template deployment-runbook \
  --title "Production Deployment Guide"
```

## Plan Recipes

### Understanding Plan Recipes

Plan recipes are complete project scaffolds that create multiple related jobs in a structured workflow. They define entire development processes, from initial specification through final review.

### Using Recipes with Plan Initialization

Initialize plans with pre-configured recipes:

```bash
# Use a built-in recipe
flow plan init user-auth --recipe standard-feature

# List available recipes
flow plan recipes list

# Interactive recipe selection
flow plan init --tui
```

### Built-in Recipes Overview

#### Standard Feature Recipe (`standard-feature`)

A comprehensive feature development workflow:

**Jobs Created**:
1. **Specification** (`01-spec.md`): Define detailed requirements
2. **Implementation** (`02-implement.md`): Code the feature
3. **Git Changes Review** (`03-git-changes.md`): Review code changes
4. **Git Status Check** (`04-git-status.md`): Validate repository state
5. **Final Review** (`05-review.md`): Comprehensive quality review

**Use Cases**:
- New feature development
- Structured development workflows
- Team standardization

**Dependencies**:
```
01-spec.md → 02-implement.md → 03-git-changes.md
                                    ↓
                             04-git-status.md → 05-review.md
```

#### Chat Workflow Recipe (`chat-workflow`)

A conversation-driven development process:

**Jobs Created**:
1. **Chat Discussion** (`01-chat.md`): Explore and plan through conversation
2. **Implementation** (`02-implement.md`): Build based on chat decisions
3. **Git Changes Review** (`03-git-changes.md`): Review implementation
4. **Git Status Check** (`04-git-status.md`): Validate state
5. **Final Review** (`05-review.md`): Quality assessment

**Use Cases**:
- Exploratory development
- Requirements clarification through conversation
- Iterative design processes

#### Chat Recipe (`chat`)

Simple single-job chat for brainstorming:

**Jobs Created**:
1. **Chat** (`01-chat.md`): Open discussion and planning

**Use Cases**:
- Initial brainstorming
- Problem exploration
- Quick ideation sessions

#### Documentation Customization Recipe (`docgen-customize`)

Comprehensive documentation generation workflow:

**Jobs Created**:
1. **Customize Documentation Plan** (`01-customize-docs.md`): Plan documentation structure
2. **Generate Documentation** (`02-generate-docs.md`): Create documentation files

**Use Cases**:
- Documentation overhauls
- New project documentation
- Documentation standardization

### Recipe Variable Substitution

Recipes support template variables for customization:

#### Standard Variables

- `{{ .PlanName }}`: Automatic plan name substitution
- `{{ .ProjectName }}`: Current project context
- `{{ .Vars.model }}`: Custom model specification
- `{{ .Vars.rules_file }}`: Documentation rules file
- `{{ .Vars.output_dir }}`: Output directory specification

#### Using Variables

```bash
# Basic variable usage
flow plan init user-auth --recipe standard-feature \
  --recipe-vars "model=claude-4-opus"

# Multiple variables
flow plan init api-docs --recipe docgen-customize \
  --recipe-vars "model=claude-4-sonnet,output_dir=docs,rules_file=docs.rules"

# Comma-separated format
flow plan init feature-x --recipe standard-feature \
  --recipe-vars "model=gemini-2.5-pro,rules_file=docs/context.rules"
```

#### Variable Syntax in Recipe Files

```yaml
---
id: implement
title: "Implement {{ .PlanName }}"
{{ if .Vars.model }}model: "{{ .Vars.model }}"{{ end }}
type: interactive_agent
---

Implement the "{{ .PlanName }}" feature{{ if .Vars.rules_file }} using the context from `{{ .Vars.rules_file }}`{{ end }}.
```

## Creating Custom Recipes

### Recipe File Structure

Custom recipes are directories containing job template files:

```
~/.config/grove/recipes/my-workflow/
├── 01-planning.md
├── 02-design.md
├── 03-implement.md
├── 04-test.md
└── 05-deploy.md
```

### User Recipe Location

Store custom recipes in:
```
~/.config/grove/recipes/
├── api-workflow/
├── mobile-feature/
└── data-pipeline/
```

### Recipe Development Process

1. **Plan the Workflow**: Define the logical sequence of jobs
2. **Create Job Templates**: Write each job file with appropriate frontmatter
3. **Add Variables**: Use template variables for customization
4. **Test the Recipe**: Initialize test plans to validate the workflow
5. **Refine**: Iterate based on usage experience

### Example Custom Recipe

**File**: `~/.config/grove/recipes/api-workflow/01-requirements.md`
```yaml
---
id: requirements
title: "{{ .PlanName }} Requirements"
status: pending
type: oneshot
{{ if .Vars.model }}model: "{{ .Vars.model }}"{{ end }}
---

Define the requirements for {{ .PlanName }} API.

Consider:
- Functional requirements
- Performance requirements
- Security requirements
- Integration requirements
{{ if .Vars.legacy_system }}
- Migration from {{ .Vars.legacy_system }}{{ end }}
```

**File**: `~/.config/grove/recipes/api-workflow/02-design.md`
```yaml
---
id: design
title: "{{ .PlanName }} API Design"
status: pending
type: interactive_agent
depends_on:
  - 01-requirements.md
---

Design the {{ .PlanName }} API based on the requirements.

Create:
- OpenAPI specification
- Data models
- Endpoint definitions
- Authentication approach
```

## Dynamic Recipe Loading

### Configuration

Configure dynamic recipe loading in `grove.yml`:

```yaml
flow:
  get_recipe_cmd: "my-recipe-provider --list-recipes"
  recipe_vars:
    default_model: "claude-4-sonnet"
    output_format: "markdown"
```

### External Recipe Sources

Dynamic recipes can be loaded from:

**Git Repositories**:
```bash
# Configure to load from Git
get_recipe_cmd: "git-recipe-loader --repo https://github.com/company/grove-recipes"
```

**Shared Team Resources**:
```bash
# Load from shared network location
get_recipe_cmd: "team-recipes --environment production"
```

**CI/CD Integration**:
```bash
# Load recipes from CI/CD system
get_recipe_cmd: "ci-recipes --project {{ .ProjectName }}"
```

### Dynamic Recipe JSON Format

External commands should return JSON in this format:

```json
{
  "api-microservice": {
    "description": "Complete microservice development workflow",
    "jobs": {
      "01-spec.md": "---\nid: spec\n...",
      "02-implement.md": "---\nid: implement\n...",
      "03-test.md": "---\nid: test\n..."
    }
  },
  "data-pipeline": {
    "description": "Data processing pipeline workflow",
    "jobs": {
      "01-design.md": "---\nid: design\n...",
      "02-etl.md": "---\nid: etl\n..."
    }
  }
}
```

### Security Considerations

When using dynamic recipe loading:

- **Validate Sources**: Only load recipes from trusted sources
- **Review Content**: Inspect recipe content before using
- **Network Security**: Use secure connections (HTTPS, SSH)
- **Access Control**: Implement proper authentication for recipe sources
- **Audit Trail**: Log recipe loading and usage

## Template and Recipe Precedence

### Search Order

Grove Flow searches for templates and recipes in this order:

1. **Project-level** (`.grove/job-templates/`, `.grove/recipes/`)
2. **User-level** (`~/.config/grove/job-templates/`, `~/.config/grove/recipes/`)
3. **Dynamic** (loaded via `get_recipe_cmd`)
4. **Built-in** (embedded in Grove Flow)

### Overriding Built-ins

You can override built-in templates and recipes by creating files with the same names in higher-precedence locations:

```bash
# Override built-in code-review template
mkdir -p ~/.config/grove/job-templates/
cat > ~/.config/grove/job-templates/code-review.md << 'EOF'
---
description: "Custom code review template"
type: "interactive_agent"
model: "claude-4-opus"
---

Custom code review prompt with company-specific guidelines...
EOF
```

## Advanced Recipe Features

### Conditional Logic in Recipes

Use template conditionals for flexible recipes:

```yaml
---
id: testing
title: "Test {{ .PlanName }}"
{{ if .Vars.test_framework }}type: "shell"
{{ else }}type: "interactive_agent"{{ end }}
depends_on:
  - implement.md
---

{{ if .Vars.test_framework }}
Run tests using {{ .Vars.test_framework }}:
{{ .Vars.test_framework }} test
{{ else }}
Write comprehensive tests for {{ .PlanName }}.
Include unit tests, integration tests, and end-to-end tests.
{{ end }}
```

### Recipe Composition

Build complex recipes by combining simpler patterns:

```yaml
# Base implementation job
---
id: implement
title: "Implement {{ .PlanName }}"
type: interactive_agent
depends_on: {{ .Dependencies }}
---

{{ .BasePrompt }}

{{ if .Vars.include_tests }}
Also create comprehensive tests for the implementation.
{{ end }}

{{ if .Vars.include_docs }}
Update documentation to reflect the changes.
{{ end }}
```

### Performance Optimization

For large-scale recipe usage:

- **Cache Templates**: Store frequently used templates locally
- **Lazy Loading**: Load recipe content only when needed
- **Batch Operations**: Process multiple recipe operations together
- **Parallel Execution**: Run independent recipe jobs in parallel

## Best Practices

### Template Development

- **Single Responsibility**: Each template should focus on one specific task
- **Clear Descriptions**: Provide comprehensive descriptions of template purposes
- **Flexible Prompts**: Write prompts that work across different contexts
- **Consistent Naming**: Use clear, consistent naming conventions
- **Version Control**: Store custom templates in version control

### Recipe Design

- **Logical Flow**: Structure recipes with clear dependency chains
- **Modularity**: Design recipes that can be easily modified
- **Documentation**: Include README files explaining recipe usage
- **Testing**: Validate recipes with multiple project types
- **Feedback Loop**: Collect usage data to improve recipes

### Team Collaboration

- **Shared Templates**: Maintain team-wide template libraries
- **Code Reviews**: Review template and recipe changes
- **Documentation**: Document custom templates and recipes
- **Training**: Train team members on template and recipe usage
- **Standardization**: Use recipes to enforce consistent workflows

Recipes and templates are powerful tools for standardizing and automating your development workflows, enabling teams to maintain consistency while reducing the overhead of setting up common project patterns.