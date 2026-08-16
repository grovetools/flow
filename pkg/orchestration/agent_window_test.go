package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grovetools/core/pkg/mux"
)

type fakeAgentWindowEngine struct {
	exists             bool
	paneFallback       bool
	paneErr            error
	createErr          error
	createdOnError     bool
	newWindowCalls     int
	paneExistsCalls    int
	sendCalls          int
	disappearOnSend    bool
	includeOtherWindow bool
	sent               [][]string
}

// PaneExists models tmux display-message's misleading behavior for a missing
// window. It is intentionally not part of agentWindowEngine; the regression
// below proves the launch path never consults it.
func (f *fakeAgentWindowEngine) PaneExists(context.Context, string) (bool, error) {
	f.paneExistsCalls++
	return f.exists || f.paneFallback, f.paneErr
}

func (f *fakeAgentWindowEngine) ListWindows(context.Context, string) ([]mux.WindowInfo, error) {
	if f.paneErr != nil {
		return nil, f.paneErr
	}
	var windows []mux.WindowInfo
	if f.includeOtherWindow {
		windows = append(windows, mux.WindowInfo{Name: "shell"})
	}
	if f.exists {
		windows = append(windows, mux.WindowInfo{Name: "job-a"})
	}
	return windows, nil
}

func (f *fakeAgentWindowEngine) NewWindow(context.Context, string, string, string, bool) error {
	f.newWindowCalls++
	if f.createErr != nil {
		if f.createdOnError {
			f.exists = true
		}
		return f.createErr
	}
	f.exists = true
	return nil
}

func (f *fakeAgentWindowEngine) SendKeys(_ context.Context, _ string, keys ...string) error {
	f.sendCalls++
	f.sent = append(f.sent, append([]string(nil), keys...))
	if f.disappearOnSend && f.sendCalls == 1 {
		f.exists = false
		return errors.New("can't find window")
	}
	return nil
}

func TestSendAgentCommandToWindowReusesExistingPane(t *testing.T) {
	engine := &fakeAgentWindowEngine{exists: true}
	if err := sendAgentCommandToWindow(context.Background(), engine, "wt", "job-a", "/work", "agent", "C-m"); err != nil {
		t.Fatal(err)
	}
	if engine.newWindowCalls != 0 {
		t.Fatalf("NewWindow called %d times for a live pane, want 0", engine.newWindowCalls)
	}
	if engine.sendCalls != 1 {
		t.Fatalf("SendKeys called %d times, want 1", engine.sendCalls)
	}
}

func TestSendAgentCommandToWindowCreatesMissingPane(t *testing.T) {
	engine := &fakeAgentWindowEngine{}
	if err := sendAgentCommandToWindow(context.Background(), engine, "wt", "job-a", "/work", "agent", "C-m"); err != nil {
		t.Fatal(err)
	}
	if engine.newWindowCalls != 1 || engine.sendCalls != 1 {
		t.Fatalf("calls = new-window %d, send %d; want 1, 1", engine.newWindowCalls, engine.sendCalls)
	}
}

func TestEnsureAgentWindowIgnoresPaneProbeSessionFallback(t *testing.T) {
	engine := &fakeAgentWindowEngine{
		paneFallback:       true,
		includeOtherWindow: true,
	}

	if _, err := ensureAgentWindow(context.Background(), engine, "wt", "job-a", "/work"); err != nil {
		t.Fatal(err)
	}
	if engine.newWindowCalls != 1 {
		t.Fatalf("NewWindow called %d times, want 1 for missing exact window", engine.newWindowCalls)
	}
	if engine.paneExistsCalls != 0 {
		t.Fatalf("PaneExists called %d times; fallback-prone probe must not be used", engine.paneExistsCalls)
	}
}

func TestSendAgentCommandToWindowRecreatesPaneClosedBeforeSend(t *testing.T) {
	engine := &fakeAgentWindowEngine{exists: true, disappearOnSend: true}
	if err := sendAgentCommandToWindow(context.Background(), engine, "wt", "job-a", "/work", "agent", "C-m"); err != nil {
		t.Fatal(err)
	}
	if engine.newWindowCalls != 1 {
		t.Fatalf("NewWindow called %d times, want one recreation", engine.newWindowCalls)
	}
	if engine.sendCalls != 2 {
		t.Fatalf("SendKeys called %d times, want initial attempt plus retry", engine.sendCalls)
	}
	if got := strings.Join(engine.sent[1], " "); got != "agent C-m" {
		t.Fatalf("retried keys = %q, want the original command", got)
	}
}

func TestSendAgentCommandToWindowDoesNotRetryAgainstLivePane(t *testing.T) {
	engine := &fakeAgentWindowEngine{exists: true}
	// Model a delivery error where the pane remains live: unlike the kill race,
	// this must not create a second pane or risk sending the command twice.
	engineSendError := errors.New("tmux rejected keys")
	engineWithError := &livePaneSendErrorEngine{fakeAgentWindowEngine: engine, sendErr: engineSendError}

	err := sendAgentCommandToWindow(context.Background(), engineWithError, "wt", "job-a", "/work", "agent", "C-m")
	if !errors.Is(err, engineSendError) {
		t.Fatalf("error = %v, want original send error", err)
	}
	if engine.newWindowCalls != 0 || engine.sendCalls != 1 {
		t.Fatalf("calls = new-window %d, send %d; want 0, 1", engine.newWindowCalls, engine.sendCalls)
	}
}

type livePaneSendErrorEngine struct {
	*fakeAgentWindowEngine
	sendErr error
}

func (e *livePaneSendErrorEngine) SendKeys(_ context.Context, _ string, keys ...string) error {
	e.sendCalls++
	e.sent = append(e.sent, append([]string(nil), keys...))
	return e.sendErr
}

func TestEnsureAgentWindowAcceptsConcurrentCreator(t *testing.T) {
	engine := &fakeAgentWindowEngine{
		createErr:      errors.New("duplicate window"),
		createdOnError: true,
	}
	if _, err := ensureAgentWindow(context.Background(), engine, "wt", "job-a", "/work"); err != nil {
		t.Fatalf("ensureAgentWindow rejected a pane created concurrently: %v", err)
	}
	if engine.newWindowCalls != 1 {
		t.Fatalf("NewWindow called %d times, want 1", engine.newWindowCalls)
	}
}

func TestEnsureAgentWindowDoesNotHideCreateFailure(t *testing.T) {
	engine := &fakeAgentWindowEngine{createErr: errors.New("tmux server unavailable")}
	_, err := ensureAgentWindow(context.Background(), engine, "wt", "job-a", "/work")
	if err == nil || !strings.Contains(err.Error(), "tmux server unavailable") {
		t.Fatalf("ensureAgentWindow error = %v, want create failure", err)
	}
}
