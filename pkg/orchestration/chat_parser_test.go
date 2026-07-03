package orchestration_test

import (
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

func TestParseChatFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantTurns int
		wantErr   bool
	}{
		{
			// Directive model: turns are split ONLY on <!-- grove: ... -->
			// directives. Leading content before the first directive is one
			// user turn; a directive with a template is a user turn, one
			// without is an llm turn.
			name: "simple conversation",
			content: `---
id: test-plan
title: Test Plan
---

Initial user prompt

<!-- grove: {"model": "claude-3-opus"} -->
This is the LLM's response

<!-- grove: {"template": "refine-plan-generic"} -->
> User feedback here`,
			wantTurns: 3,
			wantErr:   false,
		},
		{
			name: "with directive",
			content: `---
id: test-plan
title: Test Plan
---

Initial prompt

---

<!-- grove: {"template": "refine-plan-generic"} -->
> Please refine the plan`,
			wantTurns: 2,
			wantErr:   false,
		},
		{
			// Empty input yields a single empty user turn (a freshly created
			// chat file), not an error.
			name:      "empty file",
			content:   "",
			wantTurns: 1,
			wantErr:   false,
		},
		{
			// Frontmatter with a closing "---" at EOF (no trailing newline) is
			// now valid; the empty body yields one empty user turn.
			name: "only frontmatter",
			content: `---
id: test-plan
title: Test Plan
---`,
			wantTurns: 1,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turns, err := orchestration.ParseChatFile([]byte(tt.content))

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseChatFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(turns) != tt.wantTurns {
				t.Errorf("ParseChatFile() got %d turns, want %d", len(turns), tt.wantTurns)
			}
		})
	}
}

func TestParseChatFileWithDirective(t *testing.T) {
	content := `---
id: test-plan
title: Test Plan
---

Initial prompt

---

<!-- grove: {"template": "refine-plan-generic", "model": "claude-3-opus", "vars": {"focus": "security"}} -->
> Please focus on the database schema`

	turns, err := orchestration.ParseChatFile([]byte(content))
	if err != nil {
		t.Fatalf("ParseChatFile() error = %v", err)
	}

	if len(turns) != 2 {
		t.Fatalf("Expected 2 turns, got %d", len(turns))
	}

	userTurn := turns[1]
	if userTurn.Speaker != "user" {
		t.Errorf("Expected speaker to be 'user', got %s", userTurn.Speaker)
	}

	if userTurn.Directive == nil {
		t.Fatal("Expected directive to be parsed")
	}

	if userTurn.Directive.Template != "refine-plan-generic" {
		t.Errorf("Expected template 'refine-plan-generic', got %s", userTurn.Directive.Template)
	}

	if userTurn.Directive.Model != "claude-3-opus" {
		t.Errorf("Expected model 'claude-3-opus', got %s", userTurn.Directive.Model)
	}

	if userTurn.Directive.Vars == nil || userTurn.Directive.Vars["focus"] != "security" {
		t.Errorf("Expected vars with focus=security")
	}

	expectedContent := "Please focus on the database schema"
	if strings.TrimSpace(userTurn.Content) != expectedContent {
		t.Errorf("Expected content '%s', got '%s'", expectedContent, userTurn.Content)
	}
}

