package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/subjobmon"
)

// subjobStubDaemon is a recording stub groved for the subjob host-routing
// tests: it serves exactly the three endpoints `flow subjob publish|watch`
// touch — event publication, the reconcile snapshot, and the SSE stream.
type subjobStubDaemon struct {
	socketPath string

	mu      sync.Mutex
	events  []models.SubjobEvent
	streams int
}

func newSubjobStubDaemon(t *testing.T, socketPath string) *subjobStubDaemon {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &subjobStubDaemon{socketPath: socketPath}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", socketPath, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/subjobs/event", func(w http.ResponseWriter, r *http.Request) {
		var ev models.SubjobEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		d.mu.Lock()
		d.events = append(d.events, ev)
		d.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/subjobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(&models.SubjobSnapshot{Reports: map[string]*models.SubjobState{}})
	})
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.streams++
		d.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); ln.Close() })
	return d
}

func (d *subjobStubDaemon) eventCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.events)
}

func (d *subjobStubDaemon) streamCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streams
}

func (d *subjobStubDaemon) firstEventKind() models.SubjobEventKind {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.events) == 0 {
		return ""
	}
	return d.events[0].Kind
}

// subjobRoutingFixture builds the dual-daemon topology from plan perf-audit
// job 27: a plan dir whose scope resolves to a WORKTREE-style scoped socket
// (the stale-notebook-groved stand-in), plus the global host socket. Pinning
// GROVE_HOME to a short /tmp dir keeps a developer's live treemux
// registration out of the registry lookup (bit us in job 26) and keeps
// socket paths under macOS's sun_path limit.
type subjobRoutingFixture struct {
	canonical  string
	childPath  string
	scopedSock string
}

func newSubjobRoutingFixture(t *testing.T) *subjobRoutingFixture {
	t.Helper()

	groveHome, err := os.MkdirTemp("/tmp", "sjr-home")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(groveHome) })
	t.Setenv("GROVE_HOME", groveHome)
	t.Setenv(daemon.HostSocketEnv, "")
	_ = os.Unsetenv(daemon.HostSocketEnv)
	t.Setenv("GROVE_SCOPE", "")
	_ = os.Unsetenv("GROVE_SCOPE")

	planDir, err := os.MkdirTemp("/tmp", "sjr-plan")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(planDir) })
	// git init gives the plan dir a non-empty scope, so its scoped socket is
	// distinct from the global/host socket — the exact topology that let a
	// stale notebook-root groved capture subjob events.
	if out, gitErr := exec.Command("git", "init", "-q", planDir).CombinedOutput(); gitErr != nil {
		t.Fatalf("git init plan dir: %v\n%s", gitErr, out)
	}

	p := &orchestration.Plan{Config: &orchestration.PlanConfig{Status: "active"}, Jobs: []*orchestration.Job{
		{ID: "parent", Filename: "01-parent.md", Title: "parent", Type: orchestration.JobTypeInteractiveAgent, Status: orchestration.JobStatusRunning},
		{ID: "child", Filename: "02-child.md", Title: "child", Type: orchestration.JobTypeInteractiveAgent, ParentJobID: "parent", Status: orchestration.JobStatusRunning},
	}}
	if err := orchestration.SavePlan(planDir, p); err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(planDir, ".artifacts", "child")
	if err := os.MkdirAll(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	report := `{"schema_version":1,"child_job_id":"child","parent_job_id":"parent","summary":"done","artifacts":{},"created_at":"now"}`
	if err := os.WriteFile(filepath.Join(artifacts, "final-report.json"), []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}

	canonical, _, err := subjobmon.CanonicalPlan(planDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := workspace.ResolveScope(canonical)
	if scope == "" {
		t.Fatalf("plan dir %s resolved to the global scope; the dual-socket topology needs a distinct scoped socket", canonical)
	}
	f := &subjobRoutingFixture{
		canonical:  canonical,
		childPath:  filepath.Join(canonical, "02-child.md"),
		scopedSock: paths.SocketPath(scope),
	}
	if f.scopedSock == paths.SocketPath("") {
		t.Fatal("scoped socket collapsed onto the global socket; fixture invalid")
	}
	return f
}

func runSubjobPublishCmd(t *testing.T, ctx context.Context, childPath string) string {
	t.Helper()
	cmd := NewSubjobCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"publish", childPath, "--state", "report_ready"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("subjob publish: %v\nstderr: %s", err, errOut.String())
	}
	return out.String()
}

