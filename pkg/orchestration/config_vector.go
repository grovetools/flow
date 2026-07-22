package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/git"
	"github.com/grovetools/eval/pkg/record"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	"github.com/grovetools/skills/pkg/skills"
	"github.com/sirupsen/logrus"
)

// Reserved component keys (D10). A harness-supplied component may not collide
// with one of these — the executor owns their values, and letting an external
// input shadow them would make two genuinely different configurations hash the
// same.
const (
	componentPrompt   = "prompt"
	componentContext  = "context"
	componentMemory   = "memory"
	componentSkills   = "skills"
	componentPlan     = "plan"
	componentBriefing = "briefing"
)

var reservedComponentKeys = map[string]struct{}{
	componentPrompt:   {},
	componentContext:  {},
	componentMemory:   {},
	componentSkills:   {},
	componentPlan:     {},
	componentBriefing: {},
}

// ConfigVectorArtifactPath returns the on-disk location of a job's config
// vector, mirroring the convention used by WriteBriefingFile and
// TokenUsageArtifactPath.
func ConfigVectorArtifactPath(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, "config-vector.json")
}

// AgentConfigArtifactPath is the isolated, per-run config consumed by the
// grove-pi metrics extension. It deliberately lives with the job artifacts,
// never in the shared worktree's .pi directory.
func AgentConfigArtifactPath(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, "grove-config.json")
}

type agentConfigArtifact struct {
	Version        int               `json:"version"`
	Config         map[string]string `json:"config,omitempty"`
	Model          string            `json:"model"`
	Provider       string            `json:"provider,omitempty"`
	FixtureCommit  string            `json:"fixture_commit,omitempty"`
	WorktreeCommit string            `json:"worktree_commit,omitempty"`
	BundleFiles    []string          `json:"bundle_files,omitempty"`
}

