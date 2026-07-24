// Command connectprobe replicates the exact daemon-connection sequence of
// `flow plan tui` (pre-TUI NewWithAutoStart, then the browser's
// factory→StreamState→GetPlanIndex loop with its 5s reconnect tick) and
// prints wall-clock timings for every leg. It then stays attached to the
// SSE stream and reports every plan-index update it receives, applying the
// browser's revision-merge guards so client-side drop decisions are visible.
//
// Diagnostic tool for ticket
// 20260723-tui-plans-large-portfolio-takes-over-110s-to-render; run it
// inside a sandboxed fixture the way the TUI pilot runs flow.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/paths"
)

var t0 = time.Now()

func logf(format string, args ...any) {
	fmt.Printf("%s +%07.3f %s\n", time.Now().Format("15:04:05.000"), time.Since(t0).Seconds(), fmt.Sprintf(format, args...))
}

func main() {
	cwd, _ := os.Getwd()
	scope := daemon.ResolveClientScope()
	socketPath := paths.SocketPath(scope)
	logf("probe start cwd=%s scope=%s socket=%s", cwd, scope, socketPath)

	// Watch for the socket file appearing, independent of connect attempts.
	go func() {
		for {
			if _, err := os.Stat(socketPath); err == nil {
				logf("socket file exists")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Leg 0: what runPlanTUI does before the TUI renders.
	logf("pre-TUI NewWithAutoStart begin")
	preClient := daemon.NewWithAutoStart()
	logf("pre-TUI NewWithAutoStart done running=%v type=%T", preClient.IsRunning(), preClient)

	// Leg 1: the browser's connect loop (Init + planIndexReconnectMsg cadence).
	attempt := 0
	for {
		attempt++
		aStart := time.Now()
		logf("attempt %d: factory begin", attempt)
		client := daemon.NewWithAutoStartOpts(cwd, daemon.SuppressStartNotice())
		logf("attempt %d: factory done in %.3fs running=%v type=%T", attempt, time.Since(aStart).Seconds(), client.IsRunning(), client)

		ctx, cancel := context.WithCancel(context.Background())
		sStart := time.Now()
		updates, err := client.StreamState(ctx)
		logf("attempt %d: StreamState done in %.3fs err=%v", attempt, time.Since(sStart).Seconds(), err)
		if err != nil {
			cancel()
			client.Close()
			logf("attempt %d: FAILED (stream); sleeping 5s like planIndexReconnectTick", attempt)
			time.Sleep(5 * time.Second)
			continue
		}

		gStart := time.Now()
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 3*time.Second)
		snapshot, err := client.GetPlanIndex(fetchCtx)
		fetchCancel()
		if err != nil {
			logf("attempt %d: GetPlanIndex FAILED in %.3fs err=%v; sleeping 5s", attempt, time.Since(gStart).Seconds(), err)
			cancel()
			client.Close()
			time.Sleep(5 * time.Second)
			continue
		}
		planCount, revision := 0, uint64(0)
		if snapshot != nil {
			planCount, revision = len(snapshot.Plans), snapshot.Revision
		}
		logf("attempt %d: GetPlanIndex done in %.3fs plans=%d revision=%d", attempt, time.Since(gStart).Seconds(), planCount, revision)
		logf("CONNECTED (daemon live equivalent) plans=%d revision=%d", planCount, revision)

		// Leg 2: stay attached; report every update with the browser's guards.
		clientRevision := revision
		for update := range updates {
			if snap := update.PlanIndexSnapshot; snap != nil {
				verdict := "APPLY (snapshot)"
				if snap.Revision <= clientRevision {
					verdict = "IGNORE (snapshot revision not newer)"
				} else {
					clientRevision = snap.Revision
				}
				logf("SSE snapshot revision=%d plans=%d -> %s", snap.Revision, len(snap.Plans), verdict)
			}
			if delta := update.PlanIndex; delta != nil {
				names := ""
				for i, up := range delta.Upserts {
					if i >= 3 {
						names += ",..."
						break
					}
					if i > 0 {
						names += ","
					}
					names += up.PlanName
				}
				verdict := "APPLY"
				switch {
				case delta.Revision <= clientRevision:
					verdict = "IGNORE (buffered pre-snapshot revision)"
				case delta.Revision > clientRevision+1:
					verdict = fmt.Sprintf("GAP -> reconnect (firstRevision %d > clientRevision %d+1)", delta.Revision, clientRevision)
					clientRevision = delta.Revision
				default:
					clientRevision = delta.Revision
				}
				logf("SSE delta revision=%d upserts=%d removed=%d [%s] -> %s", delta.Revision, len(delta.Upserts), len(delta.Removed), names, verdict)
			}
			if update.PlanIndex == nil && update.PlanIndexSnapshot == nil {
				logf("SSE other update type=%q workspaces=%d", update.UpdateType, len(update.Workspaces))
			}
		}
		logf("stream closed; sleeping 5s then reconnecting")
		cancel()
		client.Close()
		time.Sleep(5 * time.Second)
	}
}
