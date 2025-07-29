package orchestration_test

import (
	"strings"
	"testing"
	
	"github.com/grovepm/grove-jobs/pkg/orchestration"
)

func TestParseChatFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantTurns int
		wantErr   bool
	}{
		{
			name: "simple conversation",
			content: `---
id: test-plan
title: Test Plan
---

Initial user prompt

---

## LLM Response

This is the LLM's response

---

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
			name:      "empty file",
			content:   "",
			wantTurns: 0,
			wantErr:   true,
		},
		{
			name: "only frontmatter",
			content: `---
id: test-plan
title: Test Plan
---`,
			wantTurns: 0,
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

func TestParseChatFileMultipleTurns(t *testing.T) {
	content := `---
id: test-plan
title: Test Plan
---

Initial prompt about a web API

---

## LLM Response (2024-01-15 10:30:00)

Here's my plan for the web API:
1. Create endpoints
2. Add authentication
3. Implement database

---

<!-- grove: {"template": "refine-plan-generic"} -->
> Can you add more detail about the authentication?

---

## LLM Response (2024-01-15 10:35:00)

Sure! For authentication:
- Use JWT tokens
- Implement OAuth2
- Add rate limiting

---

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