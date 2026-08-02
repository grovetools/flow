package review_packet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Writing the packet goes through the `nb` CLI rather than writing a file into
// the notebook directly, for the reason flow's whole note lifecycle does (see
// orchestration/note_link.go and plan_finish/ledger_note.go): nb owns note
// naming, frontmatter and the event the daemon indexes on. A raw write behind
// nb's back produces a note the notebook does not know about until something
// rescans.
//
// PLACEMENT: the packet is created with `-t review`, nb's built-in lifecycle
// group whose docstring already reads "Notes or PRs ready for review". No
// directory is invented and — per D5 — nothing here MOVES a note. The packet is
// a brand-new note created directly in its final home; the originating ticket
// is linked, never relocated.
//
// IDENTITY: the packet finds itself again by frontmatter title. `nb list
// --plan-ref` reports both the filename and the frontmatter title, and only the
// latter is exactly the string the generator passed to `nb new` — the filename
// carries a date prefix and a slug this package must not try to reproduce.

// syncMarker is nb's machine/human ownership boundary (nb/pkg/notedoc.Marker).
// flow and nb are separate modules, so the literal is repeated here rather than
// imported; it is frozen history in every note nb has ever synced, and the
// rewrite seam on the nb side would reject a mismatch by simply not finding it.
const syncMarker = "<!-- nb-sync-marker -->"

// Outcome reports what a refresh did.
type Outcome struct {
	// Path is the packet note. Empty only when creation succeeded but nb did
	// not report a parseable path.
	Path string
	// Created is true when this refresh brought the note into existence.
	Created bool
	// Changed is false when the note was already byte-identical to what this
	// refresh would have written.
	Changed bool
	// Warnings holds non-fatal problems (frontmatter enrichment that did not
	// take, an unparseable creation line, more than one candidate note).
	Warnings []string
}

// Store is the notebook seam. Tests substitute their own so no test can write
// into the owner's real notebook (D7).
type Store struct {
	// RunNb shells the nb binary with dir as the working directory.
	RunNb func(dir string, stdin io.Reader, args ...string) (string, error)
	// ReadFile reads a note from disk.
	ReadFile func(path string) ([]byte, error)
}

// DefaultStore is the real notebook: the nb binary and the filesystem.
func DefaultStore() Store {
	return Store{RunNb: runNbInDir, ReadFile: os.ReadFile}
}

// Refresh generates or refreshes the plan's review packet note.
//
// It is idempotent in the strongest sense: when nothing about the plan's review
// state, scope or disposition has changed, the note file is not written at all,
// so it stays byte-identical (unchanged mtime, no note event, nothing for the
// notebook syncer to replicate). That is achieved by re-rendering the candidate
// with the timestamp the EXISTING note carries — if the only difference would
// have been "now", there is no difference.
//
// planDir is the working directory for every nb call, so nb resolves the
// notebook workspace the PLAN belongs to rather than wherever the caller was
// invoked from.
func Refresh(store Store, planDir string, p Packet) (Outcome, error) {
	var out Outcome
	if store.RunNb == nil || store.ReadFile == nil {
		return out, fmt.Errorf("review packet: store is not configured")
	}

	path, created, warnings, err := findOrCreate(store, planDir, p)
	out.Warnings = append(out.Warnings, warnings...)
	out.Created = created
	out.Path = path
	if err != nil {
		return out, err
	}
	if path == "" {
		// The note exists but we cannot address it, so there is nothing to
		// refresh into. Reported, not fatal: a packet with only its creation
		// content beats failing the review (or the finish).
		return out, nil
	}

	content, err := store.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("read review packet %s: %w", path, err)
	}

	if unchanged(string(content), p) {
		return out, nil
	}

	payload, err := buildPayload(p)
	if err != nil {
		return out, err
	}
	if _, err := store.RunNb(planDir, bytes.NewReader(payload), "internal", "rewrite-note", "--path", path); err != nil {
		return out, fmt.Errorf("rewrite review packet %s: %w", path, err)
	}
	out.Changed = true
	return out, nil
}

