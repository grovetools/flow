package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/workspace"
)

func TestSeedSiblingWorkspaces(t *testing.T) {
	gitEcoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(gitEcoRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirEcoRoot := t.TempDir() // grove.toml workspaces, no .git

	ecoNode := func(path string) *workspace.WorkspaceNode {
		return &workspace.WorkspaceNode{Name: filepath.Base(path), Path: path, Kind: workspace.KindEcosystemRoot}
	}

	tests := []struct {
		name    string
		cmd     *PlanInitCmd
		node    *workspace.WorkspaceNode
		want    []string
		wantErr bool
	}{
		{
			name: "explicit list untouched",
			cmd:  &PlanInitCmd{SiblingWorkspaces: []string{"core"}},
			node: ecoNode(gitEcoRoot),
			want: []string{"core"},
		},
		{
			name: "nil node untouched",
			cmd:  &PlanInitCmd{},
			node: nil,
			want: nil,
		},
		{
			name: "git ecosystem root seeds sentinel",
			cmd:  &PlanInitCmd{Worktree: "__AUTO__"},
			node: ecoNode(gitEcoRoot),
			want: []string{"__ALL__"},
		},
		{
			name: "directory ecosystem root with anchor seeds anchor",
			cmd:  &PlanInitCmd{Worktree: "__AUTO__", Anchor: "foodee"},
			node: ecoNode(dirEcoRoot),
			want: []string{"foodee"},
		},
		{
			name:    "directory ecosystem root with worktree but no anchor errors",
			cmd:     &PlanInitCmd{Worktree: "__AUTO__"},
			node:    ecoNode(dirEcoRoot),
			wantErr: true,
		},
		{
			name: "directory ecosystem root without worktree stays empty",
			cmd:  &PlanInitCmd{},
			node: ecoNode(dirEcoRoot),
			want: nil,
		},
		{
			name: "standalone project seeds own name",
			cmd:  &PlanInitCmd{Worktree: "__AUTO__"},
			node: &workspace.WorkspaceNode{Name: "myrepo", Path: "/tmp/code/myrepo", Kind: workspace.KindStandaloneProject},
			want: []string{"myrepo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := seedSiblingWorkspaces(tt.cmd, tt.node)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (siblings=%v)", tt.cmd.SiblingWorkspaces)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tt.cmd.SiblingWorkspaces) != len(tt.want) {
				t.Fatalf("siblings = %v, want %v", tt.cmd.SiblingWorkspaces, tt.want)
			}
			for i := range tt.want {
				if tt.cmd.SiblingWorkspaces[i] != tt.want[i] {
					t.Fatalf("siblings = %v, want %v", tt.cmd.SiblingWorkspaces, tt.want)
				}
			}
		})
	}
}
