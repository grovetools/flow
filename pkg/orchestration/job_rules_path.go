package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	grovecontext "github.com/grovetools/cx/pkg/context"
)

// ResolveJobRulesFilePath resolves a job's `rules_file:` frontmatter to an
// on-disk path, trying, in order: the plan directory, the process working
// directory, the project git root, an absolute path, and finally a named cx
// preset (notebook presets, .cx/, .cx.work/).
//
// An empty file is treated as a miss, matching the absent-file path used for
// newly created jobs: a rules file nobody has authored yet must fail through
// the same pre-provider error funnel as one that does not exist.
//
// This is the single resolver for the frontmatter field. The chat path uses it
// on its way to cx generation; the pi-session launch path uses it to size and
// freeze context without generating anything. Two copies of a five-step
// fallback cascade is exactly how the two paths would come to disagree about
// which file a job's rules_file names.
func ResolveJobRulesFilePath(plan *Plan, job *Job, contextDir string) (string, error) {
	if job == nil || job.RulesFile == "" {
		return "", fmt.Errorf("job declares no rules_file")
	}

	candidates := []string{}
	if plan != nil {
		candidates = append(candidates, filepath.Join(plan.Directory, job.RulesFile))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, job.RulesFile))
	}
	if plan != nil {
		if gitRoot, err := GetProjectGitRoot(plan.Directory); err == nil {
			candidates = append(candidates, filepath.Join(gitRoot, job.RulesFile))
		}
	}
	if filepath.IsAbs(job.RulesFile) {
		candidates = append(candidates, job.RulesFile)
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, validateRulesFileNonEmpty(candidate, job.RulesFile)
		}
	}

	// Named preset: strip the .rules extension and ask cx to find it.
	presetName := strings.TrimSuffix(job.RulesFile, ".rules")
	if resolved, err := grovecontext.NewManager(contextDir).FindRulesetFile(contextDir, presetName); err == nil {
		return resolved, validateRulesFileNonEmpty(resolved, job.RulesFile)
	}

	return "", rulesFileNotFoundError(job.RulesFile)
}

func validateRulesFileNonEmpty(path, declared string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return rulesFileNotFoundError(declared)
	}
	return nil
}

// rulesFileNotFoundError is the one wording for an unresolvable rules_file.
// Kept verbatim from the chat path so existing tests and user muscle memory
// still match.
func rulesFileNotFoundError(declared string) error {
	return fmt.Errorf("rules file '%s' not found in plan directory, current directory, git root, or named presets", declared)
}
