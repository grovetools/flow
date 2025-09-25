# Recipes and Templates Documentation

Generate comprehensive documentation for Grove Flow's automation and reusability features through recipes and templates.

## Content to Cover:

### Job Templates
- What are job templates and their purpose
- Using templates with `flow plan add --template`
- Listing available templates with `flow plan templates list`
- Template structure and components:
  - Pre-defined prompts
  - Default configurations
  - Common patterns

### Built-in Templates
- Document each built-in template:
  - Purpose and use case
  - What it provides
  - When to use it
  - Example usage

### Plan Recipes
- Understanding plan recipes as project scaffolds
- Using recipes with `flow plan init --recipe`
- Built-in recipes overview:
  - `standard-feature`: Standard feature development workflow
  - `chat-workflow`: Chat-based exploration workflow
  - Other available recipes

### Creating Custom Recipes
- Recipe file structure and format
- Creating user-defined recipes in `~/.config/grove/recipes/`
- Recipe components:
  - Directory structure
  - Job templates
  - Default configurations
  - README templates

### Template Variables
- Using template variables in recipes:
  - `{{ .PlanName }}`: Automatic plan name substitution
  - `{{ .ProjectName }}`: Project context
  - Custom variables via `--recipe-vars`
- Variable syntax and escaping
- Best practices for variable usage

### Dynamic Recipe Loading
- Configuring `get_recipe_cmd` in `grove.yml`
- Loading recipes from external sources:
  - Git repositories
  - Shared team resources
  - CI/CD integration
- Security considerations

### Recipe Development
- Best practices for recipe creation
- Testing and validating recipes
- Sharing recipes with teams
- Version control for recipes

### Advanced Features
- Conditional logic in recipes
- Recipe inheritance and composition
- Integration with project tooling
- Performance optimization

Include practical examples of creating and using both simple and complex recipes.