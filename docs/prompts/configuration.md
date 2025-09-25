# Configuration Documentation

Generate comprehensive documentation for all Grove Flow configuration options and files.

## Content to Cover:

### Project-Level Configuration (`grove.yml`)
Document all settings under the `flow` key:

#### Core Settings
- `plans_directory`: Where plan directories are stored
- `chat_directory`: Location for chat sessions
- `oneshot_model`: Default model for oneshot jobs
- `agent_model`: Default model for agent jobs
- `chat_model`: Default model for chat sessions

#### Recipe Configuration
- `get_recipe_cmd`: Command for dynamic recipe loading
- `recipe_vars`: Default variables for recipes
- Recipe search paths and resolution

#### Advanced Settings
- `worktree_base`: Base directory for Git worktrees
- `tmux_config`: Tmux integration settings
- `completion_behavior`: Job completion options
- Performance and resource settings

### Plan-Level Configuration (`.grove-plan.yml`)
- Purpose and structure of plan config files
- Plan-specific overrides:
  - `model`: Override default model for the plan
  - `worktree`: Worktree configuration
  - `dependencies`: Global dependencies
  - Custom metadata

### Managing Plan Configuration
- Using `flow plan config` command:
  - Setting configuration values
  - Viewing current configuration
  - Resetting to defaults
- Configuration inheritance hierarchy:
  1. Job-level settings (highest priority)
  2. Plan-level settings
  3. Project-level settings
  4. System defaults (lowest priority)

### Environment Variables
- Supported environment variables:
  - `GROVE_HOME`: Override Grove home directory
  - `GROVE_CONFIG`: Custom config file location
  - Model API keys and endpoints
- Precedence and override behavior

### Configuration File Formats
- YAML structure and validation
- Comments and documentation in configs
- Migration from older versions

### Best Practices
- Organizing configuration for teams
- Security considerations for API keys
- Performance tuning guidelines
- Configuration management in CI/CD

Include complete examples of configuration files and common configuration patterns.