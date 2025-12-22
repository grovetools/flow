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
   - Press `Enter` to advance to the next step
   - Press `a` to attach and see the full test output
   - Press `q` to quit the test
2. **editor_test_dir** - Neovim editor for the test directory
3. **editor_test_steps** - Neovim editor for the test steps/scenario file
4. **term** - Shell with the sandboxed test environment (⭐ **USE THIS FOR EXPLORATION!**)
5. **logs** - Log viewer (core logs --tui)

### Understanding the Runner Window

The **runner** window shows test progress and **pauses at each step** when using `--debug-session`. This is intentional - it lets you explore the test state in the `term` window before moving on.

**Important**: The test won't advance until you press Enter in the runner window. This means:
- ✅ You can take your time exploring in the `term` window
- ✅ The test environment stays stable while you investigate
- ⚠️ If you send commands but nothing happens, check if the runner is waiting for you

**Pro tip**: You can monitor both windows:
```bash
# Check if runner is waiting
tend sessions capture <session>:runner | tail -5

# If you see "Press ENTER to continue", advance the test:
tend sessions send-keys <session>:runner -- Enter
```

### The `term` Window - Your Exploration Playground

The `term` window is where you should do your TUI exploration. It has:

- **Working directory**: The test's temporary root directory (e.g., `/var/folders/.../tend-debug-XXXXXXXX`)
- **Sandboxed HOME**: A fake HOME directory with test data at `home/`
- **Environment variables**: XDG_CONFIG_HOME, XDG_DATA_HOME, etc. all point to the test environment
- **Test context**: Whatever the test scenario set up is available here

### Understanding the Directory Structure

When you start a debug session, the directory structure looks like:

```
/var/folders/.../tend-debug-XXXXXXXX/    ← You are here (test root)
├── home/                                 ← Sandboxed HOME directory
│   ├── .config/                         ← XDG_CONFIG_HOME
│   ├── .local/share/                    ← XDG_DATA_HOME
│   ├── .cache/                          ← XDG_CACHE_HOME
│   └── code/                            ← Projects might be here
│       └── my-project/                  ← The actual project being tested
└── .grove/                              ← Test metadata
```

**Key insight**: The test root is NOT where your project is. Your project is typically at `home/code/<project-name>` or similar, depending on what the test set up.

**Pro tip**: First command to run:
```bash
tend sessions send-keys <session>:term -- "find . -name grove.yml" Enter
# This shows you where projects are located
```

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
# Basic capture (ANSI codes stripped by default for easier parsing)
tend sessions capture tend_my_session
tend sessions capture tend_my_session:0  # Specific window
tend sessions capture tend_my_session:0.1  # Specific pane

# Preserve ANSI codes if you need color information
tend sessions capture tend_my_session --with-ansi

# Wait for text to appear (polls every 200ms)
tend sessions capture tend_my_session --wait-for "Ready" --timeout 5s

# Combine: wait and preserve ANSI
tend sessions capture tend_my_session --wait-for "Complete" --with-ansi
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

**Flags:**
- `--with-ansi`: Preserve ANSI escape codes in output (default: strip for easier parsing)
- `--wait-for <text>`: Poll until text appears in the pane (useful after sending keys)
- `--timeout <duration>`: Timeout for --wait-for (default: 5s)

**Notes:**
- **By default, ANSI codes are stripped** for easier parsing by agents
- Returns clean plain text unless `--with-ansi` is specified
- Perfect for checking current TUI state before/after interactions
- Can target specific windows/panes using tmux target syntax
- Use `--wait-for` to avoid manual sleep commands - it polls until text appears or times out

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

## Advanced: Recording TUI Interactions with `tend record`

For more comprehensive documentation of TUI interactions, combine `tend sessions` with `tend record`:

