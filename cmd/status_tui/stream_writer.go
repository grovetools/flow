package status_tui

import (
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/components/logviewer"
)

// chanStreamWriter is an io.Writer that buffers incoming bytes and emits
// complete lines as logviewer.LogLineMsg values onto a Model's MsgCh channel.
// It mirrors logviewer.StreamWriter but does not hold a reference to a
// *tea.Program, so it can be used from an embedded TUI where the root program
// is owned by a host other than this package.
type chanStreamWriter struct {
	ch                chan<- tea.Msg
	workspace         string
	buffer            strings.Builder
	mu                sync.Mutex
	NoWorkspacePrefix bool
}

// newChanStreamWriter constructs a chanStreamWriter bound to the given channel
// and tagged with the given workspace.
func newChanStreamWriter(ch chan<- tea.Msg, workspace string) *chanStreamWriter {
	return &chanStreamWriter{ch: ch, workspace: workspace}
}

// Write buffers incoming data and emits complete lines (terminated by
// newlines) as logviewer.LogLineMsg values onto the channel.
func (w *chanStreamWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buffer.Write(p)

	content := w.buffer.String()
	lines := strings.Split(content, "\n")

	if len(lines) > 0 {
		w.buffer.Reset()
		lastLine := lines[len(lines)-1]
		w.buffer.WriteString(lastLine)

		for i := 0; i < len(lines)-1; i++ {
			sendToMsgCh(w.ch, logviewer.LogLineMsg{
				Workspace: w.workspace,
				Line:      lines[i],
				NoPrefix:  w.NoWorkspacePrefix,
			})
		}
	}

	return len(p), nil
}