// hashBytes returns the lowercase, full-length hex sha256 of b (D37).
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashFileSet concatenates the bytes of paths in the given order and returns
// their hash. A file that cannot be read contributes empty bytes rather than
// failing the stamp — capture must never fail a job. The second return value
// reports whether any path was supplied at all, so the caller can omit the
// component key entirely rather than emit the hash of nothing (D4/D10).
func hashFileSet(paths []string) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}
	h := sha256.New()
	for _, p := range paths {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			// Missing/unreadable file contributes empty bytes. Deliberate: the
			// vector must remain a total function over its inputs.
			continue
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// resolveSkillBytes concatenates the rendered SKILL.md bodies that back this
// job, in the same order and via the same resolution chain the briefing uses
// (ResolveJobSkillContent for job.Skill, then a leaf-order walk of the
// sequence tree mirroring collectSkillContent). Returns ok=false when the job
// declares no skills at all.
//
// This deliberately does NOT reuse skills.computeSkillManifest: that helper's
// fileStamp hashes size+mtime, which is the explicit D11 anti-precedent.
func resolveSkillBytes(job *Job, workDir string) (string, bool) {
	var b strings.Builder

	if job.Skill != "" {
		content, err := ResolveJobSkillContent(job, workDir)
		if err == nil {
			b.WriteString(content)
		}
	}

	if len(job.SkillSequence) > 0 {
		var nodes []SkillSequenceNode
		var err error
		if job.Skill != "" {
			nodes, err = ResolveSkillSequenceWithParent(job.SkillSequence, workDir, job.Skill)
		} else {
			nodes, err = ResolveSkillSequenceMetadata(job.SkillSequence, workDir)
		}
		if err == nil {
			appendSkillSequenceBytes(&b, nodes, workDir)
		}
	}

	if b.Len() == 0 {
		return "", false
	}
	return hashBytes([]byte(b.String())), true
}

// appendSkillSequenceBytes walks the sequence tree in leaf order, matching
// collectSkillContent's traversal so the hashed bytes correspond to the
// content the sequence actually resolves to.
func appendSkillSequenceBytes(b *strings.Builder, nodes []SkillSequenceNode, workDir string) {
	for _, node := range nodes {
		if len(node.Children) > 0 {
			appendSkillSequenceBytes(b, node.Children, workDir)
			continue
		}
		loadedSkill, err := skills.LoadSkillBypassingAccess(workDir, node.Metadata.Name)
		if err != nil {
			continue
		}
		content, ok := loadedSkill.Files["SKILL.md"]
		if !ok {
			continue
		}
		b.WriteString(stripSkillFrontmatter(content))
	}
}

// resolveEffectiveModel applies the oneshot/chat model precedence chain:
// CLI override > chat turn directive > job frontmatter > plan config >
// global config > the Anthropic default, then resolves aliases.
//
// It is the ONE chain in the codebase: both (*OneShotExecutor).Execute and
// executeChatJob call it, so no execution path can pick a model by a rule this
// function does not know. The chat directive branch lives here rather than in
// a private copy inside executeChatJob because that copy is exactly what let a
// `model:` directive select a model the config vector never heard of.
//
// directive is the chat turn's active directive; oneshot callers pass nil.
func resolveEffectiveModel(cfg *ExecutorConfig, job *Job, plan *Plan, directive *ChatDirective) (model, source string) {
	switch {
	case cfg != nil && cfg.ModelOverride != "":
		model, source = cfg.ModelOverride, "CLI override"
	case directive != nil && directive.Model != "":
		model, source = directive.Model, "chat directive"
	case job != nil && job.Model != "":
		model, source = job.Model, "job frontmatter"
	case plan != nil && plan.Config != nil && plan.Config.Model != "":
		model, source = plan.Config.Model, "plan config"
	case plan != nil && plan.Orchestration != nil && plan.Orchestration.OneshotModel != "":
		model, source = plan.Orchestration.OneshotModel, "global config"
	default:
		model, source = anthropicmodels.DefaultModel, "default fallback"
	}
	return resolveModelAlias(model), source
}

// ComputeJobConfigVector builds the provider-neutral description of everything
// held fixed for this job: a content hash per rendered component, plus the
// scalars that identify the run's configuration.
//
// briefingPath is a path rather than the briefing string because the executors
// do not agree on a single content variable — InteractiveAgentExecutor writes
// two different contents on two branches and only the resulting path survives
// to the convergence point. Reading the bytes back from the file that
// WriteBriefingFile just wrote gives one uniform code path across all five
// stamp sites, and is literally D11's "hash the exact rendered artifact bytes".
//
// This function never fails: every component that cannot be resolved is simply
// omitted, because nil/absent means "not measured" (D4) and a stamping failure
// must never fail a job.
//
// resolvedModel is the model the calling execution path ACTUALLY resolved and
// ran on (resolveEffectiveModel's output at the call site); for oneshot/chat
// it is stamped verbatim. This function deliberately re-derives nothing: a
// stamp that runs the precedence chain itself can disagree with the run — a
// chat `model:` directive did exactly that, because the copy of the chain
// reached from here had no directive case — whereas a stamp that can only
// record the value it was handed cannot drift by construction. Agent-family
// callers pass "": their model is job.Model.
func ComputeJobConfigVector(
	job *Job,
	plan *Plan,
	resolvedModel string,
	workDir string,
	jobCtx *jobContextPaths,
	contextFiles []string,
	briefingPath string,
) record.ConfigVector {
	v := record.ConfigVector{
		Components: map[string]string{},
	}

	// briefing — the exact bytes handed to the runtime (claude-only umbrella
	// key, D10).
	if briefingPath != "" {
		if b, err := os.ReadFile(briefingPath); err == nil {
			v.Components[componentBriefing] = hashBytes(b)
		}
	}

	// prompt — the resolved template body, i.e. the same bytes BuildXMLPrompt
	// embeds. Omitted when the job declares no template.
	if job != nil && job.Template != "" {
		if tmpl, err := NewTemplateManager().FindTemplate(job.Template); err == nil && tmpl != nil {
			v.Components[componentPrompt] = hashBytes([]byte(tmpl.Prompt))
		}
	}

	// skills — concatenated resolved SKILL.md bodies in sequence order.
	if job != nil {
		if h, ok := resolveSkillBytes(job, workDir); ok {
			v.Components[componentSkills] = h
		}
	}

	// plan — the job's own prompt body.
	if job != nil && job.PromptBody != "" {
		v.Components[componentPlan] = hashBytes([]byte(job.PromptBody))
	}

	// context — the D10 family split. oneshot/chat carry generated cold+hot
	// bundles; agent families carry a gathered file list. Cold precedes hot,
	// and that order is load-bearing: swapping it is a different hash.
	switch {
	case jobCtx != nil:
		if h, ok := hashFileSet([]string{jobCtx.Cold, jobCtx.Hot}); ok {
			v.Components[componentContext] = h
		}
	case len(contextFiles) > 0:
		if h, ok := hashFileSet(contextFiles); ok {
			v.Components[componentContext] = h
		}
	}

	// Harness-supplied components (P1-06). Merged before Hash() so they land
	// inside ConfigHash; reserved keys are refused rather than allowed to
	// shadow an executor-owned value.
	if job != nil {
		for k, val := range job.ConfigComponents {
			if _, reserved := reservedComponentKeys[k]; reserved {
				logrus.Warnf("config vector: ignoring harness-supplied component %q: collides with a reserved key", k)
				continue
			}
			v.Components[k] = val
		}
	}

	// Scalars.
	if job != nil {
		switch job.Type {
		case JobTypeOneshot, JobTypeChat:
			// Grove-internal API calls: the model is whatever the execution
			// path resolved and handed us, and Provider stays empty because
			// there is no agent runtime involved.
			v.Model = resolvedModel
		default:
			// Agent families: the model that actually reaches the agent is
			// job.Model, passed through as the provider's model flag. It may
			// legitimately be empty — do not invent a default.
			v.Model = job.Model
			v.Provider = ResolveJobProviderNameFromConfig(job)
		}
	}

	// WorktreeCommit — the full-length hash actually checked out at stamp time
	// (D37). On any error leave it empty and continue.
	if workDir != "" {
		if commit, err := git.GetHeadCommit(workDir); err == nil {
			v.WorktreeCommit = strings.TrimSpace(commit)
		}
	}

	// FixtureCommit is deliberately left empty: the harness stamps it (D12).

	if len(v.Components) == 0 {
		v.Components = nil
	}
	return v
}

// stampJobConfigVector computes a job's config vector and writes it to the
// job's artifact directory. It is the single entry point used by every stamp
// site. Any failure is warned and swallowed: capture must never fail a job.
func stampJobConfigVector(
	ctx context.Context,
	job *Job,
	plan *Plan,
	resolvedModel string,
	workDir string,
	jobCtx *jobContextPaths,
	contextFiles []string,
	briefingPath string,
) {
	if job == nil || plan == nil {
		return
	}
	v := ComputeJobConfigVector(job, plan, resolvedModel, workDir, jobCtx, contextFiles, briefingPath)
	if err := WriteConfigVectorArtifact(plan.Directory, job.ID, v); err != nil {
		ulog.Warn("Failed to write config vector artifact").
			Err(err).
			Field("job_id", job.ID).
			Log(ctx)
	}
	if err := WriteAgentConfigArtifact(plan.Directory, job.ID, v, workDir, contextFiles); err != nil {
		ulog.Warn("Failed to write isolated agent config artifact").
			Err(err).
			Field("job_id", job.ID).
			Log(ctx)
	}
}

// WriteAgentConfigArtifact materializes the exact flow-computed vector for the
// Pi launch. Components remain hashes; human arm labels belong in eval fixtures.
func WriteAgentConfigArtifact(planDir, jobID string, v record.ConfigVector, workDir string, contextFiles []string) error {
	if planDir == "" || jobID == "" {
		return fmt.Errorf("agent config: empty planDir or jobID")
	}
	path := AgentConfigArtifactPath(planDir, jobID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating job artifact directory: %w", err)
	}
	bundle := make([]string, 0, len(contextFiles))
	for _, name := range contextFiles {
		if rel, err := filepath.Rel(workDir, name); err == nil {
			bundle = append(bundle, filepath.ToSlash(rel))
		}
	}
	artifact := agentConfigArtifact{
		Version: 1, Config: v.Components, Model: v.Model, Provider: v.Provider,
		FixtureCommit: v.FixtureCommit, WorktreeCommit: v.WorktreeCommit, BundleFiles: bundle,
	}
	out, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent config: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing agent config: %w", err)
	}
	return nil
}

