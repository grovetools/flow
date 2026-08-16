package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/flow/pkg/orchestration"
)

var (
	demoteWorkspaceFlag string
	demoteReasonFlag    string
	demoteStatusFlag    string
	demoteDryRunFlag    bool
	demoteJSONFlag      bool
)

// defaultDemoteStatuses are the job statuses a plan-directory argument selects
// when `--status` is not given: the work that has not started and is not already parked. It is
// deliberately narrow — bulk demote is for "save these for later", not for
// abandoning running or completed work.
var defaultDemoteStatuses = []orchestration.JobStatus{
	orchestration.JobStatusPending,
	orchestration.JobStatusPendingUser,
	orchestration.JobStatusPendingLLM,
	orchestration.JobStatusTodo,
	orchestration.JobStatusBlocked,
	orchestration.JobStatusHold,
}

// allDemotableStatuses is what `--status all` means: everything that is not
// finished (completed/abandoned) and not live (running). A running job is
// deliberately excluded — parking one would abandon work an agent is doing
// right now, which no "save these for later" sweep intends.
var allDemotableStatuses = append([]orchestration.JobStatus{
	orchestration.JobStatusFailed,
	orchestration.JobStatusNeedsReview,
	orchestration.JobStatusIdle,
	orchestration.JobStatusInterrupted,
	orchestration.JobStatusOrphaned,
}, defaultDemoteStatuses...)

func init() {
	planDemoteCmd.Flags().StringVar(&demoteWorkspaceFlag, "workspace", "", "Override target workspace for the demoted note")
	planDemoteCmd.Flags().StringVar(&demoteReasonFlag, "reason", "", "Why the job is being parked; recorded on the note")
	planDemoteCmd.Flags().StringVar(&demoteStatusFlag, "status", "", "Comma-separated job statuses to select when a PLAN DIRECTORY is given (default: pending,pending_user,pending_llm,todo,blocked,hold; 'all' selects every unfinished job)")
	planDemoteCmd.Flags().BoolVar(&demoteDryRunFlag, "dry-run", false, "Print what would be demoted without moving notes or touching jobs")
	planDemoteCmd.Flags().BoolVar(&demoteJSONFlag, "json", false, "Emit the demotion results as JSON")
}

var planDemoteCmd = &cobra.Command{
	Use:   "demote <job-file|plan-dir>...",
	Short: "Demote plan jobs back to nb inbox notes",
	Long: `Move flow plan jobs back to the nb inbox as notes.

For each job, the linked note (found by querying nb for the plan's notes) is
moved back to inbox/ and stamped with where it came from: demoted_from,
demoted_job, demoted_at, and demote_reason frontmatter plus a provenance
trailer in the body. When no linked note exists, one is created via nb new.

Each job's status is set to "abandoned" after demotion.

An argument that is a PLAN DIRECTORY expands to every job in it matching
--status — the "save the pending jobs for later" pass. A plan's notes are
queried ONCE for the whole batch rather than once per job, so parking a dozen
jobs costs one cross-workspace nb query, not twelve.

Output: one demoted note path per line on stdout (so the single-job form stays
pipe-friendly), with a human summary on stderr. --json emits a structured
result array instead.

Examples:
  flow plan demote plans/my-plan/03-stale-task.md
  flow plan demote plans/my-plan/03-stale.md plans/my-plan/04-later.md --reason "waiting on upstream"
  flow plan demote plans/my-plan --dry-run
  flow plan demote plans/my-plan --reason "parking until Q3" --json
  flow plan demote plans/my-plan --status failed,blocked
  flow plan demote job.md --workspace /path/to/workspace`,
	Args: cobra.ArbitraryArgs,
	RunE: runPlanDemote,
}

