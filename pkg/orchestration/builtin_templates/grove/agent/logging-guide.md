---
description: Guide for using grove-core structured logging to debug grove ecosystem applications
type: interactive_agent
---

You are debugging a grove ecosystem application and need to add logging to understand what's happening at runtime. The grove ecosystem uses **grove-core's structured logging** for consistent, filterable log output.

## Overview

Grove-core provides a structured logging system built on top of logrus with:
- **Component-based filtering**: Show/hide logs by component name
- **JSON and pretty formats**: Machine-parseable or human-readable output
- **File-based logging**: All logs written to `.grove/logs/workspace-YYYY-MM-DD.log`
- **Runtime configuration**: Control logging via environment variables or config files

## Quick Start: Adding Logs to Your Code

### 1. Create a Logger

```go
import "github.com/mattsolo1/grove-core/logging"

log := logging.NewLogger("myapp.feature")
```

**Component naming convention**:
- Use dot notation: `myapp.subcomponent.feature`
- Examples: `nb.service`, `tui.browser.linking`, `flow.executor.docker`
- Helps with filtering: `GROVE_LOG_INCLUDE=tui` shows all TUI components

### 2. Log with Structured Fields

```go
import "github.com/sirupsen/logrus"

// Info level - general information
log.WithFields(logrus.Fields{
    "user_id": userID,
    "action":  "create",
}).Info("User created new resource")

// Debug level - detailed debugging info
log.WithFields(logrus.Fields{
    "path":       filepath,
    "size_bytes": size,
    "checksum":   hash,
}).Debug("File processed successfully")

// Warn level - something unexpected but handled
log.WithFields(logrus.Fields{
    "retry_count": retries,
    "error":       err.Error(),
}).Warn("Retrying failed operation")

// Error level - errors that need attention
log.WithFields(logrus.Fields{
    "operation": "database_query",
    "query":     sqlQuery,
    "error":     err.Error(),
}).Error("Database operation failed")
```

### 2a. Log Structured Data with `StructToLogrusFields`

For complex data structures, use `StructToLogrusFields` to automatically convert structs to log fields:

```go
import "github.com/mattsolo1/grove-core/logging"

// Define a struct with json tags
type RequestInfo struct {
    UserID    string `json:"user_id"`
    Method    string `json:"method"`
    Path      string `json:"path"`
    Duration  int64  `json:"duration_ms"`
    Status    int    `json:"status_code"`
}

// Create instance
reqInfo := RequestInfo{
    UserID:   "user123",
    Method:   "GET",
    Path:     "/api/users",
    Duration: 150,
    Status:   200,
}

// Convert to logrus fields automatically
fields := logging.StructToLogrusFields(reqInfo)
log.WithFields(fields).Info("Request completed")

// JSON output will be:
// {
//   "component": "api.server",
//   "msg": "Request completed",
//   "user_id": "user123",
//   "method": "GET",
//   "path": "/api/users",
//   "duration_ms": 150,
//   "status_code": 200
// }
```

**Benefits**:
- Automatically extracts fields from struct using `json` tags
- No manual `logrus.Fields{}` construction needed
- Type-safe at compile time
- Consistent field naming across logs

**With verbosity levels** (advanced):
```go
type DetailedInfo struct {
    ID        string `json:"id" verbosity:"0"`          // Always shown
    Name      string `json:"name" verbosity:"0"`        // Always shown
    Details   string `json:"details" verbosity:"1"`     // Show with -v
    DebugInfo string `json:"debug_info" verbosity:"2"`  // Show with -vv
}

// The verbosity metadata is included in _verbosity field
fields := logging.StructToLogrusFields(DetailedInfo{...})
log.WithFields(fields).Info("Processing item")
```

### 3. Choose the Right Log Level

**Debug** - Detailed information for debugging:
```go
log.Debug("Entering function")
log.WithFields(logrus.Fields{
    "input": data,
}).Debug("Processing input data")
```

**Info** - Important events and state changes:
```go
log.Info("Service started successfully")
log.WithFields(logrus.Fields{
    "count": results,
}).Info("Processing completed")
```

**Warn** - Unexpected but handled situations:
```go
log.Warn("Configuration file not found, using defaults")
log.WithFields(logrus.Fields{
    "path": configPath,
}).Warn("Using fallback configuration")
```

**Error** - Errors requiring attention:
```go
log.WithFields(logrus.Fields{
    "error": err.Error(),
}).Error("Failed to connect to database")
```

## Viewing Logs

**Important**: Logs are ALWAYS written to `.grove/logs/workspace-YYYY-MM-DD.log` files regardless of how you run the app.

