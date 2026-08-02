package orchestration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Writing the review packet goes through the `nb` CLI to CREATE the note, for
// the same reason flow's note lifecycle does (see note_link.go): nb owns note
// naming, frontmatter and the event the daemon indexes on. REFRESHING an
// existing packet is a direct file rewrite — the note already exists and is
// indexed, and nb has no verb that can replace a note's body while preserving
// content below the sync marker. That is exactly what nb's own syncer does to
// synced notes (nb/pkg/sync/syncer.go updateNoteFromItemPreserveLocal), so the
// packet follows the established shape rather than inventing one.
//
// DEVIATION from the job brief: the brief describes the snapshot as "a
// versioned packet frontmatter key", written through nb. nb's frontmatter write
// seam (`nb internal update-frontmatter`) accepts a CLOSED field list —
// plan_ref, plan_job, title, repository, branch, worktree
// (nb/pkg/frontmatter/frontmatter.go UpdateField) — so there is no nb verb that
// can set `review_snapshot`. The key is therefore written by editing the note
// file directly, and it survives nb-side rewrites because nb's frontmatter
// parser keeps unmodelled keys in its inline catch-all (Extra) and re-emits
// them verbatim.
//
// PLACEMENT: the note is created with `-t review`, the built-in lifecycle group
// whose docstring is "Notes or PRs ready for review" (nb/pkg/service/
// notetypes.go). No new directory is invented, and — per the plan's D5 —
// nothing here MOVES a note: the packet is created directly in its final home
// and the originating ticket is not touched. (`flow plan finish` later moves
// every plan-linked note, the packet included, to completed/ through the
// pre-existing FinishPlanNotes path; the packet is located by plan_ref, not by
// path, so that move does not strand it.)

// ReviewPacketNotebook is the seam between the packet writer and the notebook.
// The default implementation shells out to `nb`; tests substitute a fake so no
// test can ever write into the owner's real notebook (D7).
type ReviewPacketNotebook interface {
	// ListPlanNotes returns every note joined to the plan by plan_ref.
	ListPlanNotes(planDir, planName string) ([]PlanNote, error)
	// CreateNote creates the packet note and returns its path plus any
	// non-fatal warnings.
	CreateNote(planDir, planName, title, body string) (path string, warnings []string, err error)
	// ReadNote reads a note file.
	ReadNote(path string) ([]byte, error)
	// WriteNote replaces a note file's contents.
	WriteNote(path string, data []byte) error
}

// ReviewPacketRequest is the input to WriteReviewPacket.
type ReviewPacketRequest struct {
	// PlanDir is the plan directory. Required.
	PlanDir string
	// Plan is the loaded plan. Optional — loaded from PlanDir when nil.
	Plan *Plan
	// ContainerPath is the plan's worktree container. Optional — resolved from
	// the plan's worktree name when empty. Callers that already resolved it
	// (plan_finish, which is registry-aware and handles archived worktrees)
	// should pass it.
	ContainerPath string
	// Trigger is ReviewTriggerReview or ReviewTriggerFinish.
	Trigger string
	// Now is the checkpoint timestamp. Zero means time.Now().
	Now time.Time
	// Notebook is the notebook seam. nil means the real `nb` shell-out.
	Notebook ReviewPacketNotebook
}

// ReviewPacketResult reports what the write did.
type ReviewPacketResult struct {
	// Path is the packet note's path. Empty only when nothing was written.
	Path string
	// Created is true when this call created the note.
	Created bool
	// Changed is false when the packet was already byte-identical and no write
	// was performed — the idempotent refresh.
	Changed bool
	// Snapshot is the snapshot the packet now carries.
	Snapshot ReviewSnapshot
	// SnapshotPreserved is true when the live review state was empty and the
	// packet's existing (non-empty) snapshot was kept instead of being
	// replaced. This is what keeps a second finish, or a finish after the
	// tombstone already stripped SessionState, from erasing the checkpoint.
	SnapshotPreserved bool
	// Warnings holds non-fatal problems worth printing.
	Warnings []string
}

// Summary renders a one-line human report of the result.
func (r ReviewPacketResult) Summary() string {
	switch {
	case r.Path == "":
		return "no review packet written"
	case r.Created:
		return fmt.Sprintf("created review packet %s (%d file mark(s), %d checklist(s))",
			r.Path, len(r.Snapshot.Files), len(r.Snapshot.Checklists))
	case !r.Changed:
		return fmt.Sprintf("review packet %s already up to date", r.Path)
	case r.SnapshotPreserved:
		return fmt.Sprintf("refreshed review packet %s (kept the existing review snapshot: no live review state to checkpoint)", r.Path)
	default:
		return fmt.Sprintf("refreshed review packet %s (%d file mark(s), %d checklist(s))",
			r.Path, len(r.Snapshot.Files), len(r.Snapshot.Checklists))
	}
}

