package plancreate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/worktreeregistry"
)

// Validate performs no mutations. It returns every check so the review screen
// can explain both successful and failed preconditions.
func Validate(req Request) (ValidationReport, MutationManifest) {
	report := ValidationReport{CheckedAt: time.Now()}
	add := func(id string, severity Severity, ok bool, detail string) {
		report.Checks = append(report.Checks, Check{ID: id, Severity: severity, OK: ok, Detail: detail})
	}

	nameOK := req.PlanName != "" && filepath.Base(req.PlanName) == req.PlanName && req.PlanName != "." && req.PlanName != ".."
	add("identity.plan_name", SeverityError, nameOK, map[bool]string{true: "plan name is valid", false: "plan name must be one path segment"}[nameOK])

	// The worktree name is joined onto a container base, and filepath.Join
	// CONCATENATES an absolute second element instead of replacing the base —
	// so a path pasted into the wizard's worktree field would synthesize a deep
	// tree inside the container rather than being rejected. Check it here so
	// the review screen says so before anything is created.
	if req.WorktreeName != "" {
		wtErr := workspace.ValidateWorktreeName(req.WorktreeName)
		add("identity.worktree_name", SeverityError, wtErr == nil,
			ternary(wtErr == nil, "worktree name is valid: "+req.WorktreeName, fmt.Sprintf("%v", wtErr)))
	}

	planDir := filepath.Join(req.PlansDir, req.PlanName)
	_, statErr := os.Stat(planDir)
	collisionFree := os.IsNotExist(statErr)
	collisionAllowed := collisionFree || req.Force
	collisionDetail := ternary(collisionFree, "available: "+planDir, "already exists: "+planDir)
	if !collisionFree && req.Force {
		collisionDetail += " (force overwrite requested)"
	}
	add("collision.plan_dir", SeverityError, collisionAllowed, collisionDetail)

	layoutOK := req.Layout == "" || req.Layout == "xdg" || req.Layout == "legacy"
	add("location.layout", SeverityError, layoutOK, ternary(layoutOK, "worktree layout: "+ternary(req.Layout == "", "default", req.Layout), "layout must be xdg or legacy"))
	if req.Anchor != "" {
		add("location.anchor", SeverityInfo, true, "anchor will resolve within target ecosystem: "+req.Anchor)
	}

	workspaceInfo, workspaceErr := os.Stat(req.TargetWorkspace)
	workspaceOK := workspaceErr == nil && workspaceInfo.IsDir()
	add("target.workspace", SeverityError, workspaceOK, ternary(workspaceOK, "workspace: "+req.TargetWorkspace, fmt.Sprintf("workspace unavailable: %v", workspaceErr)))

	// A worktree needs a git repo to be created from. A directory ecosystem
	// root (grove.toml `workspaces`, no .git) can host plans but not worktrees
	// — those must be anchored to a member repo.
	if req.WorktreeName != "" && workspaceOK && req.Anchor == "" {
		if _, err := os.Stat(filepath.Join(req.TargetWorkspace, ".git")); err != nil {
			add("git.worktree_source", SeverityError, false, "target workspace is not a git repository; select an anchor repository for the worktree")
		}
	}

	plansInfo, plansErr := os.Stat(req.PlansDir)
	plansOK := plansErr == nil && plansInfo.IsDir() && plansInfo.Mode().Perm()&0o200 != 0
	add("permissions.plans_dir", SeverityError, plansOK, ternary(plansOK, "plans directory is writable", "plans directory is missing or not writable"))

	if req.BaseBranch != "" && workspaceOK {
		cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+req.BaseBranch)
		cmd.Dir = req.TargetWorkspace
		branchOK := cmd.Run() == nil
		add("git.base_branch", SeverityError, branchOK, ternary(branchOK, "base branch exists: "+req.BaseBranch, "base branch not found: "+req.BaseBranch))
	}
	if workspaceOK {
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = req.TargetWorkspace
		out, err := cmd.Output()
		clean := err == nil && strings.TrimSpace(string(out)) == ""
		add("git.cleanliness", SeverityWarning, clean, ternary(clean, "target checkout is clean", "target checkout has changes (creation may still be safe)"))
	}

	registryFree := true
	if req.WorktreeName != "" {
		if entries, err := worktreeregistry.ListAll(); err == nil {
			for _, entry := range entries {
				if entry != nil && !entry.IsArchived() && filepath.Base(entry.AbsPath) == req.WorktreeName && filepath.Clean(entry.Owner) == filepath.Clean(req.TargetWorkspace) {
					registryFree = false
					break
				}
			}
		}
	}
	add("registry.worktree_ownership", SeverityError, registryFree, ternary(registryFree, "worktree identity is unowned", "worktree identity is already registered"))

	manifest := MutationManifest{Steps: []MutationStep{
		{ID: "journal", Kind: "write_init_journal", Target: filepath.Join(req.PlansDir, ".init-"+filepath.Base(req.PlanName)+".journal.json"), Reversible: true},
		{ID: "plan-dir", Kind: "create_plan_dir", Target: planDir, Reversible: true},
		{ID: "plan-files", Kind: "write_plan_and_job_files", Target: planDir, Reversible: true},
	}}
	if req.WorktreeName != "" {
		manifest.Steps = append(manifest.Steps,
			MutationStep{ID: "branch", Kind: "create_branch", Target: req.WorktreeName, Reversible: true},
			MutationStep{ID: "worktree", Kind: "create_worktree", Target: req.WorktreeName, Reversible: true},
			MutationStep{ID: "registry", Kind: "upsert_registry_entry", Target: req.WorktreeName, Reversible: true})
	}
	if req.RunInitHooks {
		manifest.Steps = append(manifest.Steps, MutationStep{ID: "hooks", Kind: "run_init_hooks", Target: req.TargetWorkspace, Reversible: false})
	}
	return report, manifest
}

func ternary(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}
