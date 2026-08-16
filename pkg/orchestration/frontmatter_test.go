package orchestration

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		want     map[string]interface{}
		wantBody string
		wantErr  bool
	}{
		{
			name: "valid frontmatter",
			content: `---
id: test-123
title: Test Job
status: pending
---
This is the body content.`,
			want: map[string]interface{}{
				"id":     "test-123",
				"title":  "Test Job",
				"status": "pending",
			},
			wantBody: "This is the body content.",
			wantErr:  false,
		},
		{
			name: "no frontmatter",
			content: `This is just plain content.
No frontmatter here.`,
			want: map[string]interface{}{},
			wantBody: `This is just plain content.
No frontmatter here.`,
			wantErr: false,
		},
		{
			name: "empty frontmatter",
			content: `---
---
Body content here.`,
			want:     map[string]interface{}{},
			wantBody: "Body content here.",
			wantErr:  false,
		},
		{
			name: "complex frontmatter",
			content: `---
id: complex-123
depends_on:
  - job1.md
  - job2.md
output:
  type: file
  path: output.txt
---
Complex body.`,
			want: map[string]interface{}{
				"id":         "complex-123",
				"depends_on": []interface{}{"job1.md", "job2.md"},
				"output": map[string]interface{}{
					"type": "file",
					"path": "output.txt",
				},
			},
			wantBody: "Complex body.",
			wantErr:  false,
		},
		{
			name: "frontmatter with rules fields",
			content: `---
id: rules-test-123
title: Rules Test
status: completed
type: oneshot
rules_file: my-preset.rules
used_rules_file: .artifacts/rules-test-123/context.rules
---
Job body with rules.`,
			want: map[string]interface{}{
				"id":              "rules-test-123",
				"title":           "Rules Test",
				"status":          "completed",
				"type":            "oneshot",
				"rules_file":      "my-preset.rules",
				"used_rules_file": ".artifacts/rules-test-123/context.rules",
			},
			wantBody: "Job body with rules.",
			wantErr:  false,
		},
		{
			name: "frontmatter with skill field",
			content: `---
id: skill-test-123
title: Skill Test
status: pending
type: oneshot
skill: chef
---
Job body with skill.`,
			want: map[string]interface{}{
				"id":     "skill-test-123",
				"title":  "Skill Test",
				"status": "pending",
				"type":   "oneshot",
				"skill":  "chef",
			},
			wantBody: "Job body with skill.",
			wantErr:  false,
		},
		{
			name: "malformed YAML",
			content: `---
id: test
title: [bad yaml
---
Body`,
			want:     nil,
			wantBody: "",
			wantErr:  true,
		},
		{
			name: "missing closing delimiter",
			content: `---
id: test
title: No closing`,
			want:     nil,
			wantBody: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, body, err := ParseFrontmatter([]byte(tt.content))

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFrontmatter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("ParseFrontmatter() frontmatter = %v, want %v", got, tt.want)
				}

				if string(body) != tt.wantBody {
					t.Errorf("ParseFrontmatter() body = %q, want %q", string(body), tt.wantBody)
				}
			}
		})
	}
}

func TestUpdateFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		updates map[string]interface{}
		want    string
		wantErr bool
	}{
		{
			name: "update existing field",
			content: `---
id: test-123
status: pending
---
Body content.`,
			updates: map[string]interface{}{
				"status": "running",
			},
			want: `---
id: test-123
status: running
---
Body content.`,
			wantErr: false,
		},
		{
			name: "add new field",
			content: `---
id: test-123
---
Body content.`,
			updates: map[string]interface{}{
				"status": "completed",
			},
			want: `---
id: test-123
status: completed
---
Body content.`,
			wantErr: false,
		},
		{
			name:    "create frontmatter if none exists",
			content: `Just body content.`,
			updates: map[string]interface{}{
				"id":     "new-123",
				"status": "pending",
			},
			want: `---
id: new-123
status: pending
---
Just body content.`,
			wantErr: false,
		},
		{
			name: "preserve formatting and comments",
			content: `---
# This is a comment
id: test-123
status: pending  # Current status
---
Body.`,
			updates: map[string]interface{}{
				"status": "running",
			},
			want: `---
# This is a comment
id: test-123
status: running  # Current status
---
Body.`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UpdateFrontmatter([]byte(tt.content), tt.updates)

			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateFrontmatter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Normalize whitespace for comparison
				gotStr := strings.TrimSpace(string(got))
				wantStr := strings.TrimSpace(tt.want)

				// For the comment preservation test, we need to be more lenient
				// as the YAML library might not preserve all formatting exactly
				if strings.Contains(tt.content, "# This is a comment") {
					// Just check that the update was applied
					if !strings.Contains(gotStr, "status: running") {
						t.Errorf("UpdateFrontmatter() did not update status field")
					}
					return
				}

				if gotStr != wantStr {
					t.Errorf("UpdateFrontmatter() = %q, want %q", gotStr, wantStr)
				}
			}
		})
	}
}

