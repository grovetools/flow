package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	coremodels "github.com/grovetools/core/pkg/models"
	coreslug "github.com/grovetools/core/pkg/slug"
)

// This file is flow's single seam for the inverted note↔plan linkage. NOTE
// frontmatter is the source of truth: `plan_ref: plans/<planName>` plus
// `plan_job: <jobFile>.md`. Flow no longer stores resolvable note pointers —
// it QUERIES nb. Every flow consumer of note lifecycle (plan_init, demote,
// finish, job complete) goes through the functions here so the shell-out
// contract with nb lives in exactly one place.

// PlanNote is a note linked to a plan, as reported by `nb list --plan-ref`.
// Field names mirror the nb Note model's JSON (see nb/pkg/models/note.go).
type PlanNote struct {
	Path      string `json:"path"`
	PlanRef   string `json:"plan_ref"`
	PlanJob   string `json:"plan_job"`
	Workspace string `json:"workspace"`
	// Title is the note's FILENAME, not its frontmatter title — that is what
	// nb's Note model puts in `title`. Callers identifying a note by the name
	// it was created under want FrontmatterTitle.
	Title string `json:"title"`
	// FrontmatterTitle is the `title:` from the note's frontmatter. It is the
	// stable identity of a GENERATED note: the filename carries a date prefix
	// and a slug, but the frontmatter title is exactly the string the
	// generator passed to `nb new`, so a generator can find its own note again
	// without guessing at slugging rules.
	FrontmatterTitle string `json:"frontmatter_title"`
	Type             string `json:"type"`
}

// NoteMoveState is the result of attempting to move a single note through the
// notebook lifecycle. Reported by finish/complete so no note is ever silently
// skipped.
type NoteMoveState string

const (
	// NoteMoved: the note was relocated to the target group.
	NoteMoved NoteMoveState = "moved"
	// NoteAlreadyCompleted: the note already resided in the target group;
	// treated as success (idempotent), not an error.
	NoteAlreadyCompleted NoteMoveState = "already-completed"
	// NoteFailed: the move failed; Err carries the reason.
	NoteFailed NoteMoveState = "failed"
)

// NoteOutcome is a per-note result used by finish/complete reporting.
type NoteOutcome struct {
	Path  string
	State NoteMoveState
	Err   error
}

// String renders a NoteOutcome for console reporting, e.g.
// "moved", "already-completed", or "failed: <err>".
func (o NoteOutcome) String() string {
	if o.State == NoteFailed && o.Err != nil {
		return fmt.Sprintf("failed: %v", o.Err)
	}
	return string(o.State)
}

// planRefFor renders the frontmatter plan_ref value for a plan name.
func planRefFor(planName string) string {
	return "plans/" + planName
}

// NoteLinkedToPlan is the shared association predicate for flow readers.
// Tags and worktree names are deliberately ignored: plan_ref is the sole link.
func NoteLinkedToPlan(notePlanRef, planName string) bool {
	return coremodels.NoteLinkedToPlan(notePlanRef, planName)
}

