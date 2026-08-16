package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPiTranscriptDiscoveryIsBoundToLaunch is the multi-attempt regression for
// retry discovery. Each attempt inherits the same job-scoped session directory,
// but it must wait for a transcript filename that was absent before its own
// spawn rather than immediately binding the new PID to the prior attempt.
func TestPiTranscriptDiscoveryIsBoundToLaunch(t *testing.T) {
	planDir := t.TempDir()
	job := &Job{ID: "job-1", Type: JobTypeInteractiveAgent}
	sessionDir := piJobSessionDir(planDir, job.ID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}

	attempt1 := filepath.Join(sessionDir, "2026-08-16T15-52-26-630Z_attempt-1.jsonl")
	if err := os.WriteFile(attempt1, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	attempt2Launch, err := capturePiTranscriptLaunch(job, planDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := discoverPiTranscriptForLaunch(planDir, job.ID, attempt2Launch); err == nil {
		t.Fatalf("attempt 2 discovered %q before its transcript existed; prior attempt must be ignored", got)
	}
	attempt2 := filepath.Join(sessionDir, "2026-08-16T16-47-25-184Z_attempt-2.jsonl")
	if err := os.WriteFile(attempt2, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverPiTranscriptForLaunch(planDir, job.ID, attempt2Launch); err != nil || got != attempt2 {
		t.Fatalf("attempt 2 discovery = (%q, %v), want %q", got, err, attempt2)
	}

	attempt3Launch, err := capturePiTranscriptLaunch(job, planDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := discoverPiTranscriptForLaunch(planDir, job.ID, attempt3Launch); err == nil {
		t.Fatalf("attempt 3 discovered %q before its transcript existed; both prior attempts must be ignored", got)
	}
	attempt3 := filepath.Join(sessionDir, "2026-08-16T17-02-00-000Z_attempt-3.jsonl")
	if err := os.WriteFile(attempt3, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverPiTranscriptForLaunch(planDir, job.ID, attempt3Launch); err != nil || got != attempt3 {
		t.Fatalf("attempt 3 discovery = (%q, %v), want %q", got, err, attempt3)
	}
}

func TestPiTranscriptDiscoveryPreservesExplicitExistingSession(t *testing.T) {
	t.Run("resume native id", func(t *testing.T) {
		planDir := t.TempDir()
		job := &Job{ID: "job-1", Type: JobTypeInteractiveAgent}
		dir := piJobSessionDir(planDir, job.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "2026-08-16T15-52-26-630Z_resume-id.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		launch, err := capturePiTranscriptLaunch(job, planDir, "resume-id")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := discoverPiTranscriptForLaunch(planDir, job.ID, launch); err != nil || got != path {
			t.Fatalf("resume discovery = (%q, %v), want %q", got, err, path)
		}
	})

	t.Run("seeded pi-session path", func(t *testing.T) {
		planDir := t.TempDir()
		job := &Job{ID: "pi-chat", Type: JobTypeChat, Responder: ResponderPiSession}
		dir := piJobSessionDir(planDir, job.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		seed := filepath.Join(dir, "seed.jsonl")
		if err := os.WriteFile(seed, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WritePiSessionDescriptor(planDir, PiSessionDescriptor{JobID: job.ID, SessionFile: seed}); err != nil {
			t.Fatal(err)
		}
		launch, err := capturePiTranscriptLaunch(job, planDir, "")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := discoverPiTranscriptForLaunch(planDir, job.ID, launch); err != nil || got != seed {
			t.Fatalf("seeded session discovery = (%q, %v), want %q", got, err, seed)
		}
	})
}
