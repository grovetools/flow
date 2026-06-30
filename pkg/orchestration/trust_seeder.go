package orchestration

import (
	"context"

	"github.com/grovetools/core/pkg/daemon"
)

// NewTrustSeedFallback returns the closure that workspace.Prepare invokes when
// its in-process Claude folder-trust write is denied by the OS sandbox
// (~/.claude.json is outside the sandbox's writable boundary). The closure
// delegates the privileged write to an unsandboxed daemon.
//
// It dials in two steps:
//
//  1. The scoped daemon at scopeRoot (the parent ecosystem / gitRoot — the scope
//     whose daemon is actively running the provisioning job, when there is one).
//     scopeRoot must NOT be the brand-new worktree's own path: that scope has no
//     daemon yet.
//  2. If no scoped daemon is reachable (daemon.New degrades to LocalClient, whose
//     SeedTrust hard-fails), fall back to the always-on global/unscoped daemon.
//
// The global fallback is safe and deliberate here even though daemon.New itself
// refuses a global fallback for general RPCs: /api/trust/seed derives the trusted
// paths SOLELY from the worktree registry (keyed by worktreeRef), so ANY
// unsandboxed daemon services the request identically — there is no
// "which daemon am I talking to?" ambiguity to preserve. This covers the common
// case where only the global daemon (the shared proxy host) is running, which
// otherwise left the worktree untrusted and stalled the agent at the trust prompt.
//
// Both steps are best-effort: if neither daemon is reachable the final error is
// surfaced as a warning, worktree creation still finishes, and the user just gets
// the interactive trust prompt.
func NewTrustSeedFallback(scopeRoot string) func(context.Context, string) error {
	return func(ctx context.Context, worktreeRef string) error {
		if err := daemon.New(scopeRoot).SeedTrust(ctx, worktreeRef); err == nil {
			return nil
		}
		return daemon.NewGlobalClient().SeedTrust(ctx, worktreeRef)
	}
}
