package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/util/sanitize"
	"github.com/sirupsen/logrus"
)

// CompleteJob is the shared function that handles job completion logic.
// It can be called from both the CLI and TUI.
// Set silent=true to suppress output (useful for TUI).
func CompleteJob(job *Job, plan *Plan, silent bool) error {
	// Log entry to trace who is calling completeJob
	logger := grovelogging.NewLogger("flow.complete")
	logger.WithFields(logrus.Fields{
		"job_id":     job.ID,
		"job_title":  job.Title,
		"job_status": job.Status,
		"silent":     silent,
	}).Debug("CompleteJob called - tracing caller")

	// Check current status
	alreadyCompleted := job.Status == JobStatusCompleted
	if alreadyCompleted && !silent {
		fmt.Printf("Job already completed: %s\n", job.Filename)
		// Still need to clean up associated resources (Claude process, tmux window)
	}

	// Every pre-status cleanup step below is best-effort: it kills agent
	// processes and closes terminal windows. None of it may prevent the job's
	// lifecycle state from being persisted, so each runs behind a non-fatal
	// boundary. Without it a single panic — historically a nil mux engine
	// reached through DetectMuxEngine's failure path — killed the process
	// before the status write, stranding a finished job at idle with no way to
	// recover except a rerun.
	//
	// Cleanup still runs before the status update: the agent holds a lock on
	// the job file, so it must be terminated first. Cleanup is attempted even
	// for an already-completed job, since the process may still be alive.
	switch job.Type {
	case JobTypeIsolatedAgent:
		runNonFatalCleanup(logger, "isolated agent", silent, func() { cleanupIsolatedAgent(job, silent) })
	case JobTypeHeadlessAgent:
		runNonFatalCleanup(logger, "headless agent", silent, func() { cleanupHeadlessAgent(job, silent) })
	case JobTypeInteractiveAgent:
		runNonFatalCleanup(logger, "interactive agent", silent, func() { cleanupInteractiveAgent(job, plan, silent) })
	case JobTypeChat:
		// A pi-session chat owns a live Pi process and a pane, launched through
		// the interactive provider lifecycle — so completing it must tear those
		// down through the same path an interactive agent uses. Without this the
		// `flow complete` gate marks the record finished while the session keeps
		// running, holding the job's lock file and answering into a chat nobody
		// is reading. Every other chat responder has no process and skips this.
		if job.IsPiSessionResponded() {
			runNonFatalCleanup(logger, "pi session", silent, func() {
				cleanupInteractiveAgent(job, plan, silent)
				finalizePiSessionArtifacts(job, plan, silent)
			})
		}
	}

	// Frontmatter status is the authoritative success state. Before moving an
	// agent job to completed, require current-attempt execution evidence. This
	// turns a provider outage that produced zero turns into failed instead of a
	// false completed result. Already-completed jobs retain the recovery-only
	// behavior below for compatibility.
	if !alreadyCompleted && isAgentJobType(job.Type) {
		if evidenceErr := successfulExecutionEvidence(job, plan); evidenceErr != nil {
			// Rejection means "this completion cannot be verified", which is
			// not "the work failed". Marking the job failed here made merely
			// attempting to complete an unverifiable job destructive: the
			// attempt itself stamped status: failed and completed_at on a job
			// whose agent had run to the end. The job's status is left exactly
			// as it was; only the error is returned.
			return unverifiableCompletionError(job, plan, evidenceErr)
		}
	}

	// Update status (skip if already completed)
	oldStatus := job.Status
	if !alreadyCompleted {
		job.Status = JobStatusCompleted

		// Use the state persister to update the job file
		// (UpdateJobStatus will fire notify_on_complete if configured)
		persister := NewStatePersister()
		if err := persister.UpdateJobStatus(job, JobStatusCompleted); err != nil {
			return fmt.Errorf("update job status: %w", err)
		}

		// Notify the daemon that the session has ended. This MUST target the
		// same daemon the provider registered the session with — the host UI's
		// — not whatever GROVE_SCOPE resolves to in the completing process.
		// `flow plan complete` usually runs somewhere else entirely (a parent
		// coordinator's flow_subjob join, the status TUI), and a scope-resolved
		// client sends "completed" to the worktree's groved while the live
		// record sits on the host's, so the rail keeps rendering a finished
		// agent as running.
		daemonClient := sessionHostClientForJob(job, plan)
		if err := daemonClient.EndSession(context.Background(), job.ID, "completed"); err != nil {
			logger.WithError(err).Debug("Failed to notify daemon of session end")
		}
		daemonClient.Close()
	}

	// Finalize the per-repo commit record (commits.json sidecar). Idempotent:
	// once a prior finalize (e.g. FinalizeHeadlessJob) stamped finished_at, a
	// late repeat completion leaves the record untouched instead of
	// misattributing commits made after the job.
	if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent {
		if err := FinalizeJobCommits(job, plan); err != nil {
			logger.WithError(err).Debug("Failed to finalize job commit record")
		}
	}

	// Archive session artifacts for agent jobs — runs even when already
	// completed so a late/repeat `flow plan complete` recovers artifacts the
	// first completion missed (e.g. a headless job whose deferred status
	// update landed before CompleteJob ran). Both archive functions are
	// idempotent overwrites into the same destination dirs.
	if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent || job.Type == JobTypeIsolatedAgent {
		if !silent {
			fmt.Println("Archiving session artifacts...")
		}
		if err := ArchiveInteractiveSession(job, plan); err != nil {
			// Log a warning but don't fail the entire completion process.
			if !silent {
				fmt.Printf("Warning: failed to archive session artifacts: %v\n", err)
			}
		} else if !silent {
			fmt.Println(color.GreenString("*") + " Session artifacts archived.")
		}
		if err := ArchiveWorkflowRuns(job, plan); err != nil {
			// Log a warning but don't fail the entire completion process.
			if !silent {
				fmt.Printf("Warning: failed to archive workflow runs: %v\n", err)
			}
		}
		if err := ArchiveStandaloneSubagents(job, plan); err != nil {
			// Log a warning but don't fail the entire completion process.
			if !silent {
				fmt.Printf("Warning: failed to archive standalone subagents: %v\n", err)
			}
		}
		// Summarize per-job token usage + cost into
		// .artifacts/<job>/token-usage.json and a "## Token Usage" section.
		// ArchiveTokenUsage warns-and-continues internally, so it never
		// returns an error that could fail completion.
		if err := ArchiveTokenUsage(job, plan); err != nil {
			if !silent {
				fmt.Printf("Warning: failed to archive token usage: %v\n", err)
			}
		}
	}

	// Capture the partial run record. Placed after the archival block above so
	// token-usage.json is already on disk for the Cost mapping to read back.
	// Attached unconditionally, for every job type: this single site covers all
	// three agent families (both the `flow plan complete` route and the
	// FinalizeHeadlessJob success route) and pending_user chats completed via
	// `flow complete`, which never re-enter executeChatJob.
	writeMetricsRecordQuietly(job, plan)

	// Append transcript for agent jobs — runs even when already completed so
	// `flow plan complete` recovers the transcript if the agent was killed
	// before the first completion call landed. AppendAgentTranscript compares
	// existing vs new content and skips if unchanged.
	if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent || job.Type == JobTypeIsolatedAgent {
		if !silent {
			fmt.Println("Appending agent session transcript...")
		}
		if err := AppendAgentTranscript(job, plan); err != nil {
			if !silent {
				fmt.Printf("Warning: failed to append transcript: %v\n", err)
			}
		} else if !silent {
			fmt.Println(color.GreenString("*") + " Appended session transcript.")
		}
		// Snapshot only after transcript insertion so final-report.md is the
		// complete, fetchable operator record.
		if err := ArchiveFinalReport(job, plan); err != nil && !silent {
			fmt.Printf("Warning: failed to archive final report: %v\n", err)
		}
	}

	// Persist the transcript outline (toc.ansi / toc.txt) into the artifact
	// dir. After the transcript block so the archived transcript is on disk as
	// a fallback source. Attached for every job type: jobs without a
	// resolvable transcript (oneshots, plain chats) skip silently inside.
	writeTranscriptTocQuietly(job, plan)

	// Move this job's linked note to completed/. The note is resolved by
	// QUERYING nb (plan_ref + plan_job), never by reading job.NoteRef — which is
	// now a non-load-bearing provenance hint. A non-empty note_ref is used only
	// as a cheap "this job had a note" signal to avoid an nb query for ordinary
	// jobs. Resolution failures are reported, never silently skipped.
	if job.NoteRef != "" && !alreadyCompleted {
		note, err := JobNote(plan.Name, job.Filename)
		switch {
		case err != nil:
			if !silent {
				fmt.Printf("Warning: could not query nb for linked note of %s: %v\n", job.Filename, err)
			}
		case note == nil:
			if !silent {
				fmt.Printf("Warning: no note resolved via nb for job %s (plan_ref=plans/%s, plan_job=%s); leaving note unmoved\n", job.Filename, plan.Name, job.Filename)
			}
		default:
			outcome := MoveNoteToGroup(note.Path, "completed")
			if !silent {
				switch outcome.State {
				case NoteMoved:
					fmt.Printf("%s Moved linked note to completed/: %s\n", color.GreenString("*"), outcome.Path)
				case NoteAlreadyCompleted:
					fmt.Printf("%s Linked note already in completed/: %s\n", color.GreenString("*"), outcome.Path)
				case NoteFailed:
					fmt.Printf("Warning: failed to move linked note %s to completed/: %v\n", outcome.Path, outcome.Err)
				}
			}
		}
	}

	// Success message
	if !silent {
		if !alreadyCompleted {
			fmt.Printf("%s Job completed: %s\n", color.GreenString("*"), job.Title)
			fmt.Printf("Status: %s → %s\n", oldStatus, JobStatusCompleted)
		} else {
			fmt.Printf("%s Cleaned up resources for: %s\n", color.GreenString("*"), job.Title)
		}

		// Special message for chat jobs
		if job.Type == JobTypeChat && !alreadyCompleted {
			fmt.Printf("\nChat conversation ended. You can transform this chat into executable jobs using:\n")
			fmt.Printf("  flow plan add %s --template generate-plan --prompt-file %s\n",
				plan.Directory, job.Filename)
		}
	}

	return nil
}