// demoteOutcome is one job's demotion result, and the shape of --json output.
type demoteOutcome struct {
	Job     string `json:"job"`
	JobFile string `json:"job_file"`
	Plan    string `json:"plan"`
	Status  string `json:"status_before,omitempty"`
	Note    string `json:"note,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Created bool   `json:"note_created,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
	Error   string `json:"error,omitempty"`
}

func runPlanDemote(cmd *cobra.Command, args []string) error {
	jobPaths, err := resolveDemoteTargets(args)
	if err != nil {
		return err
	}
	if len(jobPaths) == 0 {
		fmt.Fprintln(os.Stderr, "No jobs matched — nothing to demote.")
		if demoteJSONFlag {
			return emitDemoteJSON(nil)
		}
		return nil
	}

	// Query each plan's notes ONCE for the whole batch. The nb query walks
	// every workspace, so doing it per job is what made parking a dozen jobs
	// feel like it had hung.
	noteIndex := newPlanNoteIndex()

	outcomes := make([]demoteOutcome, 0, len(jobPaths))
	var firstErr error
	for _, jobPath := range jobPaths {
		out := demoteOneJob(jobPath, noteIndex)
		outcomes = append(outcomes, out)
		if out.Error != "" && firstErr == nil {
			firstErr = fmt.Errorf("%s: %s", filepath.Base(jobPath), out.Error)
		}
	}

	if demoteJSONFlag {
		if err := emitDemoteJSON(outcomes); err != nil {
			return err
		}
		return firstErr
	}

	reportDemoteOutcomes(outcomes)
	return firstErr
}

// resolveDemoteTargets turns the command's arguments into an ordered list of
// absolute job-file paths. A file argument is one job; a DIRECTORY argument is
// a plan, and expands to its jobs whose status matches --status. Overlapping
// arguments (a plan plus one of its jobs) are de-duplicated, so a job is never
// demoted twice in one run.
//
// The plan-directory form deliberately avoids a --plan flag: flow reserves
// --plan as a deprecated alias of the global --at target, so a new flag by
// that name would warn on every use.
func resolveDemoteTargets(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("name at least one job file, or a plan directory to select its jobs in bulk")
	}

	wanted, err := parseDemoteStatuses(demoteStatusFlag)
	if err != nil {
		return nil, err
	}

	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	for _, arg := range args {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, fmt.Errorf("resolving path %s: %w", arg, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", arg, err)
		}
		if !info.IsDir() {
			add(abs)
			continue
		}

		plan, err := orchestration.LoadPlan(abs)
		if err != nil {
			return nil, fmt.Errorf("loading plan %s: %w", abs, err)
		}
		for _, job := range plan.Jobs {
			if wanted[job.Status] {
				add(filepath.Join(abs, job.Filename))
			}
		}
	}
	return paths, nil
}

// parseDemoteStatuses resolves the --status flag into a status set. An empty
// flag yields defaultDemoteStatuses; "all" yields every status except the
// terminal ones (completed/abandoned), which demote has nothing to say about.
func parseDemoteStatuses(flag string) (map[orchestration.JobStatus]bool, error) {
	wanted := map[orchestration.JobStatus]bool{}
	trimmed := strings.TrimSpace(flag)
	if trimmed == "" {
		for _, s := range defaultDemoteStatuses {
			wanted[s] = true
		}
		return wanted, nil
	}
	if strings.EqualFold(trimmed, "all") {
		for _, s := range allDemotableStatuses {
			wanted[s] = true
		}
		return wanted, nil
	}
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		wanted[orchestration.JobStatus(part)] = true
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("--status listed no statuses")
	}
	return wanted, nil
}

// planNoteIndex memoizes `nb list --plan-ref` per plan so a bulk demote pays
// the cross-workspace note query once per plan instead of once per job.
type planNoteIndex struct {
	byPlan map[string]map[string]*orchestration.PlanNote
}

func newPlanNoteIndex() *planNoteIndex {
	return &planNoteIndex{byPlan: map[string]map[string]*orchestration.PlanNote{}}
}

