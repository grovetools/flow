package orchestration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/core/pkg/sessions"
)

func TestVerifiedTranscriptSpec(t *testing.T) {
	tests := []struct {
		name         string
		metadata     *sessions.SessionMetadata
		findErr      error
		wantSpec     string
		wantVerified bool
	}{
		{
			name:         "registry hit with native session id is verified",
			metadata:     &sessions.SessionMetadata{ClaudeSessionID: "abc-123-uuid"},
			wantSpec:     "abc-123-uuid",
			wantVerified: true,
		},
		{
			name:         "registry miss is unverified",
			metadata:     nil,
			findErr:      errors.New("no session found for job"),
			wantVerified: false,
		},
		{
			name:         "registry hit without native session id is unverified",
			metadata:     &sessions.SessionMetadata{},
			wantVerified: false,
		},
		{
			name:         "error with stale metadata is unverified",
			metadata:     &sessions.SessionMetadata{ClaudeSessionID: "abc-123-uuid"},
			findErr:      errors.New("registry corrupt"),
			wantVerified: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, verified := verifiedTranscriptSpec(tt.metadata, tt.findErr)
			if verified != tt.wantVerified {
				t.Fatalf("verified = %v, want %v", verified, tt.wantVerified)
			}
			if spec != tt.wantSpec {
				t.Fatalf("spec = %q, want %q", spec, tt.wantSpec)
			}
		})
	}
}

func TestMarkTranscriptUnverified_WritesSingleMarkerLine(t *testing.T) {
	dir := t.TempDir()
	plan := &Plan{Name: "test-plan", Directory: dir}
	job := &Job{
		ID:       "job-42",
		Type:     JobTypeHeadlessAgent,
		Filename: "01-test.md",
		Title:    "test",
	}

	ctx := t.Context()
	markTranscriptUnverified(ctx, job, plan)
	// Calling again must not duplicate the marker.
	markTranscriptUnverified(ctx, job, plan)

	logPath := filepath.Join(dir, ".artifacts", job.ID, "job.log")
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading job.log: %v", err)
	}

	got := string(content)
	want := unverifiedBindingMarker(job.ID)
	if got != want {
		t.Fatalf("job.log = %q, want exactly one marker %q", got, want)
	}
	if !strings.Contains(got, "session binding unverified for job job-42") {
		t.Fatalf("marker missing job id context: %q", got)
	}
}
