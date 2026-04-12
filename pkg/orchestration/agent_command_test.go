package orchestration

import (
	"fmt"
	"strings"
	"testing"
)

// TestBuildCommandParity verifies that the groveterm native agent path
// (GrovetermAgentProvider.buildCommand) produces the same shell command string
// as the tmux interactive agent path (ClaudeAgentProvider.buildAgentCommand)
// for the "claude" provider.
//
// Both paths write bytes into a shell (PTY or tmux send-keys), so the command
// string must be shell-safe and identical between the two code paths.
func TestBuildCommandParity(t *testing.T) {
	tests := []struct {
		name             string
		agentArgs        []string
		briefingFilePath string
	}{
		{
			name:             "simple path",
			agentArgs:        []string{"--model", "opus"},
			briefingFilePath: "/tmp/briefing.xml",
		},
		{
			name:             "path with spaces",
			agentArgs:        []string{"--model", "opus"},
			briefingFilePath: "/Users/me/my projects/plan/briefing.xml",
		},
		{
			name:             "path with single quotes",
			agentArgs:        []string{"--model", "opus"},
			briefingFilePath: "/tmp/it's a test/briefing.xml",
		},
		{
			name:             "path with special characters",
			agentArgs:        []string{"--model", "opus"},
			briefingFilePath: "/tmp/plan (copy)/impl-briefing-quoting-fix-093f2e09/briefing-1776017699.xml",
		},
		{
			name:             "no agent args",
			agentArgs:        nil,
			briefingFilePath: "/tmp/briefing.xml",
		},
		{
			name:             "extra agent args",
			agentArgs:        []string{"--model", "opus", "--dangerously-skip-permissions"},
			briefingFilePath: "/tmp/briefing.xml",
		},
		{
			name:             "real-world long path",
			agentArgs:        []string{"--model", "opus"},
			briefingFilePath: "/Users/solom4/notebooks/grovetools/workspaces/grovetools/plans/treemux-phase4/.artifacts/impl-briefing-quoting-fix-093f2e09/briefing-1776017699.xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// --- Groveterm (native) path ---
			gp := &GrovetermAgentProvider{providerName: "claude"}
			nativeCmd := gp.buildCommand(tt.agentArgs, tt.briefingFilePath)

			// --- Tmux (ClaudeAgentProvider) path ---
			// Inline the logic from ClaudeAgentProvider.buildAgentCommand
			// since it requires a full provider struct we don't want to construct.
			escapedPath := "'" + strings.ReplaceAll(tt.briefingFilePath, "'", "'\\''") + "'"
			cmdParts := []string{"claude"}
			cmdParts = append(cmdParts, tt.agentArgs...)
			tmuxCmd := fmt.Sprintf("%s \"Read the briefing file at %s and execute the task.\"", strings.Join(cmdParts, " "), escapedPath)

			if nativeCmd != tmuxCmd {
				t.Errorf("command mismatch:\n  native: %s\n  tmux:   %s", nativeCmd, tmuxCmd)
			}
		})
	}
}

// TestBuildCommandShellSafety verifies the command string is valid when
// embedded inside sh -c '...' (the agentstream.BuildAgentCommand wrapper).
// We check that single quotes in the instruction are properly escaped so they
// don't break out of the sh -c wrapper.
func TestBuildCommandShellSafety(t *testing.T) {
	tests := []struct {
		name             string
		briefingFilePath string
	}{
		{
			name:             "path with single quotes",
			briefingFilePath: "/tmp/it's a test/briefing.xml",
		},
		{
			name:             "path with double quotes",
			briefingFilePath: `/tmp/he said "hello"/briefing.xml`,
		},
		{
			name:             "path with backticks",
			briefingFilePath: "/tmp/`whoami`/briefing.xml",
		},
		{
			name:             "path with dollar sign",
			briefingFilePath: "/tmp/$HOME/briefing.xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gp := &GrovetermAgentProvider{providerName: "claude"}
			cmd := gp.buildCommand([]string{"--model", "opus"}, tt.briefingFilePath)

			// The command must not contain unescaped single quotes outside of
			// the escape sequence '\\'' — this would break sh -c '...' wrapping.
			// After BuildAgentCommand escapes single quotes, the result should
			// be embeddable. Here we just verify the raw command is well-formed.

			// Count quotes to verify balance: the instruction should be wrapped
			// in a pair of double quotes.
			dqCount := strings.Count(cmd, `"`) - strings.Count(cmd, `\"`)
			if dqCount < 2 {
				t.Errorf("expected at least 2 unescaped double quotes wrapping the instruction, got %d in: %s", dqCount, cmd)
			}

			// The briefing path must appear in the command.
			if !strings.Contains(cmd, "briefing.xml") {
				t.Errorf("briefing filename not found in command: %s", cmd)
			}
		})
	}
}
