package status_tui

import (
	"testing"
)

func TestParseAgentPane_RunningState_Thinking(t *testing.T) {
	// Simulating Claude CLI output when thinking (✶ icon)
	output := `
some previous output here
more content

✶ Unravelling… (esc to interrupt · 8s · ↓ 220 tokens · thinking)

  ⏵⏵ bypass permissions on (shift+tab to                                                96162 tokens
`

	status := ParseAgentPane(output)
	if status == nil {
		t.Fatal("Expected status, got nil")
	}

	// All active states (thinking, streaming, working) map to "running"
	if status.State != "running" {
		t.Errorf("Expected state 'running', got '%s'", status.State)
	}

	// RawLine should capture the status line with "esc to interrupt" stripped
	expectedRaw := "✶ Unravelling… (8s · ↓ 220 tokens · thinking)"
	if status.RawLine != expectedRaw {
		t.Errorf("Expected RawLine '%s', got '%s'", expectedRaw, status.RawLine)
	}

	if status.Activity != "Unravelling" {
		t.Errorf("Expected activity 'Unravelling', got '%s'", status.Activity)
	}

	if status.Duration != "8s" {
		t.Errorf("Expected duration '8s', got '%s'", status.Duration)
	}

	if status.TokenFlow != "↓" {
		t.Errorf("Expected token flow '↓', got '%s'", status.TokenFlow)
	}

	if status.DeltaTokens != "220" {
		t.Errorf("Expected delta tokens '220', got '%s'", status.DeltaTokens)
	}

	if status.TotalTokens != 96162 {
		t.Errorf("Expected total tokens 96162, got %d", status.TotalTokens)
	}
}

func TestParseAgentPane_RunningState_Streaming(t *testing.T) {
	// Simulating Claude CLI output when streaming (· icon)
	output := `
· Unravelling… (esc to interrupt · 20s · ↓ 1.4k tokens · thought for 6s)

  >> some input here                                                           133990 tokens
`

	status := ParseAgentPane(output)
	if status == nil {
		t.Fatal("Expected status, got nil")
	}

	// All active states map to "running"
	if status.State != "running" {
		t.Errorf("Expected state 'running', got '%s'", status.State)
	}

	if status.DeltaTokens != "1.4k" {
		t.Errorf("Expected delta tokens '1.4k', got '%s'", status.DeltaTokens)
	}

	if status.TotalTokens != 133990 {
		t.Errorf("Expected total tokens 133990, got %d", status.TotalTokens)
	}
}

func TestParseAgentPane_RunningState_Working(t *testing.T) {
	// Simulating Claude CLI output when working (✻ icon)
	output := `
✻ Building and verifying changes… (esc to interrupt · ctrl+t to hide todos · 1m 58s · ↑ 4.9k tokens)
  ⎿  ☒ Capture tmux pane to see todo UI format
     ☐ Document todo list parsing patterns
     ☐ Update spec with todo UI details

  ⏵⏵ testing                                                                    45000 tokens
`

	status := ParseAgentPane(output)
	if status == nil {
		t.Fatal("Expected status, got nil")
	}

	// All active states map to "running"
	if status.State != "running" {
		t.Errorf("Expected state 'running', got '%s'", status.State)
	}

	if status.Activity != "Building and verifying changes" {
		t.Errorf("Expected activity 'Building and verifying changes', got '%s'", status.Activity)
	}

	if status.Duration != "1m 58s" {
		t.Errorf("Expected duration '1m 58s', got '%s'", status.Duration)
	}

	if status.TokenFlow != "↑" {
		t.Errorf("Expected token flow '↑', got '%s'", status.TokenFlow)
	}

	if status.DeltaTokens != "4.9k" {
		t.Errorf("Expected delta tokens '4.9k', got '%s'", status.DeltaTokens)
	}

	if status.TotalTokens != 45000 {
		t.Errorf("Expected total tokens 45000, got %d", status.TotalTokens)
	}

	// Verify todo items were parsed
	if len(status.TodoItems) != 3 {
		t.Errorf("Expected 3 todo items, got %d", len(status.TodoItems))
	}
	if len(status.TodoItems) >= 3 {
		// First item should be completed
		if !status.TodoItems[0].Completed {
			t.Errorf("Expected first todo item to be completed")
		}
		if status.TodoItems[0].Text != "Capture tmux pane to see todo UI format" {
			t.Errorf("Expected first todo item text 'Capture tmux pane to see todo UI format', got '%s'", status.TodoItems[0].Text)
		}
		// Second and third items should be pending
		if status.TodoItems[1].Completed {
			t.Errorf("Expected second todo item to be pending")
		}
		if status.TodoItems[1].Text != "Document todo list parsing patterns" {
			t.Errorf("Expected second todo item text 'Document todo list parsing patterns', got '%s'", status.TodoItems[1].Text)
		}
		if status.TodoItems[2].Completed {
			t.Errorf("Expected third todo item to be pending")
		}
		if status.TodoItems[2].Text != "Update spec with todo UI details" {
			t.Errorf("Expected third todo item text 'Update spec with todo UI details', got '%s'", status.TodoItems[2].Text)
		}
	}
}

func TestParseAgentPane_IdleState(t *testing.T) {
	// When idle, there's no status line - just the input prompt
	output := `
Done! You can now test through the TUI.

────────────────────────────────────────────────────────────────────────────────

  > type something here                                                        50000 tokens
`

	status := ParseAgentPane(output)
	if status == nil {
		t.Fatal("Expected status, got nil")
	}

	if status.State != "idle" {
		t.Errorf("Expected state 'idle', got '%s'", status.State)
	}

	if status.Activity != "Waiting for input..." {
		t.Errorf("Expected activity 'Waiting for input...', got '%s'", status.Activity)
	}

	if status.TotalTokens != 50000 {
		t.Errorf("Expected total tokens 50000, got %d", status.TotalTokens)
	}
}

func TestParseAgentPane_NoContent(t *testing.T) {
	output := ""
	status := ParseAgentPane(output)
	if status != nil {
		t.Errorf("Expected nil for empty output, got %+v", status)
	}
}

func TestParseAgentPane_NoUsefulContent(t *testing.T) {
	output := `
Just some random text
without any status indicators
or token counts
`
	status := ParseAgentPane(output)
	if status != nil {
		t.Errorf("Expected nil for output without useful content, got %+v", status)
	}
}

func TestAgentStatus_StateIcon(t *testing.T) {
	tests := []struct {
		state    string
		expected string
	}{
		{"running", "●"},
		{"idle", "○"},
		{"unknown", "?"},
	}

	for _, tt := range tests {
		status := &AgentStatus{State: tt.state}
		if got := status.StateIcon(); got != tt.expected {
			t.Errorf("StateIcon() for state '%s': expected '%s', got '%s'", tt.state, tt.expected, got)
		}
	}
}
