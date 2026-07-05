package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/util/pathutil"
	grovecontext "github.com/grovetools/cx/pkg/context"
	"github.com/grovetools/skills/pkg/skills"
)

// DetermineWorkingDirectory determines the working directory for a job based on
// its worktree and repository configuration. This is the canonical logic used by
// both interactive agent execution and resume operations.
func DetermineWorkingDirectory(plan *Plan, job *Job) (string, error) {
	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		return "", fmt.Errorf("could not find project git root: %w", err)
	}

	var workDir string
	if job.Worktree != "" {
		ownerRoot, worktreePath, exists := resolveWorktreeForJob(gitRoot, job.Worktree)
		if !exists {
			// Do NOT fall back to a guessed path: silently handing an agent
			// the main checkout when its worktree is missing is the
			// highest-risk wrong-cwd failure. Surface it instead.
			return "", fmt.Errorf("worktree %q not found for repository %s", job.Worktree, ownerRoot)
		}
		workDir = worktreePath
	} else {
		// No worktree, use the main git repository root
		workDir = gitRoot
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	workDir = ScopeToSubProject(workDir, job)

	// Normalize the path to get canonical case (important on macOS)
	// This ensures paths like /users/solom4/code become /Users/solom4/Code
	// which is required for matching Claude's project directory paths.
	if canonical, err := pathutil.CanonicalPath(workDir); err == nil {
		workDir = canonical
	}

	return workDir, nil
}

// resolveWorktreeForJob resolves the repository that owns a job's worktree and
// locates the worktree on disk. It is the shared detection logic behind the
// per-executor determineWorkDir/prepareWorktree helpers.
//
//   - ownerRoot is the repository the worktree belongs to: gitRoot itself, or
//     its owner when gitRoot is already inside a worktree checkout.
//   - worktreePath is the existing worktree when one is found, otherwise the
//     legacy creation target for ownerRoot.
//   - exists reports whether the worktree is present on disk.
//
// All layout knowledge is delegated to the core workspace helpers so the same
// code resolves legacy and (in later phases) XDG worktrees.
func resolveWorktreeForJob(gitRoot, worktreeName string) (ownerRoot, worktreePath string, exists bool) {
	ownerRoot = gitRoot
	if workspace.IsWorktreePath(gitRoot) {
		if owner, ok := workspace.WorktreeOwner(gitRoot); ok {
			ownerRoot = owner
		}
	}
	// Registry-first resolver so ANCHORED worktrees (created with
	// `--anchor <sub-repo>`, which live under the anchor repo's XDG base rather
	// than ownerRoot's) are found. Owner-scope is ownerRoot:
	// ResolveWorktreePathByName accepts any owner under it.
	if found, ok := workspace.ResolveWorktreePathByName(ownerRoot, worktreeName, []string{ownerRoot}); ok {
		return ownerRoot, found, true
	}
	return ownerRoot, workspace.ResolveNewWorktreePath(ownerRoot, worktreeName, false), false
}

// workspaceIsEcosystem reports whether gitRoot is an ecosystem root. Used to
// decide worktree layout (XDG for ecosystems, legacy for standalone) WITHOUT
// gating on the sibling list — an anchored full-ecosystem worktree persists an
// empty repos: yet must live in the XDG layout. Mirrors resolveWorktreeLayout's
// default and the ecosystem check workspace.Prepare itself uses.
func workspaceIsEcosystem(gitRoot string) bool {
	if node, _ := workspace.GetProjectByPath(gitRoot); node != nil {
		return node.IsEcosystem()
	}
	return false
}

// ScopeToSubProject adjusts a working directory to point to a sub-project
// within an ecosystem worktree when job.Repository is specified.
// This ensures that context generation, command execution, and agent sessions
// all operate in the correct sub-project directory rather than the ecosystem root.
func ScopeToSubProject(workDir string, job *Job) string {
	if job == nil || job.Repository == "" {
		return workDir
	}

	// If the working directory already ends with the repository name,
	// we're already scoped to the sub-project (e.g., when GetProjectGitRoot
	// returned a notebook-resolved project path that is itself the sub-project).
	if filepath.Base(workDir) == job.Repository {
		return workDir
	}

	subProjectPath := filepath.Join(workDir, job.Repository)
	if info, err := os.Stat(subProjectPath); err == nil && info.IsDir() {
		return subProjectPath
	}

	// Sub-project directory doesn't exist, return original workDir
	return workDir
}