// WriteReviewPacket creates or refreshes the plan's review packet note and
// checkpoints the live review state into its frontmatter. It is the single
// write path shared by `flow plan review` and the finish checkpoint item.
//
// It is idempotent by construction: when the rendered packet and the snapshot
// are unchanged, the file is not written at all (byte-identical, no mtime
// churn, no sync noise). Content below the sync marker is never touched.
//
// It performs no network I/O: local reads plus `nb` (which is local too), so it
// is safe on the offline finish path (D3).
func WriteReviewPacket(req ReviewPacketRequest) (ReviewPacketResult, error) {
	var result ReviewPacketResult

	if req.PlanDir == "" {
		return result, fmt.Errorf("review packet: plan directory is required")
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = ReviewTriggerReview
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	nb := req.Notebook
	if nb == nil {
		nb = newReviewPacketNotebook()
	}

	plan := req.Plan
	if plan == nil {
		loaded, err := LoadPlan(req.PlanDir)
		if err != nil {
			return result, fmt.Errorf("review packet: loading plan: %w", err)
		}
		plan = loaded
	}
	planName := filepath.Base(req.PlanDir)
	title := ReviewPacketTitle(planName)

	container := req.ContainerPath
	if container == "" {
		container = resolveReviewPacketContainer(plan, req.PlanDir)
	}

	// The notebook query answers two questions at once: where the packet
	// already lives, and which notes are joined to this plan. A failure here is
	// fatal — proceeding blind would create a SECOND packet for the plan — with
	// one exception: a machine with no `nb` at all has no notebook to write to,
	// which is a degraded environment rather than a failed write. That mirrors
	// how the finish ledger item reports "N/A (nb not found)" instead of
	// failing the finish.
	notes, err := nb.ListPlanNotes(req.PlanDir, planName)
	if err != nil {
		if errors.Is(err, ErrNotebookUnavailable) {
			result.Warnings = append(result.Warnings, "no review packet written: "+err.Error())
			return result, nil
		}
		return result, fmt.Errorf("review packet: querying the plan's notes: %w", err)
	}

	existingPath, existingRaw, err := findReviewPacketNote(nb, notes, title)
	if err != nil {
		return result, err
	}

	var (
		existingFM   string
		existingBody string
		stored       ReviewSnapshot
		hasStored    bool
	)
	if existingPath != "" {
		fmBlock, body, ok := splitNoteFrontmatter(existingRaw)
		if !ok {
			return result, fmt.Errorf("review packet %s: no YAML frontmatter; refusing to rewrite it", existingPath)
		}
		existingFM, existingBody = fmBlock, body
		stored, hasStored, err = parseStoredReviewSnapshot(fmBlock)
		if err != nil {
			return result, fmt.Errorf("review packet %s: %w", existingPath, err)
		}
	}

	packet := CollectReviewPacket(plan, req.PlanDir, container, trigger, now,
		reviewPacketTickets(notes, existingPath, title), "")

	// Never replace evidence with the absence of evidence. SessionState is
	// stripped by the tombstone and is empty on any later run, so an empty
	// snapshot means "nothing to checkpoint", never "the review was undone".
	if hasStored && packet.Snapshot.IsEmpty() && !stored.IsEmpty() {
		packet.Snapshot = stored
		result.SnapshotPreserved = true
	}
	result.Snapshot = packet.Snapshot

	machine := RenderReviewPacket(packet)

	if existingPath == "" {
		body := composeReviewPacketBody(machine, "")
		path, warnings, err := nb.CreateNote(req.PlanDir, planName, title, body)
		result.Warnings = append(result.Warnings, warnings...)
		if err != nil {
			return result, fmt.Errorf("review packet: creating the note: %w", err)
		}
		if path == "" {
			// The note exists (nb exited 0) but we cannot address it, so the
			// snapshot — the entire point of the checkpoint — cannot be
			// written. That is a failure, not a warning.
			return result, fmt.Errorf("review packet: nb did not report the created note's path; the review snapshot was not checkpointed")
		}
		result.Path = path
		result.Created = true
		result.Changed = true

		raw, err := nb.ReadNote(path)
		if err != nil {
			return result, fmt.Errorf("review packet %s: reading back the created note: %w", path, err)
		}
		fmBlock, _, ok := splitNoteFrontmatter(string(raw))
		if !ok {
			return result, fmt.Errorf("review packet %s: created note has no YAML frontmatter", path)
		}
		// The body is re-emitted in canonical form rather than kept as nb
		// wrote it: nb's own spacing between frontmatter and body would
		// otherwise differ from what the refresh path produces, making the
		// FIRST refresh after creation a spurious write.
		content := assembleNote(setFrontmatterKey(fmBlock, ReviewSnapshotKey, renderSnapshotBlock(packet.Snapshot)), body)
		if err := nb.WriteNote(path, []byte(content)); err != nil {
			return result, fmt.Errorf("review packet %s: writing the review snapshot: %w", path, err)
		}
		return result, nil
	}

	result.Path = existingPath

	local, ok := reviewPacketLocalContent(existingBody)
	if !ok {
		// The marker is how the human half is told apart from the machine
		// half. Without it a refresh would either duplicate the generated
		// sections or delete somebody's writing; refuse instead.
		return result, fmt.Errorf("review packet %s: sync marker %s is missing; refusing to rewrite it",
			existingPath, reviewPacketSyncMarker)
	}
	newBody := composeReviewPacketBody(machine, local)

	// Compare against the packet as it would look with the STORED timestamp:
	// the only field that changes on an otherwise identical refresh is
	// checkpointed_at, and letting it force a write would make every refresh
	// dirty the note (and its sync).
	probe := packet.Snapshot
	if hasStored {
		probe.CheckpointedAt = stored.CheckpointedAt
	}
	candidate := assembleNote(setFrontmatterKey(existingFM, ReviewSnapshotKey, renderSnapshotBlock(probe)), newBody)
	if candidate == existingRaw {
		result.Snapshot = probe
		return result, nil
	}

	final := packet.Snapshot
	if result.SnapshotPreserved {
		// A preserved snapshot keeps its own checkpoint time: this run did not
		// observe that state, it merely declined to destroy it.
		final.CheckpointedAt = stored.CheckpointedAt
	}
	result.Snapshot = final
	content := assembleNote(setFrontmatterKey(existingFM, ReviewSnapshotKey, renderSnapshotBlock(final)), newBody)
	if err := nb.WriteNote(existingPath, []byte(content)); err != nil {
		return result, fmt.Errorf("review packet %s: writing the note: %w", existingPath, err)
	}
	result.Changed = true
	return result, nil
}

// resolveReviewPacketContainer resolves the plan's worktree container path. An
// unresolvable worktree is not an error: the packet still records scope,
// disposition (as unavailable) and an explicitly empty snapshot.
func resolveReviewPacketContainer(plan *Plan, planDir string) string {
	if plan == nil || plan.Config == nil || plan.Config.Worktree == "" {
		return ""
	}
	gitRoot, err := GetProjectGitRoot(planDir)
	if err != nil {
		return ""
	}
	_, worktreePath, exists := resolveWorktreeForJob(gitRoot, plan.Config.Worktree)
	if !exists {
		return ""
	}
	return worktreePath
}

// findReviewPacketNote locates the plan's existing packet among its notes. It
// matches on the packet's marker comment (authoritative, filename- and
// title-independent), preferring notes that also carry the packet title so a
// deterministic winner emerges if a duplicate was ever created.
func findReviewPacketNote(nb ReviewPacketNotebook, notes []PlanNote, title string) (path, raw string, err error) {
	ordered := append([]PlanNote(nil), notes...)
	titled := func(n PlanNote) bool {
		// nb's `title` is the FILENAME; the note's real title is
		// FrontmatterTitle (see PlanNote). Both are accepted so the preference
		// holds however the note was named.
		return n.FrontmatterTitle == title || n.Title == title
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if titled(ordered[i]) != titled(ordered[j]) {
			return titled(ordered[i])
		}
		return ordered[i].Path < ordered[j].Path
	})
	for _, note := range ordered {
		if note.Path == "" {
			continue
		}
		data, readErr := nb.ReadNote(note.Path)
		if readErr != nil {
			// A note nb listed but we cannot read is not evidence that the
			// packet is absent, and creating a second packet is worse than
			// failing.
			if os.IsNotExist(readErr) {
				continue
			}
			return "", "", fmt.Errorf("review packet: reading %s: %w", note.Path, readErr)
		}
		if strings.Contains(string(data), reviewPacketMarker) {
			return note.Path, string(data), nil
		}
	}
	return "", "", nil
}

