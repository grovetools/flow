package status

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/panes"
	"github.com/grovetools/flow/pkg/orchestration"
)

// Embedding daemon.Client keeps the fake focused on the single method the run
// key path consults; every other call would panic on the nil embedded value,
// which is the point — the test must not reach one.
type runKeyDaemonClient struct {
	daemon.Client
	running bool
}

func (c runKeyDaemonClient) IsRunning() bool { return c.running }

func runKeyModel(t *testing.T, client daemon.Client) Model {
	t.Helper()

	first := &orchestration.Job{ID: "job-1", Filename: "01-first.md", Title: "first", Type: orchestration.JobTypeInteractiveAgent, Status: orchestration.JobStatusRunning}
	second := &orchestration.Job{ID: "job-2", Filename: "02-second.md", Title: "second", Type: orchestration.JobTypeInteractiveAgent, Status: orchestration.JobStatusPending}

	keys := NewKeyMap(nil)
	m := Model{
		Jobs:         []*orchestration.Job{first, second},
		KeyMap:       keys,
		WhichKey:     keymap.NewWhichKeyHost(nil, keys.Namespaces()...),
		DaemonClient: client,
		// The TUI already launched job-1 and is streaming it: an interactive
		// agent never leaves "running", so this flag stays set.
		IsRunningJob: true,
		ActiveLogJob: first,
		LogViewer:    logviewer.New(60, 20),
		Manager: panes.New(
			panes.Pane{ID: "jobs", Model: NewJobsPaneModel(), Flex: 1, MinSize: 20},
			panes.Pane{ID: "detail", Model: NewDetailPaneModel(), Flex: 2, MinSize: 20, Hidden: true},
		),
	}
	m.DisplayRows = []DisplayRow{
		{Type: RowTypeJob, NodeID: "job:job-1", Job: first},
		{Type: RowTypeJob, NodeID: "job:job-2", Job: second},
	}
	// Cursor on the second job — the user scrolled down to launch it too.
	m.Cursor = 1
	return m
}

// A live daemon runs jobs concurrently, so an already-running job must not
// gate the run key. Before the fix, launching an interactive agent left
// IsRunningJob set forever and 'r' was dead for the rest of the session.
func TestRunKeySubmitsSecondJobWhileFirstRunsUnderDaemon(t *testing.T) {
	m := runKeyModel(t, runKeyDaemonClient{running: true})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	got := updated.(Model)

	if strings.Contains(got.StatusSummary, "already running") {
		t.Fatalf("run key was blocked under a live daemon: %q", got.StatusSummary)
	}
	if cmd == nil {
		t.Fatal("run key returned no command — the job was never submitted")
	}
	if _, ok := got.InitializingJobs["job-2"]; !ok {
		t.Errorf("job-2 was not marked initializing: %v", got.InitializingJobs)
	}
	if got.ActiveLogJob == nil || got.ActiveLogJob.ID != "job-2" {
		t.Errorf("ActiveLogJob = %v, want job-2", got.ActiveLogJob)
	}
}

// Without a daemon the TUI itself owns the executor and can only drive one
// run at a time, so the guard must stay.
func TestRunKeyStillBlocksWithoutDaemon(t *testing.T) {
	for name, client := range map[string]daemon.Client{
		"no client":          nil,
		"daemon not running": runKeyDaemonClient{running: false},
	} {
		t.Run(name, func(t *testing.T) {
			m := runKeyModel(t, client)

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
			got := updated.(Model)

			if !strings.Contains(got.StatusSummary, "already running") {
				t.Errorf("expected the in-process guard to block the run, got %q", got.StatusSummary)
			}
			if cmd != nil {
				t.Error("blocked run should not return a command")
			}
			if _, ok := got.InitializingJobs["job-2"]; ok {
				t.Error("blocked run should not mark job-2 initializing")
			}
		})
	}
}
