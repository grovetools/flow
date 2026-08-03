package orchestration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPiDiscoveryAfterTime covers the filter that decides whether transcript
// discovery may reject a session for being "too old".
//
// The failure this guards is subtle and total: a seeded pi-session chat opens a
// transcript whose header timestamp is necessarily EARLIER than job.StartTime
// (Flow writes the seed, then launches). With the filter left on, discovery
// matches nothing, the session is never confirmed with the daemon, and a
// perfectly healthy agent becomes invisible to liveness checks, live-token
// collection, and `flow plan complete`.
func TestPiDiscoveryAfterTime(t *testing.T) {
	spec, _ := LookupAgentProvider("pi")
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	t.Run("fresh launch keeps the filter", func(t *testing.T) {
		planDir := t.TempDir()
		if got := piDiscoveryAfterTime(spec, planDir, "job-1", false, start); !got.Equal(start) {
			t.Errorf("AfterTime = %v, want the job start time — a launch that CREATES its transcript still needs the race guard", got)
		}
	})

	t.Run("resume drops the filter", func(t *testing.T) {
		planDir := t.TempDir()
		if got := piDiscoveryAfterTime(spec, planDir, "job-1", true, start); !got.IsZero() {
			t.Errorf("AfterTime = %v, want zero for a resume", got)
		}
	})

	t.Run("pre-existing seeded session drops the filter", func(t *testing.T) {
		planDir := t.TempDir()
		sessionDir := filepath.Join(planDir, ".artifacts", "job-1", "sessions")
		if err := os.MkdirAll(sessionDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "seed.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := piDiscoveryAfterTime(spec, planDir, "job-1", false, start); !got.IsZero() {
			t.Errorf("AfterTime = %v, want zero when the job's session dir already holds a transcript", got)
		}
	})

	t.Run("non-pi provider is untouched", func(t *testing.T) {
		claude, _ := LookupAgentProvider("claude")
		planDir := t.TempDir()
		if got := piDiscoveryAfterTime(claude, planDir, "job-1", true, start); !got.Equal(start) {
			t.Errorf("AfterTime = %v, want the job start time for a non-Pi provider", got)
		}
	})
}
