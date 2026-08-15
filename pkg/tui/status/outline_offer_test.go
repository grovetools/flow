package status

import (
	"os"
	"path/filepath"
	"testing"
)

// The offer declines when nothing anywhere records a transcript for the job:
// a pin chord over such a job must read as "nothing to pin", not pin an empty
// outline.
func TestOutlineOfferDeclinesWithoutTranscript(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // keep the real hooks registry out
	m, _ := newArtifactJobModel(t, true)
	if offer, ok := m.OutlineOffer(); ok {
		t.Fatalf("offer resolved with no transcript anywhere: %+v", offer)
	}
}

// The offer resolves a finished job through the archived-transcript fallback —
// the case the pin exists for: the agent and its registry record are gone, the
// archive is not. Provider and working directory come from the archived
// metadata when it is present.
func TestOutlineOfferResolvesArchivedTranscript(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m, artifactDir := newArtifactJobModel(t, true)
	archived := filepath.Join(artifactDir, "transcript.jsonl")
	if err := os.WriteFile(archived, []byte(`{"type":"message"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	metadata := `{"session_id":"j1","provider":"claude","working_directory":"/wt/checkout"}`
	if err := os.WriteFile(filepath.Join(artifactDir, "metadata.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}

	offer, ok := m.OutlineOffer()
	if !ok {
		t.Fatal("offer declined a job with an archived transcript")
	}
	if offer.JobID != "j1" || offer.Title != "job one" {
		t.Fatalf("offer identity = %q/%q, want the cursor job", offer.JobID, offer.Title)
	}
	if offer.TranscriptPath != archived {
		t.Fatalf("offer path = %q, want the archived transcript %q", offer.TranscriptPath, archived)
	}
	if offer.Provider != "claude" || offer.WorkingDirectory != "/wt/checkout" {
		t.Fatalf("offer provider/workdir = %q/%q, want them read from the archived metadata", offer.Provider, offer.WorkingDirectory)
	}
	if offer.PlanDirectory != filepath.Dir(filepath.Dir(artifactDir)) {
		t.Fatalf("offer plan dir = %q, want the plan's directory", offer.PlanDirectory)
	}
}