// findOrCreate locates the plan's packet note, creating it when absent. The new
// note's body is nothing but the sync marker: `nb new` writes its own template
// above whatever it is given, and that template is machine boilerplate the
// first refresh is meant to replace. Starting at the marker means the very
// first refresh follows exactly the same code path as every later one.
func findOrCreate(store Store, planDir string, p Packet) (path string, created bool, warnings []string, err error) {
	notes, listErr := planNotes(p.Plan)
	if listErr != nil {
		return "", false, nil, fmt.Errorf("list plan notes: %w", listErr)
	}

	title := Title(p.Plan)
	var matches []string
	for _, n := range notes {
		if n.FrontmatterTitle == title {
			matches = append(matches, n.Path)
		}
	}
	if len(matches) > 0 {
		// Deterministic pick, so a duplicate cannot make successive refreshes
		// ping-pong between two notes. Duplicates are reported rather than
		// merged: this wave is single-writer, and silently choosing which of
		// two humans' packets to overwrite is exactly the data loss the
		// concurrency model is deferred to solve properly.
		sort.Strings(matches)
		if len(matches) > 1 {
			warnings = append(warnings, fmt.Sprintf(
				"%d notes claim to be this plan's review packet; refreshing %s and leaving the others alone",
				len(matches), matches[0]))
		}
		return matches[0], false, warnings, nil
	}

	out, err := store.RunNb(planDir, strings.NewReader(syncMarker+"\n"),
		"new", "--type", "review", "--stdin", "--no-edit", title)
	if err != nil {
		return "", false, warnings, fmt.Errorf("create review packet note: %w", err)
	}

	path = parseNbCreatedPath(out)
	if path == "" {
		// The note was created — nb exited 0 — we just cannot say where.
		warnings = append(warnings,
			"nb did not report the created note's path; the review packet exists but was not linked or filled in")
		return "", true, warnings, nil
	}

	// plan_ref is the notebook↔plan join: it makes the packet discoverable
	// through the same `nb list --plan-ref` query every other plan note answers
	// to, and it is how the NEXT refresh finds this note again.
	if _, err := store.RunNb(planDir, nil, "internal", "update-frontmatter",
		"--path", path, "--field", "plan_ref", "--value", p.PlanRef); err != nil {
		warnings = append(warnings, "could not set plan_ref on the review packet: "+err.Error())
	}
	return path, true, warnings, nil
}

// unchanged reports whether the note already says exactly what this refresh
// would say. The comparison replays the render with the note's OWN checkpoint
// timestamp, so a refresh that would differ only in "when" counts as unchanged.
func unchanged(content string, p Packet) bool {
	fmBlock, body, ok := splitFrontmatter(content)
	if !ok {
		return false
	}

	var existing map[string]any
	if err := yaml.Unmarshal([]byte(fmBlock), &existing); err != nil {
		return false
	}
	existingSnap, present := existing[SnapshotKey]
	if !present {
		return false
	}
	prevStamp := existingStamp(existingSnap)
	if prevStamp == "" {
		return false
	}

	replay := p
	replay.Snapshot.CheckpointedAt = prevStamp
	if Render(replay) != aboveMarker(body) {
		return false
	}
	return sameSnapshot(replay.Snapshot, existingSnap)
}

// existingStamp pulls checkpointed_at out of a parsed snapshot value.
func existingStamp(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m["checkpointed_at"].(string)
	return s
}

// sameSnapshot compares a candidate snapshot with the one already on the note.
// Both sides are normalized through a JSON round-trip so a YAML integer and a
// JSON number compare equal — otherwise every refresh would look different for
// purely representational reasons.
func sameSnapshot(candidate Snapshot, existing any) bool {
	left, err := canonical(candidate)
	if err != nil {
		return false
	}
	right, err := canonical(existing)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func canonical(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// buildPayload renders the stdin document for `nb internal rewrite-note`.
func buildPayload(p Packet) ([]byte, error) {
	snapshot, err := canonical(p.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode review snapshot: %w", err)
	}
	return json.Marshal(struct {
		BodyAboveMarker string         `json:"body_above_marker"`
		Frontmatter     map[string]any `json:"frontmatter"`
	}{
		BodyAboveMarker: Render(p),
		Frontmatter:     map[string]any{SnapshotKey: snapshot},
	})
}

var frontmatterRe = regexp.MustCompile(`(?s)^---\n(.*?)\n---\n(.*)`)

// splitFrontmatter separates a note's YAML frontmatter block from its body,
// mirroring nb's own parse.
func splitFrontmatter(content string) (fm, body string, ok bool) {
	m := frontmatterRe.FindStringSubmatch(content)
	if len(m) != 3 {
		return "", content, false
	}
	return m[1], m[2], true
}

// aboveMarker returns the machine-owned region of a note body, normalized to
// how Render emits it (trailing newline, nothing more).
func aboveMarker(body string) string {
	i := strings.Index(body, syncMarker)
	if i < 0 {
		return ""
	}
	return strings.TrimLeft(strings.TrimRight(body[:i], "\n")+"\n", "\n")
}

// nbCreatedPathRe matches the path in nb's "Created: <path>" line.
var nbCreatedPathRe = regexp.MustCompile(`Created:\s*(\S+\.md)`)

// ansiEscapeRe strips SGR sequences so parsing does not depend on whether nb
// decided its output was a terminal.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func parseNbCreatedPath(out string) string {
	plain := ansiEscapeRe.ReplaceAllString(out, "")
	if m := nbCreatedPathRe.FindStringSubmatch(plain); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// runNbInDir shells nb with dir as the working directory, so nb's workspace
// resolution follows the PLAN, not the process's cwd.
func runNbInDir(dir string, stdin io.Reader, args ...string) (string, error) {
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

// Available reports whether the notebook CLI this package needs is present.
// Callers use it to report a packet step as unavailable rather than failing:
// nothing in this wave may require a tool the machine does not have.
func Available() bool {
	_, err := exec.LookPath("nb")
	return err == nil
}
