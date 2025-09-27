# CLI Reference Documentation

Generate a comprehensive command-line interface reference for Grove Flow.

## Requirements:

Create a complete reference guide covering all `flow` subcommands and their options. This should serve as a quick lookup guide for users.

## Structure:

### Main Commands
Document each primary command with:
- Command syntax
- Description
- All available flags and options
- Default values
- Examples

### Commands to Document:

#### `flow plan`
- `init`: Initialize a new plan
- `add`: Add jobs to a plan
- `list`: List all plans
- `tui`: Interactive plan browser
- `set`: Set active plan
- `current`: Show active plan
- `unset`: Clear active plan
- `status`: Show plan status
- `graph`: Visualize dependencies
- `run`: Execute jobs
- `complete`: Mark job as complete
- `open`: Open plan directory
- `launch`: Launch in Tmux
- `finish`: Complete and clean up plan
- `config`: Manage plan configuration
- `extract`: Extract plan from chat
- `templates list`: List job templates

#### `flow chat`
- Starting new chats
- Chat management commands
- `launch`: Interactive chat session
- Chat history and navigation

#### `flow models`
- `list`: List available models
- Model configuration

#### `flow version`
- Version information
- Build details

### Global Options
- `--config`: Specify config file
- `--verbose`: Verbose output
- `--quiet`: Suppress output
- `--format`: Output format options
- Help and documentation flags

### Exit Codes
- Document standard exit codes and their meanings

### Environment Variables
- List all recognized environment variables
- Their effects on command behavior

### Configuration Files
- Quick reference to config file locations
- Priority and override behavior

Include practical examples for common workflows and command combinations.