// reviewPacketTickets is the plan's linked notes minus the packet itself.
func reviewPacketTickets(notes []PlanNote, packetPath, title string) []PlanNote {
	out := make([]PlanNote, 0, len(notes))
	for _, note := range notes {
		if note.Path != "" && note.Path == packetPath {
			continue
		}
		if note.FrontmatterTitle == title || note.Title == title {
			continue
		}
		out = append(out, note)
	}
	return out
}

// composeReviewPacketBody joins the machine-owned half, the sync marker, and
// whatever was below it. local is passed through verbatim, including its
// leading newline — this is the "preserve what a human wrote" contract.
//
// The result is CANONICAL: the same inputs always produce the same bytes,
// including the blank line that separates the frontmatter from the body. Both
// the create path and the refresh path go through it, which is what lets an
// unchanged refresh compare byte-identical instead of re-normalizing whitespace
// on the first refresh after creation.
func composeReviewPacketBody(machine, local string) string {
	return "\n" + strings.TrimRight(machine, "\n") + "\n\n" + reviewPacketSyncMarker + local
}

// reviewPacketLocalContent returns everything below the sync marker. ok is
// false when the marker is absent.
func reviewPacketLocalContent(body string) (string, bool) {
	idx := strings.Index(body, reviewPacketSyncMarker)
	if idx < 0 {
		return "", false
	}
	return body[idx+len(reviewPacketSyncMarker):], true
}

