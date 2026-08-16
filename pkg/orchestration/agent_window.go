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

// findAgentWindow checks the session's exact window list. It returns a unique
// mux identity rather than a session:name target: tmux permits duplicate window
// names, and a name target becomes unresolvable once duplicates exist.
func findAgentWindow(ctx context.Context, engine agentWindowEngine, sessionName, windowName string) (mux.WindowInfo, bool, error) {
	windows, err := engine.ListWindows(ctx, sessionName)
	if err != nil {
		return mux.WindowInfo{}, false, err
	}
	var found mux.WindowInfo
	ok := false
	for _, window := range windows {
		if window.Name != windowName {
			continue
		}
		// Prefer the newest/highest-index match. This deterministically avoids
		// older empty windows left by interrupted retries.
		if !ok || window.Index > found.Index || (window.Index == found.Index && window.ID > found.ID) {
			found, ok = window, true
		}
	}
	return found, ok, nil
}

func agentWindowTarget(sessionName string, window mux.WindowInfo) string {
	if window.ID != "" {
		return window.ID
	}
	// ListWindows always supplies an index even when an engine has no opaque
	// window ID. Unlike a name, session:index remains unambiguous when names are
	// duplicated.
	return fmt.Sprintf("%s:%d", sessionName, window.Index)
}

// ensureAgentWindow returns an unambiguous job-window identity, creating a
// window only when no exact-name match exists. A failed create is re-probed
// because another launcher may have won the race.
func ensureAgentWindow(ctx context.Context, engine agentWindowEngine, sessionName, windowName, workDir string) (string, error) {
	fallback := fmt.Sprintf("%s:%s", sessionName, windowName)
	window, exists, err := findAgentWindow(ctx, engine, sessionName, windowName)
	if err != nil {
		return fallback, fmt.Errorf("checking agent window %s: %w", fallback, err)
	}
	if exists {
		return agentWindowTarget(sessionName, window), nil
	}

	if createErr := engine.NewWindow(ctx, sessionName, windowName, workDir, true); createErr != nil {
		window, exists, probeErr := findAgentWindow(ctx, engine, sessionName, windowName)
		if probeErr == nil && exists {
			return agentWindowTarget(sessionName, window), nil
		}
		return fallback, fmt.Errorf("creating agent window %s: %w", fallback, createErr)
	}

	window, exists, err = findAgentWindow(ctx, engine, sessionName, windowName)
	if err != nil {
		return fallback, fmt.Errorf("verifying agent window %s: %w", fallback, err)
	}
	if !exists {
		return fallback, fmt.Errorf("agent window %s was created but is missing from the session", fallback)
	}
	return agentWindowTarget(sessionName, window), nil
}

// sendAgentCommandToWindow closes the create/send and kill/retry races. target
// is the exact identity returned by ensureAgentWindow; returning the identity
// actually used lets the caller stamp that same value into the daemon record.
// If target disappears before delivery, select (or create) the current
// exact-name window and retry exactly once. An error against the same still-live
// identity is returned without retrying, so a command is never delivered twice.
func sendAgentCommandToWindow(ctx context.Context, engine agentWindowEngine, sessionName, windowName, workDir, target string, keys ...string) (string, error) {
	if err := engine.SendKeys(ctx, target, keys...); err != nil {
		window, exists, probeErr := findAgentWindow(ctx, engine, sessionName, windowName)
		if probeErr != nil {
			return target, err
		}
		if exists {
			replacement := agentWindowTarget(sessionName, window)
			if replacement == target {
				return target, err
			}
			target = replacement
		} else {
			target, probeErr = ensureAgentWindow(ctx, engine, sessionName, windowName, workDir)
			if probeErr != nil {
				return target, fmt.Errorf("agent window disappeared before send (%v); recreating it failed: %w", err, probeErr)
			}
		}
		if retryErr := engine.SendKeys(ctx, target, keys...); retryErr != nil {
			return target, fmt.Errorf("sending agent command after selecting %s: %w", target, retryErr)
		}
	}
	return target, nil
}