// noteFor returns the note linked to (planName, jobFile), or nil when nb knows
// of none. A failed query is reported once per plan and then treated as "no
// linked notes", which falls the caller through to the nb new path.
func (idx *planNoteIndex) noteFor(planName, jobFile string) *orchestration.PlanNote {
	jobs, ok := idx.byPlan[planName]
	if !ok {
		jobs = map[string]*orchestration.PlanNote{}
		notes, err := orchestration.PlanNotes(planName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: nb query for plan %s notes failed: %v\n", planName, err)
		}
		for i := range notes {
			if notes[i].PlanJob != "" {
				jobs[notes[i].PlanJob] = &notes[i]
			}
		}
		idx.byPlan[planName] = jobs
	}
	return jobs[jobFile]
}

// demoteOneJob performs (or, under --dry-run, describes) a single job's
// demotion. Errors are captured in the outcome rather than returned so a bulk
// run reports every job instead of stopping at the first failure.
func demoteOneJob(jobFilePath string, idx *planNoteIndex) demoteOutcome {
	planName := filepath.Base(filepath.Dir(jobFilePath))
	out := demoteOutcome{
		JobFile: filepath.Base(jobFilePath),
		Plan:    planName,
		Reason:  demoteReasonFlag,
		DryRun:  demoteDryRunFlag,
	}

	job, err := orchestration.LoadJob(jobFilePath)
	if err != nil {
		out.Error = fmt.Sprintf("loading job file: %v", err)
		return out
	}
	job.FilePath = jobFilePath
	job.Filename = filepath.Base(jobFilePath)
	out.Job = job.Title
	out.Status = string(job.Status)

	// The note is resolved by QUERYING nb for the plan's notes and filtering on
	// plan_job — note frontmatter (plan_ref/plan_job) is the source of truth, not
	// the job's note_ref (which is now a non-load-bearing provenance hint).
	notePath := ""
	if note := idx.noteFor(planName, job.Filename); note != nil {
		notePath = note.Path
	}

	// Fallback: only when the query finds nothing, honor a path-shaped legacy
	// note_ref that still stats clean (older job files stored absolute note
	// paths). Say so in the output so the resolution path is not silent.
	if notePath == "" && job.NoteRef != "" && filepath.IsAbs(job.NoteRef) {
		if _, statErr := os.Stat(job.NoteRef); statErr == nil {
			fmt.Fprintln(os.Stderr, "Note: nb query found no linked note; falling back to legacy note_ref path.")
			notePath = job.NoteRef
		}
	}

	demotion := orchestration.Demotion{
		PlanName:       planName,
		JobFile:        job.Filename,
		OriginalNoteID: job.NoteRef,
		At:             time.Now(),
		Reason:         demoteReasonFlag,
	}

	if demoteDryRunFlag {
		if notePath != "" {
			out.Note = notePath
		} else {
			out.Created = true
		}
		return out
	}

	if notePath != "" {
		newPath, err := demoteResolvedNote(notePath, job, demotion)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		out.Note = newPath
		return out
	}

	// Last resort: no resolvable note anywhere — recreate one via nb new.
	newPath, err := demoteViaNbNew(job, jobFilePath, demotion)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Note = newPath
	out.Created = true
	return out
}

