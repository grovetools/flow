package status

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/core/pkg/daemon"

	"github.com/grovetools/flow/pkg/orchestration"
)

// writePlan lays out a plan directory with one job per body and returns it.
func writePlan(t *testing.T, jobs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range jobs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const jobFile = `---
id: job-one
title: one
status: pending
type: interactive_agent
---

body
`

// TestPlanFingerprintTracksWhatLoadPlanReads pins the signature the refresh
// gate is keyed on: it must move for any change LoadPlan would observe, and
// hold still otherwise. A fingerprint that moved on its own would restore the
// every-2s reload of the whole plan; one that failed to move would freeze the
// job table on stale data.
func TestPlanFingerprintTracksWhatLoadPlanReads(t *testing.T) {
	dir := writePlan(t, map[string]string{"01-one.md": jobFile})

	base, err := orchestration.PlanFingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := orchestration.PlanFingerprint(dir); again != base {
		t.Error("fingerprint changed with no change on disk")
	}

	// Content change (same length, so size alone cannot catch it).
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "01-one.md"),
		[]byte(jobFile[:len(jobFile)-5]+"other"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, _ := orchestration.PlanFingerprint(dir)
	if edited == base {
		t.Error("fingerprint did not move when a job file was rewritten")
	}

	// A new job file.
	if err := os.WriteFile(filepath.Join(dir, "02-two.md"), []byte(jobFile), 0o644); err != nil {
		t.Fatal(err)
	}
	added, _ := orchestration.PlanFingerprint(dir)
	if added == edited {
		t.Error("fingerprint did not move when a job file was added")
	}

	// A removed job file.
	if err := os.Remove(filepath.Join(dir, "02-two.md")); err != nil {
		t.Fatal(err)
	}
	if removed, _ := orchestration.PlanFingerprint(dir); removed != edited {
		t.Error("fingerprint did not return to its pre-add value when the file was removed")
	}

	// The plan config LoadPlan also reads.
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte("name: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withConfig, _ := orchestration.PlanFingerprint(dir); withConfig == edited {
		t.Error("fingerprint ignored .grove-plan.yml, which LoadPlan reads")
	}
}

// TestPlanReloadRedundantOnlyWhenNothingElseNeedsIt pins the guards on the
// skip. Everything the refresh pass derives from the job files is safe to
// skip when they have not moved; the things it derives from elsewhere are not.
func TestPlanReloadRedundantOnlyWhenNothingElseNeedsIt(t *testing.T) {
	dir := writePlan(t, map[string]string{"01-one.md": jobFile})
	fp, err := orchestration.PlanFingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}

	loaded := Model{PlanDir: dir, planFingerprint: fp, DaemonConnected: true}
	if _, redundant := loaded.planReloadRedundant(); !redundant {
		t.Error("an untouched plan directory still forced a full reload")
	}

	stale := loaded
	stale.planFingerprint = "something-else"
	if _, redundant := stale.planReloadRedundant(); redundant {
		t.Error("a moved fingerprint was treated as redundant")
	}

	never := loaded
	never.planFingerprint = ""
	if _, redundant := never.planReloadRedundant(); redundant {
		t.Error("the first refresh, before any load, was treated as redundant")
	}

	// Without the daemon, VerifyRunningJobStatus is the only thing that reaps
	// a job whose process died — and a dead process leaves the files alone.
	noDaemon := loaded
	noDaemon.DaemonConnected = false
	if _, redundant := noDaemon.planReloadRedundant(); redundant {
		t.Error("skipped the reload with no daemon to own session liveness")
	}

	// These panes re-read .artifacts/, which a running agent writes without
	// touching any job file.
	for _, pane := range []DetailPane{SkillPane, ArtifactsPaneDetail} {
		withPane := loaded
		withPane.ActiveDetailPane = pane
		if _, redundant := withPane.planReloadRedundant(); redundant {
			t.Errorf("skipped the reload while pane %v was reading artifacts", pane)
		}
	}

	gone := loaded
	gone.PlanDir = filepath.Join(dir, "does-not-exist")
	if _, redundant := gone.planReloadRedundant(); redundant {
		t.Error("an unreadable plan directory was treated as redundant")
	}
}