// WriteConfigVectorArtifact writes the vector to
// .artifacts/<job-id>/config-vector.json (D14), carrying its own hash
// alongside the fields so a reader need not recompute it. The write is a whole
// -file overwrite, so re-stamping (a chat turn, a retry) is idempotent.
func WriteConfigVectorArtifact(planDir, jobID string, v record.ConfigVector) error {
	if planDir == "" || jobID == "" {
		return fmt.Errorf("config vector: empty planDir or jobID")
	}
	path := ConfigVectorArtifactPath(planDir, jobID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating job artifact directory: %w", err)
	}

	// Marshal the vector, then splice in the hash, so the on-disk document is
	// exactly the vector's fields plus "hash" and stays parseable straight
	// back into a record.ConfigVector.
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling config vector: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("re-reading config vector: %w", err)
	}
	hashJSON, err := json.Marshal(v.Hash())
	if err != nil {
		return fmt.Errorf("marshaling config vector hash: %w", err)
	}
	doc["hash"] = hashJSON

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config vector document: %w", err)
	}
	out = append(out, '\n')

	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writing config vector: %w", err)
	}
	return nil
}

// ReadConfigVectorArtifact reads back a previously stamped vector along with
// the hash recorded at stamp time. ok is false when no vector exists — a
// legitimate state for jobs that predate this feature.
func ReadConfigVectorArtifact(planDir, jobID string) (v record.ConfigVector, hash string, ok bool) {
	b, err := os.ReadFile(ConfigVectorArtifactPath(planDir, jobID))
	if err != nil {
		return record.ConfigVector{}, "", false
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return record.ConfigVector{}, "", false
	}
	var withHash struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(b, &withHash); err == nil {
		hash = withHash.Hash
	}
	if hash == "" {
		hash = v.Hash()
	}
	return v, hash, true
}