// GetProjectRootSafe returns the project root using the workspace model.
// It supports both Grove projects (with grove.yml) and non-Grove repos.
// Falls back to git root or current directory if workspace discovery fails.
func GetProjectRootSafe(startPath string) string {
	// Try workspace discovery first - handles all workspace types including non-Grove repos
	if node, err := workspace.GetProjectByPath(startPath); err == nil {
		return node.Path
	}

	// Fallback to git root
	if root, err := git.GetGitRoot(startPath); err == nil {
		return root
	}

	// Last resort: use current directory
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}

	return startPath
}

// GetProjectRoot attempts to find the project root directory by searching upwards for a grove config file.
func GetProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get current working directory: %w", err)
	}

	configPath, err := config.FindConfigFile(dir)
	if err != nil {
		return "", fmt.Errorf("could not find project root (grove config) searching up from %s: %w", dir, err)
	}
	return filepath.Dir(configPath), nil
}

// GetGitRootSafe attempts to find git root with multiple fallback strategies.
//
// planDir is the authoritative input: each git-based and container-based
// strategy is applied to planDir BEFORE falling back to the current working
// directory. This ordering matters because an unrelated cwd (e.g. the repo a
// tool was launched from) must never mask a worktree container passed as
// planDir.
func GetGitRootSafe(planDir string) (string, error) {
	// planDir first (authoritative), then cwd as a last resort.
	candidates := []string{planDir}
	if cwd, err := os.Getwd(); err == nil && cwd != planDir {
		candidates = append(candidates, cwd)
	}

	for _, dir := range candidates {
		// Standard git-root lookup.
		if gitRoot, err := git.GetGitRoot(dir); err == nil {
			return gitRoot, nil
		}
		// Grove worktree container fallback (see resolveWorktreeContainerRoot).
		if root, ok := resolveWorktreeContainerRoot(dir); ok {
			return root, nil
		}
	}

	return "", fmt.Errorf("could not find git root from plan directory or current directory")
}

// resolveWorktreeContainerRoot resolves the git/ecosystem root for a Grove
// worktree container — the synthetic shape introduced by unified-worktrees
// Phase 1.
//
// A worktree created by `flow plan init --worktree` is now a synthetic
// CONTAINER: a directory under a worktree base (.grove-worktrees/ or the XDG
// base) holding a root grove.toml (workspaces=["*"]), a .grove/workspace
// marker, and one or more <repo>/ checkouts. The container itself has NO root
// .git, so the standard git-root lookup fails when dir IS (or is nested under)
// the container.
//
// We resolve via the workspace model: GetProjectByPath classifies the
// container as an ecosystem (Phase 1 writes the synthetic grove.toml that
// triggers this) and walks up from nested paths, so node.Path IS the container
// root — the correct "ecosystem/worktree root to operate on". This is the same
// path workspace.ResolveScope returns for the container, so daemon scope and
// git-root resolution agree. Downstream helpers that need the OWNER source repo
// (e.g. resolveWorktreeForJob, plan_session) already bubble up from a container
// via IsWorktreePath/WorktreeOwner/ParentProjectPath, so returning the
// container preserves their behavior while NOT discarding the container
// identity that scope- and context-generation callers require.
//
// Only when the workspace model does not classify dir as a worktree-rooted
// ecosystem (e.g. a missing/partial grove.toml) do we fall back to the marker
// owner, so resolution still yields a real git root rather than erroring.
func resolveWorktreeContainerRoot(dir string) (string, bool) {
	if node, err := workspace.GetProjectByPath(dir); err == nil && node != nil &&
		node.IsEcosystem() && workspace.IsWorktreePath(node.Path) {
		if canonical, cerr := pathutil.CanonicalPath(node.Path); cerr == nil {
			return canonical, true
		}
		return node.Path, true
	}
	if workspace.IsWorktreePath(dir) {
		if owner, ok := workspace.WorktreeOwner(dir); ok {
			return owner, true
		}
	}
	return "", false
}

