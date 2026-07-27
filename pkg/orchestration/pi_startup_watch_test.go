package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The incident shape: Pi prints its error, the pane is captured while it is
// still alive, then the PTY disappears and every later capture fails. The
// watcher must hand back the snapshot, not the tear-down error.
func TestPiPaneWatcherRetainsLastNonEmptyCapture(t *testing.T) {
	var (
		mu     sync.Mutex
		calls  int
		gotAll = make(chan struct{})
	)
	watcher := startPiPaneWatcher(context.Background(), func(context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1:
			return "", errors.New("pane not ready")
		case 2:
			return "Error: Failed to load extension oracle.ts", nil
		case 3:
			close(gotAll)
			return "", errors.New("pty session not found")
		default:
			return "", errors.New("pty session not found")
		}
	})

	select {
	case <-gotAll:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not reach the third capture")
	}

	output, err := watcher.stop()
	if output != "Error: Failed to load extension oracle.ts" {
		t.Fatalf("output = %q, want the snapshot taken before the pane died", output)
	}
	if err == nil || err.Error() != "pty session not found" {
		t.Fatalf("err = %v, want the last capture error to be retained", err)
	}
}

// With no successful capture at all, the reason must survive so the failure can
// explain itself instead of silently reporting "no output".
func TestPiPaneWatcherReportsCaptureError(t *testing.T) {
	captured := make(chan struct{}, 1)
	watcher := startPiPaneWatcher(context.Background(), func(context.Context) (string, error) {
		select {
		case captured <- struct{}{}:
		default:
		}
		return "", errors.New("pty session 7e3a6082 not found")
	})

	select {
	case <-captured:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher never attempted a capture")
	}

	output, err := watcher.stop()
	if output != "" {
		t.Fatalf("output = %q, want empty", output)
	}
	if err == nil || err.Error() != "pty session 7e3a6082 not found" {
		t.Fatalf("err = %v, want the capture failure", err)
	}
}

// A known-dead PID means the PTY is going away, so the watcher takes its final
// snapshot immediately rather than waiting for the next backoff tick.
func TestPiPaneWatcherCapturesWhenKnownPIDExits(t *testing.T) {
	captured := make(chan struct{})
	var once sync.Once
	watcher := startPiPaneWatcher(context.Background(), func(context.Context) (string, error) {
		once.Do(func() { close(captured) })
		return "pi output", nil
	})
	// deadPID is never alive, so liveness polling fires on the first tick.
	watcher.observePID(deadPID)

	select {
	case <-captured:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not capture after the PID exited")
	}

	output, err := watcher.stop()
	if err != nil {
		t.Fatalf("stop() err = %v", err)
	}
	if output != "pi output" {
		t.Fatalf("output = %q", output)
	}
}

// stop() must be safe to call twice: call sites defer it as a safety net and
// also call it explicitly once discovery settles.
func TestPiPaneWatcherStopIsIdempotent(t *testing.T) {
	watcher := startPiPaneWatcher(context.Background(), func(context.Context) (string, error) {
		return "pane", nil
	})
	first, _ := watcher.stop()
	second, _ := watcher.stop()
	if first != second {
		t.Fatalf("stop() returned %q then %q", first, second)
	}
}

// A nil watcher stands in for "capture unavailable" at the call sites, so every
// method must tolerate it.
func TestPiPaneWatcherNilIsSafe(t *testing.T) {
	var watcher *piPaneWatcher
	watcher.observePID(42)
	output, err := watcher.stop()
	if output != "" || err != nil {
		t.Fatalf("stop() = %q, %v; want zero values", output, err)
	}
}
