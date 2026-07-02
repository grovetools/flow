package main

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/agentlogs/pkg/transcript"
)

// TestPiSessionFilenameEncoding pins the filename munging to pi's real
// encoding: ISO timestamp with ":" and "." replaced by "-", "_", then the
// session id. This is the contract flow's piNativeSessionID parser (segment
// after the last "_") depends on.
func TestPiSessionFilenameEncoding(t *testing.T) {
	ts := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	id := "0198c2f4-9a51-7abc-8def-0123456789ab"

	got := piSessionFilename(ts, id)
	want := "2026-07-01T10-00-00-000Z_" + id + ".jsonl"
	if got != want {
		t.Fatalf("piSessionFilename = %q, want %q", got, want)
	}

	// The munged timestamp must contain no "_" so the id stays the segment
	// after the LAST underscore.
	base := strings.TrimSuffix(got, ".jsonl")
	if idx := strings.LastIndex(base, "_"); base[idx+1:] != id {
		t.Fatalf("native id after last underscore = %q, want %q", base[idx+1:], id)
	}
}

// TestPiSessionFilenameMatchesDiscoveryGlob asserts a mock-written session
// path matches the shared discovery glob from agentlogs — the same helper
// flow's transcript discovery uses — for both the all-sessions and
// id-narrowed forms.
func TestPiSessionFilenameMatchesDiscoveryGlob(t *testing.T) {
	home := "/home/tester"
	workDir := "/home/tester/code/proj"
	ts := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	id := "0198c2f4-9a51-7abc-8def-0123456789ab"

	sessionPath := filepath.Join(transcript.PiSessionsDir(home, workDir), piSessionFilename(ts, id))

	for _, narrowing := range []string{"", id} {
		pattern := transcript.PiSessionsGlob(home, narrowing)
		ok, err := filepath.Match(pattern, sessionPath)
		if err != nil {
			t.Fatalf("bad glob %q: %v", pattern, err)
		}
		if !ok {
			t.Errorf("session path %q does not match discovery glob %q", sessionPath, pattern)
		}
	}
}

// TestNewUUIDv7Shape checks the generated native id is a v7 UUID (version and
// variant bits) — the id shape pi stamps into filenames and session headers.
func TestNewUUIDv7Shape(t *testing.T) {
	id := newUUIDv7(time.Now())
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(id) {
		t.Fatalf("newUUIDv7() = %q, not a v7/RFC4122 UUID", id)
	}
}

// TestRenderSessionJSONLShape checks the emitted transcript shares the v3
// shape of the agentlogs pi fixtures: a version-3 session header carrying
// id/cwd, and message entries whose assistant messages carry usage with cost.
func TestRenderSessionJSONLShape(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	content, err := renderSessionJSONL("0198c2f4-9a51-7abc-8def-0123456789ab", "/tmp/proj", "Fix the bug", "claude-sonnet-4-5", now)
	if err != nil {
		t.Fatalf("renderSessionJSONL: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 JSONL lines (header + user + assistant), got %d", len(lines))
	}

	var header map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header line not JSON: %v", err)
	}
	if header["type"] != "session" || header["version"] != float64(3) {
		t.Errorf("header = %v, want type session version 3", header)
	}
	if header["cwd"] != "/tmp/proj" || header["id"] != "0198c2f4-9a51-7abc-8def-0123456789ab" {
		t.Errorf("header cwd/id mismatch: %v", header)
	}

	var assistant struct {
		Type    string `json:"type"`
		Message struct {
			Role  string `json:"role"`
			Model string `json:"model"`
			Usage struct {
				TotalTokens int `json:"totalTokens"`
				Cost        struct {
					Total float64 `json:"total"`
				} `json:"cost"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &assistant); err != nil {
		t.Fatalf("assistant line not JSON: %v", err)
	}
	if assistant.Type != "message" || assistant.Message.Role != "assistant" {
		t.Errorf("assistant entry shape wrong: %s", lines[2])
	}
	if assistant.Message.Usage.TotalTokens == 0 || assistant.Message.Usage.Cost.Total == 0 {
		t.Errorf("assistant usage/cost missing: %s", lines[2])
	}
	if assistant.Message.Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q, want pass-through claude-sonnet-4-5", assistant.Message.Model)
	}
}
