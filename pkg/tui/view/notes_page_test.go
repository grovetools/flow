package view

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/status"
)

func TestSixthTabUsesDedicatedNotesMode(t *testing.T) {
	root := t.TempDir()
	m := New(Config{PlansDir: filepath.Join(root, "plans"), WorkspaceDir: root})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
	got := updated.(Model)
	if got.mode != modeNotes {
		t.Fatalf("mode = %v, want modeNotes", got.mode)
	}
	if state := got.TestState()["mode"]; state != "notes" {
		t.Fatalf("TestState mode = %v, want notes", state)
	}
}

func TestNotesPageScopeCycleUsesPlanRefOnly(t *testing.T) {
	root := t.TempDir()
	plan := &orchestration.Plan{Name: "alpha", Directory: filepath.Join(root, "plans", "alpha")}
	sm := status.Model{Plan: plan}
	state := &viewState{cfg: Config{PlansDir: filepath.Join(root, "plans")}, statusModel: &sm}
	p := &notesPage{s: state, notes: []*models.NoteIndexEntry{
		{Path: filepath.Join(root, "inbox", "linked.md"), Title: "linked", Workspace: filepath.Base(root), ContentDir: "notes", PlanRef: "plans/alpha"},
		{Path: filepath.Join(root, "inbox", "tag-only.md"), Title: "tag only", Workspace: filepath.Base(root), ContentDir: "notes", Tags: []string{"alpha"}},
		{Path: filepath.Join(t.TempDir(), "other.md"), Title: "other", Workspace: "other", ContentDir: "notes", PlanRef: "plans/beta"},
	}}

	p.applyScope()
	if len(p.visible) != 1 || p.visible[0].Title != "linked" {
		t.Fatalf("This plan visible = %+v, want only exact plan_ref link", p.visible)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if p.scope != notesThisNotespace || len(p.visible) != 2 {
		t.Fatalf("This notespace scope=%v count=%d, want scope and 2", p.scope, len(p.visible))
	}
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if p.scope != notesUnlinked || len(p.visible) != 1 || p.visible[0].Title != "tag only" {
		t.Fatalf("Unlinked visible = %+v", p.visible)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if p.scope != notesAll || len(p.visible) != 3 {
		t.Fatalf("All notespaces count = %d, want 3", len(p.visible))
	}
}

func TestScanNotespaceFallbackParsesLinksAndExcludesPlans(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(inbox, "linked.md")
	if err := os.WriteFile(linked, []byte("---\ntitle: Linked note\nplan_ref: plans/alpha\nplan_job: 03-task.md\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "plans", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plans", "alpha", "03-task.md"), []byte("---\ntitle: job\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	notes, err := scanNotespace(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("scan returned %d notes, want 1: %+v", len(notes), notes)
	}
	if notes[0].PlanRef != "plans/alpha" || notes[0].PlanJob != "03-task.md" || notes[0].Title != "Linked note" {
		t.Fatalf("parsed note = %+v", notes[0])
	}
}

func TestNotesActionsShellThroughOwnedSeams(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> \"$CALL_LOG\"\nif [ \"$1\" = new ]; then echo 'Created: /tmp/new-note.md'; fi\n"
	for _, name := range []string{"nb", "flow"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALL_LOG", logPath)

	root := t.TempDir()
	plan := &orchestration.Plan{Name: "alpha", Directory: filepath.Join(root, "plans", "alpha")}
	sm := status.Model{Plan: plan}
	note := &models.NoteIndexEntry{Path: filepath.Join(root, "inbox", "note.md"), PlanRef: "plans/alpha", PlanJob: "03-task.md"}
	p := &notesPage{s: &viewState{cfg: Config{PlansDir: filepath.Join(root, "plans")}, statusModel: &sm}, visible: []*models.NoteIndexEntry{note}}
	for _, key := range []string{"p", "u", "d"} {
		cmd := p.runAction(key)
		if cmd == nil {
			t.Fatalf("%s returned no command", key)
		}
		if msg := cmd().(notesActionMsg); msg.err != nil {
			t.Fatalf("%s action: %v", key, msg.err)
		}
	}
	p.creating, p.newTitle = true, "A new note"
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("new note returned no command")
	}
	if msg := cmd().(notesActionMsg); msg.err != nil {
		t.Fatalf("new action: %v", msg.err)
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(calls)
	for _, want := range []string{
		"nb promote " + note.Path + " --plan " + plan.Directory,
		"nb internal update-frontmatter --path " + note.Path + " --field plan_ref --value ",
		"flow plan demote " + filepath.Join(plan.Directory, "03-task.md"),
		"nb new A new note --type inbox --no-edit --plan alpha",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("calls missing %q:\n%s", want, got)
		}
	}
}

func TestNotesEnterTogglesPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(path, []byte("preview body"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &notesPage{s: &viewState{}, visible: []*models.NoteIndexEntry{{Path: path}}}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !p.preview || p.content != "preview body" {
		t.Fatalf("preview=%v content=%q", p.preview, p.content)
	}
}
