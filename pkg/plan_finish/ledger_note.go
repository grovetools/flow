package plan_finish

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Writing the ledger note goes through the `nb` CLI rather than writing a file
// into the notebook directly, for the same reason flow's note lifecycle does
// (see orchestration/note_link.go): nb owns note naming, frontmatter and the
// event the daemon indexes on. A raw file write behind nb's back produces a
// note the notebook does not know about until something rescans.
//
// PLACEMENT: the note is created with `-t completed`, the built-in lifecycle
// group for "completed work and historical notes" (nb/pkg/service/notetypes.go).
// No new directory is invented, and — per the plan's D5 — nothing here MOVES an
// existing note; the ledger is a brand-new note created directly in its final
// home.
//
// DEVIATION from the job brief: the brief asked for a distinguishing "tag/type"
// on the note. nb's frontmatter write seam (`nb internal update-frontmatter`)
// accepts a CLOSED field list — plan_ref, plan_job, title, repository, branch,
// worktree (nb/pkg/frontmatter/frontmatter.go UpdateField) — and `tags`/`type`
// are not in it, nor does `nb new` take a tag flag. Adding one would be a
// change to nb's frontmatter schema, which is outside this job's scope. The
// ledger is instead distinguished by three things that ARE writable: the
// "Plan ledger: <plan>" title (which nb also slugs into the filename), the
// plan_ref join to the plan, and the machine-readable marker comment that
// RenderLedger puts on the note's first line.

// LedgerNoteResult reports where the ledger note landed.
type LedgerNoteResult struct {
	// Path is the created note's path. It can be empty when nb created the
	// note but did not report a parseable path — the note exists either way,
	// so callers must not treat an empty Path as a failure.
	Path string
	// Warnings holds non-fatal problems (frontmatter enrichment that did not
	// take, an unparseable creation line).
	Warnings []string
}

// LedgerNoteWriter creates the ledger note and returns where it landed.
// planDir is used as the working directory so nb resolves the notebook
// workspace the plan belongs to rather than wherever the finish was invoked
// from. Replaced in tests; the default is writeLedgerNoteViaNb.
type LedgerNoteWriter func(planDir, planName, title, body string) (LedgerNoteResult, error)

// nbCreatedPathRe matches the path in nb's "Created: <path>" line.
var nbCreatedPathRe = regexp.MustCompile(`Created:\s*(\S+\.md)`)

// ansiEscapeRe strips SGR sequences so parsing does not depend on whether nb
// decided its output was a terminal.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// writeLedgerNoteViaNb creates the note in the plan's notebook workspace under
// completed/ and links it back to the plan.
func writeLedgerNoteViaNb(planDir, planName, title, body string) (LedgerNoteResult, error) {
	var result LedgerNoteResult

	if _, err := exec.LookPath("nb"); err != nil {
		return result, fmt.Errorf("nb not found in PATH: %w", err)
	}

	out, err := runNbInDir(planDir, strings.NewReader(body), "new", "--type", "completed", "--stdin", "--no-edit", title)
	if err != nil {
		return result, err
	}

	result.Path = parseNbCreatedPath(out)
	if result.Path == "" {
		// The note was created — nb exited 0 — we just cannot say where.
		// Report it instead of failing the finish over a parse.
		result.Warnings = append(result.Warnings,
			"nb did not report the created note's path; the ledger note exists but was not linked to the plan")
		return result, nil
	}

	// plan_ref is the notebook↔plan join: it makes the ledger discoverable via
	// the same `nb list --plan-ref` query every other plan note answers to.
	if _, err := runNbInDir(planDir, nil, "internal", "update-frontmatter",
		"--path", result.Path, "--field", "plan_ref", "--value", "plans/"+planName); err != nil {
		result.Warnings = append(result.Warnings, "could not set plan_ref on the ledger note: "+err.Error())
	}

	return result, nil
}

// runNbInDir shells nb with dir as the working directory, so nb's workspace
// resolution follows the PLAN, not the process's cwd (a finish can be launched
// from anywhere, including a directory it is about to delete).
func runNbInDir(dir string, stdin *strings.Reader, args ...string) (string, error) {
	cmd := exec.Command("nb", args...) //nolint:gosec // args are internal, not user-shaped
	cmd.Dir = dir
	// NB_NO_EDIT keeps nb from trying to open an editor on a non-interactive
	// finish; nb also auto-detects this, and setting it makes that explicit.
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

// parseNbCreatedPath extracts the created note's path from nb's output.
func parseNbCreatedPath(out string) string {
	plain := ansiEscapeRe.ReplaceAllString(out, "")
	if m := nbCreatedPathRe.FindStringSubmatch(plain); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}