// unverifiableCompletionError explains a rejected completion in terms of what
// is missing and what to do about it. A bare "session binding unverified"
// names an internal record the user has never heard of and offers no way
// forward, so the message states which index was consulted, whether the job's
// own transcript is on disk, and the command that recovers it.
func unverifiableCompletionError(job *Job, plan *Plan, evidenceErr error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "agent completion rejected: %v", evidenceErr)
	b.WriteString("\n  job status left unchanged (this is a verification failure, not a failed run)")
	fmt.Fprintf(&b, "\n  session registry: %s", filepath.Join(paths.StateDir(), "hooks", "sessions"))

	if plan != nil && job != nil {
		artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
		if transcript := ArtifactTranscriptForAttempt(job, plan); transcript != "" {
			fmt.Fprintf(&b, "\n  transcript on disk: %s", transcript)
		} else {
			fmt.Fprintf(&b, "\n  no transcript for this attempt under %s", filepath.Join(artifactDir, "sessions"))
		}
		fmt.Fprintf(&b, "\n  inspect it with: aglogs read %s/%s", plan.Name, job.Filename)
	}
	return fmt.Errorf("%s", b.String())
}

// runNonFatalCleanup runs one best-effort resource-cleanup step, converting a
// panic inside it into a warning. Terminal and process cleanup is never
// load-bearing for correctness, so it must not be able to abort the caller
// before durable job state is written.
func runNonFatalCleanup(logger *logrus.Entry, name string, silent bool, fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		logger.WithFields(logrus.Fields{
			"cleanup": name,
			"panic":   fmt.Sprint(r),
			"stack":   string(debug.Stack()),
		}).Warn("job cleanup panicked; continuing with completion")
		if !silent {
			fmt.Printf("  Note: %s cleanup failed (%v); continuing with completion\n", name, r)
		}
	}()
	fn()
}

