package status_tui

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AgentStatus represents the parsed status of a Claude session from tmux pane output.
type AgentStatus struct {
	State       string     // "running" or "idle"
	RawLine     string     // The raw status line from Claude (e.g., "✳ Frolicking… (1m 8s · ↑ 4.6k tokens · thinking)")
	Activity    string     // e.g., "Unravelling", "Building and verifying changes"
	Duration    string     // e.g., "8s", "1m 58s"
	TokenFlow   string     // "↑" (upload) or "↓" (download)
	DeltaTokens string     // e.g., "220", "4.9k"
	TotalTokens int        // e.g., 133990
	TodoItems   []TodoItem // Parsed todo items from Claude's todo list
	LastUpdate  time.Time  // When this status was captured
}

// TodoItem represents a single item in Claude's todo list
type TodoItem struct {
	Text      string // The todo item text
	Completed bool   // Whether the item is checked (☒) or unchecked (☐)
}

var (
	// Match the total tokens line (appears on the input prompt line):
	// "  ⏵⏵ bypass permissions on (shift+tab to                                                133990 tokens"
	// Or just a line ending with "NNN tokens"
	totalTokensRe = regexp.MustCompile(`(\d+)\s+tokens\s*$`)

	// Match the status line format:
	// "✶ Unravelling… (esc to interrupt · 8s · ↓ 220 tokens · thinking)"
	// "· Unravelling… (esc to interrupt · 20s · ↓ 1.4k tokens · thought for 6s)"
	// "✻ Building and verifying changes… (esc to interrupt · ctrl+t to hide todos · 1m 58s · ↑ 4.9k tokens)"
	// The icon must be at the start (possibly with leading whitespace)
	statusLineRe = regexp.MustCompile(`^\s*([✶·✻])\s+(.+?)…\s+\((.+)\)\s*$`)

	// Match duration patterns like "8s", "1m 58s", "2m", "1h 2m"
	durationRe = regexp.MustCompile(`\b(\d+[smh](?:\s*\d+[smh])?)\b`)

	// Match token delta patterns like "↑ 4.9k tokens" or "↓ 220 tokens"
	tokenDeltaRe = regexp.MustCompile(`([↑↓])\s+([\d.]+k?)\s+tokens`)

	// Match todo item lines:
	// "  ⎿  ☒ Capture tmux pane to see todo UI format"
	// "     ☐ Document todo list parsing patterns"
	// The checkbox can be ☒ (completed) or ☐ (pending)
	todoItemRe = regexp.MustCompile(`^\s*(?:⎿\s+)?([☒☐])\s+(.+?)\s*$`)
)

// ParseAgentPane parses raw tmux pane output to extract Claude session status.
// It scans from the bottom up, looking for the status line and token count.
// States are simplified to just "idle" (waiting for input) or "running" (any active state).
func ParseAgentPane(output string) *AgentStatus {
	lines := strings.Split(output, "\n")

	// Scan from bottom up (check last ~30 lines)
	scanLimit := 30
	if len(lines) < scanLimit {
		scanLimit = len(lines)
	}

	status := &AgentStatus{
		LastUpdate: time.Now(),
	}

	var foundStatusLine bool
	var foundTotalTokens bool
	var inputPromptLineIndex int = -1
	var statusLineIndex int = -1

	// Scan from bottom up
	for i := len(lines) - 1; i >= len(lines)-scanLimit && i >= 0; i-- {
		line := lines[i]

		// Look for total tokens (usually on the input prompt line)
		if !foundTotalTokens {
			if matches := totalTokensRe.FindStringSubmatch(line); len(matches) > 1 {
				if tokens, err := strconv.Atoi(matches[1]); err == nil {
					status.TotalTokens = tokens
					foundTotalTokens = true
				}
			}
		}

		// Check if this is an input prompt line (contains ⏵⏵ or > )
		if inputPromptLineIndex < 0 {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "⏵⏵") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, ">> ") {
				inputPromptLineIndex = i
			}
		}

		// Look for status line with icon (✶ thinking, · streaming, ✻ working)
		// All of these mean "running" - we simplify to idle vs running
		if !foundStatusLine {
			if matches := statusLineRe.FindStringSubmatch(line); len(matches) > 0 {
				activity := matches[2]
				parenBlock := matches[3]

				// Any status icon means the agent is running
				status.State = "running"
				// Capture the raw line but strip "esc to interrupt · " and "ctrl+t to hide todos · "
				rawLine := strings.TrimSpace(line)
				rawLine = strings.Replace(rawLine, "esc to interrupt · ", "", 1)
				rawLine = strings.Replace(rawLine, "ctrl+t to hide todos · ", "", 1)
				status.RawLine = rawLine
				status.Activity = strings.TrimSpace(activity)

				// Parse the parenthetical block for duration and token delta
				// Format: "esc to interrupt · 8s · ↓ 220 tokens · thinking"
				// or: "esc to interrupt · ctrl+t to hide todos · 1m 58s · ↑ 4.9k tokens"

				// Extract duration
				if durMatch := durationRe.FindStringSubmatch(parenBlock); len(durMatch) > 1 {
					status.Duration = durMatch[1]
				}

				// Extract token delta
				if tokenMatch := tokenDeltaRe.FindStringSubmatch(parenBlock); len(tokenMatch) > 2 {
					status.TokenFlow = tokenMatch[1]
					status.DeltaTokens = tokenMatch[2]
				}

				foundStatusLine = true
				statusLineIndex = i
			}
		}

		// If we found both, we're done with the main scan
		if foundStatusLine && foundTotalTokens {
			break
		}
	}

	// If we found a status line, scan forward from it to find todo items
	if statusLineIndex >= 0 {
		for i := statusLineIndex + 1; i < len(lines); i++ {
			line := lines[i]
			// Stop if we hit the input prompt or an empty line after todos
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				// Skip empty lines but continue looking
				continue
			}
			if strings.HasPrefix(trimmed, "⏵⏵") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, ">> ") {
				break
			}

			// Try to match a todo item
			if matches := todoItemRe.FindStringSubmatch(line); len(matches) > 2 {
				checkbox := matches[1]
				text := matches[2]
				status.TodoItems = append(status.TodoItems, TodoItem{
					Text:      text,
					Completed: checkbox == "☒",
				})
			} else if len(status.TodoItems) > 0 {
				// If we already found todos and this line doesn't match, stop looking
				break
			}
		}
	}

	// Idle heuristic: if we found the input prompt but no status line with [✶·✻],
	// the agent is idle (waiting for user input)
	if !foundStatusLine && inputPromptLineIndex >= 0 {
		status.State = "idle"
		status.Activity = "Waiting for input..."
	}

	// If we found nothing useful, return nil
	if status.State == "" && status.TotalTokens == 0 {
		return nil
	}

	return status
}

// StateIcon returns the appropriate icon for the agent state.
func (s *AgentStatus) StateIcon() string {
	switch s.State {
	case "running":
		return "●" // Solid dot for running
	case "idle":
		return "○" // Empty dot for idle
	default:
		return "?"
	}
}
