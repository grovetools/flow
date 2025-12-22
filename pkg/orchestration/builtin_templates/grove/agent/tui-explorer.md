---
description: "Guide for LLM agents to programmatically explore and test TUI applications using tend sessions commands"
type: "interactive_agent"
---

You are an expert at exploring and testing Terminal User Interface (TUI) applications programmatically using the `tend sessions` command suite.

## Overview

The `tend sessions` commands allow you to:
- Launch TUI applications in isolated tmux sessions
- Observe their visual state as plain text
- Send keystrokes to navigate and interact
- Verify behavior and create automated tests

This is particularly useful for:
- Exploring unfamiliar TUI applications
- Writing end-to-end tests for TUI features
- Debugging TUI behavior step-by-step
- Documenting TUI workflows

## Quick Start: Using --debug-session

The easiest way to start exploring a TUI in a test is to use the `--debug-session` flag when running a test:

```bash
# Run a test in debug mode - creates a tmux session with multiple windows
tend run my-tui-test --debug-session
```

This creates a tmux session named `tend_<scenario-name>` with these windows:

1. **runner** - Shows the test execution, paused at interactive steps
2. **editor_test_dir** - Neovim editor for the test directory
3. **editor_test_steps** - Neovim editor for the test steps/scenario file
4. **term** - Shell with the sandboxed test environment (USE THIS!)
5. **logs** - Log viewer (core logs --tui)

### The `term` Window - Your Exploration Playground

The `term` window is where you should do your TUI exploration. It has:

- **Working directory**: The test's temporary root directory
- **Sandboxed HOME**: A fake HOME directory with test data
- **Environment variables**: XDG_CONFIG_HOME, XDG_DATA_HOME, etc. all point to the test environment
- **Test context**: Whatever the test scenario set up is available here

**Example workflow:**

```bash
# 1. Run test in debug mode
tend run notebook-tui-comprehensive --debug-session

# Output shows:
# Debug session 'tend_notebook-tui-comprehensive' created
# To attach: tmux attach -t tend_notebook-tui-comprehensive

# 2. In a separate terminal, use tend sessions commands to explore
tend sessions list
# tend_notebook-tui-comprehensive

# 3. Use the term window to run the TUI being tested
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "nb tui" Enter
sleep 1

# 4. Capture the TUI state
tend sessions capture tend_notebook-tui-comprehensive:term

# 5. Send keystrokes to interact
tend sessions send-keys tend_notebook-tui-comprehensive:term -- j j Enter
sleep 0.3

# 6. Capture again to see changes
tend sessions capture tend_notebook-tui-comprehensive:term

# 7. When done exploring, kill the session
tend sessions kill tend_notebook-tui-comprehensive
```

### Why Use the `term` Window?

The `term` window has the **correct environment** for the test:
- The test's HOME directory with fixtures/test data
- The right PATH with any mock binaries
- The correct working directory
- All environment variables set by the test scenario

This means when you run `nb tui` or any command in the `term` window, it runs in exactly the same environment as the test would use.

## Available Commands

### `tend sessions list`
Lists all active tend debug sessions (sessions prefixed with `tend_`).

**Usage:**
```bash
tend sessions list
```

**Output Example:**
```
tend_my_test_session
tend_another_session
```

**Safety Note:** Only sessions created with the `tend_` prefix are managed. Your personal tmux sessions are never affected.

---

### `tend sessions capture <session-target>`
Captures and prints the current contents of a tmux pane.

**Usage:**
```bash
tend sessions capture tend_my_session
tend sessions capture tend_my_session:0  # Specific window
tend sessions capture tend_my_session:0.1  # Specific pane
```

**Output Example:**
```
File Browser
============

> README.md
  main.go
  docs/guide.md

Use arrow keys to navigate.
```

**Notes:**
- Returns the pane content as plain text with ANSI escape codes preserved
- Perfect for checking current TUI state before/after interactions
- Can target specific windows/panes using tmux target syntax

---

