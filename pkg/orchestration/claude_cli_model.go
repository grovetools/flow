package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
)

// claudeCLIDefaultFamily is the model family the claude CLI self-selects when
// nothing configures one. It is a family, not a version, so the concrete label
// tracks the grove-anthropic registry as new models land instead of pinning a
// version that goes stale on the next release.
const claudeCLIDefaultFamily = "opus"

// claudeModelFamilies are the bare family names the claude CLI accepts wherever
// a model is named (--model, the `model` setting, /model). Matching is by
// prefix so variants like "opusplan" resolve to their family.
var claudeModelFamilies = []string{"fable", "opus", "sonnet", "haiku"}

// claudeSettingsFile is the sliver of a Claude Code settings file flow reads.
// Every other key is ignored — this is a read-only peek at what the CLI will
// pick, never a rewrite of the user's settings.
type claudeSettingsFile struct {
	Model string `json:"model"`
}

// resolveClaudeCLIModel returns the canonical alias of the model the claude CLI
// will run with when flow passes no --model, or "" when that can't be
// determined. It reads the same sources the CLI does, in the CLI's own
// precedence order:
//
//  1. ANTHROPIC_MODEL — from flow's configured agent env (which flow injects
//     into the agent process), then from flow's own environment.
//  2. <workDir>/.claude/settings.local.json — the per-worktree settings grove
//     seeds from the [claude] profile in grove.toml.
//  3. <workDir>/.claude/settings.json — project-shared settings.
//  4. <config dir>/settings.json — the user's settings, under CLAUDE_CONFIG_DIR
//     when set, else ~/.claude.
//
// The first source that *names* a model wins outright: if its value can't be
// resolved to a registry alias the result is "" rather than a lower-precedence
// answer, since a confidently-wrong label is worse than an absent one. Two
// sources are deliberately not consulted — enterprise managed policy (not
// readable from here) and a mid-session /model switch (happens after launch).
func resolveClaudeCLIModel(workDir string, agentEnv map[string]string) string {
	if raw, ok := agentEnv["ANTHROPIC_MODEL"]; ok && strings.TrimSpace(raw) != "" {
		return normalizeClaudeModelLabel(raw)
	}
	if raw := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")); raw != "" {
		return normalizeClaudeModelLabel(raw)
	}

	var paths []string
	if workDir != "" {
		paths = append(paths,
			filepath.Join(workDir, ".claude", "settings.local.json"),
			filepath.Join(workDir, ".claude", "settings.json"),
		)
	}
	if userDir := claudeUserConfigDir(); userDir != "" {
		paths = append(paths, filepath.Join(userDir, "settings.json"))
	}

	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // settings path is derived, not user input
		if err != nil {
			continue // absent or unreadable: fall through to the next source
		}
		var settings claudeSettingsFile
		if err := json.Unmarshal(data, &settings); err != nil {
			continue // malformed settings: the CLI ignores it, so do we
		}
		if strings.TrimSpace(settings.Model) == "" {
			continue // present but silent on model: keep looking
		}
		return normalizeClaudeModelLabel(settings.Model)
	}
	return ""
}

// claudeUserConfigDir returns the claude CLI's user config directory —
// CLAUDE_CONFIG_DIR when set, else ~/.claude. Empty when the home directory
// can't be resolved.
func claudeUserConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// normalizeClaudeModelLabel maps a raw claude CLI model string to its canonical
// registry alias, or "" when the registry doesn't recognize it. It handles the
// three shapes the CLI accepts: a full dated ID ("claude-sonnet-4-6-20260115"),
// an alias ("claude-opus-5"), and a bare family ("opus", "opusplan"), each
// optionally carrying the CLI's context-window marker ("opus[1m]").
func normalizeClaudeModelLabel(raw string) string {
	s := strings.TrimSpace(raw)
	// Strip the context-window marker: "opus[1m]" and "claude-opus-5[1m]" name
	// the same model as their unmarked forms.
	if i := strings.IndexByte(s, '['); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	if s == "" {
		return ""
	}

	// A known ID or alias collapses to its canonical alias.
	if canon := canonicalClaudeModel(s); canon != "" {
		if _, known := anthropicmodels.Aliases()[canon]; known {
			return canon
		}
	}

	// A bare family resolves to whatever is current in that family.
	lower := strings.ToLower(s)
	for _, family := range claudeModelFamilies {
		if !strings.HasPrefix(lower, family) {
			continue
		}
		if alias, ok := anthropicmodels.LatestAliasForFamily(family); ok {
			return alias
		}
	}
	return ""
}

// claudeCLIFallbackModel is the label used when the CLI's configured model
// can't be determined: the current member of the family the CLI defaults to,
// falling back to the registry's declared agent default if that family somehow
// has no current entry.
func claudeCLIFallbackModel() string {
	if alias, ok := anthropicmodels.LatestAliasForFamily(claudeCLIDefaultFamily); ok {
		return alias
	}
	return anthropicmodels.DefaultAgentAlias
}