// TestRefreshTickSkipsTheReloadWhenNothingMoved pins that the saving reaches
// the message layer: an idle tick must re-arm itself and nothing else. Every
// extra message costs an Update pass and a full re-render of the job table.
func TestRefreshTickSkipsTheReloadWhenNothingMoved(t *testing.T) {
	dir := writePlan(t, map[string]string{"01-one.md": jobFile})
	fp, _ := orchestration.PlanFingerprint(dir)
	m := Model{PlanDir: dir, planFingerprint: fp, DaemonConnected: true}

	_, cmd := m.Update(RefreshTickMsg(time.Now()))
	for _, msg := range runBatch(t, cmd) {
		if _, ok := msg.(RefreshMsg); ok {
			t.Fatal("an idle refresh tick still dispatched a plan reload")
		}
	}

	// And the reload does go out once the directory moves.
	moved := m
	moved.planFingerprint = "stale"
	_, cmd = moved.Update(RefreshTickMsg(time.Now()))
	sawRefresh := false
	for _, msg := range runBatch(t, cmd) {
		if _, ok := msg.(RefreshMsg); ok {
			sawRefresh = true
		}
	}
	if !sawRefresh {
		t.Error("a changed plan directory did not dispatch a reload")
	}
}

// runBatch runs cmd and returns every message it produces, flattening one
// level of tea.Batch. Tick commands are reported by type rather than waited on.
func runBatch(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var msgs []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func(c tea.Cmd) { done <- c() }(c)
		select {
		case m := <-done:
			msgs = append(msgs, m)
		case <-time.After(500 * time.Millisecond):
			// A pending tick (the refresh loop re-arming). Not a message.
		}
	}
	return msgs
}

// TestDaemonListenerDropsUpdatesTheViewNeverReads pins the SSE filter. The
// subscription is host-wide and carries the daemon's whole state firehose;
// waking the Update loop for an update this view derives nothing from costs a
// full re-render for no visible change.
func TestDaemonListenerDropsUpdatesTheViewNeverReads(t *testing.T) {
	ch := make(chan daemon.StateUpdate, 16)
	m := Model{streamCh: ch}

	for _, noise := range []string{
		"workspaces", "workspaces_delta", "git_status",
		"focus", "note_index", "plan_index", "skill_sync", "watcher_status",
	} {
		ch <- daemon.StateUpdate{UpdateType: noise}
	}
	ch <- daemon.StateUpdate{UpdateType: "job_completed"}

	msg := m.listenToDaemon()()
	update, ok := msg.(daemonStateUpdateMsg)
	if !ok {
		t.Fatalf("listener returned %T, want the actionable update", msg)
	}
	if update.update.UpdateType != "job_completed" {
		t.Errorf("listener surfaced %q; the noise before it should have been dropped",
			update.update.UpdateType)
	}

	// Every type the Update loop acts on must survive the filter, or it would
	// never arrive at all.
	for _, actionable := range []string{
		"session", "job_submitted", "job_started",
		"job_completed", "job_failed", "job_cancelled", "job_pending_user",
	} {
		if !daemonUpdateActionable[actionable] {
			t.Errorf("update type %q is acted on in Update but dropped by the listener", actionable)
		}
	}

	// A closed stream still reports as an error rather than hanging.
	closed := make(chan daemon.StateUpdate)
	close(closed)
	if _, ok := (Model{streamCh: closed}).listenToDaemon()().(daemonStreamErrorMsg); !ok {
		t.Error("a closed daemon stream did not surface as a stream error")
	}
}

// TestDaemonStreamFilterMatchesTheActionableSet pins the server-side half. The
// filter is what the daemon is told at subscribe time, so a type that is
// actionable here but missing from the declaration would be dropped in the
// daemon and never reach the listener that would have accepted it — a silent
// failure the client-side guard cannot catch.
func TestDaemonStreamFilterMatchesTheActionableSet(t *testing.T) {
	f := daemonStreamFilter()

	for typ, want := range daemonUpdateActionable {
		if want && !f.AllowsType(typ) {
			t.Errorf("actionable type %q is not declared to the daemon", typ)
		}
	}
	if len(f.Types) != len(daemonUpdateActionable) {
		t.Errorf("declared %d types, actionable set has %d — they must not drift",
			len(f.Types), len(daemonUpdateActionable))
	}

	// The subscribe-time snapshot is the largest frame on the wire and this
	// view reads nothing out of it. Declaring it would give back most of the
	// saving this filter exists for.
	if f.AllowsType(daemon.StreamTypeInitial) {
		t.Error("the status view declared the initial snapshot it never reads")
	}
	for _, noise := range []string{
		"workspaces", "workspaces_delta", "note_index",
		"plan_index", "skill_sync", "focus", "watcher_status", "memory_index",
	} {
		if f.AllowsType(noise) {
			t.Errorf("filter admits %q, which the Update loop discards", noise)
		}
	}

	// No path allow-list: job and session frames carry no workspace path, so
	// one would narrow nothing and risk starving the view.
	if len(f.Paths) != 0 {
		t.Errorf("filter declared paths %v; job/session frames carry none", f.Paths)
	}
}
