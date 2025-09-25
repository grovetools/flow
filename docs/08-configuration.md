# Configuration

Grove Flow uses a hierarchical configuration system that allows you to customize behavior at project, plan, and job levels. This document covers all configuration options and how to manage them effectively.

## Project-Level Configuration (`grove.yml`)

The main configuration file for Grove Flow is `grove.yml`, located in your project root. All Grove Flow settings are specified under the `flow` key.

### Core Settings

#### Directory Configuration

```yaml
flow:
  plans_directory: ./plans        # Where plan directories are stored
  chat_directory: ./chats         # Location for chat sessions
```

**plans_directory**: Defines where Grove Flow creates and looks for plan directories. Can be relative to project root or absolute path.

**chat_directory**: Specifies the location for chat session files. Supports the same path formats as plans_directory.

#### Default Model Configuration

```yaml
flow:
  oneshot_model: claude-4-sonnet       # Default model for oneshot jobs
  agent_model: claude-4-sonnet         # Default model for agent jobs  
  chat_model: claude-4-sonnet          # Default model for chat sessions
```

**oneshot_model**: Model used for simple, single-response jobs that don't require tool usage.

**agent_model**: Model used for complex agent jobs that can use tools and make file modifications.

**chat_model**: Model used for interactive chat sessions and conversations.

#### Agent Configuration

```yaml
flow:
  target_agent_container: grove-agent-ide    # Default container for agent jobs
  max_consecutive_steps: 25                  # Maximum steps for agent execution
```

**target_agent_container**: Specifies which container environment agent jobs should use by default.

**max_consecutive_steps**: Limits how many consecutive actions an agent can take to prevent runaway execution.

#### Job Completion Settings

```yaml
flow:
  summarize_on_complete: true                     # Auto-summarize completed jobs
  summary_model: claude-3-haiku                   # Model for generating summaries
  summary_prompt: "Summarize the key outcomes..."  # Custom summary prompt
  summary_max_chars: 1000                         # Maximum summary length
```

**summarize_on_complete**: When enabled, Grove Flow automatically creates summaries of completed jobs.

**summary_model**: Specifies which model to use for generating job summaries (typically a fast, efficient model).

**summary_prompt**: Custom prompt template for job summarization.

**summary_max_chars**: Limits the length of generated summaries.

### Recipe Configuration

#### Dynamic Recipe Loading

```yaml
flow:
  get_recipe_cmd: "recipe-loader --project {{.ProjectName}}"  # Command for loading recipes
```

**get_recipe_cmd**: Command that Grove Flow executes to load dynamic recipes. The command should output JSON in the expected recipe format.

#### Recipe Variables

```yaml
flow:
  recipes:
    standard-feature:
      vars:
        model: claude-4-sonnet
        rules_file: docs.rules
        output_dir: docs
    api-workflow:
      vars:
        test_framework: pytest
        database: postgresql
```

Define default variables for specific recipes that will be applied when the recipe is used.

### Advanced Settings

#### Worktree Configuration

```yaml
flow:
  worktree_base: /path/to/worktrees    # Base directory for Git worktrees
```

**worktree_base**: Specifies where Git worktrees should be created. If not specified, Grove Flow uses `.grove-worktrees` in the project root.

#### Tmux Integration

```yaml
flow:
  tmux_config:
    session_prefix: "grove-"          # Prefix for tmux session names
    default_shell: "/bin/zsh"         # Default shell for sessions
    enable_mouse: true                # Enable mouse support in tmux
```

**tmux_config**: Configuration options for tmux session integration.

## Plan-Level Configuration (`.grove-plan.yml`)

Each plan can have its own configuration file that overrides project defaults for that specific plan.

### Plan Configuration Structure

```yaml
# Default model for jobs in this plan
model: claude-4-opus

# Default worktree for agent jobs
worktree: feature-branch

# Default container for agent jobs
target_agent_container: grove-agent-ide

# Plan metadata
name: user-authentication
description: "User authentication system implementation"
version: "1.0.0"

# Custom plan variables
vars:
  database_type: postgresql
  auth_provider: oauth2
  test_coverage_threshold: 85

# Global dependencies for all jobs
dependencies:
  - external-api-ready
  - database-schema-migrated
```

### Configuration Fields

**model**: Override the default model for all jobs in this plan.

**worktree**: Specify a default worktree name for agent jobs in this plan.

**target_agent_container**: Override the default container for agent jobs.

**vars**: Custom variables that can be used in job templates and prompts.

**dependencies**: Global dependencies that apply to all jobs in the plan.

### Plan Configuration Management

#### Viewing Configuration

```bash
# Show all plan configuration
flow plan config my-plan

# Show specific configuration value
flow plan config my-plan --get model

# JSON output
flow plan config my-plan --json
```