// removeStaleLockFile gives a just-signalled agent a moment to exit, then drops
// the lock file it held on the job.
func removeStaleLockFile(job *Job, silent bool) {
	time.Sleep(200 * time.Millisecond)
	if err := RemoveLockFile(job.FilePath); err != nil {
		if !silent {
			fmt.Printf("  Note: could not remove lock file: %v\n", err)
		}
	}
}

// cleanupIsolatedAgent tears down an isolated agent's dedicated tmux server and
// agent process.
func cleanupIsolatedAgent(job *Job, silent bool) {
	if !silent {
		fmt.Println("Cleaning up isolated agent tmux server...")
	}

	if err := KillIsolatedAgentServer(job.ID); err != nil {
		if !silent {
			fmt.Printf("  Note: could not kill isolated tmux server (it may already be closed): %v\n", err)
		}
	} else if !silent {
		fmt.Println("  * Isolated tmux server terminated.")
	}

	// Also try to kill the agent process via session metadata
	if err := killAgentSession(job.ID); err != nil {
		if !silent {
			fmt.Printf("  Note: could not kill agent session: %v\n", err)
		}
	} else if !silent {
		fmt.Println("  * Agent process killed.")
	}

	removeStaleLockFile(job, silent)
}

// cleanupHeadlessAgent kills the headless agent process. Without this, pressing
// 'c' in the TUI leaves the agent running.
func cleanupHeadlessAgent(job *Job, silent bool) {
	if !silent {
		fmt.Println("Cleaning up headless agent process...")
	}

	if err := killAgentSession(job.ID); err != nil {
		if !silent {
			fmt.Printf("  Note: could not kill agent session: %v\n", err)
		}
	} else if !silent {
		fmt.Println("  * Agent process killed.")
	}

	removeStaleLockFile(job, silent)
}

