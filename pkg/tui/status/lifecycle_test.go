package status

import (
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCloseUnblocksListenStream verifies that Close() closes MsgCh, which
// unblocks the recursive listenStream tea.Cmd so its goroutine exits. Without
// this, embedding hosts (e.g. grove terminal) that replace a status.Model on
// workspace switch would leak one listener goroutine per instance.
func TestCloseUnblocksListenStream(t *testing.T) {
	// Minimal Model with just the fields needed for the listener lifecycle.
	// We bypass New() because the full constructor pulls in plan loading,
	// daemon clients, and filesystem state unrelated to this test.
	m := &Model{
		MsgCh:          make(chan tea.Msg, 1),
		streamWg:       &sync.WaitGroup{},
		msgChCloseOnce: &sync.Once{},
	}

	// Arm the listener and execute its Cmd in a goroutine, mirroring how
	// bubbletea schedules tea.Cmds. The goroutine must block on <-ch until
	// Close() closes the channel.
	cmd := m.listenStream()
	if cmd == nil {
		t.Fatal("listenStream returned nil cmd")
	}

	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()

	// Give the goroutine a moment to park on the receive.
	time.Sleep(10 * time.Millisecond)

	// Close() must close MsgCh (unblocking the listener) and wait for the
	// goroutine to actually exit before returning.
	closeDone := make(chan struct{})
	go func() {
		_ = m.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return within 2s — listener goroutine likely leaked")
	}

	// The listener goroutine should have returned nil (signalling end of
	// stream) and terminated.
	select {
	case msg := <-done:
		if msg != nil {
			t.Errorf("listener returned non-nil msg after Close(): %#v", msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("listener goroutine did not exit after Close()")
	}

	// Close() must be idempotent — calling it twice must not panic on the
	// double close of MsgCh.
	if err := m.Close(); err != nil {
		t.Errorf("second Close() returned error: %v", err)
	}
}