// demoteResolvedNote moves an already-resolved note back to inbox/ via nb,
// swaps its plan_ref/plan_job link for demote provenance, appends the
// provenance trailer, and marks the job abandoned. It returns the note's new
// path.
//
// When --workspace is set the note is routed to THAT workspace's inbox/ rather
// than its own, using nb move's explicit destination-path form. Without the
// flag the behavior is unchanged: a plain group move within the note's own
// workspace.
func demoteResolvedNote(notePath string, job *orchestration.Job, demotion orchestration.Demotion) (string, error) {
	workspaceOverride, err := resolveWorkspaceOverride()
	if err != nil {
		return "", err
	}

	var newPath string
	if workspaceOverride != "" {
		newPath, err = orchestration.MoveNoteToWorkspace(notePath, workspaceOverride, "inbox")
	} else {
		newPath, err = orchestration.MoveNote(notePath, "inbox")
	}
	if err != nil {
		return "", fmt.Errorf("moving note back to inbox: %w", err)
	}

	// Swap the note↔plan link for a record of where the note came from, in one
	// nb call. Provenance failures are warnings: the note IS back in the inbox
	// at this point, and losing the stamp is not worth failing the demote.
	if err := orchestration.ClearNoteLinkWithProvenance(newPath, demotion); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not record demote provenance on %s: %v\n", newPath, err)
	}
	if err := orchestration.AppendNoteBody(newPath, demotion.Trailer()); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not append demote trailer to %s: %v\n", newPath, err)
	}

	// Mark the job as abandoned
	sp := orchestration.NewStatePersister()
	if err := sp.UpdateJobStatus(job, orchestration.JobStatusAbandoned); err != nil {
		return newPath, fmt.Errorf("updating job status to abandoned: %w", err)
	}

	return newPath, nil
}

// demoteViaNbNew creates a new note via nb new (fallback when no linked note
// exists). The provenance trailer is part of the created body, so the note
// records where it came from even on this path.
func demoteViaNbNew(job *orchestration.Job, jobFilePath string, demotion orchestration.Demotion) (string, error) {
	// Determine the target workspace directory. --workspace wins here exactly as
	// it does on the resolved path; both routes share resolveWorkspaceOverride
	// so the flag can never be honored by one and ignored by the other.
	targetWorkspaceDir, err := resolveWorkspaceOverride()
	if err != nil {
		return "", err
	}
	if targetWorkspaceDir == "" {
		targetWorkspaceDir, err = resolveTargetNotespace(jobFilePath)
		if err != nil {
			return "", err
		}
	}

	// Preserve the original note's date-prefixed filename when note_ref carries
	// nb's stable note id. A legacy/path-shaped reference cannot safely recover
	// a filename, so it keeps nb's generated-name fallback.
	nbArgs := demoteNbNewArgs(job.Title, demotion)
	nbCmd := exec.Command("nb", nbArgs...)
	nbCmd.Dir = targetWorkspaceDir

	// Pipe the job's prompt body (plus provenance) to stdin
	body := strings.TrimRight(job.PromptBody, "\n")
	if body != "" {
		body += "\n"
	}
	nbCmd.Stdin = strings.NewReader(body + demotion.Trailer())

	// Capture stdout for the new note path
	nbCmd.Stderr = os.Stderr
	output, err := nbCmd.Output()
	if err != nil {
		return "", fmt.Errorf("creating note via nb new: %w", err)
	}

	// Parse the note path from nb output (nb logs "Created: <path>")
	notePath := parseNotePathFromOutput(string(output))

	// Stamp the machine-readable provenance too, so a recreated note is
	// queryable exactly like a moved one.
	if notePath != "" {
		if err := orchestration.ClearNoteLinkWithProvenance(notePath, demotion); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not record demote provenance on %s: %v\n", notePath, err)
		}
	}

	// Update the job status to abandoned
	sp := orchestration.NewStatePersister()
	if err := sp.UpdateJobStatus(job, orchestration.JobStatusAbandoned); err != nil {
		return notePath, fmt.Errorf("updating job status to abandoned: %w", err)
	}

	return notePath, nil
}

func demoteNbNewArgs(title string, demotion orchestration.Demotion) []string {
	args := []string{"new", title, "--type", "inbox", "--no-edit"}
	if filename := demotion.OriginalNoteFilename(); filename != "" {
		args = append(args, "--filename", filename)
	}
	return args
}

