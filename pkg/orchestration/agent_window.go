package orchestration

import (
	"context"
	"fmt"

	"github.com/grovetools/core/pkg/mux"
)

// agentWindowEngine is the narrow part of mux.MuxEngine needed to launch an
// interactive agent into its job window.
type agentWindowEngine interface {
	ListWindows(ctx context.Context, sessionName string) ([]mux.WindowInfo, error)
	NewWindow(ctx context.Context, sessionName, windowName, workDir string, detached bool) error
	SendKeys(ctx context.Context, target string, keys ...string) error
}

// agentWindowExists checks the session's window list rather than probing the
// target as a pane. tmux display-message accepts a missing window target by
// falling back to the session's current pane, which makes PaneExists unsuitable
// for deciding whether an exact job window exists.
func agentWindowExists(ctx context.Context, engine agentWindowEngine, sessionName, windowName string) (bool, error) {
	windows, err := engine.ListWindows(ctx, sessionName)
	if err != nil {
		return false, err
	}
	for _, window := range windows {
		if window.Name == windowName {
			return true, nil
		}
	}
	return false, nil
}

// ensureAgentWindow returns the job pane, creating it only when it is missing.
// A failed create is re-probed because another launcher may have won the race.
func ensureAgentWindow(ctx context.Context, engine agentWindowEngine, sessionName, windowName, workDir string) (string, error) {
	target := fmt.Sprintf("%s:%s", sessionName, windowName)
	exists, err := agentWindowExists(ctx, engine, sessionName, windowName)
	if err != nil {
		return target, fmt.Errorf("checking agent window %s: %w", target, err)
	}
	if exists {
		return target, nil
	}

	if createErr := engine.NewWindow(ctx, sessionName, windowName, workDir, true); createErr != nil {
		// Treat a concurrent creator as success, but never swallow a create
		// failure merely because it might have been a duplicate.
		exists, probeErr := agentWindowExists(ctx, engine, sessionName, windowName)
		if probeErr == nil && exists {
			return target, nil
		}
		return target, fmt.Errorf("creating agent window %s: %w", target, createErr)
	}

	exists, err = agentWindowExists(ctx, engine, sessionName, windowName)
	if err != nil {
		return target, fmt.Errorf("verifying agent window %s: %w", target, err)
	}
	if !exists {
		return target, fmt.Errorf("agent window %s was created but is missing from the session", target)
	}
	return target, nil
}

// sendAgentCommandToWindow closes the kill/retry race: completion cleanup can
// remove the old attempt's window after a retry has first checked it. If the
// first send finds that the pane disappeared, recreate the window and retry
// exactly once. An error against a still-live pane is returned without retrying
// so a command is never delivered twice to an existing agent.
func sendAgentCommandToWindow(ctx context.Context, engine agentWindowEngine, sessionName, windowName, workDir string, keys ...string) error {
	target, err := ensureAgentWindow(ctx, engine, sessionName, windowName, workDir)
	if err != nil {
		return err
	}
	if err := engine.SendKeys(ctx, target, keys...); err != nil {
		exists, probeErr := agentWindowExists(ctx, engine, sessionName, windowName)
		if probeErr != nil || exists {
			return err
		}
		if _, ensureErr := ensureAgentWindow(ctx, engine, sessionName, windowName, workDir); ensureErr != nil {
			return fmt.Errorf("agent pane %s disappeared before send (%v); recreating it failed: %w", target, err, ensureErr)
		}
		if retryErr := engine.SendKeys(ctx, target, keys...); retryErr != nil {
			return fmt.Errorf("sending agent command after recreating %s: %w", target, retryErr)
		}
	}
	return nil
}
