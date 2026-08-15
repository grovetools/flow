package status

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/grovetools/flow/pkg/orchestration"
)

func TestLoadAndStreamAgentLogsUnverifiedShowsJobLogBeneathNotice(t *testing.T) {
	job := &orchestration.Job{
		ID:     "unverified-truth-surface-test",
		Title:  "startup failure",
		Type:   orchestration.JobTypeHeadlessAgent,
		Status: orchestration.JobStatusRunning,
	}
	plan := &orchestration.Plan{Name: "truth", Directory: t.TempDir()}
	logPath := filepath.Join(plan.Directory, ".artifacts", job.ID, "job.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const breadcrumb = "ERROR provider exited before session start\npane tail: trust dialog"
	if err := os.WriteFile(logPath, []byte(breadcrumb), 0o644); err != nil {
		t.Fatal(err)
	}

	msg, ok := loadAndStreamAgentLogsCmd(plan, job)().(LogContentLoadedMsg)
	if !ok {
		t.Fatalf("command returned %T, want LogContentLoadedMsg", msg)
	}
	if msg.StartStreaming {
		t.Fatal("unverified session must never start transcript streaming")
	}
	if !msg.ShouldRetry {
		t.Fatal("running unverified session should retry registry discovery")
	}
	noticeAt := strings.Index(msg.Content, unverifiedBindingNotice)
	logAt := strings.Index(msg.Content, breadcrumb)
	if noticeAt < 0 || logAt < 0 || noticeAt >= logAt {
		t.Fatalf("job.log breadcrumb must appear beneath the binding notice:\n%s", msg.Content)
	}
}

func TestCompletionRejectionDetailsPreserveEveryLine(t *testing.T) {
	m := newAgentJobModel(t, false)
	completionErr := errors.New("completion evidence rejected\nmissing final report\nrun flow subjob finish")

	mdl, _ := m.Update(JobCompletedMsg{Err: completionErr})
	m = mdl.(Model)
	wantFull := "Error completing job: " + completionErr.Error()
	if m.LastActionError != wantFull {
		t.Fatalf("LastActionError lost detail:\n got %q\nwant %q", m.LastActionError, wantFull)
	}
	footer := ansi.Strip(m.renderFooter())
	if strings.Contains(footer, "\n") {
		t.Fatalf("completion error footer wrapped onto multiple lines: %q", footer)
	}
	if !strings.Contains(footer, "ve details") {
		t.Fatalf("completion error footer lacks expand hint: %q", footer)
	}

	m, _ = press(t, m, "ve")
	if m.ActiveDetailPane != ActionErrorPaneDetail {
		t.Fatalf("ve opened pane %d, want ActionErrorPaneDetail", m.ActiveDetailPane)
	}
	body := ansi.Strip(m.renderDetailContent())
	for _, line := range strings.Split(wantFull, "\n") {
		if !strings.Contains(body, line) {
			t.Errorf("details pane omitted %q:\n%s", line, body)
		}
	}
}