// reportDemoteOutcomes writes the human report: note paths on stdout (one per
// line, so `flow plan demote <job>` still prints exactly the new note path and
// nothing else), and the narrative on stderr.
func reportDemoteOutcomes(outcomes []demoteOutcome) {
	for _, out := range outcomes {
		if out.Note != "" {
			fmt.Println(out.Note)
		} else if out.Error == "" && !out.DryRun {
			fmt.Println("Job demoted to note (note path not captured)")
		}
	}

	verb := "Demoted"
	if outcomes[0].DryRun {
		verb = "Would demote"
	}
	demoted := 0
	for _, out := range outcomes {
		switch {
		case out.Error != "":
			fmt.Fprintf(os.Stderr, "  ✗ %s: %s\n", out.JobFile, out.Error)
		default:
			demoted++
			dest := out.Note
			if dest == "" {
				dest = "a new inbox note"
			}
			created := ""
			if out.Created {
				created = " (new note)"
			}
			fmt.Fprintf(os.Stderr, "  %s %s → %s%s\n", verb, out.JobFile, dest, created)
		}
	}
	if len(outcomes) > 1 {
		fmt.Fprintf(os.Stderr, "%s %d of %d job(s) from plan %s\n", verb, demoted, len(outcomes), outcomes[0].Plan)
	}
	if demoteReasonFlag != "" && demoted > 0 {
		fmt.Fprintf(os.Stderr, "Reason recorded: %s\n", demoteReasonFlag)
	}
}

func emitDemoteJSON(outcomes []demoteOutcome) error {
	if outcomes == nil {
		outcomes = []demoteOutcome{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(outcomes)
}

// resolveWorkspaceOverride resolves the code-plane --workspace path through
// the recorded primary mapping. The resulting notespace root, not the code
// workspace path or its basename, is the nb working directory.
func resolveWorkspaceOverride() (string, error) {
	if demoteWorkspaceFlag == "" {
		return "", nil
	}
	cfg, machine, err := loadNotespaceRouting()
	if err != nil {
		return "", err
	}
	return resolveWorkspaceOverrideWithRouting(demoteWorkspaceFlag, cfg, machine)
}

func resolveWorkspaceOverrideWithRouting(path string, cfg *config.Config, machine *config.MachineConfig) (string, error) {
	resolution, err := workspace.ResolveNotespace(path, cfg, machine)
	if err != nil {
		return "", fmt.Errorf("resolving --workspace notespace: %w", err)
	}
	return resolution.Root, nil
}

// resolveTargetNotespace derives note creation's destination from the plan's
// own notes-plane location. There is deliberately no note_ref or positional
// fallback: stale provenance must never redirect a write into a sibling root.
func resolveTargetNotespace(jobFilePath string) (string, error) {
	cfg, _, err := loadNotespaceRouting()
	if err != nil {
		return "", err
	}
	return resolveTargetNotespaceWithConfig(jobFilePath, cfg)
}

func resolveTargetNotespaceWithConfig(jobFilePath string, cfg *config.Config) (string, error) {
	_, _, root, err := notespaceAtNotesPath(jobFilePath, cfg)
	if err != nil {
		return "", fmt.Errorf("resolve demote target notespace: %w", err)
	}
	return root, nil
}

func loadNotespaceRouting() (*config.Config, *config.MachineConfig, error) {
	cfg, err := config.LoadDefault()
	if err != nil {
		return nil, nil, fmt.Errorf("load notespace configuration: %w", err)
	}
	machine, err := config.LoadMachineConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load machine notespace routing: %w", err)
	}
	return cfg, machine, nil
}

// parseNotePathFromOutput extracts the note file path from nb new output.
// nb new outputs lines like "Created: /path/to/note.md"
func parseNotePathFromOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Created: ") {
			return strings.TrimPrefix(line, "Created: ")
		}
		// Also handle if the path is just printed directly
		if strings.HasSuffix(line, ".md") && filepath.IsAbs(line) {
			return line
		}
	}
	return ""
}
