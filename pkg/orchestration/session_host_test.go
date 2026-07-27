package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/paths"
)

// stubDaemon is a unix-socket HTTP server standing in for a groved. Two of
// them model the split that hid a live native agent from treemux: a GLOBAL
// daemon hosting the UI, and a WORKTREE-SCOPED daemon that the job's own
// working directory resolves to.
type stubDaemon struct {
	socketPath string

	mu       sync.Mutex
	intents  []daemon.SessionIntent
	spawns   []daemon.SpawnAgentRequest
	confirms []daemon.SessionConfirmation
	ends     []sessionEndCall
}

// sessionEndCall records a POST /api/sessions/{id}/end — the terminal half of
// the lifecycle, which must come home to the same daemon as intent/confirm.
type sessionEndCall struct {
	JobID   string
	Outcome string
}

// serveStubDaemon binds a stub groved at socketPath. Both stubs answer the
// same three session-lifecycle endpoints, so which one recorded a call is the
// only thing that distinguishes them — exactly the property under test.
func serveStubDaemon(t *testing.T, socketPath string) *stubDaemon {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	d := &stubDaemon{socketPath: socketPath}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", socketPath, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions/intent", func(w http.ResponseWriter, r *http.Request) {
		var v daemon.SessionIntent
		_ = json.NewDecoder(r.Body).Decode(&v)
		d.mu.Lock()
		d.intents = append(d.intents, v)
		d.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/agents/spawn", func(w http.ResponseWriter, r *http.Request) {
		var v daemon.SpawnAgentRequest
		_ = json.NewDecoder(r.Body).Decode(&v)
		d.mu.Lock()
		d.spawns = append(d.spawns, v)
		d.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/sessions/confirm", func(w http.ResponseWriter, r *http.Request) {
		var v daemon.SessionConfirmation
		_ = json.NewDecoder(r.Body).Decode(&v)
		d.mu.Lock()
		d.confirms = append(d.confirms, v)
		d.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	// Catch-all for the per-session routes (…/{id}, …/{id}/end). The exact
	// patterns registered above still win for intent/confirm.
	mux.HandleFunc("/api/sessions/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		if id, ok := strings.CutSuffix(rest, "/end"); ok {
			var v struct {
				Outcome string `json:"outcome"`
			}
			_ = json.NewDecoder(r.Body).Decode(&v)
			d.mu.Lock()
			d.ends = append(d.ends, sessionEndCall{JobID: id, Outcome: v.Outcome})
			d.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		// GET /api/sessions/{id}: unknown to a stub that was never told about
		// this session.
		w.WriteHeader(http.StatusNotFound)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); ln.Close() })
	return d
}

func (d *stubDaemon) endCalls() []sessionEndCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]sessionEndCall(nil), d.ends...)
}

func (d *stubDaemon) counts() (intents, spawns, confirms int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.intents), len(d.spawns), len(d.confirms)
}

// hostedLaunchFixture reproduces the failing topology: a GLOBAL treemux host
// (no GROVE_SCOPE pinned) while the user browses a worktree, and a job whose
// working directory is in that worktree and therefore resolves to a DIFFERENT,
// live daemon. Both daemons are real listeners, so "which socket got the call"
// is decided by the code under test rather than by which one happens to exist.
type hostedLaunchFixture struct {
	host    *stubDaemon
	scoped  *stubDaemon
	workDir string
	job     *Job
	plan    *Plan
	home    string
}