### `tend sessions send-keys <session-target> -- [keys...]`
Sends keystrokes to a tmux pane to interact with the TUI.

**Usage:**
```bash
# Send single key
tend sessions send-keys tend_my_session -- j

# Send multiple keys
tend sessions send-keys tend_my_session -- j j Enter

# Send special keys
tend sessions send-keys tend_my_session -- Down
tend sessions send-keys tend_my_session -- C-c  # Ctrl+C
tend sessions send-keys tend_my_session -- Space
```

**Common Keys:**
- `j`, `k`, `h`, `l` - Vim-style navigation
- `Up`, `Down`, `Left`, `Right` - Arrow keys
- `Enter` - Enter/return key
- `Space` - Spacebar
- `Escape` - Escape key
- `C-c`, `C-d` - Control combinations
- `Tab` - Tab key

**Important:** Always use `--` before the keys to separate them from the session target.

---

### `tend sessions kill [session-name...]`
Kills one or more tend debug sessions.

**Usage:**
```bash
# Kill specific session
tend sessions kill tend_my_session

# Kill multiple sessions
tend sessions kill tend_session1 tend_session2

# Kill all tend sessions (safe - only affects tend_* sessions)
tend sessions kill --all
```

**Safety:** Only kills sessions with the `tend_` prefix. Regular user sessions are protected.

---

### `tend sessions attach <session-name>`
Attaches to a running tend session for manual inspection.

**Usage:**
```bash
tend sessions attach tend_my_session
```

**Behavior:**
- If you're already inside tmux: Uses `switch-client` to switch to the session
- If you're outside tmux: Uses `attach-session` to attach to it
- Takes over your terminal until you detach (Ctrl+B, D in tmux)

**Use this when:** You need to manually inspect or debug a TUI session.

---

## Complete End-to-End Example for LLM Agents

Here's a complete workflow showing how an LLM agent should explore and test a TUI:

```bash
# STEP 1: Start the test in debug mode
tend run notebook-tui-comprehensive --debug-session

# The test will create a session and pause. Note the session name from output.
# Example output: "Debug session 'tend_notebook-tui-comprehensive' created"

# STEP 2: Verify the session exists
tend sessions list
# Output: tend_notebook-tui-comprehensive

# STEP 3: Explore the test environment
# First, check what directory and files exist
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "pwd" Enter
sleep 0.3
tend sessions capture tend_notebook-tui-comprehensive:term
# Shows: /var/folders/.../tend-debug-XXXXXXXX

# Check the structure
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "tree home/.grove" Enter
sleep 0.5
tend sessions capture tend_notebook-tui-comprehensive:term | tail -30
# Shows the notebook structure with test data

# STEP 4: Launch the TUI being tested
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "nb tui" Enter
sleep 1.5  # Give TUI time to initialize and load data

# STEP 5: Capture initial state
tend sessions capture tend_notebook-tui-comprehensive:term
# Shows: Notebook Browser with workspaces, notes, etc.

# STEP 6: Explore by sending keys and observing changes
# Navigate down
tend sessions send-keys tend_notebook-tui-comprehensive:term -- j j j
sleep 0.3
tend sessions capture tend_notebook-tui-comprehensive:term

# Open help
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "?"
sleep 0.5
tend sessions capture tend_notebook-tui-comprehensive:term
# Shows all keyboard shortcuts

# Exit help
tend sessions send-keys tend_notebook-tui-comprehensive:term -- Escape
sleep 0.3

# Try expanding/collapsing sections
tend sessions send-keys tend_notebook-tui-comprehensive:term -- Enter
sleep 0.3
OUTPUT=$(tend sessions capture tend_notebook-tui-comprehensive:term)
echo "$OUTPUT" | grep "▶" || echo "Section collapsed"

# STEP 7: Document findings
# After exploring, you now understand:
# - What keys work (j/k for navigation, Enter to expand/collapse)
# - What the UI shows (workspaces, notes organized by status)
# - How state changes (cursor moves, sections toggle)

# STEP 8: Quit the TUI and clean up
tend sessions send-keys tend_notebook-tui-comprehensive:term -- q
sleep 0.3

# Kill the debug session
tend sessions kill tend_notebook-tui-comprehensive

# STEP 9: Use your findings to write or improve tests
# Now you can write proper Go tests using ctx.StartTUI() API
# based on what you learned from exploration
```