// splitNoteFrontmatter splits a note into its YAML frontmatter block (without
// the --- delimiters, keeping the trailing newline of the last key) and the
// body that follows. ok is false when the note has no frontmatter.
func splitNoteFrontmatter(raw string) (fm, body string, ok bool) {
	const open = "---\n"
	if !strings.HasPrefix(raw, open) {
		return "", raw, false
	}
	rest := raw[len(open):]
	if strings.HasPrefix(rest, "---\n") { // empty frontmatter
		return "", rest[len("---\n"):], true
	}
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return "", raw, false
	}
	return rest[:idx+1], rest[idx+len("\n---\n"):], true
}

// assembleNote is splitNoteFrontmatter's inverse.
func assembleNote(fm, body string) string {
	return "---\n" + fm + "---\n" + body
}

// setFrontmatterKey replaces (or appends) a single top-level frontmatter key
// and its indented block. Everything else in the frontmatter is preserved
// byte-for-byte: this is surgery on one key, not a re-serialization of the
// note's frontmatter — flow does not model nb's schema and must not rewrite
// keys it does not understand.
func setFrontmatterKey(fm, key, block string) string {
	var out strings.Builder
	lines := strings.SplitAfter(fm, "\n")
	skipping := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		trimmed := strings.TrimRight(line, "\n")
		isTopLevel := trimmed != "" && !strings.HasPrefix(trimmed, " ") && !strings.HasPrefix(trimmed, "\t") && !strings.HasPrefix(trimmed, "-")
		if skipping {
			if !isTopLevel {
				continue // still inside the old block
			}
			skipping = false
		}
		if isTopLevel && (trimmed == key+":" || strings.HasPrefix(trimmed, key+": ")) {
			skipping = true
			continue
		}
		out.WriteString(line)
	}
	rendered := out.String()
	if rendered != "" && !strings.HasSuffix(rendered, "\n") {
		rendered += "\n"
	}
	return rendered + block
}

// renderSnapshotBlock renders the snapshot as a top-level frontmatter key. The
// wrapper map produces exactly `<key>:` followed by the indented struct, and
// struct field order makes the output deterministic.
func renderSnapshotBlock(snap ReviewSnapshot) string {
	data, err := yaml.Marshal(map[string]ReviewSnapshot{ReviewSnapshotKey: snap})
	if err != nil {
		// yaml.Marshal of this struct cannot fail; degrade to the minimum that
		// still identifies the shape rather than writing nothing.
		return fmt.Sprintf("%s:\n    schema_version: %d\n", ReviewSnapshotKey, ReviewSnapshotSchemaVersion)
	}
	return string(data)
}

// parseStoredReviewSnapshot reads the snapshot already on the note. A key that
// is present but unparseable is an ERROR: overwriting it would destroy a
// checkpoint we could not read, which is the one outcome this whole mechanism
// exists to prevent.
func parseStoredReviewSnapshot(fm string) (ReviewSnapshot, bool, error) {
	var doc struct {
		Snapshot *ReviewSnapshot `yaml:"review_snapshot"`
	}
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
		if frontmatterHasKey(fm, ReviewSnapshotKey) {
			return ReviewSnapshot{}, false, fmt.Errorf("existing %s could not be parsed (%v); refusing to overwrite it", ReviewSnapshotKey, err)
		}
		return ReviewSnapshot{}, false, nil
	}
	if doc.Snapshot == nil {
		if frontmatterHasKey(fm, ReviewSnapshotKey) {
			return ReviewSnapshot{}, false, fmt.Errorf("existing %s could not be decoded; refusing to overwrite it", ReviewSnapshotKey)
		}
		return ReviewSnapshot{}, false, nil
	}
	return *doc.Snapshot, true, nil
}

