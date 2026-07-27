package orchestration

import (
	"context"
	"sync"
	"time"

	"github.com/grovetools/core/pkg/process"
)

// piPaneCapture reads the current terminal contents of a Pi job's pane. It is
// injected so the groveterm (daemon relay) and tmux (mux engine) providers can
// share one watcher, and so tests can drive it without a terminal.
type piPaneCapture func(ctx context.Context) (string, error)

const (
	// First capture happens almost immediately: the failure this watcher exists
	// for (a bad extension) is printed within a few hundred milliseconds of
	// spawn, and Pi exits right after.
	piPaneCaptureFirstDelay = 300 * time.Millisecond
	// Captures back off exponentially so a healthy long-lived pane costs a
	// handful of round-trips per minute rather than one per second.
	piPaneCaptureMaxDelay = 5 * time.Second
	// Hard ceiling on the watcher's lifetime. Discovery normally stops it in
	// seconds, and its own worst case (30s pidfile wait + 30s process-tree
	// fallback + 10s transcript retries) fits inside this; the budget only
	// bounds a watcher whose caller leaked it.
	piPaneCaptureBudget = 120 * time.Second
	// A single capture can hang (the daemon relays to the terminal UI, which
	// may be gone), so every attempt gets its own deadline.
	piPaneCaptureTimeout = 5 * time.Second
	// Liveness polling is cheap (a signal 0 probe), so it runs far more often
	// than capture to catch the instant the PTY is about to disappear.
	piPaneLivenessInterval = 250 * time.Millisecond
	// Bound on how long stop() waits for the capture goroutine to unwind, so a
	// wedged capture can never block the job's status transition.
	piPaneWatcherStopTimeout = 3 * time.Second
)

// piPaneWatcher keeps the most recent useful snapshot of a Pi pane while
// session discovery runs.
//
// Capturing once *after* discovery gives up is too late: Pi can print an
// extension load error and exit ~1s after spawn, while discovery is still
// inside its PID wait and transcript retry loop tens of seconds later. By then
// the PTY is gone and the capture only yields "pty session ... not found", so
// the failure transcript can say nothing about what actually happened. The
// watcher polls from spawn instead, retains the last non-empty capture, and
// remembers the last capture error so an empty result can still explain itself.
type piPaneWatcher struct {
	capture piPaneCapture
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once

	mu      sync.Mutex
	output  string
	lastErr error
	pid     int
}

// startPiPaneWatcher begins polling capture in the background. The caller must
// call stop() to release the goroutine and read the retained snapshot.
func startPiPaneWatcher(ctx context.Context, capture piPaneCapture) *piPaneWatcher {
	watchCtx, cancel := context.WithCancel(ctx)
	w := &piPaneWatcher{
		capture: capture,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go w.run(watchCtx)
	return w
}

// observePID tells the watcher which process owns the pane. Once known, the
// watcher captures the moment that process stops being alive rather than
// waiting for the next backoff tick.
func (w *piPaneWatcher) observePID(pid int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.pid = pid
	w.mu.Unlock()
}

// stop halts the watcher and returns the last non-empty capture together with
// the last capture error. When the returned output is empty the error explains
// why there is none. Safe to call more than once (callers defer it as a
// safety net and also call it explicitly once discovery settles).
func (w *piPaneWatcher) stop() (string, error) {
	if w == nil {
		return "", nil
	}
	w.once.Do(func() {
		w.cancel()
		select {
		case <-w.done:
		case <-time.After(piPaneWatcherStopTimeout):
			// A wedged capture must never delay the terminal status
			// transition; the goroutine writes under the mutex, so reading
			// the snapshot below stays safe even if it is still running.
		}
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output, w.lastErr
}

func (w *piPaneWatcher) run(ctx context.Context) {
	defer close(w.done)

	budget := time.NewTimer(piPaneCaptureBudget)
	defer budget.Stop()
	liveness := time.NewTicker(piPaneLivenessInterval)
	defer liveness.Stop()

	delay := piPaneCaptureFirstDelay
	next := time.NewTimer(delay)
	defer next.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-budget.C:
			return
		case <-liveness.C:
			if w.knownPIDExited() {
				// The PTY dies with the process, so this is the last moment
				// its output can be read at all. Take one final snapshot and
				// stop: nothing more will ever appear on this pane.
				w.snapshot(ctx)
				return
			}
		case <-next.C:
			w.snapshot(ctx)
			delay = min(delay*2, piPaneCaptureMaxDelay)
			next.Reset(delay)
		}
	}
}

// knownPIDExited reports death only for a PID we actually discovered. An
// unknown PID (0) says nothing about the process and must not be read as death.
func (w *piPaneWatcher) knownPIDExited() bool {
	w.mu.Lock()
	pid := w.pid
	w.mu.Unlock()
	return pid > 0 && !process.IsProcessAlive(pid)
}

func (w *piPaneWatcher) snapshot(ctx context.Context) {
	captureCtx, cancel := context.WithTimeout(ctx, piPaneCaptureTimeout)
	output, err := w.capture(captureCtx)
	cancel()

	w.mu.Lock()
	defer w.mu.Unlock()
	// Only overwrite on success: a capture that fails once the pane is torn
	// down must not erase the snapshot taken while Pi was still printing.
	if output != "" {
		w.output = output
	}
	if err != nil && ctx.Err() != nil {
		// Our own shutdown aborted an in-flight capture. That is not a
		// diagnosis, so keep whatever the last real attempt reported.
		return
	}
	w.lastErr = err
}