```bash
# STEP 1: Start the test in debug mode
tend run notebook-tui-comprehensive --debug-session

# STEP 2: Launch tend record via the term window
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "tend record -- nb tui" Enter
sleep 2  # Wait for TUI to initialize

# STEP 3: Perform interactions via tend sessions commands
# Navigate down
tend sessions send-keys tend_notebook-tui-comprehensive:term -- j j j
sleep 0.5

# Open help menu
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "?"
sleep 0.5

# Close help
tend sessions send-keys tend_notebook-tui-comprehensive:term -- Escape
sleep 0.3

# Collapse a section
tend sessions send-keys tend_notebook-tui-comprehensive:term -- Enter
sleep 0.3

# Navigate more
tend sessions send-keys tend_notebook-tui-comprehensive:term -- j j
sleep 0.3

# STEP 4: Quit to save recording
tend sessions send-keys tend_notebook-tui-comprehensive:term -- q
sleep 0.5

# STEP 5: Examine the recordings
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "ls -la tend-recording.*" Enter
sleep 0.3
tend sessions capture tend_notebook-tui-comprehensive:term

# View the markdown recording
tend sessions send-keys tend_notebook-tui-comprehensive:term -- "cat tend-recording.md" Enter
sleep 0.5
tend sessions capture tend_notebook-tui-comprehensive:term
```

### What `tend record` Generates:

When you use `tend record -- <tui-command>`, it creates multiple output formats:

- **`tend-recording.md`** - Markdown with frames showing input and terminal state (clean text)
- **`tend-recording.ansi.md`** - Markdown preserving ANSI color codes
- **`tend-recording.xml`** - XML format optimized for LLM consumption
- **`tend-recording.ansi.xml`** - XML with ANSI codes preserved
- **`tend-recording.html`** - HTML playback for visual review

Each frame in the recording shows:
- **Input**: The keystroke(s) that were sent
- **Terminal State**: What the screen looked like after that input
- **Timestamp**: When the input occurred

### Why Use `tend record` with `tend sessions`?

1. **Automatic documentation** - Creates a complete record of your exploration
2. **Multiple formats** - Choose the best format for your use case (XML for parsing, MD for reading, HTML for playback)
3. **Frame-by-frame analysis** - See exactly what changed after each keystroke
4. **Test case generation** - Use recorded frames as the basis for assertions in tests
5. **Bug reports** - Attach recordings to show exactly what happened

### Example Use Case:

```bash
# You're exploring a new TUI feature and want to document it
tend run my-feature-test --debug-session

# Record the exploration
tend sessions send-keys tend_my-feature-test:term -- "tend record -- my-app tui" Enter
sleep 2

# Perform a sequence of actions
tend sessions send-keys tend_my-feature-test:term -- "n" "e" "w" Enter  # Create new item
sleep 0.5
tend sessions send-keys tend_my-feature-test:term -- "test-item" Enter  # Name it
sleep 0.5
tend sessions send-keys tend_my-feature-test:term -- "d" "d"  # Delete it
sleep 0.5
tend sessions send-keys tend_my-feature-test:term -- "y"  # Confirm
sleep 0.5

# Quit and save
tend sessions send-keys tend_my-feature-test:term -- "q"
sleep 0.5

# Now you have a complete recording showing:
# - Frame 1: Initial state
# - Frame 2: After pressing 'n' (new item dialog appears)
# - Frame 3: After entering name (item appears in list)
# - Frame 4: After 'dd' (delete confirmation)
# - Frame 5: After 'y' (item removed from list)

# Use this to write tests!
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

### 1. Use --wait-for Instead of sleep
Instead of guessing how long to wait, use `--wait-for` to poll until the TUI is ready:
```bash
# Old way (guessing timing)
tend sessions send-keys tend_my_session -- j
sleep 0.5  # Hope this is enough?
tend sessions capture tend_my_session

# Better way (wait for expected state)
tend sessions send-keys tend_my_session -- j
tend sessions capture tend_my_session --wait-for "main.go" --timeout 3s
```

### 2. ANSI Codes Stripped by Default
Capture strips ANSI codes by default for easier parsing. Use `--with-ansi` only if needed:
```bash
# Default: clean text (ANSI stripped)
OUTPUT=$(tend sessions capture tend_test)
# Output: "main"

