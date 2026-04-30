package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/grovetools/tuimux"
)

type TuimuxEngine struct {
	api        *tuimux.ApiClient
	socketPath string
}

func NewTuimuxEngine() (*TuimuxEngine, error) {
	socketPath := tuimux.DefaultSocketPath()
	api, err := tuimux.EnsureDaemon(socketPath)
	if err != nil {
		return nil, fmt.Errorf("tuimux daemon: %w", err)
	}
	return &TuimuxEngine{api: api, socketPath: socketPath}, nil
}

func (e *TuimuxEngine) CreateSession(ctx context.Context, name string, workDir string) error {
	if err := e.api.CreateSession(name); err != nil {
		return fmt.Errorf("create tuimux session: %w", err)
	}

	// Start a detached headless primary in background.
	args := []string{"new", "-s", name, "-d"}
	cmd := exec.CommandContext(ctx, "tuimux", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start tuimux primary: %w", err)
	}
	_ = cmd.Process.Release()

	// Wait briefly for primary to connect.
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		sessions, err := e.api.ListSessions()
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if s.Name == name && s.ClientCount > 0 {
				return nil
			}
		}
	}
	return nil
}

func (e *TuimuxEngine) KillSession(ctx context.Context, name string) error {
	return e.api.DeleteSession(name)
}

func (e *TuimuxEngine) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	sessions, err := e.api.ListSessions()
	if err != nil {
		return nil, err
	}
	result := make([]SessionInfo, len(sessions))
	for i, s := range sessions {
		result[i] = SessionInfo{
			Name:        s.Name,
			ClientCount: s.ClientCount,
		}
	}
	return result, nil
}

func (e *TuimuxEngine) SessionExists(ctx context.Context, name string) (bool, error) {
	sessions, err := e.api.ListSessions()
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if s.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (e *TuimuxEngine) SendKeys(ctx context.Context, target string, keys ...string) error {
	session, pane := splitTarget(target)
	args := []string{"send-keys"}
	if pane != "" {
		args = append(args, "-t", pane)
	}
	args = append(args, keys...)
	result, err := e.api.Execute(session, args)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("send-keys failed: %s", result.Error)
	}
	return nil
}

func (e *TuimuxEngine) CapturePane(ctx context.Context, target string) (string, error) {
	session, pane := splitTarget(target)
	args := []string{"capture-pane", "-p"}
	if pane != "" {
		args = append(args, "-t", pane)
	}
	result, err := e.api.Execute(session, args)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("capture-pane failed: %s", result.Error)
	}
	return result.Output, nil
}

func (e *TuimuxEngine) WaitForIdle(ctx context.Context, target string, timeout time.Duration) error {
	session, _ := splitTarget(target)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := e.api.Execute(session, []string{"list-panes", "--json"})
		if err == nil && result.ExitCode == 0 {
			var panes []struct {
				Idle bool `json:"idle"`
			}
			if json.Unmarshal([]byte(result.Output), &panes) == nil {
				allIdle := len(panes) > 0
				for _, p := range panes {
					if !p.Idle {
						allIdle = false
						break
					}
				}
				if allIdle {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting for idle on %s", target)
}

func (e *TuimuxEngine) WaitForText(ctx context.Context, target string, pattern string, timeout time.Duration) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := e.CapturePane(ctx, target)
		if err != nil {
			return "", err
		}
		if match := re.FindString(output); match != "" {
			return match, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("timeout waiting for pattern %q on %s", pattern, target)
}

func (e *TuimuxEngine) Run(ctx context.Context, target string, command string, timeout time.Duration) (string, error) {
	session, _ := splitTarget(target)
	result, err := e.api.Execute(session, []string{"run", command})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return result.Output, fmt.Errorf("run failed (exit %d): %s", result.ExitCode, result.Error)
	}
	return result.Output, nil
}

func (e *TuimuxEngine) SplitWindow(ctx context.Context, target string, horizontal bool) error {
	session, pane := splitTarget(target)
	args := []string{"split-window"}
	if horizontal {
		args = append(args, "-h")
	}
	if pane != "" {
		args = append(args, "-t", pane)
	}
	result, err := e.api.Execute(session, args)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("split-window failed: %s", result.Error)
	}
	return nil
}

func (e *TuimuxEngine) ListPanes(ctx context.Context, sessionName string) ([]PaneInfo, error) {
	result, err := e.api.Execute(sessionName, []string{"list-panes", "--json"})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("list-panes failed: %s", result.Error)
	}
	var raw []struct {
		ID                string `json:"id"`
		Active            bool   `json:"active"`
		Idle              bool   `json:"idle"`
		ForegroundProcess string `json:"foreground_process"`
		Cwd               string `json:"cwd"`
	}
	if err := json.Unmarshal([]byte(result.Output), &raw); err != nil {
		return nil, fmt.Errorf("parse panes: %w", err)
	}
	panes := make([]PaneInfo, len(raw))
	for i, r := range raw {
		panes[i] = PaneInfo{
			ID:                r.ID,
			Active:            r.Active,
			Idle:              r.Idle,
			ForegroundProcess: r.ForegroundProcess,
			Cwd:               r.Cwd,
		}
	}
	return panes, nil
}

func (e *TuimuxEngine) NewWindow(ctx context.Context, sessionName string, windowName string, workDir string, detached bool) error {
	args := []string{"new-window", "-n", windowName}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	if detached {
		args = append(args, "-d")
	}
	result, err := e.api.Execute(sessionName, args)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("new-window failed: %s", result.Error)
	}
	return nil
}

func (e *TuimuxEngine) GetSessionPID(ctx context.Context, sessionName string) (int, error) {
	// Tuimux daemon owns sessions — return daemon PID from socket.
	pid := os.Getpid()
	return pid, nil
}

// splitTarget splits "session:pane" into session and pane components.
func splitTarget(target string) (session, pane string) {
	if i := strings.Index(target, ":"); i >= 0 {
		return target[:i], target[i+1:]
	}
	return target, ""
}

var _ MuxEngine = (*TuimuxEngine)(nil)
