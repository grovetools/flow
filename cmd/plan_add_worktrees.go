package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/grovetools/core/git"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/grovetools/flow/pkg/orchestration"
)

var (
	planAddWorktreesAll        bool
	planAddWorktreesPlan       string
	planAddWorktreesWorkspaces []string
)

// planAddWorktreesCmd implements the `flow plan add-worktrees` command, which
// links ADDITIONAL sibling repos into an EXISTING ecosystem worktree that was
// created with a subset of repos (via --sibling-workspaces). It grows the
// worktree in place rather than re-creating it.
var planAddWorktreesCmd = &cobra.Command{
	Use:   "add-worktrees <repo,repo,...>",
	Short: "Link additional sibling repos into an existing ecosystem worktree",
	Long: `Link additional sibling repos into an existing ecosystem worktree.

The worktree must already exist (created with 'flow plan init --worktree
--sibling-workspaces ...'). The named repos are linked on the SAME branch as
the existing worktree, the go.work 'use (...)' block is regenerated to include
them, default context rules are applied, and both the plan config and the
worktree's .grove/workspace marker are updated to the new union of repos.

Pass --all to fill the worktree out to every direct-child repo of the
ecosystem.

Repos to add may be given as a positional comma-separated list or via
-w/--workspaces (matching the workspace-name scoping used elsewhere, e.g.
'core logs -w'). Target a specific plan with -p/--plan; otherwise the active
plan in the --dir context (default cwd) is used.

Examples:
  flow plan add-worktrees nav,treemux
  flow plan add-worktrees -w nav,treemux --plan my-feature
  flow plan add-worktrees nav,treemux --dir ~/Code/myapp
  flow plan add-worktrees --all --plan my-feature`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanAddWorktrees,
}

func init() {
	planAddWorktreesCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
	planAddWorktreesCmd.Flags().StringVarP(&planAddWorktreesPlan, "plan", "p", "", "Target plan slug or directory (defaults to the active plan in --dir)")
	planAddWorktreesCmd.Flags().StringSliceVarP(&planAddWorktreesWorkspaces, "workspaces", "w", nil, "Workspaces (repos) to link, comma-separated (alternative to the positional arg)")
	planAddWorktreesCmd.Flags().BoolVar(&planAddWorktreesAll, "all", false, "Link every direct-child repo of the ecosystem")
	planCmd.AddCommand(planAddWorktreesCmd)
}

