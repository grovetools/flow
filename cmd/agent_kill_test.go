package cmd

import (
	"strings"
	"testing"
)

// TestAgentKill_ArgValidation verifies the kill command's target-shape checks
// happen before any daemon interaction, and that the --id orphan escape hatch
// is mutually exclusive with the positional <slug> <job> form.
func TestAgentKill_ArgValidation(t *testing.T) {
	t.Run("rejects both --id and positional args", func(t *testing.T) {
		cmd := newAgentKillCmd()
		cmd.SetArgs([]string{"--id", "sess-123", "my-plan", "my-job"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "not both") {
			t.Fatalf("expected 'not both' error, got %v", err)
		}
	})

	t.Run("rejects fewer than two positional args without --id", func(t *testing.T) {
		cmd := newAgentKillCmd()
		cmd.SetArgs([]string{"only-one"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "<slug> <job>") {
			t.Fatalf("expected usage error, got %v", err)
		}
	})

	t.Run("rejects zero args without --id", func(t *testing.T) {
		cmd := newAgentKillCmd()
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "<slug> <job>") {
			t.Fatalf("expected usage error, got %v", err)
		}
	})
}