// cleanupInteractiveAgent kills the interactive agent process and closes the
// terminal window it ran in.
func cleanupInteractiveAgent(job *Job, plan *Plan, silent bool) {
	if !silent {
		fmt.Println("Cleaning up interactive agent session...")
	}

	// Kill the agent process by reading the PID from grove-hooks session metadata
	if err := killAgentSession(job.ID); err != nil {
		if !silent {
			fmt.Printf("  Note: could not kill agent session: %v\n", err)
		}
	} else if !silent {
		fmt.Println("  * Agent process killed.")
	}

	// Also kill the tmux window for the interactive_agent job
	worktreePath, err := getWorktreePathFromSession(job.ID)

	// If we can't find the session (e.g., resumed job), fall back to using the job's worktree
	if err != nil && job.Worktree != "" {
		gitRoot, gitErr := GetProjectGitRoot(plan.Directory)
		if gitErr == nil {
			if workspace.IsNotebookRepo(gitRoot) {
				gitRoot = ""
				gitErr = fmt.Errorf("skipping notebook repo")
			}
		}
		if gitErr == nil {
			gitRootInfo, gitRootErr := workspace.GetProjectByPath(gitRoot)
			if gitRootErr == nil && gitRootInfo.IsWorktree() && gitRootInfo.ParentProjectPath != "" {
				gitRoot = gitRootInfo.ParentProjectPath
			}
			if found, ok := workspace.FindWorktreePath(gitRoot, job.Worktree); ok {
				worktreePath = found
			} else {
				worktreePath = workspace.ResolveNewWorktreePath(gitRoot, job.Worktree, false)
			}
			err = nil
		}
	}

	if err != nil {
		if !silent {
			fmt.Printf("  Note: could not determine working directory: %v\n", err)
		}
	} else {
		projInfo, err := ResolveProjectForSessionNaming(worktreePath)
		if err != nil {
			if !silent {
				fmt.Printf("  Note: could not get project info to determine session name: %v\n", err)
			}
		} else {
			sessionName := projInfo.Identifier("_")
			windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
			closeAgentWindow(sessionName, windowName, silent)
		}
	}

	removeStaleLockFile(job, silent)
}

// closeAgentWindow closes the terminal window a job ran in, falling back to a
// prefix match over the session's windows when the exact name misses.
//
// Detection failure short-circuits the whole thing: the error path yields no
// usable engine, and calling into one anyway is what turned a missing tuimux
// daemon into a nil-receiver panic inside job completion. An engine is only
// touched when detection succeeded and returned a live one. Detection is also
// the non-starting variant — closing a window is not a reason to bring up a
// multiplexer that is not already running.
func closeAgentWindow(sessionName, windowName string, silent bool) {
	targetWindow := fmt.Sprintf("%s:%s", sessionName, windowName)
	if !silent {
		fmt.Printf("  Closing tmux window: %s\n", targetWindow)
	}

	engine, engineErr := mux.DetectExistingMuxEngine(context.Background())
	if engineErr != nil || mux.IsNilEngine(engine) {
		if !silent {
			reason := engineErr
			if reason == nil {
				reason = fmt.Errorf("no mux engine available")
			}
			fmt.Printf("  Note: no terminal multiplexer available to close window: %v\n", reason)
		}
		return
	}

	err := engine.KillWindow(context.Background(), targetWindow)
	if err != nil {
		windows, listErr := engine.ListWindows(context.Background(), sessionName)
		if listErr == nil {
			for _, win := range windows {
				if !strings.HasPrefix(win.Name, windowName) {
					continue
				}
				targetWindow = fmt.Sprintf("%s:%s", sessionName, win.Name)
				if !silent {
					fmt.Printf("  Found window with prefix: %s\n", targetWindow)
				}
				if killErr := engine.KillWindow(context.Background(), targetWindow); killErr == nil {
					if !silent {
						fmt.Println("  * Window closed.")
					}
					err = nil
					break
				}
			}
		}
	}

	if err != nil {
		if !silent {
			fmt.Printf("  Note: could not close tmux window (it may already be closed): %v\n", err)
		}
	} else if !silent {
		fmt.Println("  * Tmux window closed.")
	}
}

// killAgentSession kills the agent process associated with the given job ID
// by reading the PID from grove-hooks session metadata.
func killAgentSession(jobID string) error {
	pid, _, err := findAgentSessionInfo(jobID)
	if err != nil {
		return err // The error from findAgentSessionInfo is already descriptive
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	// Send SIGTERM to gracefully terminate
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead, which is fine
		if !strings.Contains(err.Error(), "process already finished") {
			return fmt.Errorf("kill process: %w", err)
		}
	}

	return nil
}
