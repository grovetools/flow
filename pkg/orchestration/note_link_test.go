package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNbStub installs a fake `nb` executable first on PATH for the duration of
// the test. Every invocation is appended (one line, space-joined args) to a
// record file whose path is returned. `nb list` prints the JSON in the file at
// $NB_STUB_LIST (or "[]"); `nb move` prints a canned `To:` line; everything else
// (internal update-frontmatter) is a success no-op. This lets unit tests assert
// the exact nb contract flow emits without a real nb binary.
func writeNbStub(t *testing.T) (recordPath string, setListJSON func(json string)) {
	t.Helper()
	binDir := t.TempDir()
	recordPath = filepath.Join(binDir, "invocations.log")
	listPath := filepath.Join(binDir, "list.json")

	script := `#!/bin/sh
printf '%s\n' "$*" >> "$NB_STUB_RECORD"
case "$1" in
  list)
    if [ -n "$NB_STUB_LIST" ] && [ -f "$NB_STUB_LIST" ]; then
      cat "$NB_STUB_LIST"
    else
      echo "[]"
    fi
    ;;
  move)
    path="$2"; group="$3"
    base=$(basename "$path")
    wsroot=$(dirname "$(dirname "$path")")
    echo "Moved $base"
    echo "To: $wsroot/$group/$base"
    ;;
esac
exit 0
`
	nbPath := filepath.Join(binDir, "nb")
	if err := os.WriteFile(nbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing nb stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NB_STUB_RECORD", recordPath)
	t.Setenv("NB_STUB_LIST", listPath)

	setListJSON = func(json string) {
		if err := os.WriteFile(listPath, []byte(json), 0o644); err != nil {
			t.Fatalf("writing list json: %v", err)
		}
	}
	return recordPath, setListJSON
}

func readRecordLines(t *testing.T, recordPath string) []string {
	t.Helper()
	data, err := os.ReadFile(recordPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading record: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func countContaining(lines []string, substr string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

// TestFinishPlanNotes_MovesAllAndReportsAlreadyCompleted verifies that finish's
// native note handling queries all of a plan's notes and moves each to
// completed/, reporting already-completed (not an error, no move) for a note
// that is already there.
func TestFinishPlanNotes_MovesAllAndReportsAlreadyCompleted(t *testing.T) {
	recordPath, setList := writeNbStub(t)
	setList(`[
      {"path":"/nb/ws/in_progress/alpha.md","plan_ref":"plans/myplan","plan_job":"01-alpha.md"},
      {"path":"/nb/ws/in_progress/beta.md","plan_ref":"plans/myplan","plan_job":"02-beta.md"},
      {"path":"/nb/ws/completed/gamma.md","plan_ref":"plans/myplan","plan_job":"03-gamma.md"}
    ]`)

	outcomes, err := FinishPlanNotes("myplan")
	if err != nil {
		t.Fatalf("FinishPlanNotes: %v", err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes, got %d: %+v", len(outcomes), outcomes)
	}

	var moved, already int
	for _, o := range outcomes {
		switch o.State {
		case NoteMoved:
			moved++
		case NoteAlreadyCompleted:
			already++
		default:
			t.Fatalf("unexpected outcome state %q for %s (err=%v)", o.State, o.Path, o.Err)
		}
	}
	if moved != 2 || already != 1 {
		t.Fatalf("expected 2 moved + 1 already-completed, got moved=%d already=%d", moved, already)
	}

	lines := readRecordLines(t, recordPath)
	if got := countContaining(lines, "list --plan-ref plans/myplan --json --workspaces"); got != 1 {
		t.Fatalf("expected 1 list query, got %d (%v)", got, lines)
	}
	// Only the two non-completed notes should be moved.
	if got := countContaining(lines, "move /nb/ws/in_progress/alpha.md completed --force"); got != 1 {
		t.Fatalf("expected alpha move, record: %v", lines)
	}
	if got := countContaining(lines, "move /nb/ws/in_progress/beta.md completed --force"); got != 1 {
		t.Fatalf("expected beta move, record: %v", lines)
	}
	if got := countContaining(lines, "gamma.md completed"); got != 0 {
		t.Fatalf("gamma is already in completed/ and must not be moved, record: %v", lines)
	}
}

// TestJobNote_ResolvesByPlanJob verifies the query-based resolution used by job
// complete: JobNote picks the note whose plan_job matches THIS job, not by
// reading job.NoteRef.
func TestJobNote_ResolvesByPlanJob(t *testing.T) {
	_, setList := writeNbStub(t)
	setList(`[
      {"path":"/nb/ws/in_progress/alpha.md","plan_ref":"plans/myplan","plan_job":"01-alpha.md"},
      {"path":"/nb/ws/in_progress/beta.md","plan_ref":"plans/myplan","plan_job":"02-beta.md"}
    ]`)

	note, err := JobNote("myplan", "02-beta.md")
	if err != nil {
		t.Fatalf("JobNote: %v", err)
	}
	if note == nil {
		t.Fatal("expected a note for 02-beta.md, got nil")
	}
	if note.Path != "/nb/ws/in_progress/beta.md" {
		t.Fatalf("resolved wrong note: %s", note.Path)
	}

	// A job with no linked note resolves to nil (no error).
	none, err := JobNote("myplan", "99-missing.md")
	if err != nil {
		t.Fatalf("JobNote (missing): %v", err)
	}
	if none != nil {
		t.Fatalf("expected nil for unlinked job, got %s", none.Path)
	}
}

// TestJobComplete_MoveNoteToGroupOutcomes covers the outcome contract job
// complete relies on: an in_progress note moves; a completed note is idempotent.
func TestJobComplete_MoveNoteToGroupOutcomes(t *testing.T) {
	recordPath, _ := writeNbStub(t)

	moved := MoveNoteToGroup("/nb/ws/in_progress/alpha.md", "completed")
	if moved.State != NoteMoved {
		t.Fatalf("expected moved, got %q (%v)", moved.State, moved.Err)
	}
	already := MoveNoteToGroup("/nb/ws/completed/gamma.md", "completed")
	if already.State != NoteAlreadyCompleted {
		t.Fatalf("expected already-completed, got %q", already.State)
	}

	lines := readRecordLines(t, recordPath)
	if got := countContaining(lines, "move /nb/ws/in_progress/alpha.md completed --force"); got != 1 {
		t.Fatalf("expected one move invocation, record: %v", lines)
	}
	if got := countContaining(lines, "gamma"); got != 0 {
		t.Fatalf("already-completed note must not be moved, record: %v", lines)
	}
}
