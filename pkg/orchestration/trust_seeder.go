package orchestration

import (
	"context"

	"github.com/grovetools/core/pkg/daemon"
)

// NewTrustSeedFallback returns the closure that workspace.Prepare invokes when
// its in-process Claude folder-trust write is denied by the OS sandbox
// (~/.claude.json is outside the sandbox's writable boundary). The closure
// delegates the privileged write to the daemon dialed at scopeRoot.
//
// scopeRoot MUST be the parent ecosystem / gitRoot — the scope whose daemon is
// actively running the provisioning job and is therefore unsandboxed and live.
// It must NOT be the brand-new worktree's own path: that scope has no daemon yet
// and would fall through to LocalClient, whose SeedTrust hard-fails. Dialing the
// gitRoot scope hits the daemon that launched the flow agent.
//
// If no daemon is running at scopeRoot, daemon.New degrades to LocalClient and
// the closure returns LocalClient's "daemon unavailable" error, which Prepare
// surfaces as a best-effort warning — worktree creation still finishes; the user
// just gets the interactive trust prompt.
func NewTrustSeedFallback(scopeRoot string) func(context.Context, string) error {
	return func(ctx context.Context, worktreeRef string) error {
		return daemon.New(scopeRoot).SeedTrust(ctx, worktreeRef)
	}
}
