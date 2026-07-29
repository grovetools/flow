package orchestration

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/sirupsen/logrus"
)

// AccessedFile is one deduped row from a job's accessed_files.jsonl trace:
// the file's absolute path, the action of the most recent access, how many
// times it was touched, and the timestamp of the last access.
type AccessedFile struct {
	Path          string `json:"path"`
	Action        string `json:"action"`
	Count         int    `json:"count"`
	LastTimestamp string `json:"last_timestamp"`
}

// accessedFileEntry mirrors the row shape written by the hooks repo
// (appendFileAccessEntries): {"timestamp","tool","path","action"}.
type accessedFileEntry struct {
	Timestamp string `json:"timestamp"`
	Tool      string `json:"tool"`
	Path      string `json:"path"`
	Action    string `json:"action"`
}

// AccessedFilesPath locates the accessed_files.jsonl for a job within planDir.
// The artifacts dir is named after the job ID for flow-launched sessions, but
// the hooks writer can also fall back to other identifiers, so the job's
// filename (with and without extension) is tried too. Returns "" when no
// trace file exists.
func AccessedFilesPath(planDir string, job *Job) string {
	var candidates []string
	if job.ID != "" {
		candidates = append(candidates, job.ID)
	}
	if job.Filename != "" {
		candidates = append(candidates,
			strings.TrimSuffix(job.Filename, filepath.Ext(job.Filename)),
			job.Filename,
		)
	}
	seen := make(map[string]bool, len(candidates))
	for _, name := range candidates {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		p := filepath.Join(planDir, ".artifacts", name, "accessed_files.jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// AccessedFilesBase resolves the base directory that a job's relative trace
// entries were recorded against: the job's working directory (worktree root,
// scoped to job.Repository when set). Returns "" when the working directory
// cannot be resolved (e.g. the worktree has since been removed) — callers
// still get cleaned relative paths, just not absolute ones.
func AccessedFilesBase(plan *Plan, job *Job) string {
	base, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return ""
	}
	return base
}

// absolutizeAccessedPath converts one trace entry path to a normalized,
// preferably absolute form. The hooks writer records paths exactly as the
// tools reported them, relative to the tool call's cwd — usually the job's
// worktree root, but agents cd into sub-repos of an ecosystem worktree, so a
// row like "pkg/context/cache.go" may really live at <base>/cx/pkg/context/
// cache.go. When <base>/<rel> does not exist, exactly one immediate
// subdirectory of base containing the file disambiguates it; otherwise the
// base-joined path is kept as the best effort.
func absolutizeAccessedPath(baseDir, p string) string {
	p = filepath.Clean(p)
	if filepath.IsAbs(p) || baseDir == "" {
		return p
	}
	primary := filepath.Join(baseDir, p)
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return primary
	}
	match := ""
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		cand := filepath.Join(baseDir, e.Name(), p)
		if _, err := os.Stat(cand); err == nil {
			if match != "" {
				return primary // ambiguous: more than one sub-repo has this path
			}
			match = cand
		}
	}
	if match != "" {
		return match
	}
	return primary
}

// ReadAccessedFiles parses the jsonl trace at path into a deduped list: one
// row per file, carrying the last action, total access count, and last
// timestamp, ordered by most-recent access last (append order of the last
// occurrence). Relative entries are absolutized against baseDir (the job's
// working directory; see AccessedFilesBase) and normalized before dedup, so
// the same file accessed via different spellings collapses into one row. A
// missing or empty path yields an empty list, not an error; malformed lines
// are skipped.
func ReadAccessedFiles(path, baseDir string) ([]AccessedFile, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	type agg struct {
		file      AccessedFile
		lastIndex int
	}
	byPath := make(map[string]*agg)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	index := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry accessedFileEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Path == "" {
			continue
		}
		normPath := absolutizeAccessedPath(baseDir, entry.Path)
		a, ok := byPath[normPath]
		if !ok {
			a = &agg{file: AccessedFile{Path: normPath}}
			byPath[normPath] = a
		}
		a.file.Count++
		a.file.Action = entry.Action
		a.file.LastTimestamp = entry.Timestamp
		a.lastIndex = index
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	aggs := make([]*agg, 0, len(byPath))
	for _, a := range byPath {
		aggs = append(aggs, a)
	}
	sort.Slice(aggs, func(i, j int) bool { return aggs[i].lastIndex < aggs[j].lastIndex })
	files := make([]AccessedFile, len(aggs))
	for i, a := range aggs {
		files[i] = a.file
	}
	return files, nil
}

// ReadJobAccessedFiles locates and parses the accessed-files trace for job,
// absolutizing relative entries against the job's working directory. A job
// with no trace yields an empty list.
func ReadJobAccessedFiles(plan *Plan, job *Job) ([]AccessedFile, error) {
	return ReadAccessedFiles(AccessedFilesPath(plan.Directory, job), AccessedFilesBase(plan, job))
}

// WorkspaceRootedPath converts an absolute path to cx-style workspace-rooted
// form (<repo>/rel/path), naming a worktree file by its parent repo so the
// result is worktree-unrooted. Falls back to the input path when the file is
// not under any known workspace.
//
// The rooting itself lives on the provider so treemux's accessed-files drawer
// renders paths identically to `flow plan files --workspace-rooted`; this stays
// as the name flow's own callers already use.
func WorkspaceRootedPath(provider *workspace.Provider, absPath string) string {
	return provider.RootedPath(absPath)
}

// NewDisplayWorkspaceProvider builds a workspace provider for workspace-rooted
// display naming. It prefers the daemon's cached workspace graph (connect-only,
// never autostarts) and falls back to direct discovery.
func NewDisplayWorkspaceProvider(ctx context.Context) (*workspace.Provider, error) {
	client := daemon.New()
	defer client.Close()
	if client.IsRunning() {
		if nodes, err := client.GetWorkspaces(ctx); err == nil {
			return workspace.NewProviderFromNodes(nodes), nil
		}
	}
	logger := logrus.New()
	logger.SetOutput(os.Stderr)
	logger.SetLevel(logrus.ErrorLevel)
	nodes, err := workspace.GetProjects(logger)
	if err != nil {
		return nil, err
	}
	return workspace.NewProviderFromNodes(nodes), nil
}