// frontmatterHasKey reports whether a top-level key is present in the block.
func frontmatterHasKey(fm, key string) bool {
	for _, line := range strings.Split(fm, "\n") {
		if line == key+":" || strings.HasPrefix(line, key+": ") {
			return true
		}
	}
	return false
}

// newReviewPacketNotebook builds the notebook seam used when a caller does not
// supply one — notably the implicit packet write inside MarkPlanReview, which
// has no place to thread a fake through. Tests replace this so a unit test can
// never create a note in the owner's real notebook (D7).
var newReviewPacketNotebook = func() ReviewPacketNotebook { return nbReviewPacketNotebook{} }

// nbReviewPacketNotebook is the real notebook: `nb` for queries and creation,
// the filesystem for refreshes.
type nbReviewPacketNotebook struct{}

// ErrNotebookUnavailable means there is no `nb` on this machine, so there is no
// notebook to write a packet into. Callers treat it as "skipped", not "failed".
var ErrNotebookUnavailable = errors.New("nb not found in PATH")

func (nbReviewPacketNotebook) ListPlanNotes(planDir, planName string) ([]PlanNote, error) {
	if _, err := exec.LookPath("nb"); err != nil {
		return nil, ErrNotebookUnavailable
	}
	out, err := runNbInPlanDir(planDir, nil, "list", "--plan-ref", planRefFor(planName), "--json", "--workspaces")
	if err != nil {
		return nil, err
	}
	return parsePlanNotes(out)
}

func (nbReviewPacketNotebook) CreateNote(planDir, planName, title, body string) (string, []string, error) {
	var warnings []string
	out, err := runNbInPlanDir(planDir, strings.NewReader(body), "new", "--type", "review", "--stdin", "--no-edit", title)
	if err != nil {
		return "", warnings, err
	}
	path := parseNbCreatedNotePath(out)
	if path == "" {
		return "", warnings, nil
	}
	// plan_ref is the notebook↔plan join: it is what makes the packet
	// discoverable — including by the refresh path, which finds the packet by
	// querying nb rather than by remembering a filename.
	if _, err := runNbInPlanDir(planDir, nil, "internal", "update-frontmatter",
		"--path", path, "--field", "plan_ref", "--value", planRefFor(planName)); err != nil {
		warnings = append(warnings, "could not set plan_ref on the review packet: "+err.Error())
	}
	return path, warnings, nil
}

func (nbReviewPacketNotebook) ReadNote(path string) ([]byte, error) { return os.ReadFile(path) }

func (nbReviewPacketNotebook) WriteNote(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// runNbInPlanDir shells nb with the PLAN directory as its working directory, so
// nb's workspace resolution follows the plan rather than the process's cwd (a
// review or finish can be invoked from anywhere). note_link.go's runNb
// deliberately keeps its cwd-relative behavior for the note-lifecycle calls, so
// this is a separate, narrower seam rather than a change to that contract.
func runNbInPlanDir(dir string, stdin *strings.Reader, args ...string) (string, error) {
	cmd := exec.Command("nb", args...) //nolint:gosec // args are internal, not user-shaped
	cmd.Dir = dir
	// NB_NO_EDIT keeps nb from trying to open an editor on a non-interactive
	// run; nb also auto-detects this, and setting it makes that explicit.
	cmd.Env = append(os.Environ(), "NB_NO_EDIT=1")
	if stdin != nil {
		cmd.Stdin = stdin
	}
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

// nbCreatedPathPattern matches the path in nb's "Created: <path>" line.
var nbCreatedPathPattern = regexp.MustCompile(`Created:\s*(\S+\.md)`)

// ansiEscapePattern strips SGR sequences so parsing does not depend on whether
// nb decided its output was a terminal.
var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// parseNbCreatedNotePath extracts the created note's path from nb's output.
func parseNbCreatedNotePath(out string) string {
	plain := ansiEscapePattern.ReplaceAllString(out, "")
	if m := nbCreatedPathPattern.FindStringSubmatch(plain); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}
