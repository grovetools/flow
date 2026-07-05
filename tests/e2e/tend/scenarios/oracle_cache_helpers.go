package scenarios

// Shared plumbing for the oracle-cache-lineage suite (spec 19 §5): every
// scenario runs real chat turns through the flow binary against the mock LLM
// (GROVE_MOCK_LLM_RESPONSE_FILE) and asserts on the on-disk artifacts the
// layer engine + request manifest write — context-layers/, layers.json,
// snapshot.json, request-manifest-<turnID>.json, job.log, and job
// frontmatter. No scenario ever touches a live API.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/grovetools/tend/pkg/command"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"

	"github.com/grovetools/flow/pkg/orchestration"
)

// Source fixtures for the layer store. Comment-free on purpose: the default
// strip_comments=true then renders files byte-identical to what we wrote,
// keeping content assertions simple.
const (
	oracleAlphaV1 = "package main\n\nfunc Alpha() int {\n\treturn 1\n}\n"
	oracleAlphaV2 = "package main\n\nfunc Alpha() int {\n\treturn 100 + AlphaEdited()\n}\n\nfunc AlphaEdited() int {\n\treturn 42\n}\n"
	oracleBetaV1  = "package main\n\nfunc Beta() int {\n\treturn 2\n}\n"
)

// oracleCacheEnv carries the per-scenario sandbox layout.
type oracleCacheEnv struct {
	ProjectDir string
	PlanName   string
	PlanPath   string
	MockFile   string
	Repo       *git.Git
}

// setupOracleCacheEnv builds the standard suite sandbox: a git project with
// alpha.go/beta.go, a default .grove/rules, a job rules file (base.rules ->
// alpha.go only), a mock LLM response file, and an initialized plan.
func setupOracleCacheEnv(ctx *harness.Context, name string) (*oracleCacheEnv, error) {
	projectName := "oc-" + name
	planName := "oc-" + name + "-plan"

	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
	if err != nil {
		return nil, err
	}

	if err := fs.WriteString(filepath.Join(projectDir, "alpha.go"), oracleAlphaV1); err != nil {
		return nil, err
	}
	if err := fs.WriteString(filepath.Join(projectDir, "beta.go"), oracleBetaV1); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".grove"), 0o755); err != nil {
		return nil, err
	}
	if err := fs.WriteString(filepath.Join(projectDir, ".grove", "rules"), "*.go\n"); err != nil {
		return nil, err
	}
	// base.rules: the job-scoped context surface — alpha.go only, so widening
	// scenarios have beta.go to add.
	if err := fs.WriteString(filepath.Join(projectDir, "base.rules"), "alpha.go\n"); err != nil {
		return nil, err
	}

	repo := git.New(projectDir)
	if err := repo.AddCommit("seed oracle-cache fixtures"); err != nil {
		return nil, err
	}

	mockFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
	if err := fs.WriteString(mockFile, "Mock oracle response."); err != nil {
		return nil, err
	}
	ctx.Set("llm_response_file", mockFile)

	planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
	initCmd := ctx.Bin("plan", "init", planName)
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return nil, fmt.Errorf("plan init failed: %w\nStderr: %s", result.Error, result.Stderr)
	}
	ctx.Set("plan_path", planPath)

	env := &oracleCacheEnv{
		ProjectDir: projectDir,
		PlanName:   planName,
		PlanPath:   planPath,
		MockFile:   mockFile,
		Repo:       repo,
	}
	ctx.Set("oracle_cache_env", env)
	return env, nil
}

// oracleEnv fetches the env stored by setupOracleCacheEnv.
func oracleEnv(ctx *harness.Context) *oracleCacheEnv {
	return ctx.Get("oracle_cache_env").(*oracleCacheEnv)
}

// addChat adds a chat job to the plan and applies extra frontmatter (e.g.
// rules_file, model, cache_ttl). Returns the loaded job.
func (e *oracleCacheEnv) addChat(ctx *harness.Context, title, prompt string, frontmatter map[string]interface{}, extraAddArgs ...string) (*orchestration.Job, error) {
	args := append([]string{"plan", "add", e.PlanName, "--type", "chat", "--title", title, "-p", prompt}, extraAddArgs...)
	addCmd := ctx.Bin(args...)
	addCmd.Dir(e.ProjectDir)
	if err := addCmd.Run().AssertSuccess(); err != nil {
		return nil, fmt.Errorf("adding chat job %s: %w", title, err)
	}
	if len(frontmatter) > 0 {
		if err := updateJobFrontmatter(e.PlanPath, title, frontmatter); err != nil {
			return nil, fmt.Errorf("updating %s frontmatter: %w", title, err)
		}
	}
	return e.job(title)
}

// job loads the freshest state of the job with the given title.
func (e *oracleCacheEnv) job(title string) (*orchestration.Job, error) {
	plan, err := orchestration.LoadPlan(e.PlanPath)
	if err != nil {
		return nil, err
	}
	for _, job := range plan.Jobs {
		if job.Title == title {
			return job, nil
		}
	}
	return nil, fmt.Errorf("no job titled %q in plan %s", title, e.PlanPath)
}