// GetProjectGitRoot returns the git root for the project associated with a plan.
// If the plan is inside a notebook, it returns the associated project's path.
// Otherwise, it falls back to GetGitRootSafe.
func GetProjectGitRoot(planDir string) (string, error) {
	// Canonicalize first (absolute + symlinks + macOS case): notebook
	// detection is a string-prefix match against the configured notebook
	// root, so a case-variant or symlinked spelling of a notebook plan dir
	// (e.g. /Users/x/Notebooks/... for /Users/x/notebooks/...) would
	// otherwise silently fail detection and fall through to git/worktree
	// fallbacks rooted at the wrong checkout — the oracle-plays job-25
	// wrong-root turn.
	if canonical, err := pathutil.CanonicalPath(planDir); err == nil {
		planDir = canonical
	}

	// First check if the plan directory is inside a notebook
	if project, notebookRoot, _ := workspace.GetProjectFromNotebookPath(planDir); notebookRoot != "" && project != nil {
		// Plan is in a notebook - use the associated project's path
		return project.Path, nil
	}

	// Normal case - get git root from plan directory
	return GetGitRootSafe(planDir)
}

// resolveGitRootForWorktree resolves the project git root that worktree
// CREATION will run against, with retry + notebook-aware hard-fail semantics.
//
// Notebook plan dirs resolve via workspace discovery (GetProjectFromNotebookPath
// → findProjectByWorkspaceName), which can transiently find no project while
// racing the daemon's workspace collectors. The old callers silently fell back
// to `gitRoot = plan.Directory` on ANY error — for a notebook plan that
// fabricates a fresh worktree CONTAINER at the notebook plan dir, and
// SetupSubmodules then tries `git worktree add -B <branch>` for every repo,
// each failing with "branch already used by worktree" ("incomplete worktree:
// failed to create linked worktree(s) for N repo(s)"). A re-runnable failed
// job beats creating a bogus container, so notebook plans hard-fail instead.
func resolveGitRootForWorktree(ctx context.Context, planDir string) (string, error) {
	return resolveGitRootForWorktreeWith(ctx, planDir, GetProjectGitRoot, isUnderNotebook)
}

// resolveGitRootForWorktreeWith is the injectable core of
// resolveGitRootForWorktree (resolver/notebook-detector split out for tests).
func resolveGitRootForWorktreeWith(ctx context.Context, planDir string, resolveRoot func(string) (string, error), underNotebook func(string) bool) (string, error) {
	gitRoot, err := resolveRoot(planDir)
	if err == nil {
		return gitRoot, nil
	}

	// Non-notebook plan in a non-git dir (e.g. bare tmp plan dirs in tests):
	// resolution failure is deterministic, not a discovery race — keep the
	// historical fallback of treating the plan directory itself as the root.
	if !underNotebook(planDir) {
		return planDir, nil
	}

	// Notebook plan: the failure is the transient discovery race. It clears
	// within seconds (observed: runs minutes apart on the same worktree
	// succeed), so retry briefly before giving up.
	for _, backoff := range []time.Duration{100 * time.Millisecond, 250 * time.Millisecond} {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
		ulog.Warn("Git-root resolution failed for notebook plan; retrying").
			Err(err).
			Field("plan_dir", planDir).
			Field("backoff", backoff.String()).
			Log(ctx)
		if gitRoot, err = resolveRoot(planDir); err == nil {
			return gitRoot, nil
		}
	}

	return "", fmt.Errorf("could not resolve project git root for notebook plan %s (transient workspace-discovery failure — retry the job): %w", planDir, err)
}

// isUnderNotebook reports whether planDir lives inside a configured notebook.
// Detection is deterministic (config prefix match / notebook.yml marker), so a
// true here plus a GetProjectGitRoot failure means the PROJECT lookup raced —
// the case that must never fall back to fabricating a container at planDir.
// Canonicalized for the same reason as GetProjectGitRoot: a case-variant or
// symlinked spelling of a notebook plan dir must still be detected as
// notebook-resident, or resolveGitRootForWorktree's non-notebook fallback
// fabricates a worktree container at the plan dir.
func isUnderNotebook(planDir string) bool {
	if canonical, err := pathutil.CanonicalPath(planDir); err == nil {
		planDir = canonical
	}
	_, notebookRoot, _ := workspace.GetProjectFromNotebookPath(planDir)
	return notebookRoot != ""
}

// ResolveProjectForSessionNaming resolves the appropriate project for tmux session naming.
// If workDir is inside a notebook, it returns the associated project.
// Otherwise, it returns the project at workDir.
func ResolveProjectForSessionNaming(workDir string) (*workspace.WorkspaceNode, error) {
	// First check if we're in a notebook
	if project, notebookRoot, _ := workspace.GetProjectFromNotebookPath(workDir); notebookRoot != "" && project != nil {
		return project, nil
	}
	// Normal case - get project at workDir
	return workspace.GetProjectByPath(workDir)
}