func newHostedLaunchFixture(t *testing.T) *hostedLaunchFixture {
	t.Helper()

	// Short root: macOS caps unix sun_path around 104 bytes, and the scoped
	// socket name carries a basename plus hash.
	home, err := os.MkdirTemp("/tmp", "fh")
	if err != nil {
		t.Fatalf("mkdir temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	t.Setenv("HOME", home)
	t.Setenv("GROVE_HOME", home) // pins paths.RuntimeDir()/StateDir() under home
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	// Global host: no scope pinned, nothing published yet.
	t.Setenv("GROVE_SCOPE", "")
	_ = os.Unsetenv("GROVE_SCOPE")
	t.Setenv(daemon.HostSocketEnv, "")
	_ = os.Unsetenv(daemon.HostSocketEnv)

	f := &hostedLaunchFixture{home: home}

	// The worktree the user is viewing. A real git repo so
	// workspace.ResolveScope treats it as its own boundary.
	f.workDir = filepath.Join(home, "wt")
	if err := os.MkdirAll(f.workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = f.workDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	scope := resolveJobScope(f.workDir)
	if scope == "" {
		t.Fatalf("worktree %s resolved to the empty (global) scope; fixture cannot distinguish daemons", f.workDir)
	}
	scopedSocket := paths.SocketPath(scope)
	globalSocket := paths.SocketPath("")
	if scopedSocket == globalSocket {
		t.Fatalf("scoped and global sockets collide at %s", scopedSocket)
	}
	f.host = serveStubDaemon(t, globalSocket)
	f.scoped = serveStubDaemon(t, scopedSocket)

	planDir := filepath.Join(home, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	jobPath := filepath.Join(planDir, "job.md")
	if err := os.WriteFile(jobPath, []byte("---\nid: job-1\nstatus: pending\n---\n\nbody\n"), 0o600); err != nil {
		t.Fatalf("write job: %v", err)
	}

	f.job = &Job{ID: "job-1", Title: "Live agent", FilePath: jobPath}
	f.plan = &Plan{Name: "perf-audit", Directory: planDir}
	return f
}

// TestNativeLaunchRoutesThroughPublishedHostDaemon is the core regression. A
// native interactive agent launched from Flow inside a globally-hosted treemux,
// with a working directory in a worktree that has its OWN live daemon, must
// register intent and request the PTY spawn on the HOST daemon — and must
// still carry the worktree as WorkingDirectory, which is what the rail and
// Agents drawer filter on. Before the fix both calls went to the scoped daemon,
// whose sessions the host never streams.
func TestNativeLaunchRoutesThroughPublishedHostDaemon(t *testing.T) {
	f := newHostedLaunchFixture(t)
	t.Setenv(daemon.HostSocketEnv, f.host.socketPath)

	spec, ok := LookupAgentProvider("claude")
	if !ok {
		t.Fatal("claude provider spec missing from registry")
	}
	p := NewGrovetermAgentProvider(spec, false, "native")

	if err := p.LaunchPrepared(context.Background(), f.job, f.plan, f.workDir, "claude", ""); err != nil {
		t.Fatalf("LaunchPrepared: %v", err)
	}

	if hi, hs, _ := f.host.counts(); hi != 1 || hs != 1 {
		t.Fatalf("host daemon received intents=%d spawns=%d, want 1/1", hi, hs)
	}
	if si, ss, sc := f.scoped.counts(); si+ss+sc != 0 {
		t.Fatalf("worktree-scoped daemon saw traffic it must not: intents=%d spawns=%d confirms=%d", si, ss, sc)
	}

	f.host.mu.Lock()
	intent := f.host.intents[0]
	spawn := f.host.spawns[0]
	f.host.mu.Unlock()

	if intent.WorkDir != f.workDir {
		t.Fatalf("intent WorkDir = %q, want the real worktree %q", intent.WorkDir, f.workDir)
	}
	if spawn.WorkDir != f.workDir {
		t.Fatalf("spawn WorkDir = %q, want the real worktree %q", spawn.WorkDir, f.workDir)
	}
	// Identity stays worktree-derived: the session record must still name the
	// worktree's ecosystem even though the transport went to the host.
	if got, want := spawn.Env["GROVE_SCOPE"], resolveJobScope(f.workDir); got != want {
		t.Fatalf("spawn env GROVE_SCOPE = %q, want the worktree scope %q", got, want)
	}
	// Transport is carried into the agent process so its hooks come home to
	// the same daemon instead of following GROVE_SCOPE.
	if got := spawn.Env[daemon.HostSocketEnv]; got != f.host.socketPath {
		t.Fatalf("spawn env %s = %q, want host socket %q", daemon.HostSocketEnv, got, f.host.socketPath)
	}
}

// TestNativeConfirmRoutesThroughPublishedHostDaemon covers the second half of
// the lifecycle. Confirmation must promote the pending intent on the SAME host
// daemon; confirming elsewhere leaves the host holding a forever-pending record
// — an agent that shows up as intent-only and never becomes attachable.
func TestNativeConfirmRoutesThroughPublishedHostDaemon(t *testing.T) {
	f := newHostedLaunchFixture(t)
	t.Setenv(daemon.HostSocketEnv, f.host.socketPath)

	// Make PID and transcript discovery resolve immediately: the pidfile the
	// agentstream wrapper writes, and a claude transcript in the sanitized
	// projects directory.
	if err := os.MkdirAll(filepath.Dir(agentstream.PidFilePath(f.job.ID)), 0o755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	if err := os.WriteFile(agentstream.PidFilePath(f.job.ID), []byte("4242\n"), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	projects := filepath.Join(f.home, ".claude", "projects", agentstream.SanitizePathForClaude(f.workDir))
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	transcriptPath := filepath.Join(projects, "0199f81a-dead-beef-0000-000000000001.jsonl")
	line := fmt.Sprintf("{\"timestamp\":%q}\n", time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	if err := os.WriteFile(transcriptPath, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	spec, _ := LookupAgentProvider("claude")
	p := NewGrovetermAgentProvider(spec, false, "native")
	f.job.StartTime = time.Now().Add(-time.Minute)

	if err := p.discoverAndRegisterSessionAsync(f.job, f.plan, f.workDir, ""); err != nil {
		t.Fatalf("discoverAndRegisterSessionAsync: %v", err)
	}

	if _, _, hc := f.host.counts(); hc != 1 {
		t.Fatalf("host daemon received %d confirmations, want 1", hc)
	}
	if _, _, sc := f.scoped.counts(); sc != 0 {
		t.Fatalf("worktree-scoped daemon received %d confirmations, want 0", sc)
	}

	f.host.mu.Lock()
	confirm := f.host.confirms[0]
	f.host.mu.Unlock()
	if confirm.PID != 4242 {
		t.Fatalf("confirm PID = %d, want 4242", confirm.PID)
	}
	if confirm.TranscriptPath != transcriptPath {
		t.Fatalf("confirm TranscriptPath = %q, want %q", confirm.TranscriptPath, transcriptPath)
	}
}

// TestNativeLaunchFallsBackToScopedDaemonWithoutHost pins the unhosted
// contract: with nothing published (bare `flow plan run`, CI), routing is
// unchanged — the job's own scope wins, and no host endpoint is stamped into
// the agent environment.
func TestNativeLaunchFallsBackToScopedDaemonWithoutHost(t *testing.T) {
	f := newHostedLaunchFixture(t)

	spec, _ := LookupAgentProvider("claude")
	p := NewGrovetermAgentProvider(spec, false, "native")
	if err := p.LaunchPrepared(context.Background(), f.job, f.plan, f.workDir, "claude", ""); err != nil {
		t.Fatalf("LaunchPrepared: %v", err)
	}

	if si, ss, _ := f.scoped.counts(); si != 1 || ss != 1 {
		t.Fatalf("scoped daemon received intents=%d spawns=%d, want 1/1", si, ss)
	}
	if hi, hs, _ := f.host.counts(); hi+hs != 0 {
		t.Fatalf("global daemon saw traffic with no host published: intents=%d spawns=%d", hi, hs)
	}

	f.scoped.mu.Lock()
	spawn := f.scoped.spawns[0]
	f.scoped.mu.Unlock()
	if _, present := spawn.Env[daemon.HostSocketEnv]; present {
		t.Fatalf("unhosted launch stamped %s into the agent env", daemon.HostSocketEnv)
	}
}

// TestHostSocketEnvInlineQuotesForTmuxProviders covers the tmux-based
// providers, which prefix env onto the agent command string rather than passing
// a map. Their agents' hooks need the same endpoint.
func TestHostSocketEnvInlineQuotesForTmuxProviders(t *testing.T) {
	// Hermetic state dir: the ambient UI-host registry must not leak in.
	t.Setenv("GROVE_HOME", t.TempDir())
	t.Setenv("GROVE_SCOPE", "")
	_ = os.Unsetenv("GROVE_SCOPE")
	t.Setenv(daemon.HostSocketEnv, "")
	_ = os.Unsetenv(daemon.HostSocketEnv)
	if got := hostSocketEnvInline("/some/workdir"); got != "" {
		t.Fatalf("hostSocketEnvInline() = %q with no host declared, want empty", got)
	}

	t.Setenv(daemon.HostSocketEnv, "/tmp/gr ove/groved.sock")
	got := hostSocketEnvInline("/some/workdir")
	want := daemon.HostSocketEnv + "='/tmp/gr ove/groved.sock' "
	if got != want {
		t.Fatalf("hostSocketEnvInline() = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, " ") {
		t.Fatal("inline env fragment must end with a separator space")
	}
}

// TestNativeLaunchRoutesThroughRegisteredHostDaemon is the topology the env
// var can never fix, observed live on 2026-07-26: the provider executes
// inside a groved's jobrunner — a process treemux never spawned — so
// daemon.HostSocketEnv is necessarily absent there. The UI-host REGISTRY must
// route the launch: intent and PTY spawn land on the host daemon, nothing
// reaches the worktree-scoped daemon, and the agent env carries the host
// endpoint (stamped from the registry, since this process env has none) next
// to its worktree GROVE_SCOPE identity.
func TestNativeLaunchRoutesThroughRegisteredHostDaemon(t *testing.T) {
	f := newHostedLaunchFixture(t)

	// A global treemux registered as UI host; no env published in THIS
	// process, exactly like groved's jobrunner.
	unregister, err := daemon.RegisterUIHost("", "treemux")
	if err != nil {
		t.Fatalf("RegisterUIHost: %v", err)
	}
	defer unregister()

	spec, ok := LookupAgentProvider("claude")
	if !ok {
		t.Fatal("claude provider spec missing from registry")
	}
	p := NewGrovetermAgentProvider(spec, false, "native")

	if err := p.LaunchPrepared(context.Background(), f.job, f.plan, f.workDir, "claude", ""); err != nil {
		t.Fatalf("LaunchPrepared: %v", err)
	}

	if hi, hs, _ := f.host.counts(); hi != 1 || hs != 1 {
		t.Fatalf("host daemon received intents=%d spawns=%d, want 1/1", hi, hs)
	}
	if si, ss, sc := f.scoped.counts(); si+ss+sc != 0 {
		t.Fatalf("worktree-scoped daemon saw traffic it must not: intents=%d spawns=%d confirms=%d", si, ss, sc)
	}

	f.host.mu.Lock()
	intent := f.host.intents[0]
	spawn := f.host.spawns[0]
	f.host.mu.Unlock()

	if intent.WorkDir != f.workDir {
		t.Fatalf("intent WorkDir = %q, want the real worktree %q", intent.WorkDir, f.workDir)
	}
	if got, want := spawn.Env["GROVE_SCOPE"], resolveJobScope(f.workDir); got != want {
		t.Fatalf("spawn env GROVE_SCOPE = %q, want the worktree scope %q", got, want)
	}
	if got := spawn.Env[daemon.HostSocketEnv]; got != f.host.socketPath {
		t.Fatalf("spawn env %s = %q, want the registered host socket %q", daemon.HostSocketEnv, got, f.host.socketPath)
	}
}

// TestCompleteJobEndsSessionOnHostDaemon is the "Pi subjobs never turn into
// checkmarks in the rail" regression.
//
// The completing process is almost never the launching one: a parent
// coordinator's `flow_subjob join` shells out to `flow plan complete` from
// inside its own worktree, so its GROVE_SCOPE names the WORKTREE while the
// session it is closing was registered on the HOST daemon (that is what every
// interactive provider does). CompleteJob used to build its client by scope,
// so "completed" went to the worktree's groved. The host's record stayed
// non-terminal forever, and since nothing revisits a rail row afterwards, a
// finished subjob kept rendering as a live agent even though flow's own status
// view showed it complete.
func TestCompleteJobEndsSessionOnHostDaemon(t *testing.T) {
	f := newHostedLaunchFixture(t)
	// The completing shell's ambient state: scoped to its worktree, but
	// launched under a host that published its endpoint.
	t.Setenv("GROVE_SCOPE", resolveJobScope(f.workDir))
	t.Setenv(daemon.HostSocketEnv, f.host.socketPath)

	// A chat job keeps the assertion on routing: no agent process to kill, no
	// tmux window, no transcript archival.
	f.job.Type = JobTypeChat
	f.job.Status = JobStatusRunning

	if err := CompleteJob(f.job, f.plan, true); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	hostEnds := f.host.endCalls()
	if len(hostEnds) != 1 {
		t.Fatalf("host daemon received %d session-end calls, want 1 (scoped got %d)", len(hostEnds), len(f.scoped.endCalls()))
	}
	if hostEnds[0].JobID != f.job.ID || hostEnds[0].Outcome != "completed" {
		t.Fatalf("host end call = %+v, want {JobID:%s Outcome:completed}", hostEnds[0], f.job.ID)
	}
	if scopedEnds := f.scoped.endCalls(); len(scopedEnds) != 0 {
		t.Fatalf("worktree-scoped daemon received %d session-end calls, want 0: %+v", len(scopedEnds), scopedEnds)
	}
}

// TestCompleteJobEndsSessionOnScopedDaemonWithoutHost pins the unhosted
// contract: with nothing published (bare `flow plan complete` in CI), the
// terminal status still follows ordinary scope resolution.
func TestCompleteJobEndsSessionOnScopedDaemonWithoutHost(t *testing.T) {
	f := newHostedLaunchFixture(t)
	t.Setenv("GROVE_SCOPE", resolveJobScope(f.workDir))

	f.job.Type = JobTypeChat
	f.job.Status = JobStatusRunning

	if err := CompleteJob(f.job, f.plan, true); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}

	if scopedEnds := f.scoped.endCalls(); len(scopedEnds) != 1 {
		t.Fatalf("worktree-scoped daemon received %d session-end calls, want 1", len(scopedEnds))
	}
	if hostEnds := f.host.endCalls(); len(hostEnds) != 0 {
		t.Fatalf("global daemon saw a session end with no host published: %+v", hostEnds)
	}
}
