package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparePiJobSessionDirIsJobScoped(t *testing.T) {
	plan := t.TempDir()
	a, err := preparePiJobSessionDir(plan, "job-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := preparePiJobSessionDir(plan, "job-b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a != filepath.Join(plan, ".artifacts", "job-a", "sessions") {
		t.Fatalf("unexpected session dirs %q %q", a, b)
	}
	info, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestAppendPiJobSessionArgsCoversEveryPiFamilyProvider(t *testing.T) {
	plan := t.TempDir()
	for _, provider := range []string{"pi", "grove-agent"} {
		spec, ok := LookupAgentProvider(provider)
		if !ok {
			t.Fatalf("missing provider %q", provider)
		}
		configured := make([]string, 1, 4)
		configured[0] = "--verbose"
		args, err := appendPiJobSessionArgs(spec, plan, "job-a", configured)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(plan, ".artifacts", "job-a", "sessions")
		if len(args) != 3 || args[1] != "--session-dir" || args[2] != want {
			t.Fatalf("%s args = %#v, want Flow-owned session dir %q", provider, args, want)
		}
		if len(configured) != 1 || configured[0] != "--verbose" {
			t.Fatalf("%s mutated configured args: %#v", provider, configured)
		}
	}
}

func TestRequirePiTranscriptPathPreventsPathlessLiveSession(t *testing.T) {
	for _, provider := range []string{"pi", "grove-agent"} {
		spec, _ := LookupAgentProvider(provider)
		if err := requirePiTranscriptPath(spec, "/flow", "job", ""); err == nil {
			t.Fatalf("%s accepted empty transcript path", provider)
		}
		exact := "/flow/.artifacts/job/sessions/session.jsonl"
		if err := requirePiTranscriptPath(spec, "/flow", "job", exact); err != nil {
			t.Fatalf("%s rejected exact transcript path: %v", provider, err)
		}
		if err := requirePiTranscriptPath(spec, "/flow", "job", "/tmp/session.jsonl"); err == nil {
			t.Fatalf("%s accepted transcript outside Flow-owned session directory", provider)
		}
	}

	claude, _ := LookupAgentProvider("claude")
	if err := requirePiTranscriptPath(claude, "/flow", "job", ""); err != nil {
		t.Fatalf("non-Pi provider should retain existing registration semantics: %v", err)
	}
}

func TestArchiveFinalReportBounded(t *testing.T) {
	planDir := t.TempDir()
	jobPath := filepath.Join(planDir, "job.md")
	if err := os.WriteFile(jobPath, []byte("# Final\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "job", FilePath: jobPath}
	plan := &Plan{Directory: planDir}
	if err := ArchiveFinalReport(job, plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(planDir, ".artifacts", "job", "final-report.md"))
	if err != nil || string(got) != "# Final\n\ndone\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := os.Truncate(jobPath, (1<<20)+1); err != nil {
		t.Fatal(err)
	}
	if err := ArchiveFinalReport(job, plan); err == nil {
		t.Fatal("oversized final report accepted")
	}
}