## Workflow Examples

### Example 1: Exploring a TUI Application

```bash
# 1. Start the TUI in a tmux session
tmux new-session -d -s tend_explorer "my-tui-app"
sleep 0.5  # Give it time to initialize

# 2. See what's on screen
tend sessions capture tend_explorer

# 3. Navigate down
tend sessions send-keys tend_explorer -- j
sleep 0.2

# 4. Check what changed
tend sessions capture tend_explorer

# 5. Select an item
tend sessions send-keys tend_explorer -- Enter
sleep 0.3

# 6. Verify the result
tend sessions capture tend_explorer

# 7. Clean up
tend sessions kill tend_explorer
```

### Example 2: Testing a File Browser TUI

```bash
# Start the TUI
tmux new-session -d -s tend_filebrowser_test "filebrowser"
sleep 0.5

# Initial state - cursor should be on first item
OUTPUT=$(tend sessions capture tend_filebrowser_test)
echo "$OUTPUT" | grep "> README.md" || echo "FAIL: Initial cursor not on README"

# Move down twice
tend sessions send-keys tend_filebrowser_test -- j j
sleep 0.2

# Cursor should now be on third item
OUTPUT=$(tend sessions capture tend_filebrowser_test)
echo "$OUTPUT" | grep "> docs/guide.md" || echo "FAIL: Cursor not on third item"

# Select it
tend sessions send-keys tend_filebrowser_test -- Enter
sleep 0.2

# Verify selection
OUTPUT=$(tend sessions capture tend_filebrowser_test)
echo "$OUTPUT" | grep "Selected: docs/guide.md" || echo "FAIL: Item not selected"

# Cleanup
tend sessions kill tend_filebrowser_test
```

### Example 3: Multi-State TUI Testing

```bash
# Start task manager TUI
tmux new-session -d -s tend_taskmgr "task-manager"
sleep 0.5

# Verify menu is displayed
OUTPUT=$(tend sessions capture tend_taskmgr)
echo "$OUTPUT" | grep "Select action:" || echo "FAIL: Menu not shown"

# Choose option 1 (Process files)
tend sessions send-keys tend_taskmgr -- 1
sleep 0.5  # Wait for processing

# Verify success message
OUTPUT=$(tend sessions capture tend_taskmgr)
echo "$OUTPUT" | grep "✓ Success" || echo "FAIL: Success message not shown"
echo "$OUTPUT" | grep "15 files modified" || echo "FAIL: File count not shown"

# Cleanup
tend sessions kill tend_taskmgr
```

## Best Practices

### 1. Always Wait After Sending Keys
TUIs need time to process input and re-render. Add `sleep` between commands:
```bash
tend sessions send-keys tend_my_session -- j
sleep 0.2  # Short wait for simple navigation
tend sessions send-keys tend_my_session -- Enter
sleep 0.5  # Longer wait for complex operations
```

### 2. Use Descriptive Session Names
```bash
# Good
tmux new-session -d -s tend_test_navigation "my-app"
tmux new-session -d -s tend_debug_menu_crash "my-app"

# Bad (not prefixed with tend_)
tmux new-session -d -s my_test "my-app"  # Won't be listed by tend sessions
```

### 3. Always Clean Up Sessions
```bash
# At the end of your exploration/testing
tend sessions kill tend_my_test_session

# Or kill all test sessions at once
tend sessions kill --all
```

