package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/subjobmon"
	"github.com/spf13/cobra"
)

func NewSubjobCmd() *cobra.Command {
	root := &cobra.Command{Use: "subjob", Short: "Publish and watch Pi Flow child report state"}
	var state string
	publish := &cobra.Command{Use: "publish <child-job>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runSubjobPublish(cmd, args[0], state) }}
	publish.Flags().StringVar(&state, "state", "", "report_ready or joined")
	_ = publish.MarkFlagRequired("state")
	publish.Flags().Bool("json", false, "emit normalized JSON")
	var parent string
	watch := &cobra.Command{Use: "watch", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return runSubjobWatch(cmd, parent) }}
	watch.Flags().StringVar(&parent, "parent-job-id", "", "owning parent job ID")
	_ = watch.MarkFlagRequired("parent-job-id")
	watch.Flags().Bool("jsonl", false, "emit normalized JSON Lines")
	root.AddCommand(publish, watch)
	return root
}

func subjobPlanDir(cmd *cobra.Command, childArg string) (string, error) {
	if target, ok := TargetFromContext(cmd.Context()); ok && target.PlanDir != "" {
		return target.PlanDir, nil
	}
	if childArg != "" && filepath.IsAbs(childArg) {
		return filepath.Dir(childArg), nil
	}
	return resolvePlanPathCtx(cmd.Context(), ".", ".")
}

func findSubjob(plan *orchestration.Plan, target string) (*orchestration.Job, error) {
	if filepath.IsAbs(target) {
		target = filepath.Base(target)
	}
	if j, ok := plan.GetJobByID(target); ok {
		return j, nil
	}
	if j, ok := plan.GetJobByFilename(target); ok {
		return j, nil
	}
	return nil, fmt.Errorf("child job %s not found", target)
}

func runSubjobPublish(cmd *cobra.Command, target, state string) error {
	planDir, err := subjobPlanDir(cmd, target)
	if err != nil {
		return err
	}
	canonical, _, err := subjobmon.CanonicalPlan(planDir)
	if err != nil {
		return err
	}
	plan, err := orchestration.LoadPlan(canonical)
	if err != nil {
		return err
	}
	child, err := findSubjob(plan, target)
	if err != nil {
		return err
	}
	kind := models.SubjobEventKind(state)
	ev, err := subjobmon.BuildEvent(canonical, child, kind)
	if err != nil {
		return err
	}
	// Subjob events are session-lifecycle-adjacent: they must land on the
	// daemon the UI/parent coordinator streams, unconditionally — a stale
	// scoped groved capturing them makes reports invisible to the watch.
	// Host routing (env → registry → canonical scope) keeps publish (child
	// agent's shell) and watch (coordinator's shell) on the SAME daemon in
	// all three tiers: both inherit the same GROVE_HOST_DAEMON_SOCKET, fall
	// back to the same registry file, then to the same canonical plan dir.
	client := daemon.NewSessionHostClient(canonical)
	defer client.Close()
	if err := client.PublishSubjobEvent(cmd.Context(), *ev); err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(ev)
}

func runSubjobWatch(cmd *cobra.Command, parentID string) error {
	planDir, err := subjobPlanDir(cmd, "")
	if err != nil {
		return err
	}
	canonical, _, err := subjobmon.CanonicalPlan(planDir)
	if err != nil {
		return err
	}
	// Must resolve to the SAME daemon as `subjob publish` (see
	// runSubjobPublish): host routing (env → registry → canonical scope)
	// holds that invariant in all three tiers, and keeps watch streaming the
	// daemon the UI/parent streams rather than a stray scoped groved.
	client := daemon.NewSessionHostClient(canonical)
	defer client.Close()
	delivered := map[string]bool{}
	backoff := time.Second
	for {
		streamCtx, cancel := context.WithCancel(cmd.Context())
		stream, streamErr := client.StreamState(streamCtx)
		if streamErr == nil {
			if err := emitSubjobReconcile(cmd, client, canonical, parentID, delivered); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "flow subjob watch:", err)
			}
			backoff = time.Second
			var reconcileTimer *time.Timer
			var reconcileC <-chan time.Time
			for {
				select {
				case <-cmd.Context().Done():
					if reconcileTimer != nil {
						reconcileTimer.Stop()
					}
					cancel()
					return nil
				case update, ok := <-stream:
					if !ok {
						if reconcileTimer != nil {
							reconcileTimer.Stop()
						}
						cancel()
						goto reconnect
					}
					if (strings.HasPrefix(update.UpdateType, "subjob_") || strings.HasPrefix(update.UpdateType, "job_")) && reconcileTimer == nil {
						reconcileTimer = time.NewTimer(100 * time.Millisecond)
						reconcileC = reconcileTimer.C
					}
				case <-reconcileC:
					reconcileTimer = nil
					reconcileC = nil
					if err := emitSubjobReconcile(cmd, client, canonical, parentID, delivered); err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "flow subjob watch:", err)
					}
				}
			}
		}
		cancel()
	reconnect:
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func emitSubjobReconcile(cmd *cobra.Command, client subjobmon.Client, planDir, parentID string, delivered map[string]bool) error {
	records, err := subjobmon.Reconcile(cmd.Context(), client, planDir, parentID)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	for _, r := range records {
		key := r.Kind + "/" + r.ChildJobID + "/" + r.ReportSHA256 + "/" + string(r.JobStatus)
		if delivered[key] {
			continue
		}
		delivered[key] = true
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