func TestInspectTrailingChatTurn(t *testing.T) {
	const fm = `---
id: test-plan
title: Test Plan
---

`
	tests := []struct {
		name       string
		body       string
		wantUser   bool
		wantOrphan bool
	}{
		{
			// A chat ending on a bare marker with nothing after it has no user
			// turn to dispatch.
			name: "bare trailing marker",
			body: `Initial prompt

<!-- grove: {"model": "claude-3-opus"} -->
An LLM response

<!-- grove: {"template": "chat"} -->`,
			wantUser:   false,
			wantOrphan: false,
		},
		{
			// Content after the last marker is a real user turn — dispatch it.
			name: "content after marker dispatches",
			body: `Initial prompt

<!-- grove: {"model": "claude-3-opus"} -->
An LLM response

<!-- grove: {"template": "chat"} -->
Please add more detail`,
			wantUser:   true,
			wantOrphan: false,
		},
		{
			// User typed the question, then pasted a fresh marker below it: the
			// question is stranded before the empty trailing marker.
			name: "content before trailing marker is orphaned",
			body: `My real question for the model

<!-- grove: {"template": "chat"} -->`,
			wantUser:   false,
			wantOrphan: true,
		},
		{
			// A brand-new empty chat (no content anywhere) has no user turn but
			// nothing is orphaned.
			name:       "empty chat",
			body:       ``,
			wantUser:   false,
			wantOrphan: false,
		},
		{
			// The normal "waiting after an LLM response" shape must NOT be
			// flagged as orphaned — the turn before the empty marker is the LLM.
			name: "waiting after llm response is not orphan",
			body: `Initial prompt

<!-- grove: {"model": "claude-3-opus"} -->
Here is my answer

<!-- grove: {"template": "chat"} -->`,
			wantUser:   false,
			wantOrphan: false,
		},
		{
			// A plain first-turn prompt with no markers at all is a user turn.
			name:       "leading prompt only",
			body:       `Just a prompt, no markers`,
			wantUser:   true,
			wantOrphan: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orchestration.InspectTrailingChatTurn([]byte(fm + tt.body))
			if got.HasUserTurn != tt.wantUser {
				t.Errorf("HasUserTurn = %v, want %v", got.HasUserTurn, tt.wantUser)
			}
			if got.OrphanBeforeMarker != tt.wantOrphan {
				t.Errorf("OrphanBeforeMarker = %v, want %v", got.OrphanBeforeMarker, tt.wantOrphan)
			}
		})
	}
}

func TestNoUserTurnError(t *testing.T) {
	err := orchestration.NoUserTurnError("/plans/demo/03-chat.md")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	// Names the problem...
	if !strings.Contains(msg, "no user turn after last chat marker") {
		t.Errorf("error should name the problem, got %q", msg)
	}
	// ...includes the job path...
	if !strings.Contains(msg, "/plans/demo/03-chat.md") {
		t.Errorf("error should include the job path, got %q", msg)
	}
	// ...and says how to fix it.
	if !strings.Contains(msg, "add your message") || !strings.Contains(msg, "below the last") {
		t.Errorf("error should explain how to fix it, got %q", msg)
	}
}

func TestParseChatFileMultipleTurns(t *testing.T) {
	// Directive model: each turn after the initial user prompt is introduced by
	// a <!-- grove: ... --> directive. A directive with a template is a user
	// turn; one without is an llm turn.
	content := `---
id: test-plan
title: Test Plan
---

Initial prompt about a web API

<!-- grove: {"model": "claude-3-opus"} -->
Here's my plan for the web API:
1. Create endpoints
2. Add authentication
3. Implement database

<!-- grove: {"template": "refine-plan-generic"} -->
> Can you add more detail about the authentication?

<!-- grove: {"model": "claude-3-opus"} -->
Sure! For authentication:
- Use JWT tokens
- Implement OAuth2
- Add rate limiting

<!-- grove: {"template": "refine-plan-generic"} -->
> Looks good, please proceed`

	turns, err := orchestration.ParseChatFile([]byte(content))
	if err != nil {
		t.Fatalf("ParseChatFile() error = %v", err)
	}

	if len(turns) != 5 {
		t.Fatalf("Expected 5 turns, got %d", len(turns))
	}

	// Check speakers alternate correctly
	expectedSpeakers := []string{"user", "llm", "user", "llm", "user"}
	for i, turn := range turns {
		if turn.Speaker != expectedSpeakers[i] {
			t.Errorf("Turn %d: expected speaker '%s', got '%s'", i, expectedSpeakers[i], turn.Speaker)
		}
	}

	// Check directive exists on turn 2 (index 2)
	if turns[2].Directive == nil {
		t.Error("Expected directive on turn 3")
	}
}