// runNb shells the `nb` binary (resolved via PATH) with the given args,
// returning stdout. stderr is folded into the returned error for diagnostics.
func runNb(args ...string) (string, error) {
	cmd := exec.Command("nb", args...) //nolint:gosec // args are internal, not user-shaped
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return stdout.String(), fmt.Errorf("nb %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return stdout.String(), fmt.Errorf("nb %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// PlanNotes returns every note whose plan_ref frontmatter exactly matches
// plans/<planName>, across all workspaces. It is the authoritative query for
// "give me this plan's notes"; plan_job is guaranteed populated on the results.
func PlanNotes(planName string) ([]PlanNote, error) {
	out, err := runNb("list", "--plan-ref", planRefFor(planName), "--json", "--workspaces")
	if err != nil {
		return nil, err
	}
	return parsePlanNotes(out)
}

// CreatePlanNote delegates note creation and plan resolution/validation to nb.
// When jobFile is non-empty, flow adds the per-job link through the same
// frontmatter seam used by promotion and demotion.
func CreatePlanNote(title, planName, jobFile string) (*PlanNote, error) {
	out, err := runNb("new", title, "--type", "inbox", "--no-edit", "--plan", planName)
	if err != nil {
		return nil, err
	}
	path := parseCreatedNotePath(out)
	if path == "" {
		return nil, fmt.Errorf("nb created the note but did not report its path")
	}
	if jobFile != "" {
		if err := SetNoteLink(path, planName, jobFile); err != nil {
			return nil, fmt.Errorf("linking created note to job %s: %w", jobFile, err)
		}
	}
	return &PlanNote{Path: path, PlanRef: planRefFor(planName), PlanJob: jobFile}, nil
}

func parseCreatedNotePath(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Created:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// parsePlanNotes decodes the JSON array `nb list --json` prints. An empty list
// is printed as "[]"; anything that is not a JSON array (stray log lines from
// an old binary, etc.) yields a parse error the caller surfaces loudly.
func parsePlanNotes(out string) ([]PlanNote, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	// Be tolerant of a leading non-JSON preamble by seeking the first array.
	if i := strings.IndexByte(trimmed, '['); i > 0 {
		trimmed = trimmed[i:]
	}
	var notes []PlanNote
	if err := json.Unmarshal([]byte(trimmed), &notes); err != nil {
		return nil, fmt.Errorf("parsing nb list --json output: %w", err)
	}
	return notes, nil
}

// JobNote resolves the single note linked to a specific plan job by filtering
// PlanNotes on plan_job. Returns nil (no error) when no note is linked.
func JobNote(planName, jobFile string) (*PlanNote, error) {
	notes, err := PlanNotes(planName)
	if err != nil {
		return nil, err
	}
	for i := range notes {
		if notes[i].PlanJob == jobFile {
			return &notes[i], nil
		}
	}
	return nil, nil
}

// MoveNote moves a note into a lifecycle group (inbox, in_progress, review,
// completed) via `nb move`, keeping daemon indexing intact. It returns the
// note's new path parsed from the `To: <newpath>` line nb prints; if that line
// is absent the input path is returned so callers still have a usable path.
func MoveNote(path, group string) (string, error) {
	out, err := runNb("move", path, group, "--force")
	if err != nil {
		return "", err
	}
	if newPath := parseMoveDest(out); newPath != "" {
		return newPath, nil
	}
	return path, nil
}

// MoveNoteToWorkspace moves a note into another workspace's lifecycle group
// directory, i.e. <workspaceDir>/<group>/<basename>. It uses `nb move`'s
// explicit DESTINATION PATH form (`nb move <src> <destdir>`, see nb/cmd/move.go
// moveToPath) rather than the note-type form, because the note-type form always
// resolves the group relative to the note's OWN workspace and has no way to
// express "somewhere else".
//
// This keeps cross-workspace relocation inside the same nb shell-out seam as
// every other note move, so the daemon still observes a typed move event
// instead of a raw rename behind its back.
//
// workspaceDir must be an absolute path. The returned path is the note's new
// location.
func MoveNoteToWorkspace(path, workspaceDir, group string) (string, error) {
	if !filepath.IsAbs(workspaceDir) {
		return "", fmt.Errorf("workspace dir must be absolute, got %q", workspaceDir)
	}
	destDir := filepath.Join(workspaceDir, group)
	expected := filepath.Join(destDir, filepath.Base(path))

	// Pass the full destination FILE path, not destDir. `nb move` only appends
	// the source basename when the destination already exists as a directory
	// (os.Stat + IsDir); for a not-yet-created group dir it would treat the
	// argument as a literal filename and rename the note INTO a file named
	// "inbox". The full path is correct either way, since nb MkdirAll's the
	// destination's parent.
	out, err := runNb("move", path, expected, "--force")
	if err != nil {
		return "", err
	}
	if newPath := parseMoveDest(out); newPath != "" {
		return newPath, nil
	}
	return expected, nil
}

// parseMoveDest extracts the destination path from `nb move` output. The
// note-type form prints a "To: <newpath>" line; the destination-path form
// prints "Moved successfully: <src> -> <dst>". Both are recognized so callers
// always learn the real landing path when nb reports one.
func parseMoveDest(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "To:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "To:"))
		}
		if rest, ok := strings.CutPrefix(line, "Moved successfully:"); ok {
			if _, dst, found := strings.Cut(rest, "->"); found {
				return strings.TrimSpace(dst)
			}
		}
	}
	return ""
}

// MoveNoteToGroup moves a note to the target lifecycle group and reports a
// per-note outcome. A note already residing in the target group is reported as
// already-completed (idempotent), never an error — this makes finish/complete
// note handling safe to run repeatedly and safe against legacy frozen hooks
// that redundantly attempt the same move.
func MoveNoteToGroup(path, group string) NoteOutcome {
	if noteInGroup(path, group) {
		return NoteOutcome{Path: path, State: NoteAlreadyCompleted}
	}
	newPath, err := MoveNote(path, group)
	if err != nil {
		return NoteOutcome{Path: path, State: NoteFailed, Err: err}
	}
	return NoteOutcome{Path: newPath, State: NoteMoved}
}

// noteInGroup reports whether a note's path already lives directly under the
// named lifecycle group directory.
func noteInGroup(path, group string) bool {
	return filepath.Base(filepath.Dir(path)) == group
}

// SetNoteLink writes the note↔plan linkage frontmatter (plan_ref + plan_job)
// on the note at path via `nb internal update-frontmatter`.
func SetNoteLink(path, planName, jobFile string) error {
	if _, err := runNb("internal", "update-frontmatter", "--path", path, "--field", "plan_ref", "--value", planRefFor(planName)); err != nil {
		return err
	}
	if _, err := runNb("internal", "update-frontmatter", "--path", path, "--field", "plan_job", "--value", jobFile); err != nil {
		return err
	}
	return nil
}

// ClearNoteLink clears both plan_ref and plan_job frontmatter fields on the
// note at path (via `--value ""`, nb's clear seam).
func ClearNoteLink(path string) error {
	if _, err := runNb("internal", "update-frontmatter", "--path", path, "--field", "plan_ref", "--value", ""); err != nil {
		return err
	}
	if _, err := runNb("internal", "update-frontmatter", "--path", path, "--field", "plan_job", "--value", ""); err != nil {
		return err
	}
	return nil
}

// Demotion is the provenance a demoted note carries away from its plan: which
// plan and job it came from, when it was parked, and (optionally) why. A note
// that comes back to the inbox with no trace of where it was is exactly the
// complaint this record answers — "which plan was this from again?".
type Demotion struct {
	PlanName       string
	JobFile        string
	OriginalNoteID string
	At             time.Time
	Reason         string
}

// OriginalNoteFilename reconstructs nb's original date-prefixed filename from
// the stable note id stored as job provenance. IDs have the form
// YYYYMMDD-HHMMSS-<slug>; note filenames use YYYYMMDD-<slug>. Invalid or
// legacy path-shaped references return an empty string.
func (d Demotion) OriginalNoteFilename() string {
	id := d.OriginalNoteID
	if len(id) < 17 || id[8] != '-' || id[15] != '-' {
		return ""
	}
	for i := 0; i < 8; i++ {
		if id[i] < '0' || id[i] > '9' {
			return ""
		}
	}
	for i := 9; i < 15; i++ {
		if id[i] < '0' || id[i] > '9' {
			return ""
		}
	}
	rawSlug := strings.TrimSpace(id[16:])
	if rawSlug == "" || filepath.Base(rawSlug) != rawSlug {
		return ""
	}
	slug := coreslug.Canonical(rawSlug)
	if slug == "" {
		return ""
	}
	return id[:8] + "-" + slug + ".md"
}

// Trailer renders the human-readable provenance block appended to the note
// body. It is deliberately plain markdown: the note is read in an editor and
// in nb's preview far more often than it is parsed.
func (d Demotion) Trailer() string {
	var sb strings.Builder
	sb.WriteString("\n---\n\n")
	fmt.Fprintf(&sb, "_Demoted from plan `%s`", d.PlanName)
	if d.JobFile != "" {
		fmt.Fprintf(&sb, " (job `%s`)", d.JobFile)
	}
	if !d.At.IsZero() {
		fmt.Fprintf(&sb, " on %s", d.At.Format("2006-01-02 15:04"))
	}
	sb.WriteString("._\n")
	if d.Reason != "" {
		fmt.Fprintf(&sb, "\n_Reason: %s_\n", d.Reason)
	}
	return sb.String()
}

// ClearNoteLinkWithProvenance unlinks a note from its plan AND stamps where it
// came from, in ONE `nb internal update-frontmatter` invocation: plan_ref and
// plan_job are cleared while demoted_from / demoted_job / demoted_at (and
// demote_reason, when given) are set. Batching matters — demote is already
// several nb shell-outs deep, and a per-field call would double that for every
// job in a bulk park.
//
// The frontmatter record is the machine-readable half; AppendNoteBody writes
// the human-readable trailer.
func ClearNoteLinkWithProvenance(path string, d Demotion) error {
	at := d.At
	if at.IsZero() {
		at = time.Now()
	}
	args := []string{
		"internal", "update-frontmatter", "--path", path,
		"--set", "plan_ref=",
		"--set", "plan_job=",
		"--set", "demoted_from=" + planRefFor(d.PlanName),
		"--set", "demoted_at=" + at.Format(time.RFC3339),
	}
	if d.JobFile != "" {
		args = append(args, "--set", "demoted_job="+d.JobFile)
	}
	if d.Reason != "" {
		args = append(args, "--set", "demote_reason="+d.Reason)
	} else {
		// A re-demote without a reason must not inherit the previous one.
		args = append(args, "--set", "demote_reason=")
	}
	_, err := runNb(args...)
	return err
}

// AppendNoteBody appends content to a note's body through nb's own append seam
// (`nb internal update-note`), so the daemon still sees a typed update event
// rather than a write behind its back.
func AppendNoteBody(path, content string) error {
	if content == "" {
		return nil
	}
	_, err := runNb("internal", "update-note", "--path", path, "--append-content", content)
	return err
}

// FinishPlanNotes queries every note linked to the plan and moves each to
// completed/, returning per-note outcomes. It is the native replacement for the
// generated on_finish nb-move hook. Notes already in completed/ report
// already-completed rather than erroring, so the operation is idempotent.
func FinishPlanNotes(planName string) ([]NoteOutcome, error) {
	notes, err := PlanNotes(planName)
	if err != nil {
		return nil, err
	}
	outcomes := make([]NoteOutcome, 0, len(notes))
	for _, n := range notes {
		outcomes = append(outcomes, MoveNoteToGroup(n.Path, "completed"))
	}
	return outcomes, nil
}
