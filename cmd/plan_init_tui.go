package cmd

import (
	"errors"
	"fmt"

	"github.com/grovetools/core/tui/embed"

	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

// ErrTUIQuit is returned by runPlanInitTUI when the user dismisses
// the plan-init wizard without submitting.
var ErrTUIQuit = errors.New("quit")

// runPlanInitTUI launches the embeddable plan-init wizard via
// embed.RunStandalone and converts the wizard's *planinit.Request
// result back into a *PlanInitCmd for the CLI code path. The wizard
// itself is implemented in flow/pkg/tui/wizards/init; this function
// preserves the CLI contract by constructing the Config from the
// existing CLI flags and feeding the wizard's DoneMsg result through
// the executePlanInit code path.
func runPlanInitTUI(plansDir string, initialCmd *PlanInitCmd) (*PlanInitCmd, error) {
	getRecipeCmd, runInitByDefault := planinit.LoadFlowDefaults()

	cfg := planinit.Config{
		PlansDir:         plansDir,
		GetRecipeCmd:     getRecipeCmd,
		RunInitByDefault: runInitByDefault,
		Initial:          planInitCmdToRequest(initialCmd),
	}

	model := planinit.New(cfg)
	result, err := embed.RunStandalone(model)
	if err != nil {
		return nil, fmt.Errorf("error running plan init TUI: %w", err)
	}
	if result == nil {
		return nil, ErrTUIQuit
	}
	req, ok := result.(*planinit.Request)
	if !ok || req == nil || req.Dir == "" {
		return nil, ErrTUIQuit
	}
	return requestToPlanInitCmd(req, initialCmd), nil
}

// planInitCmdToRequest converts a *PlanInitCmd (populated from CLI
// flags) into a *planinit.Request the wizard can use to pre-populate
// its fields. Returns nil when the source is nil.
func planInitCmdToRequest(c *PlanInitCmd) *planinit.Request {
	if c == nil {
		return nil
	}
	return &planinit.Request{
		Dir:               c.Dir,
		Force:             c.Force,
		Model:             c.Model,
		Worktree:          c.Worktree,
		ExtractAllFrom:    c.ExtractAllFrom,
		OpenSession:       c.OpenSession,
		EnvProfile:        c.EnvProfile,
		Recipe:            c.Recipe,
		RecipeVars:        c.RecipeVars,
		RecipeCmd:         c.RecipeCmd,
		SiblingWorkspaces: c.SiblingWorkspaces,
		NoteRef:           c.NoteRef,
		FromNote:          c.firstFromNote(),
		NoteTargetFile:    c.NoteTargetFile,
		RunInit:           c.RunInit,
	}
}

// requestToPlanInitCmd builds a *PlanInitCmd from the wizard's
// Request, preserving any CLI-only fields carried on the original
// initialCmd that the wizard doesn't surface (EnvProfile, RecipeVars,
// RecipeCmd, SiblingWorkspaces, Force, etc.).
func requestToPlanInitCmd(req *planinit.Request, initialCmd *PlanInitCmd) *PlanInitCmd {
	cmd := &PlanInitCmd{
		Dir:               req.Dir,
		Force:             req.Force,
		Model:             req.Model,
		Worktree:          req.Worktree,
		ExtractAllFrom:    req.ExtractAllFrom,
		OpenSession:       req.OpenSession,
		EnvProfile:        req.EnvProfile,
		Recipe:            req.Recipe,
		RecipeVars:        req.RecipeVars,
		RecipeCmd:         req.RecipeCmd,
		SiblingWorkspaces: req.SiblingWorkspaces,
		NoteRef:           req.NoteRef,
		NoteTargetFile:    req.NoteTargetFile,
		RunInit:           req.RunInit,
	}
	if req.FromNote != "" {
		cmd.FromNotes = []string{req.FromNote}
	}
	// Carry through CLI-only fields that the wizard doesn't
	// surface in its form. The wizard never clears these, so they
	// pass through unchanged from the original CLI flags.
	if initialCmd != nil {
		if cmd.EnvProfile == "" {
			cmd.EnvProfile = initialCmd.EnvProfile
		}
		if len(cmd.RecipeVars) == 0 {
			cmd.RecipeVars = initialCmd.RecipeVars
		}
		if cmd.RecipeCmd == "" {
			cmd.RecipeCmd = initialCmd.RecipeCmd
		}
		if len(cmd.SiblingWorkspaces) == 0 {
			cmd.SiblingWorkspaces = initialCmd.SiblingWorkspaces
		}
		if cmd.NoteRef == "" {
			cmd.NoteRef = initialCmd.NoteRef
		}
		if !cmd.Force {
			cmd.Force = initialCmd.Force
		}
		// The wizard surfaces a single from-note; when the CLI passed a
		// roster whose first note the wizard kept, preserve the rest.
		if len(initialCmd.FromNotes) > 1 &&
			len(cmd.FromNotes) == 1 && cmd.FromNotes[0] == initialCmd.FromNotes[0] {
			cmd.FromNotes = initialCmd.FromNotes
		}
	}
	return cmd
}