func runPlanAddWorktrees(cmd *cobra.Command, args []string) error {
	// Collect requested repos from the positional arg AND -w/--workspaces
	// (both comma-separated); the union is what we link.
	var requestedRepos []string
	for _, src := range append([]string{}, args...) {
		for _, r := range strings.Split(src, ",") {
			if r = strings.TrimSpace(r); r != "" {
				requestedRepos = append(requestedRepos, r)
			}
		}
	}
	for _, r := range planAddWorktreesWorkspaces {
		if r = strings.TrimSpace(r); r != "" {
			requestedRepos = append(requestedRepos, r)
		}
	}
	if len(requestedRepos) == 0 && !planAddWorktreesAll {
		return fmt.Errorf("specify repos to add (positional or -w/--workspaces, comma-separated) or pass --all")
	}

	// Resolve the target plan: an explicit -p/--plan wins; otherwise fall back
	// to the active plan in the --dir context (default cwd). The explicit flag
	// avoids silently grabbing a stale active-plan pointer.
	contextDir := planContextDir
	if contextDir == "" {
		contextDir = "."
	}
	planRef := planAddWorktreesPlan
	if planRef == "" {
		planRef = coreplan.ActivePlan(contextDir)
		if planRef == "" {
			return fmt.Errorf("no plan specified and no active plan in %s; pass -p/--plan or run from inside the worktree", contextDir)
		}
	}
	planPath, err := resolvePlanPath(planRef, contextDir)
	if err != nil {
		return err
	}

	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// The worktree must exist. Every worktree is now a container holding
	// repos: >= 1 (a single-repo worktree is just a 1-repo container), so
	// there is no "ecosystem vs single-repo" distinction to reject on —
	// growing a 1-repo container works the same as growing an N-repo one.
	if plan.Config == nil || plan.Config.Worktree == "" {
		return fmt.Errorf("plan '%s' has no associated worktree", plan.Name)
	}
	worktreeName := plan.Config.Worktree

	// Resolve the owning ecosystem root the SAME notebook-aware way
	// updateWorktreeEcosystem does, then re-resolve through git so the
	// spelling matches workspace discovery.
	ecosystemRoot, err := orchestration.GetProjectGitRoot(plan.Directory)
	if err != nil {
		return fmt.Errorf("failed to resolve ecosystem root for plan %q: %w", plan.Name, err)
	}
	if normalized, gerr := git.GetGitRoot(ecosystemRoot); gerr == nil {
		ecosystemRoot = normalized
	}

	// Find where the ecosystem worktree actually lives (XDG or legacy).
	worktreePath, ok := workspace.FindWorktreePath(ecosystemRoot, worktreeName)
	if !ok {
		return fmt.Errorf("worktree '%s' not found under either layout base of %s", worktreeName, ecosystemRoot)
	}

	// Resolve the owner of the worktree (layout-independent).
	owner, ok := workspace.WorktreeOwner(worktreePath)
	if !ok {
		// Fall back to the resolved ecosystem root: an ecosystem worktree's
		// owner IS the ecosystem git root.
		owner = ecosystemRoot
	}

	// Resolve the branch from the existing worktree HEAD, falling back to the
	// worktree name (which is also the branch name at creation time).
	branch := resolveWorktreeBranch(worktreePath, plan.Config.Repos, worktreeName)

	// Build a workspace provider once for submodule setup and --all discovery.
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	discoveryService := workspace.NewDiscoveryService(logger)
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover workspaces: %w", err)
	}
	provider := workspace.NewProvider(discoveryResult)

	// Determine the repos to add: explicit list, or the full discovered
	// direct-child set when --all.
	if planAddWorktreesAll {
		requestedRepos = discoverDirectChildRepos(ecosystemRoot, provider)
		if len(requestedRepos) == 0 {
			return fmt.Errorf("--all found no direct-child repos under %s", ecosystemRoot)
		}
	}

	// Compute the UNION = existing repos ∪ requested repos, de-duped & sorted.
	union := unionRepos(plan.Config.Repos, requestedRepos)

	// If nothing new would be added, report and stop early.
	newRepos := diffRepos(union, plan.Config.Repos)
	if len(newRepos) == 0 {
		fmt.Printf("Worktree '%s' already includes all requested repos: %s\n", worktreeName, strings.Join(plan.Config.Repos, ", "))
		return nil
	}

	fmt.Printf("Linking %d new repo(s) into worktree '%s' on branch '%s': %s\n",
		len(newRepos), worktreeName, branch, strings.Join(newRepos, ", "))

	// 1. Link the NEW repos. SetupSubmodules is idempotent (it skips repos
	//    whose worktree .git already exists, submodules.go:107), so passing the
	//    full union only links the missing ones, on the SAME branch.
	if err := workspace.SetupSubmodules(context.Background(), worktreePath, owner, branch, union, provider); err != nil {
		return fmt.Errorf("failed to link new repos into worktree: %w", err)
	}

	// 2. Regenerate the Go workspace wiring so go.work's use (...) block
	//    includes the newly-linked modules. Grove uses go.work `use`, not
	//    go.mod `replace`, so there is no replace block to maintain.
	if err := configureGoWorkspace(worktreePath, union, owner, provider); err != nil {
		fmt.Printf("Warning: could not regenerate go.work: %v\n", err)
	}

	// 3. Apply default context rules for the NEW repos so cx rules exist.
	if err := applyDefaultContextRulesToWorktree(worktreePath, newRepos); err != nil {
		fmt.Printf("Warning: could not apply default context rules to new repos: %v\n", err)
	}

	// 4. Persist the new repo set: plan config Repos and the worktree marker.
	if err := updatePlanConfigRepos(planPath, union); err != nil {
		return fmt.Errorf("failed to update plan config repos: %w", err)
	}
	if err := workspace.UpdateWorktreeRepos(worktreePath, union); err != nil {
		return fmt.Errorf("failed to update worktree marker repos: %w", err)
	}

	fmt.Printf("* Worktree '%s' now includes %d repos: %s\n", worktreeName, len(union), strings.Join(union, ", "))
	return nil
}

