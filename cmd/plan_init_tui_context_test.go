package cmd

import (
	"context"
	"testing"

	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

func TestRequestToPlanInitCmdPreservesContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := requestToPlanInitCmd(&planinit.Request{Dir: "test-plan"}, &PlanInitCmd{Context: ctx})
	if got.Context == nil || got.Context.Err() != context.Canceled {
		t.Fatal("TUI command reconstruction dropped the CLI cancellation context")
	}
}
