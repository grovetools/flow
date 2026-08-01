package subjobmon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/core/pkg/daemon"
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

type fakeClient struct {
	snapshot  *models.SubjobSnapshot
	published []models.SubjobEvent
}

func (f *fakeClient) PublishSubjobEvent(_ context.Context, event models.SubjobEvent) error {
	f.published = append(f.published, event)
	return nil
}

func (f *fakeClient) GetSubjobSnapshot(context.Context, string, string) (*models.SubjobSnapshot, error) {
	if f.snapshot == nil {
		return &models.SubjobSnapshot{Reports: map[string]*models.SubjobState{}}, nil
	}
	return f.snapshot, nil
}

func (f *fakeClient) StreamState(context.Context, ...daemon.StreamFilter) (<-chan daemon.StateUpdate, error) {
	return make(chan daemon.StateUpdate), nil
}

func writeReconcilePlan(t *testing.T, status orchestration.JobStatus, withReport bool) string {
	t.Helper()
	dir := t.TempDir()
	plan := &orchestration.Plan{Config: &orchestration.PlanConfig{Status: "active"}, Jobs: []*orchestration.Job{
		{ID: "parent", Filename: "01-parent.md", Title: "parent", Type: orchestration.JobTypeInteractiveAgent, Status: orchestration.JobStatusRunning},
		{ID: "child", Filename: "02-child.md", Title: "child", Type: orchestration.JobTypeInteractiveAgent, ParentJobID: "parent", Status: status},
	}}
	if err := orchestration.SavePlan(dir, plan); err != nil {
		t.Fatal(err)
	}
	if withReport {
		root := filepath.Join(dir, ".artifacts", "child")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		report := `{"schema_version":1,"child_job_id":"child","parent_job_id":"parent","summary":"done","artifacts":{},"created_at":"now"}`
		if err := os.WriteFile(filepath.Join(root, "final-report.json"), []byte(report), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReconcileRepairsReadyAndJoinedState(t *testing.T) {
	dir := writeReconcilePlan(t, orchestration.JobStatusRunning, true)
	client := &fakeClient{}
	out, err := Reconcile(context.Background(), client, dir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != "report_ready" || out[0].ChildTitle != "child" || out[0].ReportSummary != "done" || len(client.published) != 1 || client.published[0].Kind != models.SubjobReportReady {
		t.Fatalf("ready reconciliation: out=%+v published=%+v", out, client.published)
	}

	dir = writeReconcilePlan(t, orchestration.JobStatusCompleted, true)
	client = &fakeClient{snapshot: &models.SubjobSnapshot{Reports: map[string]*models.SubjobState{
		"child": {ChildJobID: "child", State: models.SubjobReportReady, ReportSHA256: strings.Repeat("0", 64)},
	}}}
	out, err = Reconcile(context.Background(), client, dir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || len(client.published) != 1 || client.published[0].Kind != models.SubjobJoined {
		t.Fatalf("joined reconciliation: out=%+v published=%+v", out, client.published)
	}
}

func TestReconcileReportsTerminalChildWithoutReport(t *testing.T) {
	dir := writeReconcilePlan(t, orchestration.JobStatusFailed, false)
	out, err := Reconcile(context.Background(), &fakeClient{}, dir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != "terminal_without_report" || out[0].ChildTitle != "child" || out[0].JobStatus != orchestration.JobStatusFailed {
		t.Fatalf("terminal reconciliation = %+v", out)
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