// TestSubjobPublishRoutesThroughRegisteredHostDaemon mirrors
// TestNativeLaunchRoutesThroughRegisteredHostDaemon for the report channel:
// no GROVE_HOST_DAEMON_SOCKET in env, a registered global host, AND a live
// daemon on the plan's scoped socket → the event must land on the HOST
// daemon while the scoped daemon receives nothing. Before this fix,
// NewWithAutoStart(canonical) let the scoped daemon capture it.
func TestSubjobPublishRoutesThroughRegisteredHostDaemon(t *testing.T) {
	f := newSubjobRoutingFixture(t)

	unregister, err := daemon.RegisterUIHost("", "treemux")
	if err != nil {
		t.Fatalf("RegisterUIHost: %v", err)
	}
	defer unregister()

	host := newSubjobStubDaemon(t, paths.SocketPath(""))
	scoped := newSubjobStubDaemon(t, f.scopedSock)

	out := runSubjobPublishCmd(t, context.Background(), f.childPath)

	if host.eventCount() != 1 {
		t.Fatalf("host daemon received %d subjob events, want 1", host.eventCount())
	}
	if got := host.firstEventKind(); got != models.SubjobReportReady {
		t.Fatalf("host daemon event kind = %q, want %q", got, models.SubjobReportReady)
	}
	if scoped.eventCount() != 0 {
		t.Fatalf("scoped daemon captured %d subjob events; it must receive nothing", scoped.eventCount())
	}
	if !strings.Contains(out, "report_ready") {
		t.Fatalf("publish output missing normalized event: %q", out)
	}
}

// TestSubjobWatchStreamsFromRegisteredHostDaemon is the coordinator half of
// the invariant: watch must stream and reconcile against the SAME daemon
// publish targets — the host — so a live scoped daemon can never siphon the
// report lifecycle away from the parent.
func TestSubjobWatchStreamsFromRegisteredHostDaemon(t *testing.T) {
	f := newSubjobRoutingFixture(t)

	unregister, err := daemon.RegisterUIHost("", "treemux")
	if err != nil {
		t.Fatalf("RegisterUIHost: %v", err)
	}
	defer unregister()

	host := newSubjobStubDaemon(t, paths.SocketPath(""))
	scoped := newSubjobStubDaemon(t, f.scopedSock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Watch resolves its plan dir from the unified target (or cwd); inject
	// the target the way the root --at PersistentPreRunE would.
	ctx = context.WithValue(ctx, TargetContextKey, &plan.ResolvedTarget{PlanDir: f.canonical})

	cmd := NewSubjobCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"watch", "--parent-job-id", "parent"})

	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	// The initial reconcile reads disk truth (running child + fresh report,
	// empty snapshot) and repairs the daemon with a report_ready publish —
	// on the HOST daemon.
	deadline := time.Now().Add(10 * time.Second)
	for host.eventCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("subjob watch: %v\nstderr: %s", err, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subjob watch did not exit after context cancellation")
	}

	if host.eventCount() != 1 || host.streamCount() == 0 {
		t.Fatalf("host daemon saw events=%d streams=%d, want the watch to stream and repair there", host.eventCount(), host.streamCount())
	}
	if scoped.eventCount() != 0 || scoped.streamCount() != 0 {
		t.Fatalf("scoped daemon saw events=%d streams=%d; it must receive nothing", scoped.eventCount(), scoped.streamCount())
	}
	if !strings.Contains(out.String(), "report_ready") {
		t.Fatalf("watch emitted no report_ready record: %q", out.String())
	}
}

// TestSubjobPublishFallsBackToScopeWithoutHost keeps the no-host case honest:
// with nothing published and nothing registered, routing is identical to
// today — the scope-resolved daemon for the canonical plan dir.
func TestSubjobPublishFallsBackToScopeWithoutHost(t *testing.T) {
	f := newSubjobRoutingFixture(t)

	scoped := newSubjobStubDaemon(t, f.scopedSock)

	_ = runSubjobPublishCmd(t, context.Background(), f.childPath)

	if scoped.eventCount() != 1 {
		t.Fatalf("scoped daemon received %d subjob events, want 1 (no-host fallback)", scoped.eventCount())
	}
}