### CLI Apps: stderr output

For command-line apps (like `nb list`), logs are also written to stderr:

```bash
# Run with debug logging (logs appear on stderr)
GROVE_LOG_LEVEL=debug nb list

# Example stderr output:
# 2025-12-30 10:15:23 [DEBUG] [nb.service] Loading configuration
# TYPE     DATE        TITLE
# -------  ----------  -------------
```

### TUI Apps: Use `core logs` instead

For TUI apps (like `nb tui`), logs do NOT go to stderr (to avoid interfering with the display). **Always use `core logs` to view TUI app logs**:

```bash
# Terminal 1: Run the TUI
nb tui

# Terminal 2: Follow logs in real-time
core logs -f

# Or filter to specific components
core logs --component tui.browser -f
```

### Using `core logs` for Live Filtering

The `core logs` command provides powerful filtering and viewing options:

```bash
# Follow logs from current workspace
core logs -f

# Follow logs from all workspaces in ecosystem
core logs --ecosystem -f

# Filter by specific component (strict whitelist)
core logs --component tui.browser.linking -f

# Filter by multiple components
core logs --component tui,service -f

# Show last 100 lines
core logs --tail 100

# Output in JSON format
core logs --tail 50 --json

# Show all logs (ignore filtering rules)
core logs --show-all -f

# Temporarily show specific components
core logs --also-show tui.browser -f

# Interactive TUI mode
core logs --tui
```

**Example with component filtering**:
```bash
# Only show linking-related logs
core logs --component tui.browser.linking --tail 20
# Output:
# 06:04:21 [nb-tui-tree] INFO Found note with plan_ref [tui.browser.linking] ...
# 06:04:21 [nb-tui-tree] INFO Successfully linked note and plan [tui.browser.linking] ...
```

### File Output (JSON Format)

Logs are automatically written to `.grove/logs/workspace-YYYY-MM-DD.log` in JSON format.

**Recommendation**: Use `core logs` instead of reading files directly - it handles filtering, formatting, and following automatically.

**When to access files directly**: Only when you need to process logs with custom scripts or grep across multiple days.

**Example JSON log entry**:
```json
{
  "component": "tui.browser.linking",
  "file": "/path/to/update.go:1583",
  "func": "github.com/user/project.(*Model).findAndApplyLinks",
  "level": "info",
  "msg": "Successfully linked note and plan",
  "key": "workspace:plans/feature",
  "note_title": "feature-note",
  "plan_name": "plans/feature",
  "time": "2025-12-30T10:15:25-05:00"
}
```

## Controlling Log Output

### CLI Flags (Recommended)

Use `core logs` with filtering flags for the best experience:

**--component** - Show only specific components (strict whitelist):
```bash
# Single component
core logs --component tui.browser.linking -f

# Multiple components
core logs --component tui,service,flow -f
```

**--also-show** - Temporarily show components (overrides hide rules):
```bash
# Show components that would normally be hidden
core logs --also-show cache,verbose.component -f
```

**--show-all** - Ignore all filtering rules:
```bash
# See everything
core logs --show-all -f
```

**--ignore-hide** - Temporarily unhide specific components:
```bash
# Show components that are in the hide list
core logs --ignore-hide cache -f
```

**Other useful flags**:
```bash
# Follow mode (live updates)
core logs -f

# Show last N lines
core logs --tail 100

# JSON output
core logs --json --tail 50

# Ecosystem-wide logs
core logs --ecosystem -f

# Interactive TUI
core logs --tui
```

### Environment Variables

**GROVE_LOG_LEVEL** - Set minimum log level:
```bash
# Show only info and above
GROVE_LOG_LEVEL=info nb tui

# Show debug logs
GROVE_LOG_LEVEL=debug nb tui

# Show only warnings and errors
GROVE_LOG_LEVEL=warn nb tui
```

**GROVE_LOG_CALLER** - Include file/line information:
```bash
# Enable caller info
GROVE_LOG_CALLER=true nb tui
```

**GROVE_LOG_INCLUDE** - Show only specific components (env var alternative):
```bash
# Show only TUI-related logs
GROVE_LOG_INCLUDE=tui nb tui

# Show multiple components (comma-separated)
GROVE_LOG_INCLUDE=tui,service nb tui

# Show specific subcomponent
GROVE_LOG_INCLUDE=tui.browser.linking nb tui
```

**GROVE_LOG_EXCLUDE** - Hide specific components:
```bash
# Hide noisy cache logs
GROVE_LOG_EXCLUDE=cache nb tui
```

