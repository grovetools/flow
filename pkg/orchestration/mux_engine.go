package orchestration

import (
	"context"
	"os"
	"time"

	"github.com/grovetools/tuimux"
)

type MuxEngine interface {
	CreateSession(ctx context.Context, name string, workDir string) error
	KillSession(ctx context.Context, name string) error
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	SessionExists(ctx context.Context, name string) (bool, error)

	SendKeys(ctx context.Context, target string, keys ...string) error
	CapturePane(ctx context.Context, target string) (string, error)
	WaitForIdle(ctx context.Context, target string, timeout time.Duration) error
	WaitForText(ctx context.Context, target string, pattern string, timeout time.Duration) (string, error)
	Run(ctx context.Context, target string, command string, timeout time.Duration) (string, error)

	SplitWindow(ctx context.Context, target string, horizontal bool) error
	ListPanes(ctx context.Context, sessionName string) ([]PaneInfo, error)

	// NewWindow creates a new window in the given session.
	NewWindow(ctx context.Context, sessionName string, windowName string, workDir string, detached bool) error

	// GetSessionPID returns a process ID associated with the session.
	GetSessionPID(ctx context.Context, sessionName string) (int, error)
}

type SessionInfo struct {
	Name        string
	ClientCount int
}

type PaneInfo struct {
	ID                string
	Active            bool
	Idle              bool
	ForegroundProcess string
	Cwd               string
}

// DetectMuxEngine returns a MuxEngine based on GROVE_MUX env var or auto-detection.
// If GROVE_MUX=tuimux, uses TuimuxEngine. If GROVE_MUX=tmux or unset, checks
// whether the tuimux daemon is reachable and falls back to tmux.
func DetectMuxEngine() (MuxEngine, error) {
	muxEnv := os.Getenv("GROVE_MUX")
	switch muxEnv {
	case "tuimux":
		return NewTuimuxEngine()
	case "tmux":
		return NewTmuxEngine()
	default:
		client := tuimux.NewApiClient(tuimux.DefaultSocketPath())
		if err := client.Ping(); err == nil {
			return NewTuimuxEngine()
		}
		return NewTmuxEngine()
	}
}