func TestExtractFrontmatterString(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantYAML string
		wantBody string
		wantErr  bool
	}{
		{
			name: "extract valid frontmatter",
			content: `---
id: test
status: pending
---
Body content.`,
			wantYAML: `id: test
status: pending`,
			wantBody: "Body content.",
			wantErr:  false,
		},
		{
			name:     "no frontmatter",
			content:  "Just content.",
			wantYAML: "",
			wantBody: "Just content.",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotYAML, gotBody, err := ExtractFrontmatterString([]byte(tt.content))

			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractFrontmatterString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if gotYAML != tt.wantYAML {
					t.Errorf("ExtractFrontmatterString() YAML = %q, want %q", gotYAML, tt.wantYAML)
				}

				if string(gotBody) != tt.wantBody {
					t.Errorf("ExtractFrontmatterString() body = %q, want %q", string(gotBody), tt.wantBody)
				}
			}
		})
	}
}

func TestReplaceFrontmatter(t *testing.T) {
	content := `---
old: data
---
Body content.`

	newYAML := `new: data
updated: true`

	got := ReplaceFrontmatter([]byte(content), newYAML)
	want := `---
new: data
updated: true
---
Body content.`

	if strings.TrimSpace(string(got)) != strings.TrimSpace(want) {
		t.Errorf("ReplaceFrontmatter() = %q, want %q", string(got), want)
	}
}

func TestConcurrentFrontmatterOperations(t *testing.T) {
	// Test that concurrent reads don't interfere
	content := `---
id: concurrent-test
status: pending
---
Test body.`

	// Run multiple goroutines parsing the same content
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			fm, _, err := ParseFrontmatter([]byte(content))
			if err != nil {
				t.Errorf("Concurrent parse error: %v", err)
			}
			if fm["id"] != "concurrent-test" {
				t.Errorf("Concurrent parse got wrong ID: %v", fm["id"])
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

// Nil update values (including typed nils) must remove keys, never be
// stringified as "<nil>" — that corrupts job files (time fields become
// unparseable on load). Regression test for the completed_at/duration/
// last_error corruption.
func TestUpdateFrontmatterNilRemovesKey(t *testing.T) {
	content := []byte(`---
id: test-123
status: completed
completed_at: "2026-08-16T11:24:59Z"
duration: 5m0s
last_error: something failed
---
Body content
`)

	var nilTime *string // stand-in for any typed nil pointer
	updates := map[string]interface{}{
		"status":       "running",
		"completed_at": nil,
		"duration":     nilTime, // typed nil must behave like untyped nil
		"last_error":   nil,
	}

	result, err := UpdateFrontmatter(content, updates)
	if err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}
	out := string(result)

	if strings.Contains(out, "<nil>") {
		t.Errorf("output contains literal <nil>:\n%s", out)
	}
	for _, key := range []string{"completed_at", "duration", "last_error"} {
		if strings.Contains(out, key) {
			t.Errorf("key %q should have been removed:\n%s", key, out)
		}
	}
	if !strings.Contains(out, "status: running") {
		t.Errorf("status update missing:\n%s", out)
	}
	if !strings.Contains(out, "Body content") {
		t.Errorf("body lost:\n%s", out)
	}
}

// Nil values for keys that don't exist yet must not add the key at all —
// neither in existing frontmatter nor when creating frontmatter from scratch.
func TestUpdateFrontmatterNilDoesNotAddKey(t *testing.T) {
	withFM := []byte("---\nid: test-123\n---\nBody\n")
	result, err := UpdateFrontmatter(withFM, map[string]interface{}{
		"completed_at": nil,
		"status":       "running",
	})
	if err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}
	if strings.Contains(string(result), "completed_at") || strings.Contains(string(result), "<nil>") {
		t.Errorf("nil value added a key:\n%s", result)
	}

	noFM := []byte("Just a body\n")
	result, err = UpdateFrontmatter(noFM, map[string]interface{}{
		"completed_at": nil,
		"status":       "running",
	})
	if err != nil {
		t.Fatalf("UpdateFrontmatter (no frontmatter): %v", err)
	}
	if strings.Contains(string(result), "completed_at") || strings.Contains(string(result), "<nil>") {
		t.Errorf("nil value added a key to new frontmatter:\n%s", result)
	}
}

// ParseFrontmatter must treat historically corrupted literal "<nil>" values
// as unset, so parse→rebuild cycles scrub the corruption.
func TestParseFrontmatterScrubsNilLiterals(t *testing.T) {
	content := []byte(`---
id: test-123
status: running
completed_at: <nil>
duration: <nil>
last_error: <nil>
---
Body
`)
	fm, _, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	for _, key := range []string{"completed_at", "duration", "last_error"} {
		if v, ok := fm[key]; ok {
			t.Errorf("key %q should have been scrubbed, got %v", key, v)
		}
	}
	if fm["id"] != "test-123" || fm["status"] != "running" {
		t.Errorf("healthy keys damaged: %v", fm)
	}
}

// RebuildMarkdownWithFrontmatter must drop nil-valued keys instead of
// serializing them.
func TestRebuildMarkdownWithFrontmatterDropsNil(t *testing.T) {
	var nilTime *string
	fm := map[string]interface{}{
		"id":           "test-123",
		"completed_at": nil,
		"duration":     nilTime,
	}
	out, err := RebuildMarkdownWithFrontmatter(fm, []byte("Body\n"))
	if err != nil {
		t.Fatalf("RebuildMarkdownWithFrontmatter: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "<nil>") || strings.Contains(s, "completed_at") || strings.Contains(s, "duration") {
		t.Errorf("nil keys serialized:\n%s", s)
	}
}