**Note**: CLI flags (via `core logs`) are recommended over environment variables for filtering as they provide more flexibility and don't require restarting the application.

### Configuration File

Add logging configuration to `~/.config/grove/grove.yml`:

```yaml
logging:
  level: debug  # Minimum log level (debug, info, warn, error)

  file:
    enabled: true    # Write logs to files
    format: json     # File format (json or text)

  report_caller: true  # Include file:line in logs

  component_filtering:
    # Show only these components (overrides everything else)
    only: []

    # Always show these components (even if excluded)
    show: ["myapp", "important"]

    # Hide these components
    hide: ["cache", "noisy.component"]
```

**Priority**: Environment variables override config file settings.

## Debugging Patterns

### Pattern 1: Trace Function Flow

Add entry/exit logs to understand execution flow:

```go
func processData(data []byte) error {
    log := logging.NewLogger("myapp.processor")

    log.WithFields(logrus.Fields{
        "size_bytes": len(data),
    }).Debug("Entering processData")

    defer log.Debug("Exiting processData")

    // ... processing logic ...

    return nil
}
```

### Pattern 2: Log State Changes

Track important state transitions:

```go
log.WithFields(logrus.Fields{
    "old_state": oldState,
    "new_state": newState,
    "reason":    reason,
}).Info("State transition")
```

### Pattern 3: Debug Collection Building

Log collection sizes before and after operations:

```go
log.WithFields(logrus.Fields{
    "input_count": len(items),
}).Debug("Starting filter operation")

filtered := filterItems(items)

log.WithFields(logrus.Fields{
    "input_count":  len(items),
    "output_count": len(filtered),
    "filtered_out": len(items) - len(filtered),
}).Debug("Filter operation completed")
```

### Pattern 4: Log Matching/Linking Logic

When debugging why items don't match:

```go
// Log what you're looking for
log.WithFields(logrus.Fields{
    "search_key": key,
    "available_keys": availableKeys,
}).Debug("Searching for match")

// Log when match succeeds
if found {
    log.WithFields(logrus.Fields{
        "key": key,
        "matched_item": itemID,
    }).Debug("Match found")
} else {
    // Log when match fails
    log.WithFields(logrus.Fields{
        "key": key,
        "reason": "no matching item",
    }).Debug("Match failed")
}
```

### Pattern 5: Temporary Debug Logs

Add temporary high-level logs during debugging:

```go
// Change Debug to Info temporarily to see without -v flag
log.WithFields(logrus.Fields{
    "critical_value": value,
}).Info("TEMPORARY DEBUG: checking critical condition")

// Remove or change back to Debug once issue is resolved
```

## Best Practices

### 1. Use Descriptive Component Names

```go
// Good - specific and hierarchical
log := logging.NewLogger("nb.tui.browser.views")
log := logging.NewLogger("flow.executor.docker.compose")

// Avoid - too generic
log := logging.NewLogger("app")
log := logging.NewLogger("main")
```

### 2. Include Relevant Context

```go
// Good - structured fields for filtering
log.WithFields(logrus.Fields{
    "user_id":    user.ID,
    "workspace":  ws.Name,
    "operation":  "create",
}).Info("Creating new resource")

// Avoid - string interpolation (harder to filter)
log.Info(fmt.Sprintf("Creating resource for user %s in workspace %s", user.ID, ws.Name))
```

### 3. Log at Appropriate Levels

**Use Debug for**:
- Entry/exit of functions
- Intermediate calculation results
- Loop iteration details
- Detailed state inspection

**Use Info for**:
- Service started/stopped
- Important business events
- Successful operations
- Configuration loaded

**Use Warn for**:
- Deprecated feature usage
- Fallback to default behavior
- Recoverable errors
- Unusual but handled conditions

**Use Error for**:
- Operation failures
- Unrecoverable errors
- External service failures
- Data validation errors

### 4. Don't Log Sensitive Data

```go
// Avoid - logging passwords
log.WithFields(logrus.Fields{
    "username": username,
    "password": password,  // Never log passwords!
}).Debug("Authenticating user")

// Good - log only safe information
log.WithFields(logrus.Fields{
    "username": username,
}).Debug("Authenticating user")
```

### 5. Clean Up Debug Logs

Remove or downgrade temporary debug logs after fixing issues:

```go
// During debugging
log.Info("TEMP: Checking if this code path is reached")

// After fix - remove or change to Debug
log.Debug("Code path executed")
// or just remove it entirely
```

## Common Debugging Workflows

### Workflow 1: Find Why Feature Isn't Working

