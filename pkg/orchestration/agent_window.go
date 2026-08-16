package orchestration

import (
	"context"
	"fmt"
)

// agentWindowEngine is the narrow part of mux.MuxEngine needed to launch an
// interactive agent into its job window.
type agentWindowEngine interface {
	PaneExists(ctx context.Context, target string) (bool, error)
	NewWindow(ctx context.Context, sessionName, windowName, workDir string, detached bool) error
	SendKeys(ctx context.Context, target string, keys ...string) error
}

// ensureAgentWindow returns the job pane, creating it only when it is missing.
// A failed create is re-probed because another launcher may have won the race.
func ensureAgentWindow(ctx context.Context, engine agentWindowEngine, sessionName, windowName, workDir string) (string, error) {
	target := fmt.Sprintf("%s:%s", sessionName, windowName)
	exists, err := engine.PaneExists(ctx, target)
	if err != nil {
		return target, fmt.Errorf("checking agent pane %s: %w", target, err)
	}
	if exists {
		return target, nil
	}

	if createErr := engine.NewWindow(ctx, sessionName, windowName, workDir, true); createErr != nil {
		// Treat a concurrent creator as success, but never swallow a create
		// failure merely because it might have been a duplicate.
		exists, probeErr := engine.PaneExists(ctx, target)
		if probeErr == nil && exists {
			return target, nil
		}
		return target, fmt.Errorf("creating agent window %s: %w", target, createErr)
	}

	exists, err = engine.PaneExists(ctx, target)
	if err != nil {
		return target, fmt.Errorf("verifying agent pane %s: %w", target, err)
	}
	if !exists {
		return target, fmt.Errorf("agent window %s was created but its pane is missing", target)
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
		exists, probeErr := engine.PaneExists(ctx, target)
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