// resolveWorktreeBranch returns the branch of the existing ecosystem worktree.
// It reads HEAD from the first existing sub-repo worktree (they all share the
// branch), falling back to the worktree name when none can be read.
func resolveWorktreeBranch(worktreePath string, existingRepos []string, fallback string) string {
	for _, repo := range existingRepos {
		repoWorktree := filepath.Join(worktreePath, repo)
		if _, err := os.Stat(filepath.Join(repoWorktree, ".git")); err != nil {
			continue
		}
		out, err := exec.Command("git", "-C", repoWorktree, "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err == nil {
			if branch := strings.TrimSpace(string(out)); branch != "" && branch != "HEAD" {
				return branch
			}
		}
	}
	return fallback
}

// discoverDirectChildRepos returns the full set of direct-child repos of the
// ecosystem rooted at gitRoot, mirroring the --all expansion used by
// `flow plan init --sibling-workspaces`. It unions an on-disk scan (dirs with
// a .git) with provider-discovered direct children.
func discoverDirectChildRepos(gitRoot string, provider *workspace.Provider) []string {
	repoSet := make(map[string]struct{})
	if entries, err := os.ReadDir(gitRoot); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			info, ierr := os.Stat(filepath.Join(gitRoot, name))
			if ierr != nil || !info.IsDir() {
				continue
			}
			if strings.HasPrefix(name, ".") || name == ".grove-worktrees" {
				continue
			}
			if _, statErr := os.Stat(filepath.Join(gitRoot, name, ".git")); statErr != nil {
				continue
			}
			repoSet[name] = struct{}{}
		}
	}
	if provider != nil {
		for name, localPath := range provider.LocalWorkspaces() {
			if strings.EqualFold(filepath.Dir(localPath), gitRoot) {
				repoSet[name] = struct{}{}
			}
		}
	}
	repos := make([]string, 0, len(repoSet))
	for name := range repoSet {
		repos = append(repos, name)
	}
	sort.Strings(repos)
	return repos
}

// unionRepos returns the sorted, de-duplicated union of two repo lists.
func unionRepos(a, b []string) []string {
	set := make(map[string]struct{})
	for _, r := range a {
		if r = strings.TrimSpace(r); r != "" {
			set[r] = struct{}{}
		}
	}
	for _, r := range b {
		if r = strings.TrimSpace(r); r != "" {
			set[r] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// diffRepos returns the repos in `all` that are not in `existing`.
func diffRepos(all, existing []string) []string {
	have := make(map[string]struct{}, len(existing))
	for _, r := range existing {
		have[r] = struct{}{}
	}
	var out []string
	for _, r := range all {
		if _, ok := have[r]; !ok {
			out = append(out, r)
		}
	}
	return out
}

// updatePlanConfigRepos rewrites the repos: list in the plan's .grove-plan.yml
// to the given union, leaving every other key intact. It uses the same
// read/modify/write YAML round-trip as `flow plan config --set repos=...`.
func updatePlanConfigRepos(planPath string, repos []string) error {
	configPath := filepath.Join(planPath, ".grove-plan.yml")

	config := make(map[string]interface{})
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read plan config: %w", err)
		}
	} else if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse plan config: %w", err)
	}
	if config == nil {
		config = make(map[string]interface{})
	}

	config["repos"] = repos

	out, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal plan config: %w", err)
	}
	if err := os.WriteFile(configPath, out, 0o600); err != nil {
		return fmt.Errorf("failed to write plan config: %w", err)
	}
	return nil
}
