package cmd

import (
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/workspace"
)

// TestResolveAnchorPath_PrefersCanonicalSubProject is the regression test for
// the --anchor non-canonical-repo bug: a repo name that exists BOTH as the
// current ecosystem's direct sub-project AND as a copy inside some OTHER
// worktree must resolve to the CANONICAL sub-project under the ecosystem root,
// never the worktree copy.
//
// Before the fix, resolveAnchorPath called provider.FindByName, which returns
// the FIRST node matching the name with no preference — so it could (and in
// manual testing did) return the worktree copy, placing the new worktree under
// the wrong repo's XDG base.
func TestResolveAnchorPath_PrefersCanonicalSubProject(t *testing.T) {
	ecoRoot := filepath.FromSlash("/Users/test/Code/grovetools")
	canonicalSubProject := filepath.Join(ecoRoot, "project-tmpl-go")

	// A copy of the SAME-named repo living inside a DIFFERENT worktree of the
	// ecosystem — this is the decoy the buggy FindByName could return.
	worktreeCopy := filepath.Join(ecoRoot, ".grove-worktrees", "daemon-upgrade", "project-tmpl-go")

	ecoNode := &workspace.WorkspaceNode{
		Name: "grovetools",
		Path: ecoRoot,
		Kind: workspace.KindEcosystemRoot,
	}
	// The decoy worktree copy is ordered FIRST so the old FindByName (which
	// returns the first name match) would have returned it.
	decoy := &workspace.WorkspaceNode{
		Name:                "project-tmpl-go",
		Path:                worktreeCopy,
		Kind:                workspace.KindEcosystemWorktreeSubProject,
		ParentProjectPath:   canonicalSubProject,
		ParentEcosystemPath: filepath.Join(ecoRoot, ".grove-worktrees", "daemon-upgrade"),
		RootEcosystemPath:   ecoRoot,
	}
	canonical := &workspace.WorkspaceNode{
		Name:                "project-tmpl-go",
		Path:                canonicalSubProject,
		Kind:                workspace.KindEcosystemSubProject,
		ParentEcosystemPath: ecoRoot,
		RootEcosystemPath:   ecoRoot,
	}
	nodes := []*workspace.WorkspaceNode{ecoNode, decoy, canonical}

	provider := workspace.NewProviderFromNodes(nodes)

	// currentNode is the ecosystem root (the directory `flow plan init` runs in).
	currentNode := ecoNode

	got, err := resolveAnchorPath("project-tmpl-go", currentNode, provider)
	if err != nil {
		t.Fatalf("resolveAnchorPath returned error: %v", err)
	}
	if got != canonicalSubProject {
		t.Errorf("anchor resolved to %q, want canonical sub-project %q (must NOT be the worktree copy %q)",
			got, canonicalSubProject, worktreeCopy)
	}
}

// TestResolveAnchorPath_DoesNotCrossEcosystems asserts that an --anchor whose
// only same-named match lives in a DIFFERENT ecosystem does not resolve: the
// anchor must be a sub-project of the CURRENT ecosystem.
func TestResolveAnchorPath_DoesNotCrossEcosystems(t *testing.T) {
	curEco := filepath.FromSlash("/Users/test/Code/grovetools")
	otherEco := filepath.FromSlash("/Users/test/Code/otherproj")

	nodes := []*workspace.WorkspaceNode{
		{Name: "grovetools", Path: curEco, Kind: workspace.KindEcosystemRoot},
		{
			Name:                "shared-name",
			Path:                filepath.Join(otherEco, "shared-name"),
			Kind:                workspace.KindEcosystemSubProject,
			ParentEcosystemPath: otherEco,
			RootEcosystemPath:   otherEco,
		},
	}
	provider := workspace.NewProviderFromNodes(nodes)
	currentNode := nodes[0]

	if _, err := resolveAnchorPath("shared-name", currentNode, provider); err == nil {
		t.Fatalf("expected error: anchor in a different ecosystem must not resolve, got nil")
	}
}

// TestResolveAnchorPath_AutoInferFromCurrentNode covers the no-explicit-anchor
// path: it falls back to currentNode.Path (the canonical repo the command runs
// in).
func TestResolveAnchorPath_AutoInferFromCurrentNode(t *testing.T) {
	ecoRoot := filepath.FromSlash("/Users/test/Code/grovetools")
	subProject := filepath.Join(ecoRoot, "svc-a")
	currentNode := &workspace.WorkspaceNode{
		Name:                "svc-a",
		Path:                subProject,
		Kind:                workspace.KindEcosystemSubProject,
		ParentEcosystemPath: ecoRoot,
		RootEcosystemPath:   ecoRoot,
	}
	provider := workspace.NewProviderFromNodes([]*workspace.WorkspaceNode{currentNode})

	got, err := resolveAnchorPath("", currentNode, provider)
	if err != nil {
		t.Fatalf("resolveAnchorPath returned error: %v", err)
	}
	if got != subProject {
		t.Errorf("auto-inferred anchor = %q, want %q", got, subProject)
	}
}