// ResolveWorkingDirectory determines the appropriate working directory for command execution
func ResolveWorkingDirectory(plan *Plan) string {
	// If we're in a git repository, use its root (notebook-aware)
	if gitRoot, err := GetProjectGitRoot(plan.Directory); err == nil {
		return gitRoot
	}

	// Otherwise use current working directory
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}

	// Last resort: use plan directory
	return plan.Directory
}

// ResolveLogDirectory determines where log files should be written
func ResolveLogDirectory(plan *Plan, job *Job) string {
	// Try to use project root first
	if cwd, err := os.Getwd(); err == nil {
		logDir := filepath.Join(cwd, ".grove", "logs", plan.Name)
		if node, err := workspace.GetProjectByPath(cwd); err == nil {
			locator := grovecontext.NewManager(cwd).Locator()
			if ctxDir, locErr := locator.GetContextDir(node); locErr == nil {
				logDir = filepath.Join(ctxDir, "logs", plan.Name)
			}
		}
		if err := os.MkdirAll(logDir, 0o755); err == nil {
			return logDir
		}
	}

	// Fall back to plan directory
	return filepath.Join(plan.Directory, ".logs")
}

// ResolvePromptSource resolves a prompt source file with multiple strategies
func ResolvePromptSource(source string, plan *Plan) (string, error) {
	// If absolute path, use as-is
	if filepath.IsAbs(source) {
		return source, nil
	}

	// Try multiple resolution strategies
	candidates := []string{
		// Relative to plan directory
		filepath.Join(plan.Directory, source),
		// Relative to parent of plan directory (for sibling plans)
		filepath.Join(filepath.Dir(plan.Directory), source),
		// Relative to current working directory
		source,
	}

	// If we can determine a project root, also try that
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, source))
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("could not find prompt source: %s", source)
}

// FindContextFiles looks for context files in multiple locations
func FindContextFiles(plan *Plan) []string {
	var contextFiles []string

	// Resolve context path using centralized manager
	planCtxMgr := grovecontext.NewManager(plan.Directory)
	contextPath := planCtxMgr.ResolveContextPath()

	candidates := []string{
		contextPath,
		filepath.Join(plan.Directory, "CLAUDE.md"),
	}

	// Also check current working directory / project root
	if cwd, err := os.Getwd(); err == nil {
		cwdCtxMgr := grovecontext.NewManager(cwd)
		cwdContextPath := cwdCtxMgr.ResolveContextPath()
		candidates = append(candidates,
			cwdContextPath,
			filepath.Join(cwd, "CLAUDE.md"),
		)
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			contextFiles = append(contextFiles, candidate)
		}
	}

	return contextFiles
}

// ResolveTemplate resolves a template file path
func ResolveTemplate(templateName string, plan *Plan) (string, error) {
	// If it looks like a path, resolve it as a prompt source
	if strings.Contains(templateName, "/") || strings.HasSuffix(templateName, ".md") {
		return ResolvePromptSource(templateName, plan)
	}

	// Otherwise, look for built-in templates
	// First check plan directory for custom templates
	customTemplate := filepath.Join(plan.Directory, "templates", templateName+".md")
	if _, err := os.Stat(customTemplate); err == nil {
		return customTemplate, nil
	}

	// Check for built-in templates
	builtinTemplate := filepath.Join("internal", "orchestration", "builtin_templates", templateName+".md")
	if _, err := os.Stat(builtinTemplate); err == nil {
		return builtinTemplate, nil
	}

	// Try as a plain file in the plan directory
	plainFile := filepath.Join(plan.Directory, templateName+".md")
	if _, err := os.Stat(plainFile); err == nil {
		return plainFile, nil
	}

	return "", fmt.Errorf("template not found: %s", templateName)
}

