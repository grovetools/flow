package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/agentlogs/pkg/transcript"
)

// piSeedFixture is a small, fully-specified seed used by the round-trip tests.
func piSeedFixture(t *testing.T) (string, PiSessionSeed) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "seed.jsonl"), PiSessionSeed{
		SessionID: "0198c2f4-0000-7abc-8def-000000000001",
		CWD:       "/guest/worktree",
		Now:       time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC),
		Stamp: map[string]any{
			"job_id":    "design-1",
			"chat_file": "/plan/04-design.md",
		},
		Messages: []PiSeedMessage{
			{CustomType: PiSeedFramingType, Content: "framing text", Display: true},
			{CustomType: PiSeedBundleType, Content: "<layer n=\"0\">\n  <file path=\"a.go\">\npackage a\n  </file>\n</layer>\n", Display: false},
			{CustomType: PiSeedContractType, Content: "contract text", Display: true},
		},
	}
}

// TestPiSessionSeedFileShape asserts the on-disk shape a seed must have for pi
// to load it: a v3 header first, then a linear parentId chain, the identity
// stamp as an out-of-context `custom` entry, and the rest as in-context
// `custom_message` entries.
func TestPiSessionSeedFileShape(t *testing.T) {
	path, seed := piSeedFixture(t)

	result, err := WritePiSessionSeed(path, seed)
	if err != nil {
		t.Fatalf("WritePiSessionSeed() error = %v", err)
	}
	if result.SessionID != seed.SessionID {
		t.Errorf("SessionID = %q, want %q", result.SessionID, seed.SessionID)
	}
	if len(result.EntryIDs) != 4 {
		t.Fatalf("EntryIDs = %d, want 4 (stamp + 3 messages)", len(result.EntryIDs))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("seed has %d lines, want 5 (header + 4 entries)", len(lines))
	}

	var header struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		ID      string `json:"id"`
		CWD     string `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header does not parse: %v", err)
	}
	if header.Type != "session" || header.Version != 3 {
		t.Errorf("header = {type:%q version:%d}, want {session 3}", header.Type, header.Version)
	}
	// pi's SessionManager.open derives the agent's cwd from the HEADER, not
	// from the launcher's process. A wrong cwd here silently runs the session
	// against another checkout.
	if header.CWD != seed.CWD {
		t.Errorf("header cwd = %q, want %q", header.CWD, seed.CWD)
	}

	type entry struct {
		Type       string  `json:"type"`
		ID         string  `json:"id"`
		ParentID   *string `json:"parentId"`
		CustomType string  `json:"customType"`
		Display    *bool   `json:"display"`
		Content    string  `json:"content"`
	}
	var entries []entry
	for _, line := range lines[1:] {
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("entry does not parse: %v (%s)", err, line)
		}
		entries = append(entries, e)
	}

	if entries[0].Type != "custom" || entries[0].CustomType != PiSeedStampType {
		t.Errorf("first entry = {%s %s}, want the out-of-context identity stamp {custom %s}",
			entries[0].Type, entries[0].CustomType, PiSeedStampType)
	}
	if entries[0].ParentID != nil {
		t.Errorf("first entry parentId = %v, want null (root of the chain)", *entries[0].ParentID)
	}

	// The parent chain is what pi walks back from the leaf. A break silently
	// drops everything above it out of the model's context.
	for i := 1; i < len(entries); i++ {
		if entries[i].ParentID == nil {
			t.Fatalf("entry %d has a null parentId; the chain is broken", i)
		}
		if *entries[i].ParentID != entries[i-1].ID {
			t.Errorf("entry %d parentId = %q, want %q (previous entry's id)", i, *entries[i].ParentID, entries[i-1].ID)
		}
	}

	wantTypes := []string{PiSeedFramingType, PiSeedBundleType, PiSeedContractType}
	for i, want := range wantTypes {
		got := entries[i+1]
		if got.Type != "custom_message" {
			t.Errorf("message entry %d type = %q, want custom_message (only custom_message enters the LLM context)", i, got.Type)
		}
		if got.CustomType != want {
			t.Errorf("message entry %d customType = %q, want %q", i, got.CustomType, want)
		}
	}
	// Framing precedes the job-specific bundle: shared bytes first keeps the
	// seed prefix stable across jobs.
	if entries[1].CustomType != PiSeedFramingType {
		t.Error("the shared framing message must be the first in-context entry")
	}
}

// TestPiSessionSeedPreservesXML: the bundle is XML, and Go's default JSON
// encoder escapes <, > and & into <-style sequences. That would rewrite
// every byte of the oracle's context. Encoding must have HTML escaping off.
func TestPiSessionSeedPreservesXML(t *testing.T) {
	path, seed := piSeedFixture(t)
	if _, err := WritePiSessionSeed(path, seed); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(string(data), escaped) {
			t.Errorf("seed contains %s; the bundle's XML was rewritten by the encoder's HTML escaping", escaped)
		}
	}
	if !strings.Contains(string(data), `<file path=`) {
		t.Error("seed does not contain the literal bundle XML")
	}
}

// TestPiSessionSeedRoundTrip is the fixture round-trip the format contract
// rests on: the seed is parsed back by agentlogs' INDEPENDENT pi normalizer —
// the same code that reads real pi transcripts — rather than by a mirror of the
// writer. If pi's entry shapes drift, this is what fails.
func TestPiSessionSeedRoundTrip(t *testing.T) {
	path, seed := piSeedFixture(t)
	if _, err := WritePiSessionSeed(path, seed); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	normalizer := transcript.NewPiNormalizer()
	var (
		inContext []string
		stampSeen bool
	)
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		unified, err := normalizer.NormalizeLine([]byte(line))
		if err != nil {
			t.Fatalf("agentlogs failed to normalize a seed line: %v (%s)", err, line)
		}
		if unified == nil {
			// Header and `custom` entries contribute no conversation parts —
			// which is exactly how we know the stamp costs no context.
			if strings.Contains(line, PiSeedStampType) {
				stampSeen = true
			}
			continue
		}
		if unified.Role != "user" {
			t.Errorf("custom_message normalized to role %q, want user (pi converts them to user messages)", unified.Role)
		}
		inContext = append(inContext, unified.CustomType)
	}

	if !stampSeen {
		t.Error("the identity stamp line was not present")
	}
	// display:false hides an entry in pi's TUI and agentlogs honors that, so the
	// bundle is deliberately absent from the normalized conversation.
	want := []string{PiSeedFramingType, PiSeedContractType}
	if len(inContext) != len(want) {
		t.Fatalf("normalized in-context entries = %v, want %v", inContext, want)
	}
	for i := range want {
		if inContext[i] != want[i] {
			t.Errorf("normalized entry %d customType = %q, want %q", i, inContext[i], want[i])
		}
	}
}

// TestPiSessionSeedRejectsBadInput: every refusal is a case where launching
// anyway would produce a session that looks fine and holds nothing.
func TestPiSessionSeedRejectsBadInput(t *testing.T) {
	base := func() PiSessionSeed {
		_, s := piSeedFixture(t)
		return s
	}
	tests := []struct {
		name   string
		mutate func(*PiSessionSeed)
		want   string
	}{
		{"no cwd", func(s *PiSessionSeed) { s.CWD = "" }, "cwd"},
		{"no messages", func(s *PiSessionSeed) { s.Messages = nil }, "no in-context messages"},
		{"empty content", func(s *PiSessionSeed) { s.Messages[0].Content = "  " }, "empty content"},
		{"no customType", func(s *PiSessionSeed) { s.Messages[0].CustomType = "" }, "no customType"},
		{"invalid session id", func(s *PiSessionSeed) { s.SessionID = "-not/valid-" }, "not a valid pi session id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seed := base()
			tt.mutate(&seed)
			_, err := WritePiSessionSeed(filepath.Join(t.TempDir(), "seed.jsonl"), seed)
			if err == nil {
				t.Fatalf("WritePiSessionSeed() succeeded, want an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestPiSeedSessionFileName: grove reads the native session id back OUT of the
// filename (piNativeSessionID), so a seed must be named the way pi names its
// own sessions or the daemon records the wrong id.
func TestPiSeedSessionFileName(t *testing.T) {
	at := time.Date(2026, 8, 3, 10, 30, 15, 250_000_000, time.UTC)
	id := "0198c2f4-0000-7abc-8def-000000000001"
	name := piSeedSessionFileName(id, at)

	if want := "2026-08-03T10-30-15-250Z_" + id + ".jsonl"; name != want {
		t.Errorf("piSeedSessionFileName() = %q, want %q", name, want)
	}
	if got := piNativeSessionID(name); got != id {
		t.Errorf("piNativeSessionID(%q) = %q, want %q — grove would record the wrong native id", name, got, id)
	}
}

// TestReadPiSessionHeaderVersion is the version probe's reader: it must report
// the version of a real header and stay silent on anything else.
func TestReadPiSessionHeaderVersion(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.jsonl")
	if err := os.WriteFile(good, []byte(`{"type":"session","version":4,"id":"x","timestamp":"t","cwd":"/w"}`+"\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := ReadPiSessionHeaderVersion(good); err != nil || v != 4 {
		t.Errorf("ReadPiSessionHeaderVersion() = (%d, %v), want (4, nil)", v, err)
	}

	junk := filepath.Join(dir, "junk.jsonl")
	if err := os.WriteFile(junk, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Absence of evidence, not evidence of a mismatch: an unreadable header
	// must not manufacture a version disagreement.
	if v, err := ReadPiSessionHeaderVersion(junk); err != nil || v != 0 {
		t.Errorf("ReadPiSessionHeaderVersion(junk) = (%d, %v), want (0, nil)", v, err)
	}
}

// TestProbePiSessionFormat: the probe fires only on a genuine disagreement, and
// never on the seed itself.
func TestProbePiSessionFormat(t *testing.T) {
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.jsonl")
	if err := os.WriteFile(seedPath, []byte(`{"type":"session","version":3,"id":"s","timestamp":"t","cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ProbePiSessionFormat(dir, seedPath); got != "" {
		t.Errorf("probe with only the seed present = %q, want \"\" (nothing to compare against)", got)
	}

	older := filepath.Join(dir, "prior.jsonl")
	if err := os.WriteFile(older, []byte(`{"type":"session","version":9,"id":"p","timestamp":"t","cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ProbePiSessionFormat(dir, seedPath)
	if !strings.Contains(got, "version 9") || !strings.Contains(got, "version 3") {
		t.Errorf("probe = %q, want it to name both the runtime's version (9) and the writer's (3)", got)
	}
}
