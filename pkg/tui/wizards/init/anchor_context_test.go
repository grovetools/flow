package planinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildDirectoryEcosystem creates a non-git ecosystem root (grove.toml with
// workspaces, no .git) containing one real git member repo, mirroring the
// "solutils/foodee" shape.
func buildDirectoryEcosystem(t *testing.T) (ecoRoot, member string) {
	t.Helper()
	ecoRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(ecoRoot, "grove.toml"), []byte("name = \"solutils\"\nworkspaces = [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	member = filepath.Join(ecoRoot, "foodee")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "grove.toml"), []byte("name = \"foodee\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", member)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return ecoRoot, member
}

func TestAnchorContext(t *testing.T) {
	ecoRoot, member := buildDirectoryEcosystem(t)

	show, auto := anchorContext(ecoRoot)
	if !show || auto != "" {
		t.Fatalf("ecosystem root: show=%v auto=%q, want show=true auto=\"\"", show, auto)
	}

	show, auto = anchorContext(member)
	if show || auto != "foodee" {
		t.Fatalf("directory-ecosystem member: show=%v auto=%q, want show=false auto=\"foodee\"", show, auto)
	}

	// A git ecosystem's member must NOT self-anchor: plans from inside it
	// keep targeting the whole ecosystem.
	gitEco := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitEco, "grove.toml"), []byte("name = \"eco\"\nworkspaces = [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", gitEco).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitMember := filepath.Join(gitEco, "sub")
	if err := os.MkdirAll(gitMember, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitMember, "grove.toml"), []byte("name = \"sub\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", gitMember).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	show, auto = anchorContext(gitMember)
	if show || auto != "" {
		t.Fatalf("git-ecosystem member: show=%v auto=%q, want show=false auto=\"\"", show, auto)
	}
}

func TestAnchorFieldHiddenForDirectoryEcosystemMember(t *testing.T) {
	ecoRoot, member := buildDirectoryEcosystem(t)

	m := New(Config{WorkspaceDir: ecoRoot, InvocationDir: member})
	if m.showAnchor {
		t.Fatal("anchor picker should be hidden when invoked from a member repo")
	}
	if got := m.toRequest().Anchor; got != "foodee" {
		t.Fatalf("request anchor = %q, want implicit \"foodee\"", got)
	}
	if got := m.renderMainScreen(); strings.Contains(got, "Anchor Repository") {
		t.Fatal("main screen still renders the hidden anchor field")
	}

	// Focus cycling must skip the hidden index 3: from index 2, one step
	// forward lands on 4.
	m.focusIndex = 2
	m = m.stepFocus(1)
	if m.focusIndex != 4 {
		t.Fatalf("focus after step from 2 = %d, want 4", m.focusIndex)
	}
	m = m.stepFocus(-1)
	if m.focusIndex != 2 {
		t.Fatalf("focus after step back from 4 = %d, want 2", m.focusIndex)
	}
}
