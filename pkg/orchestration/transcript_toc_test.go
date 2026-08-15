package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTocFixtureTranscript writes a small claude-shaped jsonl transcript into
// the job's artifact sessions dir — the same location the Pi runtime and
// session archiver use — so resolveJobTranscript's artifact fallback finds it
// without a registry record.
func writeTocFixtureTranscript(t *testing.T, planDir, jobID string) string {
	t.Helper()
	sessionsDir := filepath.Join(planDir, ".artifacts", jobID, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(planDir, ".artifacts", jobID)
	lines := []string{
		`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"id":"u1","content":"Please fix the flaky retry test"}}`,
		`{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"id":"a1","content":[{"type":"text","text":"The retry is missing.\n\n# Root cause\nThe loop never re-arms."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":` + fmt.Sprintf("%q", filepath.Join(artifactDir, "briefing.xml")) + `}}]}}`,
		`{"type":"user","timestamp":"2026-01-01T00:00:02Z","message":{"id":"u2","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
	}
	transcriptPath := filepath.Join(sessionsDir, "2026-01-01T00-00-00-000Z_toc-fixture-session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcriptPath
}

func TestWriteTranscriptTocWritesStyledAndPlainArtifacts(t *testing.T) {
	planDir := t.TempDir()
	job := &Job{
		ID:       "toc-test-job",
		Type:     JobTypeHeadlessAgent,
		Provider: "claude",
		FilePath: filepath.Join(planDir, "01-toc-test-job.md"),
	}
	plan := &Plan{Name: "toc-test-plan", Directory: planDir}
	writeTocFixtureTranscript(t, planDir, job.ID)

	if err := WriteTranscriptToc(job, plan); err != nil {
		t.Fatalf("WriteTranscriptToc() error = %v", err)
	}

	artifactDir := filepath.Join(planDir, ".artifacts", job.ID)
	styled, err := os.ReadFile(filepath.Join(artifactDir, TranscriptTocStyledName))
	if err != nil {
		t.Fatalf("read %s: %v", TranscriptTocStyledName, err)
	}
	plain, err := os.ReadFile(filepath.Join(artifactDir, TranscriptTocPlainName))
	if err != nil {
		t.Fatalf("read %s: %v", TranscriptTocPlainName, err)
	}

	if !strings.Contains(string(styled), "\x1b[") {
		t.Error("toc.ansi carries no ANSI escapes; styled render was stripped")
	}
	if strings.Contains(string(plain), "\x1b[") {
		t.Error("toc.txt leaked ANSI escapes")
	}

	for _, want := range []string{
		"Please fix the flaky retry test", // user prompt title/body
		"The retry is missing.",           // assistant summary line
		"Root cause",                      // markdown heading row
		"briefing.xml",                    // tool row
		"$JA",                             // artifact-dir path marker elision
	} {
		if !strings.Contains(string(plain), want) {
			t.Errorf("toc.txt missing %q:\n%s", want, plain)
		}
	}

	// The writer must be idempotent (chat turns rewrite it every turn).
	if err := WriteTranscriptToc(job, plan); err != nil {
		t.Fatalf("second WriteTranscriptToc() error = %v", err)
	}

	// No stray temp files left behind by the atomic writes.
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".toc.") {
			t.Errorf("stale atomic-write temp file left behind: %s", entry.Name())
		}
	}
}

func TestWriteTranscriptTocSkipsJobsWithoutTranscript(t *testing.T) {
	planDir := t.TempDir()
	job := &Job{
		ID:       "toc-no-transcript-job",
		Type:     JobTypeOneshot,
		FilePath: filepath.Join(planDir, "01-oneshot.md"),
	}
	plan := &Plan{Name: "toc-test-plan", Directory: planDir}

	if err := WriteTranscriptToc(job, plan); err != nil {
		t.Fatalf("WriteTranscriptToc() should skip silently, got error: %v", err)
	}

	artifactDir := filepath.Join(planDir, ".artifacts", job.ID)
	for _, name := range []string{TranscriptTocStyledName, TranscriptTocPlainName} {
		if _, err := os.Stat(filepath.Join(artifactDir, name)); !os.IsNotExist(err) {
			t.Errorf("expected no %s for a job without a transcript (stat err=%v)", name, err)
		}
	}
}

func TestWriteTranscriptTocUsesArchivedTranscriptFallback(t *testing.T) {
	planDir := t.TempDir()
	job := &Job{
		ID:       "toc-archived-job",
		Type:     JobTypeInteractiveAgent,
		Provider: "claude",
		FilePath: filepath.Join(planDir, "01-archived.md"),
	}
	plan := &Plan{Name: "toc-test-plan", Directory: planDir}

	// Only the archived copy exists (registry record reaped, no live sessions
	// dir) — the shape ArchiveInteractiveSession leaves behind.
	artifactDir := filepath.Join(planDir, ".artifacts", job.ID)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"id":"u1","content":"Archived transcript prompt"}}` + "\n"
	if err := os.WriteFile(filepath.Join(artifactDir, "transcript.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteTranscriptToc(job, plan); err != nil {
		t.Fatalf("WriteTranscriptToc() error = %v", err)
	}
	plain, err := os.ReadFile(filepath.Join(artifactDir, TranscriptTocPlainName))
	if err != nil {
		t.Fatalf("read %s: %v", TranscriptTocPlainName, err)
	}
	if !strings.Contains(string(plain), "Archived transcript prompt") {
		t.Errorf("toc.txt missing archived prompt:\n%s", plain)
	}
}