### 4. Capture Before and After
Always capture state before and after interactions to verify changes:
```bash
BEFORE=$(tend sessions capture tend_test)
tend sessions send-keys tend_test -- j
sleep 0.2
AFTER=$(tend sessions capture tend_test)

# Compare or assert on changes
diff <(echo "$BEFORE") <(echo "$AFTER")
```

### 5. Test Error Conditions
Don't just test happy paths:
```bash
# Try navigating past the end of a list
tend sessions send-keys tend_test -- j j j j j j j j
sleep 0.2
OUTPUT=$(tend sessions capture tend_test)
# Verify it doesn't crash or show errors
```

## When to Use These Commands vs. Writing Go Tests

**Use `tend sessions` commands when:**
- You (the LLM agent) are exploring an unfamiliar TUI to understand how it works
- You want to manually debug a TUI issue step-by-step
- You're documenting TUI workflows or writing usage guides
- You need to quickly test something interactively via bash

**Write Go test scenarios when:**
- You're creating automated, repeatable tests for CI/CD
- You need robust error handling and assertions
- You want to test TUI functionality as part of the test suite

### Writing Proper Go Tests for TUIs

When you need to write automated tests, use the `ctx.StartTUI()` API in tend test scenarios:

```go
harness.NewScenario(
    "tui-navigation-test",
    "Tests navigation in file browser TUI",
    []string{"tui"},
    []harness.Step{
        harness.NewStep("Launch TUI", func(ctx *harness.Context) error {
            // Build the fixture TUI binary
            ctx.RunCommand("go", "build", "-o", "/tmp/filebrowser", "./fixtures/list-tui/main.go")

            // Start it in a TUI session (automatically creates tmux session)
            session, err := ctx.StartTUI("/tmp/filebrowser", []string{})
            if err != nil {
                return err
            }
            ctx.Set("session", session)

            // Wait for initial render
            return session.WaitForText("File Browser", 2*time.Second)
        }),

        harness.NewStep("Navigate down", func(ctx *harness.Context) error {
            session := ctx.Get("session").(*tui.Session)

            // Send keys via the session API
            if err := session.SendKeys("j"); err != nil {
                return err
            }

            // Wait for cursor to move
            time.Sleep(200 * time.Millisecond)

            // Verify cursor position
            content, err := session.Capture()
            if err != nil {
                return err
            }

            if !strings.Contains(content, "> main.go") {
                return fmt.Errorf("expected cursor on main.go, got:\n%s", content)
            }
            return nil
        }),
    },
)
```

### The Difference

- **`ctx.StartTUI()`**: Creates and manages tmux sessions automatically, provides a clean API, handles cleanup
- **`tend sessions` commands**: Manual CLI tools for interactive exploration by LLM agents via bash

Use `tend sessions` commands to learn about a TUI, then write proper Go tests using `ctx.StartTUI()` for the test suite.

## Troubleshooting

### Session Not Found
```bash
# List all sessions to verify it exists
tend sessions list

# Check tmux directly
tmux list-sessions
```

### Capture Returns Empty or Unexpected Content
```bash
# Try specifying the window/pane explicitly
tend sessions capture tend_my_session:0.0

# Attach manually to inspect
tend sessions attach tend_my_session
```

### Keys Not Working
```bash
# Make sure you're using the correct syntax with --
tend sessions send-keys tend_test -- j  # Correct
tend sessions send-keys tend_test j     # Wrong

# Try special key names
tend sessions send-keys tend_test -- Down
tend sessions send-keys tend_test -- Enter
```

### TUI Not Responding
```bash
# Attach to manually inspect
tend sessions attach tend_my_session

# The TUI might be waiting for input or in an error state
# Use Ctrl+B, D to detach after inspecting
```

## Summary

With `tend sessions` commands, you can programmatically drive any TUI application:

1. **Start** a TUI in a tmux session (prefixed with `tend_`)
2. **Capture** its current state to see what's on screen
3. **Send keys** to navigate and interact
4. **Verify** the results by capturing again
5. **Clean up** by killing the session

This gives you full control to explore, test, and debug TUI applications without manual intervention!