// runTurn executes one targeted chat turn through the real binary under the
// mock LLM: `flow plan run <target> --local --yes [extraArgs...]`.
func (e *oracleCacheEnv) runTurn(ctx *harness.Context, target string, extraArgs ...string) *command.Result {
	args := append([]string{"plan", "run", target, "--local", "--yes"}, extraArgs...)
	cmd := ctx.Bin(args...)
	cmd.Dir(e.ProjectDir)
	cmd.Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + e.MockFile)
	result := cmd.Run()
	ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
	return result
}

// appendUserTurn appends a new user message after the chat's trailing grove
// marker, making the chat runnable for its next turn.
func appendUserTurn(jobPath, text string) error {
	f, err := os.OpenFile(jobPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + text + "\n")
	return err
}

// layersDir / snapshotPath / jobLogPath locate the job's cache artifacts.
func (e *oracleCacheEnv) layersDir(jobID string) string {
	return orchestration.ContextLayersDir(e.PlanPath, jobID)
}

func (e *oracleCacheEnv) snapshotPath(jobID string) string {
	return orchestration.LayerSnapshotPath(e.PlanPath, jobID)
}

func (e *oracleCacheEnv) jobLogPath(jobID string) string {
	return filepath.Join(e.PlanPath, ".artifacts", jobID, "job.log")
}

// loadLayers reads the job's layers.json; a missing manifest is an error here
// (scenarios that expect absence assert on the directory instead).
func (e *oracleCacheEnv) loadLayers(jobID string) (*orchestration.LayerManifest, error) {
	m, err := orchestration.LoadLayerManifest(e.layersDir(jobID))
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("no layers.json for job %s (expected a layer store at %s)", jobID, e.layersDir(jobID))
	}
	return m, nil
}

// loadManifests reads every request-manifest-<turnID>.json for the job,
// ordered by creation time (one per executed turn).
func (e *oracleCacheEnv) loadManifests(jobID string) ([]orchestration.RequestManifest, error) {
	pattern := filepath.Join(e.PlanPath, ".artifacts", jobID, "request-manifest-*.json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	manifests := make([]orchestration.RequestManifest, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var m orchestration.RequestManifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", p, err)
		}
		manifests = append(manifests, m)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].CreatedAt.Before(manifests[j].CreatedAt) })
	return manifests, nil
}

// entriesOfKind filters a manifest's entries by block kind
// (system|layer|context|history|turn).
func entriesOfKind(m orchestration.RequestManifest, kind string) []orchestration.RequestManifestEntry {
	var out []orchestration.RequestManifestEntry
	for _, e := range m.Entries {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// breakpointCount counts entries carrying a cache breakpoint.
func breakpointCount(m orchestration.RequestManifest) int {
	n := 0
	for _, e := range m.Entries {
		if e.Breakpoint {
			n++
		}
	}
	return n
}

// entryKinds returns the manifest's block kinds in emission order.
func entryKinds(m orchestration.RequestManifest) []string {
	kinds := make([]string, len(m.Entries))
	for i, e := range m.Entries {
		kinds[i] = e.Kind
	}
	return kinds
}

// sha256File hashes a file's bytes the same way the layer store and request
// manifest do.
func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// sha256String hashes a string (for history-block <-> manifest comparisons).
func sha256String(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// findLayerBySource returns the first layer entry with the given provenance
// source, or an error naming what the manifest actually holds.
func findLayerBySource(m *orchestration.LayerManifest, source string) (*orchestration.LayerEntry, error) {
	for i := range m.Layers {
		if m.Layers[i].Source == source {
			return &m.Layers[i], nil
		}
	}
	return nil, fmt.Errorf("no layer with source %q (have: %s)", source, describeLayers(m))
}

// describeLayers renders a compact layers.json summary for error messages.
func describeLayers(m *orchestration.LayerManifest) string {
	parts := make([]string, len(m.Layers))
	for i, l := range m.Layers {
		parts[i] = fmt.Sprintf("%d:%s(%s)", l.N, l.File, l.Source)
	}
	return strings.Join(parts, ", ")
}

// layerFilePaths lists the record paths captured by one layer entry.
func layerFilePaths(l *orchestration.LayerEntry) []string {
	paths := make([]string, len(l.Files))
	for i, f := range l.Files {
		paths[i] = f.Path
	}
	sort.Strings(paths)
	return paths
}

// assertJobStatus reloads the job and checks its persisted status.
func (e *oracleCacheEnv) assertJobStatus(title string, want orchestration.JobStatus) error {
	job, err := e.job(title)
	if err != nil {
		return err
	}
	if job.Status != want {
		return fmt.Errorf("job %q: expected status %q, got %q (last_error: %q)", title, want, job.Status, job.Metadata.LastError)
	}
	return nil
}

// readJobLog returns the job.log content ("" when absent).
func (e *oracleCacheEnv) readJobLog(jobID string) string {
	data, err := os.ReadFile(e.jobLogPath(jobID))
	if err != nil {
		return ""
	}
	return string(data)
}