#### Setting Configuration

```bash
# Set single value
flow plan config my-plan --set model=claude-4-opus

# Set multiple values
flow plan config my-plan \
  --set model=claude-4-sonnet \
  --set worktree=feature/auth \
  --set target_agent_container=grove-agent-dev

# Using active plan
flow plan set my-plan
flow plan config --set model=gemini-2.5-pro
```

#### Configuration Propagation

When you set plan-level configuration, Grove Flow automatically propagates appropriate settings to existing jobs that don't have those fields explicitly set:

- **model**: Applied to jobs without a model specified
- **worktree**: Applied to agent jobs without worktree settings
- **target_agent_container**: Applied to agent jobs without container settings

Shell jobs are excluded from worktree propagation, and container settings only apply to agent job types.

## Configuration Inheritance Hierarchy

Grove Flow uses a hierarchical configuration system with the following precedence (highest to lowest):

### 1. Job-Level Settings (Highest Priority)

Settings in individual job frontmatter:

```yaml
---
id: critical-task
title: "Critical Implementation Task"
type: interactive_agent
model: claude-4-opus              # Overrides plan and project settings
target_agent_container: special-env
worktree: critical-feature
---
```

### 2. Plan-Level Settings

Settings in `.grove-plan.yml`:

```yaml
model: claude-4-sonnet            # Overrides project setting
worktree: feature-branch
```

### 3. Project-Level Settings

Settings in `grove.yml`:

```yaml
flow:
  agent_model: claude-3-haiku     # Project default
  oneshot_model: claude-3-haiku
  target_agent_container: grove-agent-ide
```

### 4. System Defaults (Lowest Priority)

Built-in Grove Flow defaults when no configuration is specified.

### Inheritance Example

Given this configuration hierarchy:

```yaml
# grove.yml
flow:
  agent_model: claude-3-haiku
  target_agent_container: grove-agent-ide

# .grove-plan.yml  
model: claude-4-sonnet

# job frontmatter
---
id: special-job
model: claude-4-opus
---
```

The effective configuration for the job would be:
- **model**: `claude-4-opus` (job-level override)
- **target_agent_container**: `grove-agent-ide` (inherited from project)
- Other settings inherited from appropriate levels

## Environment Variables

Grove Flow supports several environment variables for configuration:

### Grove-Specific Variables

```bash
# Override Grove home directory
export GROVE_HOME=/custom/grove/path

# Custom config file location
export GROVE_CONFIG=/path/to/custom/grove.yml

# Enable debug logging
export GROVE_DEBUG=1

# Override plans directory
export GROVE_PLANS_DIR=/custom/plans/path

# Override chat directory  
export GROVE_CHATS_DIR=/custom/chats/path
```

### Model API Configuration

```bash
# Anthropic API
export ANTHROPIC_API_KEY=your_key_here

# OpenAI API
export OPENAI_API_KEY=your_key_here

# Google AI API
export GOOGLE_API_KEY=your_key_here

# Custom model endpoints
export LLM_USER_PATH=/path/to/custom/models
```

### Development Environment Variables

```bash
# Enable verbose logging
export GROVE_VERBOSE=1

# Override tmux configuration
export GROVE_TMUX_CONFIG=/path/to/tmux.conf

# Custom worktree base
export GROVE_WORKTREE_BASE=/custom/worktree/path
```

### Precedence Order

Environment variables take precedence over configuration files:

1. **Environment Variables** (highest)
2. **Command-line flags**
3. **grove.yml configuration**
4. **System defaults** (lowest)

## Configuration File Formats

### YAML Structure and Validation

Grove Flow configuration files use YAML format with strict validation:

```yaml
# Valid configuration
flow:
  plans_directory: ./plans          # String
  max_consecutive_steps: 25         # Integer
  summarize_on_complete: true       # Boolean
  recipes:                          # Map
    standard-feature:
      vars:                         # Nested map
        model: claude-4-sonnet
```

### Comments and Documentation

Configuration files support YAML comments for documentation:

```yaml
flow:
  # Directory where all plans are stored
  plans_directory: ./plans
  
  # Default model for simple tasks (fast, cost-effective)
  oneshot_model: claude-3-haiku
  
  # Default model for complex agent tasks (more capable)
  agent_model: claude-4-sonnet
  
  # Auto-summarize completed jobs for better tracking
  summarize_on_complete: true
```

### Configuration Validation

Grove Flow validates configuration on startup and provides helpful error messages:

```bash
# Example validation errors
Error: Invalid configuration in grove.yml
  - flow.plans_directory: must be a string
  - flow.max_consecutive_steps: must be a positive integer
  - flow.unknown_field: unknown configuration field
```

## Best Practices

### Organizing Configuration for Teams

#### Project Templates

