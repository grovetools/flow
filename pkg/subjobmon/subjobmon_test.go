package subjobmon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/flow/pkg/orchestration"
)

func TestCanonicalPlanAndExactReportDigest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte("name: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	canonical, key, err := CanonicalPlan(alias)
	if err != nil {
		t.Fatal(err)
	}
	realDir, _ := filepath.EvalSymlinks(dir)
	if canonical != realDir {
		t.Fatalf("canonical = %s, want %s", canonical, realDir)
	}
	_, directKey, err := CanonicalPlan(dir)
	if err != nil || key != directKey {
		t.Fatalf("keys differ: %s %s (%v)", key, directKey, err)
	}
	job := &orchestration.Job{ID: "child", ParentJobID: "parent", Status: orchestration.JobStatusRunning}
	root := filepath.Join(dir, ".artifacts", "child")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	report := []byte(`{"schema_version":1,"child_job_id":"child","parent_job_id":"parent","summary":"done","artifacts":{},"created_at":"now"}`)
	if err := os.WriteFile(filepath.Join(root, "final-report.json"), report, 0o600); err != nil {
		t.Fatal(err)
	}
	ev, err := BuildEvent(alias, job, models.SubjobReportReady)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(report)
	if ev.ReportSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %s", ev.ReportSHA256)
	}
	if ev.PlanKey != key {
		t.Fatalf("plan key = %s", ev.PlanKey)
	}
}

func TestJoinedRequiresCompletedStatus(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte("name: test\n"), 0o600)
	root := filepath.Join(dir, ".artifacts", "child")
	_ = os.MkdirAll(root, 0o700)
	_ = os.WriteFile(filepath.Join(root, "final-report.json"), []byte(`{"schema_version":1,"child_job_id":"child","parent_job_id":"parent","summary":"done","artifacts":{},"created_at":"now"}`), 0o600)
	_, err := BuildEvent(dir, &orchestration.Job{ID: "child", ParentJobID: "parent", Status: orchestration.JobStatusRunning}, models.SubjobJoined)
	if err == nil {
		t.Fatal("joined before completed unexpectedly succeeded")
	}
}
