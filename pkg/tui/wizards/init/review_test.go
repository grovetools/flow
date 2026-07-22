package planinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testGitWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	return dir
}

func TestSubmitValidatesBeforeEmittingDone(t *testing.T) {
	m := New(Config{PlansDir: t.TempDir(), WorkspaceDir: testGitWorkspace(t)})
	m.nameInput.SetValue("reviewed-plan")
	m.withWorktree = false
	m.worktreeInput.SetValue("")
	m.unfocused = false
	m.focusIndex = 0

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if !got.validating || cmd == nil {
		t.Fatalf("enter should start validation: validating=%v cmd=%v", got.validating, cmd)
	}
	msg := cmd()
	if _, ok := msg.(validationCompleteMsg); !ok {
		t.Fatalf("validation command returned %T", msg)
	}
	updated, _ = got.Update(msg)
	got = updated.(Model)
	if got.currentScreen != ReviewScreen || got.validation == nil || !got.validation.Valid() {
		t.Fatalf("expected valid review screen, got screen=%v report=%+v", got.currentScreen, got.validation)
	}
}
