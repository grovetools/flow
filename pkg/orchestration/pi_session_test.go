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
