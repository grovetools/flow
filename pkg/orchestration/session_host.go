package orchestration

import (
	"fmt"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/sessions"
)

// sessionHostClient returns the daemon client every session-lifecycle call in
// a provider must use: intent registration, the native PTY spawn relay, and
// session confirmation.
//
// Routing and identity are deliberately separated here:
//
//   - TRANSPORT comes from the interactive host that will render and attach
//     the session — the published daemon.HostSocketEnv when this process
//     descends from a host, otherwise the UI-host registry that treemux
//     writes at startup (daemon.RegisterUIHost). The registry is what makes
//     this work from groved's in-process jobrunner: providers usually run
//     inside a daemon the host never spawned, where no published env can
//     ever arrive. A globally-hosted treemux streams sessions from exactly
//     one daemon; a session registered anywhere else is invisible to its
//     rail and Agents drawer no matter how correct the record is, and
//     cannot be attached.
//   - IDENTITY stays workDir-derived. The caller keeps passing the real
//     worktree as SessionIntent.WorkDir / SpawnAgentRequest.WorkDir, because
//     that is what drawer.IsSessionInWorkspace filters on and what
//     treemux's sessionForWorkDir uses to pick the owning session rail.
//
// With no host anywhere (bare `flow plan run` on a machine with no treemux,
// CI) this is exactly the previous scope-derived behavior.
func sessionHostClient(workDir string) daemon.Client {
	return daemon.NewSessionHostClient(resolveJobScope(workDir))
}

// sessionHostClientConnectOnly is sessionHostClient for the terminal-state
// writers that must never spawn a daemon (headless failure finalization): same
// host-first routing, connect-only fallback. See
// daemon.NewSessionHostClientConnectOnly.
//
// workDir may be empty when the caller failed before resolving one; the
// env/registry tiers still apply and the fallback degrades to plain scope
// resolution.
func sessionHostClientConnectOnly(workDir string) daemon.Client {
	if workDir == "" {
		// Do NOT route an empty workDir through resolveJobScope: with no
		// GROVE_SCOPE it would resolve the scope from the process CWD, which for
		// a failure path is arbitrary. Empty means "no scope intended".
		return daemon.NewSessionHostClientConnectOnly("")
	}
	return daemon.NewSessionHostClientConnectOnly(resolveJobScope(workDir))
}

// agentRelayClient returns the client for the PTY RELAY calls a running agent
// answers: capture its pane, send it input, interrupt it, ask whether its
// session is still alive. Same host-first routing as the rest of the
// session-lifecycle traffic — the daemon that owns the PTY is the only one that
// can answer — and connect-only, because none of these calls can be satisfied
// by a daemon that has to be started first. A freshly spawned groved knows
// nothing about an agent that is already running; every caller here already has
// a tmux fallback for exactly that case, and starting a worktree daemon to be
// told "no session" is how the fleet grew a scoped groved per active worktree.
//
// workDir is the agent's working directory (it identifies the host, not the
// daemon); "" is allowed and degrades to the env/registry tiers alone.
func agentRelayClient(workDir string) daemon.Client {
	return sessionHostClientConnectOnly(workDir)
}

// agentRelayClientForSession is agentRelayClient for the isolated-agent calls
// that only carry a job id. The session registry records the working directory
// the job launched with, which is what host resolution needs; an unresolvable
// session degrades to the env/registry tiers with no scope, never to a
// CWD-derived guess.
func agentRelayClientForSession(jobID string) daemon.Client {
	workDir := ""
	if registry, err := sessions.NewFileSystemRegistry(); err == nil && registry != nil {
		if metadata, err := registry.Find(jobID); err == nil && metadata != nil {
			workDir = metadata.WorkingDirectory
		}
	}
	return agentRelayClient(workDir)
}

// sessionHostClientForJob returns the session-lifecycle client for a job whose
// working directory can be derived from the plan. Completion runs long after
// launch — often from a different process than the one that registered the
// session (the parent coordinator's `flow plan complete`, the status TUI, a
// `flow_subjob join`) — so it must re-derive the SAME host routing the
// provider used at launch. Resolving the daemon by scope instead would send
// the terminal status to the worktree's scoped groved while the live record
// sits on the host daemon, leaving the host's rail and Agents drawer showing a
// finished agent as still running forever.
//
// A workDir that cannot be determined falls back to the plan directory, and
// then to no scope at all — both strictly better than guessing from CWD.
func sessionHostClientForJob(job *Job, plan *Plan) daemon.Client {
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil || workDir == "" {
		if plan != nil {
			workDir = plan.Directory
		}
	}
	if workDir == "" {
		return daemon.NewSessionHostClient("")
	}
	return sessionHostClient(workDir)
}

// terminalSessionClientForJob routes terminal best-effort writes to an already
// running host daemon without starting a new daemon merely to retire a dead
// process.
func terminalSessionClientForJob(job *Job, plan *Plan) daemon.Client {
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil || workDir == "" {
		if plan != nil {
			workDir = plan.Directory
		}
	}
	return sessionHostClientConnectOnly(workDir)
}

// hostTransportSocket returns the socket of the daemon owning the interactive
// host UI that will display workDir's session, or "" when no host is
// declared. It resolves exactly like sessionHostClient (env, then registry),
// so the endpoint stamped into an agent's environment always names the daemon
// its session lifecycle actually rides.
func hostTransportSocket(workDir string) string {
	socketPath, viaHost := daemon.ResolveSessionHostSocket(resolveJobScope(workDir))
	if !viaHost {
		return ""
	}
	return socketPath
}

// hostSocketEnvInline renders the host transport endpoint as an inline shell
// assignment for the tmux-based providers, which prefix env onto the agent
// command rather than passing a map. Empty when no host is declared.
//
// The agent process needs this because its hooks open their own daemon client
// (hooks/internal/storage.NewDaemonBackend). Without it, hook-driven status
// updates would follow GROVE_SCOPE back to the worktree daemon and diverge
// from the intent/confirm records the host is streaming.
func hostSocketEnvInline(workDir string) string {
	socketPath := hostTransportSocket(workDir)
	if socketPath == "" {
		return ""
	}
	return fmt.Sprintf("%s=%s ", daemon.HostSocketEnv, shellSingleQuote(socketPath))
}

// applyHostSocketEnv stamps the host transport endpoint into an agent env map
// (the native/groveterm path). No-op when no host is declared, so agents
// launched outside a host keep a clean environment.
func applyHostSocketEnv(envVars map[string]string, workDir string) {
	if socketPath := hostTransportSocket(workDir); socketPath != "" {
		envVars[daemon.HostSocketEnv] = socketPath
	}
}
