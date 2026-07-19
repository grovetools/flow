package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/flow/pkg/orchestration"
)

// installNbStub installs a fake `nb` executable first on PATH. Invocations are
// recorded (one space-joined line each) to the returned record path. `nb list`
// prints $NB_STUB_LIST (or "[]"), `nb move` prints a canned `To:` line, and
// `internal update-frontmatter` is a success no-op.
func installNbStub(t *testing.T) (recordPath string, setListJSON func(string)) {
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
	if err := os.WriteFile(filepath.Join(binDir, "nb"), []byte(script), 0o755); err != nil {
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

func stubRecordLines(t *testing.T, recordPath string) []string {
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

func countLines(lines []string, substr string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

// TestPlanInit_RosterLinksEveryNote verifies that a 3-note --from-note roster
// gives EACH note a distinct plan_ref/plan_job write and an in_progress move
// (the native replacement for the stripped shell hooks), and that each job's
// note_ref carries the note's frontmatter id as provenance.
func TestPlanInit_RosterLinksEveryNote(t *testing.T) {
	recordPath, _ := installNbStub(t)

	tempDir := t.TempDir()
	inbox := filepath.Join(tempDir, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}

	notes := []struct{ name, id string }{
		{"alpha.md", "id-alpha"},
		{"beta.md", "id-beta"},
		{"gamma.md", "id-gamma"},
	}
	var notePaths []string
	idSet := map[string]bool{}
	for _, n := range notes {
		p := filepath.Join(inbox, n.name)
		content := "---\nid: " + n.id + "\ntitle: " + strings.TrimSuffix(n.name, ".md") + "\ntype: inbox\n---\n\nBody of " + n.name + "\n"
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("writing note %s: %v", n.name, err)
		}
		notePaths = append(notePaths, p)
		idSet[n.id] = true
	}

	// No env provisioning during the test.
	prev := provisionEnvironmentFn
	provisionEnvironmentFn = func(worktreeName, planPath string, _ *workspace.Provider, envProfile string) (string, error) {
		return "", nil
	}
	t.Cleanup(func() { provisionEnvironmentFn = prev })

	planPath := filepath.Join(tempDir, "roster-plan")
	if _, err := executePlanInit(&PlanInitCmd{Dir: planPath, FromNotes: notePaths}); err != nil {
		t.Fatalf("executePlanInit: %v", err)
	}

	lines := stubRecordLines(t, recordPath)

	// Each of the three notes must have been moved to in_progress.
	for _, p := range notePaths {
		if got := countLines(lines, "move "+p+" in_progress --force"); got != 1 {
			t.Fatalf("expected in_progress move for %s, record:\n%s", p, strings.Join(lines, "\n"))
		}
		if got := countLines(lines, "update-frontmatter --path "+p+" --field plan_ref --value plans/roster-plan"); got != 1 {
			t.Fatalf("expected plan_ref write for %s, record:\n%s", p, strings.Join(lines, "\n"))
		}
	}

	// Three DISTINCT plan_job values must have been written, matching the plan's
	// job filenames.
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if len(plan.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(plan.Jobs))
	}
	jobFiles := map[string]bool{}
	for _, j := range plan.Jobs {
		jobFiles[j.Filename] = true
		// note_ref is provenance = the note's frontmatter id.
		if !idSet[j.NoteRef] {
			t.Fatalf("job %s note_ref %q is not one of the note ids %v", j.Filename, j.NoteRef, idSet)
		}
		if got := countLines(lines, "--field plan_job --value "+j.Filename); got != 1 {
			t.Fatalf("expected plan_job write for %s, record:\n%s", j.Filename, strings.Join(lines, "\n"))
		}
	}
	if len(jobFiles) != 3 {
		t.Fatalf("expected 3 distinct job files, got %v", jobFiles)
	}
}

// TestPlanDemote_ClearsLinkViaQuery verifies demote resolves the note by
// querying nb (plan_ref + plan_job), moves it back to inbox, and CLEARS both
// plan_ref and plan_job on the note. The job is marked abandoned.
func TestPlanDemote_ClearsLinkViaQuery(t *testing.T) {
	recordPath, setList := installNbStub(t)

	tempDir := t.TempDir()
	planDir := filepath.Join(tempDir, "workspaces", "ws", "plans", "demo-plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(planDir, ".grove-plan.yml"), []byte("name: demo-plan\nworktree: \"\"\n"), 0o644); err != nil {
		t.Fatalf("write plan config: %v", err)
	}
	jobPath := filepath.Join(planDir, "01-task.md")
	jobContent := "---\nid: task-1\ntitle: Task One\ntype: chat\nstatus: pending_user\nnote_ref: task-1\n---\n\nBody\n"
	if err := os.WriteFile(jobPath, []byte(jobContent), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}

	// nb resolves the note by plan_ref + plan_job.
	setList(`[{"path":"/nb/ws/in_progress/task.md","plan_ref":"plans/demo-plan","plan_job":"01-task.md"}]`)

	if err := runPlanDemote(&cobra.Command{}, []string{jobPath}); err != nil {
		t.Fatalf("runPlanDemote: %v", err)
	}

	lines := stubRecordLines(t, recordPath)
	if got := countLines(lines, "move /nb/ws/in_progress/task.md inbox --force"); got != 1 {
		t.Fatalf("expected inbox move, record:\n%s", strings.Join(lines, "\n"))
	}
	// The moved note (To: /nb/ws/inbox/task.md) must have BOTH link fields cleared.
	if got := countLines(lines, "update-frontmatter --path /nb/ws/inbox/task.md --field plan_ref --value"); got != 1 {
		t.Fatalf("expected plan_ref clear, record:\n%s", strings.Join(lines, "\n"))
	}
	if got := countLines(lines, "update-frontmatter --path /nb/ws/inbox/task.md --field plan_job --value"); got != 1 {
		t.Fatalf("expected plan_job clear, record:\n%s", strings.Join(lines, "\n"))
	}

	job, err := orchestration.LoadJob(jobPath)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.Status != orchestration.JobStatusAbandoned {
		t.Fatalf("expected job abandoned, got %s", job.Status)
	}
}