```bash
# 1. Run your app with debug logging
GROVE_LOG_LEVEL=debug myapp run

# 2. In another terminal, follow logs for your component
core logs --component myapp.feature -f

# 3. If not seeing logs, check all components
core logs --show-all -f

# 4. Add more granular logs and rebuild
```

### Workflow 2: Debug Data Processing Pipeline

```go
// Add logs at each pipeline stage
log.WithFields(logrus.Fields{
    "stage": "input",
    "count": len(input),
}).Debug("Pipeline stage: input")

processed := processStage1(input)
log.WithFields(logrus.Fields{
    "stage": "stage1",
    "count": len(processed),
}).Debug("Pipeline stage: stage1 complete")

filtered := processStage2(processed)
log.WithFields(logrus.Fields{
    "stage":  "stage2",
    "count":  len(filtered),
    "dropped": len(processed) - len(filtered),
}).Debug("Pipeline stage: stage2 complete")
```

### Workflow 3: Debug Timing Issues

```go
import "time"

start := time.Now()
log.Debug("Starting expensive operation")

result := expensiveOperation()

duration := time.Since(start)
log.WithFields(logrus.Fields{
    "duration_ms": duration.Milliseconds(),
    "result_size": len(result),
}).Debug("Expensive operation completed")
```

## Troubleshooting

### "I'm not seeing any logs"

**Check**:
1. Log level too high: Set `GROVE_LOG_LEVEL=debug`
2. Component filtered out: Remove `GROVE_LOG_INCLUDE` or add your component
3. Logs going only to file: Check `.grove/logs/workspace-*.log`

**Try**:
```bash
# Force debug level and show all components
GROVE_LOG_LEVEL=debug myapp run

# In another terminal, follow logs
core logs --show-all -f
```

### "Logs are too noisy"

**Solution**: Filter by component with `core logs`
```bash
# Show only your component
core logs --component myapp -f

# Show multiple specific components
core logs --component myapp,service -f
```

### "Need to see logs from multiple runs"

**Solution**: Use `core logs --tail` or access files directly
```bash
# View last 100 entries
core logs --tail 100

# Search across all log files (when core logs isn't enough)
grep -h "error_pattern" .grove/logs/*.log | jq 'select(.component == "myapp")'
```

### "Logs are hard to read in the terminal"

**Solution**: Use `core logs` pretty format or `--tui` mode
```bash
# Pretty format (default)
core logs -f

# Interactive TUI
core logs --tui

# JSON for scripting
core logs --json --tail 50
```

## Quick Reference

### Create Logger
```go
log := logging.NewLogger("component.name")
```

### Log Levels
```go
log.Debug("detailed debugging")
log.Info("important events")
log.Warn("unexpected but handled")
log.Error("operation failed")
```

### Structured Fields
```go
log.WithFields(logrus.Fields{
    "key1": value1,
    "key2": value2,
}).Info("message")
```

### View Logs (Recommended: core logs)
```bash
# Follow logs with component filter
core logs --component mycomponent -f

# Show last 100 lines
core logs --tail 100

# Interactive TUI
core logs --tui

# JSON output
core logs --json --tail 50

# JSON with component filter
core logs --json --component mycomponent --tail 10

# Show all (ignore filters)
core logs --show-all -f
```

### Environment Variables
```bash
GROVE_LOG_LEVEL=debug         # Set log level
GROVE_LOG_CALLER=true         # Show file:line
GROVE_LOG_INCLUDE=component   # Filter components
```

### Advanced: Direct File Access
```bash
# When core logs isn't enough (rare cases)
tail -f .grove/logs/workspace-$(date +%Y-%m-%d).log | jq -r '"\(.time) [\(.component)] \(.msg)"'

# Search across multiple days
grep "error_pattern" .grove/logs/*.log | jq 'select(.component == "mycomponent")'
```

## Summary

When debugging grove ecosystem applications:

1. **Add a logger**: `log := logging.NewLogger("myapp.feature")`
2. **Log with context**: Use `WithFields()` for structured data
3. **Choose appropriate levels**: Debug for details, Info for events, Error for failures
4. **View logs with core logs**: `core logs --component myapp.feature -f`
5. **Filter effectively**: Use `--component`, `--show-all`, or `--also-show` flags
6. **Use TUI mode**: `core logs --tui` for interactive browsing
7. **Clean up**: Remove temporary debug logs after fixing issues

**Remember**: `core logs` is your primary tool - it handles filtering, formatting, and following automatically. Only use direct file access for advanced cases like searching across multiple days.

Happy debugging!