// ResolveJobSkillContent resolves a job's skill field and returns the raw SKILL.md
// content for inlining into the prompt. Returns ("", nil) if no skill is configured.
// Returns an error if a skill is declared but cannot be resolved, since running a
// job without its declared skill produces silently wrong results.
//
// Uses the skills package's full resolution chain: notebook > user > embedded.
// workDir is used to resolve the workspace context for notebook skill discovery.
func ResolveJobSkillContent(job *Job, workDir string) (string, error) {
	if job.Skill == "" {
		return "", nil
	}

	// Use workspace-aware, access-controlled skill resolution.
	loadedSkill, err := skills.LoadAuthorizedSkill(workDir, job.Skill)
	if err != nil {
		return "", fmt.Errorf("skill resolution failed for %q: %w", job.Skill, err)
	}

	skillContent, ok := loadedSkill.Files["SKILL.md"]
	if !ok || len(skillContent) == 0 {
		return "", fmt.Errorf("skill %q has no SKILL.md content", job.Skill)
	}

	return stripSkillFrontmatter(skillContent), nil
}

// stripSkillFrontmatter removes YAML frontmatter from skill content, returning only the body.
func stripSkillFrontmatter(content []byte) string {
	_, body, err := ParseFrontmatter(content)
	if err != nil {
		return string(content)
	}
	return strings.TrimSpace(string(body))
}

// MaxSequenceDepth is the maximum nesting depth for recursive skill sequences.
const MaxSequenceDepth = 3

// SkillSequenceNode represents a skill in a sequence tree, with optional children
// for skills that declare their own skill_sequence in frontmatter.
type SkillSequenceNode struct {
	Metadata skills.SkillMetadata
	Children []SkillSequenceNode
}

// ResolveSkillSequenceMetadata builds a recursive tree of skills to execute.
// Root-level skills are authorized via LoadAuthorizedSkill; nested sub-skills
// are implicitly authorized by the parent via LoadSkillBypassingAccess.
func ResolveSkillSequenceMetadata(skillNames []string, workDir string) ([]SkillSequenceNode, error) {
	return resolveSequenceRecursive(skillNames, workDir, 0, make(map[string]bool))
}

// ResolveSkillSequenceWithParent builds a recursive tree of skills to execute,
// treating the sequence as implicitly authorized by the given parent skill.
// When parentSkill is non-empty, resolution starts at depth 1 (bypassing access
// control), since the parent skill is already authorized and its sub-skills
// inherit that authorization.
func ResolveSkillSequenceWithParent(skillNames []string, workDir, parentSkill string) ([]SkillSequenceNode, error) {
	if parentSkill != "" {
		return resolveSequenceRecursive(skillNames, workDir, 1, make(map[string]bool))
	}
	return resolveSequenceRecursive(skillNames, workDir, 0, make(map[string]bool))
}

func resolveSequenceRecursive(skillNames []string, workDir string, depth int, visited map[string]bool) ([]SkillSequenceNode, error) {
	if depth > MaxSequenceDepth {
		return nil, fmt.Errorf("skill sequence max depth (%d) exceeded", MaxSequenceDepth)
	}

	var sequence []SkillSequenceNode
	for _, skillName := range skillNames {
		if visited[skillName] {
			return nil, fmt.Errorf("circular skill sequence dependency detected: %s", skillName)
		}

		// Track visited state for this branch to prevent false positives across siblings
		branchVisited := make(map[string]bool)
		for k, v := range visited {
			branchVisited[k] = v
		}
		branchVisited[skillName] = true

		var loadedSkill *skills.LoadedSkill
		var err error

		// Root skills must be explicitly authorized; nested sub-skills are implicitly authorized
		if depth == 0 {
			loadedSkill, err = skills.LoadAuthorizedSkill(workDir, skillName)
		} else {
			loadedSkill, err = skills.LoadSkillBypassingAccess(workDir, skillName)
		}
		if err != nil {
			return nil, fmt.Errorf("resolving skill '%s' for sequence: %w", skillName, err)
		}

		content, ok := loadedSkill.Files["SKILL.md"]
		if !ok {
			return nil, fmt.Errorf("skill '%s' missing SKILL.md", skillName)
		}

		meta, err := skills.ParseSkillFrontmatter(content)
		if err != nil {
			return nil, fmt.Errorf("parsing metadata for skill '%s': %w", skillName, err)
		}

		node := SkillSequenceNode{Metadata: *meta}

		if len(meta.SkillSequence) > 0 {
			children, err := resolveSequenceRecursive(meta.SkillSequence, workDir, depth+1, branchVisited)
			if err != nil {
				return nil, err
			}
			node.Children = children
		}

		sequence = append(sequence, node)
	}

	return sequence, nil
}
