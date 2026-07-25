package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// TestExecuteFinishActions_RetainedWorktreeStillArchives pins the P2 policy on
// the CLI path: a worktree kept because one repo still holds uncommitted work
// is a partial success, not a teardown failure. The plan is still marked
// finished and archived; the error is still returned so the exit status and the
// operator both see it.
func TestExecuteFinishActions_RetainedWorktreeStillArchives(t *testing.T) {
	archiveRan := false
	markRan := false
	items := []*finish.Item{
		{
			ID:          plan_finish.ItemPruneWorktree,
			Name:        "Prune git worktree",
			IsEnabled:   true,
			IsAvailable: true,
			Action: func() error {
				return &plan_finish.RetainedWorktreeError{Details: []string{"core: contains modified or untracked files"}}
			},
		},
		{
			ID:        plan_finish.ItemMarkFinished,
			Name:      "Mark plan as finished",
			IsEnabled: true,
			Action:    func() error { markRan = true; return nil },
		},
		{
			ID:        plan_finish.ItemArchivePlan,
			Name:      "Archive plan directory",
			IsEnabled: true,
			Action:    func() error { archiveRan = true; return nil },
		},
	}

	err := executeFinishActions(items)
	if err == nil {
		t.Fatal("the retention must still be reported to the caller")
	}
	if !markRan {
		t.Error("mark_finished must run despite a retained worktree")
	}
	if !archiveRan {
		t.Error("archive_plan must run despite a retained worktree")
	}
}

// TestActivePlanIsReadBeforeActionsRun is a source-level guard for the CLI half
// of P6. getActivePlanWithMigration resolves the active plan from the process
// cwd; by the time the cleanup actions have finished, that directory may have
// been deleted, so the lookup returns "" and the unset block is skipped
// silently — no warning, key left set. The read has to happen first.
func TestActivePlanIsReadBeforeActionsRun(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "plan_finish.go", nil, 0)
	if err != nil {
		t.Fatalf("parse plan_finish.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "runPlanFinish" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("runPlanFinish not found")
	}

	activePlanLine, executeLine := 0, 0
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "getActivePlanWithMigration":
			if activePlanLine == 0 {
				activePlanLine = fset.Position(call.Pos()).Line
			}
		case "executeFinishActions":
			if executeLine == 0 {
				executeLine = fset.Position(call.Pos()).Line
			}
		}
		return true
	})

	if activePlanLine == 0 || executeLine == 0 {
		t.Fatalf("expected both calls in runPlanFinish (activePlan=%d execute=%d)", activePlanLine, executeLine)
	}
	if activePlanLine > executeLine {
		t.Fatalf("the active plan is read at line %d, AFTER the actions run at line %d: "+
			"by then the worktree it resolves from may be gone and the key is left set silently",
			activePlanLine, executeLine)
	}
}

// TestRebuildBinariesFlagIsStillRegistered pins that the opt-in flag exists.
// Half-removing this feature (flag registered, item unreachable) is what made
// `--rebuild-binaries` a silent no-op.
func TestRebuildBinariesFlagIsStillRegistered(t *testing.T) {
	cmd := NewPlanFinishCmd()
	if cmd.Flags().Lookup("rebuild-binaries") == nil {
		t.Fatal("--rebuild-binaries must stay registered while ItemRebuildBinaries exists")
	}
	if !strings.Contains(cmd.Flags().Lookup("force").Usage, "Force") {
		t.Error("--force usage text missing")
	}
}