Create standardized `grove.yml` templates for different project types:

```yaml
# Backend service template
flow:
  plans_directory: ./plans
  chat_directory: ./docs/chats
  agent_model: claude-4-sonnet
  oneshot_model: claude-3-haiku
  target_agent_container: grove-backend-dev
  summarize_on_complete: true
  recipes:
    api-service:
      vars:
        database: postgresql
        auth_method: jwt
        test_framework: pytest
```

#### Team Defaults

Establish team-wide configuration standards:

```yaml
# Team configuration standards
flow:
  # Use cost-effective models for routine tasks
  oneshot_model: claude-3-haiku
  
  # Use powerful models for complex work
  agent_model: claude-4-sonnet
  
  # Standardize directories across projects
  plans_directory: ./engineering/plans
  chat_directory: ./engineering/chats
  
  # Enable auto-summarization for tracking
  summarize_on_complete: true
  summary_model: claude-3-haiku
```

### Security Considerations for API Keys

#### API Key Management

```bash
# Use environment-specific key files
source ~/.grove/dev-keys        # Development environment
source ~/.grove/prod-keys       # Production environment

# Or use key management services
export ANTHROPIC_API_KEY=$(vault kv get -field=key secret/anthropic)
```

#### Key Rotation

```bash
#!/bin/bash
# Automated key rotation script
NEW_KEY=$(get-new-anthropic-key)
export ANTHROPIC_API_KEY=$NEW_KEY
echo "API key rotated successfully"
```

#### Access Control

- Store API keys in secure, encrypted storage
- Use different keys for different environments
- Implement key rotation policies
- Monitor API key usage and costs
- Restrict key permissions to minimum required scope

### Performance Tuning Guidelines

#### Model Selection Strategy

```yaml
flow:
  # Fast models for routine tasks
  oneshot_model: claude-3-haiku
  summary_model: claude-3-haiku
  
  # Balanced model for most agent work
  agent_model: claude-4-sonnet
  
  # Reserve most powerful model for complex plans
  recipes:
    complex-analysis:
      vars:
        model: claude-4-opus
```

#### Resource Optimization

```yaml
flow:
  # Limit agent steps to prevent runaway costs
  max_consecutive_steps: 20
  
  # Keep summaries concise
  summary_max_chars: 500
  
  # Use efficient summary prompts
  summary_prompt: "Briefly summarize: key decisions, outcomes, next steps"
```

#### Directory Organization

```yaml
flow:
  # Use separate directories for better organization
  plans_directory: ./work/plans
  chat_directory: ./work/chats
  
  # Configure worktree location for performance
  worktree_base: /fast/ssd/worktrees
```

### Configuration Management in CI/CD

#### Environment-Specific Configs

```yaml
# grove.yml (base)
flow:
  plans_directory: ./plans
  agent_model: claude-4-sonnet

# grove.ci.yml (CI override)
flow:
  agent_model: claude-3-haiku      # Faster, cheaper for CI
  max_consecutive_steps: 10        # Limit CI execution time
  summarize_on_complete: false     # Skip summaries in CI
```

#### CI Integration

```bash
#!/bin/bash
# CI configuration selection
if [ "$CI" = "true" ]; then
    export GROVE_CONFIG=grove.ci.yml
fi

# Run Grove Flow with CI-appropriate settings
flow plan run --config grove.ci.yml
```

### Migration from Older Versions

#### Configuration Format Updates

```bash
# Check for deprecated configuration
flow config validate

# Migrate old format to new format
flow config migrate --backup
```

#### Backward Compatibility

Grove Flow maintains backward compatibility for configuration files, but warns about deprecated options:

```bash
Warning: 'job_model' is deprecated, use 'agent_model' instead
Warning: 'default_container' is deprecated, use 'target_agent_container' instead
```

## Troubleshooting Configuration

### Common Configuration Issues

**Invalid YAML syntax**: Use a YAML validator to check syntax before deployment.

**Path resolution problems**: Use absolute paths or ensure relative paths are correct from project root.

**Model availability**: Verify that specified models are available and API keys are configured.

**Permission errors**: Ensure Grove Flow has read/write access to configured directories.

### Configuration Debugging

```bash
# Validate configuration
flow config validate

# Show effective configuration (after inheritance)
flow config show --effective

# Debug configuration loading
GROVE_DEBUG=1 flow plan list
```

### Recovery Procedures

**Corrupted configuration**: Restore from backup or reinitialize with `flow init`.

**Missing API keys**: Check environment variables and key file locations.

**Path conflicts**: Resolve directory conflicts and update configuration paths.

**Model errors**: Verify model names and API access permissions.

Configuration forms the foundation of Grove Flow's flexibility and power. Proper configuration management ensures efficient, secure, and maintainable workflows across your development projects.