# With ANSI codes preserved (if you need color info)
OUTPUT=$(tend sessions capture tend_test --with-ansi)
# Output: "\x1b[1mmain\x1b[0m"

# Easy to parse by default
if echo "$OUTPUT" | grep -q "Ready"; then
    echo "TUI is ready"
fi
```

### 3. Use Descriptive Session Names
```bash
# Good
tmux new-session -d -s tend_test_navigation "my-app"
tmux new-session -d -s tend_debug_menu_crash "my-app"

# Bad (not prefixed with tend_)
tmux new-session -d -s my_test "my-app"  # Won't be listed by tend sessions
```

### 4. Always Clean Up Sessions
```bash
# At the end of your exploration/testing
tend sessions kill tend_my_test_session

# Or kill all test sessions at once
tend sessions kill --all
```

### 5. Capture Before and After
Always capture state before and after interactions to verify changes:
```bash
BEFORE=$(tend sessions capture tend_test)
tend sessions send-keys tend_test -- j
sleep 0.2
AFTER=$(tend sessions capture tend_test)

# Compare or assert on changes
diff <(echo "$BEFORE") <(echo "$AFTER")
```

### 6. Test Error Conditions
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

### Commands Not Working / Nothing Happens

**Symptom**: You send commands to the `term` window but nothing seems to happen.

**Likely cause**: The test is paused waiting for you to advance in the `runner` window.

**Solution**:
```bash
# Check if runner is waiting
tend sessions capture <session>:runner | tail -10

# Look for: "▶ Press ENTER to continue, 'a' to attach, 'q' to quit:"

# If waiting, advance the test:
tend sessions send-keys <session>:runner -- Enter
```

### "Where is my project?"

**Symptom**: You're in `/var/folders/.../tend-debug-XXX` but can't find the project files.

**Solution**: Projects are typically in subdirectories like `home/code/`. Find them with:
```bash
tend sessions send-keys <session>:term -- "find . -name grove.yml" Enter
sleep 0.5
tend sessions capture <session>:term

# Or look in common locations:
tend sessions send-keys <session>:term -- "ls -la home/code/" Enter
```

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

## Quick Reference: Common Patterns

### Starting a Debug Session and Finding Your Project
```bash
# 1. Start the test
tend run my-test --debug-session

# 2. Find the project location
tend sessions send-keys <session>:term -- "find . -name grove.yml" Enter
sleep 0.5
tend sessions capture <session>:term

# 3. Navigate to the project (example)
tend sessions send-keys <session>:term -- "cd home/code/my-project" Enter
```

### Advancing Through Test Steps While Exploring
```bash
# Check if runner is waiting
tend sessions capture <session>:runner | grep -q "Press ENTER" && echo "Waiting for you!"

# Advance one step
tend sessions send-keys <session>:runner -- Enter

# Continue exploring in term window
tend sessions send-keys <session>:term -- "pwd" Enter
tend sessions capture <session>:term
```

### Interactive TUI Exploration Pattern
```bash
# Launch TUI
tend sessions send-keys <session>:term -- "my-tui" Enter

# Wait for it to be ready
tend sessions capture <session>:term --wait-for "Ready" --timeout 5s

# Interact and observe
tend sessions send-keys <session>:term -- j j
tend sessions capture <session>:term --wait-for "selected item"

# Quit
tend sessions send-keys <session>:term -- q
```

### Checking Both Runner and Term Status
```bash
# See where test is
echo "=== Runner Status ==="
tend sessions capture <session>:runner | tail -5

# See what's in term
echo "=== Term Status ==="
tend sessions capture <session>:term | head -20
```

## Summary

With `tend sessions` commands, you can programmatically drive any TUI application:

1. **Start** a debug session with `--debug-session`
2. **Locate** your project with `find . -name grove.yml`
3. **Advance** the runner window with Enter as needed
4. **Explore** in the `term` window with full environment
5. **Capture** state to see what's on screen (ANSI stripped by default)
6. **Send keys** to navigate and interact
7. **Verify** results by capturing again
8. **Clean up** by killing the session

This gives you full control to explore, test, and debug TUI applications without manual intervention!
