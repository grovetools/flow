package orchestration

import (
	"strings"
	"testing"
)

func TestProviderFallbackMetadataContract(t *testing.T) {
	t.Setenv("GROVE_SCOPE", "ecosystem/test")
	job := &Job{ID: "job-1", AttemptID: "01890f5d-e4b8-7cc3-98c4-dc0c0c07398f", ParentJobID: "parent", Title: "Job", FilePath: "/plan/job.md", Status: JobStatusRunning}
	plan := &Plan{Name: "plan"}
	for _, provider := range []string{"claude", "codex", "pi", "grove-agent", "opencode", "isolated"} {
		t.Run(provider, func(t *testing.T) {
			metadata := newFallbackSessionMetadata(job, plan, "/worktree", provider, "native", "interactive_agent", "/tmp/transcript", 42)
			if metadata.AttemptID != job.AttemptID || metadata.JobID != job.ID {
				t.Fatalf("identity fields = attempt %q job %q", metadata.AttemptID, metadata.JobID)
			}
			if metadata.Status != string(JobStatusRunning) || metadata.Scope != "ecosystem/test" {
				t.Fatalf("provenance fields = status %q scope %q", metadata.Status, metadata.Scope)
			}
			if metadata.Provider != provider {
				t.Fatalf("provider = %q, want %q", metadata.Provider, provider)
			}
		})
	}
}

func TestHeadlessEnvCarriesAttemptIDAndOverridesAgentEnv(t *testing.T) {
	job := &Job{ID: "job-1", AttemptID: "01890f5d-e4b8-7cc3-98c4-dc0c0c07398f", FilePath: "/plan/job.md"}
	env := buildHeadlessEnv(job, &Plan{Name: "plan", Directory: t.TempDir()}, "claude", t.TempDir(), map[string]string{"GROVE_FLOW_ATTEMPT_ID": "spoofed"})
	last := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "GROVE_FLOW_ATTEMPT_ID=") {
			last = entry
		}
	}
	if last != "GROVE_FLOW_ATTEMPT_ID="+job.AttemptID {
		t.Fatalf("effective attempt env = %q", last)
	}
}
