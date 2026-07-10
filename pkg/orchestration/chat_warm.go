package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
	anthropicconfig "github.com/grovetools/grove-anthropic/pkg/config"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
)

// `flow plan warm` (oracle-plays J5). A minimal cache-refreshing request that
// reproduces the last real turn's byte-identical cached prefix and rides it as
// a READ — refreshing the Anthropic prefix-cache TTL — without appending a
// turn, a history block, or a request manifest that would bust the prefix. The
// ONLY thing it writes is a warm receipt (.artifacts/<jobID>/warm-<ts>.json).
//
// It is called CLI-direct (not through Runtime.ExecuteJob), so it never stamps
// status: running, takes no lock, and mutates neither the chat body nor the
// frontmatter. Because it has no OneShotExecutor it constructs its own
// anthropic.RequestRunner and resolves the API key itself (addendum A7).
//
// Warm does NOT re-derive the request's cache identity: caches are model-scoped
// and the model/CacheTTL/NoCache come from the LATEST request manifest header
// (addendum A1), because at rest the last turn is the assistant and a per-turn
// --model override or the awaiting user turn's directive.Model are structurally
// invisible. If the model warm WOULD resolve to differs from the manifest's,
// warm aborts before the API call — warming a model-mismatched prefix burns
// money for a cache the next turn won't hit.

// WarmReceipt is the on-disk record `flow plan warm` writes (and the staleness
// scanner counts as activity). It carries no request-manifest fields on
// purpose: tooling and the cache-lineage tests count request-manifest-*.json as
// turns, so warm must not write one.
type WarmReceipt struct {
	CreatedAt    time.Time `json:"created_at"`
	Model        string    `json:"model"`
	CacheRead    int64     `json:"cache_read"`
	CacheWrite5m int64     `json:"cache_write_5m"`
	CacheWrite1h int64     `json:"cache_write_1h"`
	CostUSD      float64   `json:"cost_usd"`
	ParityOK     bool      `json:"parity_ok"`
}

// WarmResult is returned to the CLI for reporting.
type WarmResult struct {
	ReceiptPath string
	Receipt     WarmReceipt
	// Mock is true when the run short-circuited under GROVE_MOCK_LLM_RESPONSE_FILE
	// (assembly + parity + receipt, no API dispatch).
	Mock bool
}

// WarmChatCache refreshes the cached-prefix TTL of a chat's last turn. See the
// file header for the contract. Errors abort before writing anything.
func WarmChatCache(ctx context.Context, job *Job, plan *Plan) (*WarmResult, error) {
	// Guards: warm is oracle-chat-only and only when the chat is at rest.
	if job.Type != JobTypeChat {
		return nil, fmt.Errorf("flow plan warm only applies to chat jobs (%s is type %s)", job.Filename, job.Type)
	}
	if job.IsAgentResponded() {
		return nil, fmt.Errorf("flow plan warm does not apply to responder: agent chats (they never dispatch to an LLM API, so there is no cached prefix to keep warm)")
	}

	// --- Cache identity from the latest manifest header (addendum A1/A4) ---
	manifest, _ := latestRequestManifest(plan.Directory, job.ID)
	if manifest == nil {
		return nil, fmt.Errorf("chat %s has never fired a turn (no request manifest) — there is no cached prefix to keep warm", job.Filename)
	}
	if manifest.NoCache {
		return nil, fmt.Errorf("the last turn of chat %s ran with caching disabled (no_cache) — there is no cached prefix to keep warm", job.Filename)
	}
	// Anthropic-only: only the anthropic (and the mock provider, which describes
	// the identical Anthropic ladder assembly and is the test seam) manifests
	// carry a ladder cache to keep warm. gemini/openrouter use a flat upload with
	// no ladder breakpoints. Production real turns record "anthropic"; a "mock"
	// manifest never occurs outside the hermetic test harness.
	if manifest.Provider != requestManifestProviderAnthropic && manifest.Provider != requestManifestProviderMock {
		return nil, fmt.Errorf("the last turn of chat %s ran on provider %q — cache keepalive is Anthropic-only (the ladder cache layout is Anthropic-only)", job.Filename, manifest.Provider)
	}

	// Model gate: the model warm WOULD resolve to must equal the manifest's, or
	// the prefix is dead and warming it cold-writes a cache the next turn won't
	// reuse (addendum A1).
	resolvedModel := resolveWarmModel(job, plan)
	if resolvedModel != manifest.Model {
		return nil, fmt.Errorf(
			"cache is model-scoped: the last turn of chat %s ran under %s, this warm would run under %s — warming would cold-write a prefix the next turn may not reuse; run the next turn normally",
			job.Filename, manifest.Model, resolvedModel)
	}

	// --- Reassemble the byte-identical cached prefix (read-only) ---
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return nil, fmt.Errorf("reading chat file: %w", err)
	}
	turns, err := ParseChatFile(content)
	if err != nil {
		return nil, fmt.Errorf("parsing chat file: %w", err)
	}

	// System prompt: template threads from the last USER turn's directive
	// (addendum A2), fallback job.Template → "chat". The system hash also rides
	// the parity gate as a backstop.
	templateName := lastUserTurnTemplate(turns)
	if templateName == "" {
		templateName = job.Template
	}
	if templateName == "" {
		templateName = "chat"
	}
	template, err := NewTemplateManager().FindTemplate(templateName)
	if err != nil {
		return nil, fmt.Errorf("resolving template %s: %w", templateName, err)
	}
	systemPrompt := buildChatSystemPrompt([]byte(template.Prompt))

	// Layers: read-only load + audit (NOT PrepareContextLayers, which would
	// extend the store and could append a rules-diff layer, mutating state).
	layersDir := ContextLayersDir(plan.Directory, job.ID)
	layerManifest, err := LoadLayerManifest(layersDir)
	if err != nil {
		return nil, fmt.Errorf("loading layer manifest: %w", err)
	}
	var layerFiles []string
	var layerSources map[string]string
	if layerManifest != nil {
		if auditErr := AuditLayerArtifacts(layersDir, layerManifest); auditErr != nil {
			return nil, fmt.Errorf("auditing layer artifacts: %w", auditErr)
		}
		layerSources = make(map[string]string, len(layerManifest.Layers))
		for _, e := range layerManifest.Layers {
			p := LayerArtifactPath(layersDir, e)
			layerFiles = append(layerFiles, p)
			layerSources[p] = e.Source
		}
	}

	// Context docs: the non-lineage dependency + include attachments, resolved
	// exactly as executeChatJob's ladder path does.
	contextFiles, err := chatWarmContextFiles(job, plan)
	if err != nil {
		return nil, fmt.Errorf("resolving chat context attachments: %w", err)
	}

	// History: the byte-stable dialogue blocks. At rest FormatConversationRegions
	// lands the split on the trailing empty marker, so the cached prefix the last
	// turn recorded appears as a leading portion of these blocks (the parity gate
	// requires exactly that).
	history := FormatConversationRegions(turns).HistoryBlocks.Texts()

	warmOpts := anthropic.RequestOptions{
		Model:             resolvedModel,
		Prompt:            "ack",
		SystemPrompt:      systemPrompt,
		WorkDir:           plan.Directory, // ladder ignores WorkDir for content
		CacheLayout:       anthropic.CacheLayoutLadder,
		CacheTTL:          manifest.CacheTTL, // from the manifest header (A1)
		MaxTokens:         1,
		NoCache:           false, // refused above if the last turn was no_cache
		LayerFiles:        layerFiles,
		ContextFiles:      contextFiles,
		HistoryBlocks:     history,
		LineageLayerCount: leadingLineageLayerCount(layerFiles, layerSources),
		Caller:            "grove-flow-warm",
		JobID:             job.ID,
		PlanName:          plan.Name,
	}

	// --- Byte parity gate (addendum A1) ---
	// The manifest's recorded cached prefix must reproduce byte-identically as a
	// leading run of the warm request's blocks, or warming would cold-write.
	warmEntries, err := DescribeChatRequestManifest(warmOpts)
	if err != nil {
		return nil, fmt.Errorf("describing warm request: %w", err)
	}
	if perr := verifyWarmParity(manifest, warmEntries); perr != nil {
		return nil, perr
	}

	// Mock mode short-circuits before runner construction (addendum A7): assembly
	// + parity ran, so still write the receipt (the test seam).
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		receipt := WarmReceipt{CreatedAt: time.Now().UTC(), Model: resolvedModel, ParityOK: true}
		path, werr := writeWarmReceipt(plan.Directory, job.ID, receipt)
		if werr != nil {
			return nil, werr
		}
		return &WarmResult{ReceiptPath: path, Receipt: receipt, Mock: true}, nil
	}

	// --- Dispatch (CLI-direct: own runner + key, addendum A7) ---
	apiKey, err := anthropicconfig.ResolveAPIKey()
	if err != nil {
		return nil, fmt.Errorf("resolving Anthropic API key: %w", err)
	}
	warmOpts.APIKey = apiKey
	runner := anthropic.NewRequestRunner()
	_, usage, err := runner.RunWithUsage(ctx, warmOpts)
	if err != nil {
		return nil, fmt.Errorf("warm request: %w", err)
	}

	receipt := WarmReceipt{CreatedAt: time.Now().UTC(), Model: resolvedModel, ParityOK: true}
	if usage != nil {
		receipt.CacheRead = usage.CacheReadTokens
		receipt.CacheWrite5m = usage.CacheWrite5m
		receipt.CacheWrite1h = usage.CacheWrite1h
		receipt.CostUSD = usage.EstimatedCostUSD
	}
	path, err := writeWarmReceipt(plan.Directory, job.ID, receipt)
	if err != nil {
		return nil, err
	}
	return &WarmResult{ReceiptPath: path, Receipt: receipt}, nil
}

// resolveWarmModel resolves the model warm would use from the surfaces visible
// at rest — the frontmatter tail of executeChatJob's 6-level chain (job.Model →
// plan config → global oneshot model → default), alias-resolved. The CLI
// --model override and the awaiting user turn's directive.Model are
// structurally invisible at rest (addendum A1).
func resolveWarmModel(job *Job, plan *Plan) string {
	var m string
	switch {
	case job.Model != "":
		m = job.Model
	case plan.Config != nil && plan.Config.Model != "":
		m = plan.Config.Model
	case plan.Orchestration != nil && plan.Orchestration.OneshotModel != "":
		m = plan.Orchestration.OneshotModel
	default:
		m = anthropicmodels.DefaultModel
	}
	return resolveModelAlias(m)
}

// lastUserTurnTemplate walks the parsed turns backward to the last user turn
// carrying a directive template (addendum A2). Empty when none does.
func lastUserTurnTemplate(turns []*ChatTurn) string {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Speaker == "user" && t.Directive != nil && t.Directive.Template != "" {
			return t.Directive.Template
		}
	}
	return ""
}

// chatWarmContextFiles reproduces executeChatJob's ladder ContextFiles: the
// non-lineage dependency attachments (upload mode only — inlined deps ride in
// the volatile turn) followed by the include attachments. A completed chat
// dependency rides as cached lineage layers, not an attachment, so it is
// skipped (matching the lineageDepIDs filter), which is why a phase chat's deps
// never appear here.
func chatWarmContextFiles(job *Job, plan *Plan) ([]string, error) {
	// Lineage deps travel as layers when the chat has a rules file (the layer
	// store exists); mirror executeChatJob's gate.
	lineageDep := make(map[string]bool)
	if job.RulesFile != "" {
		for _, dep := range job.Dependencies {
			if dep != nil && dep.Type == JobTypeChat && dep.Status == JobStatusCompleted && dep.FilePath != "" {
				lineageDep[dep.ID] = true
			}
		}
	}

	var files []string
	if !job.ShouldInline(InlineDependencies) {
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" && !lineageDep[dep.ID] {
				files = append(files, dep.FilePath)
			}
		}
	}
	for _, source := range job.Include {
		p, err := ResolvePromptSource(source, plan)
		if err != nil {
			return nil, fmt.Errorf("could not find include file %s: %w", source, err)
		}
		files = append(files, p)
	}
	return files, nil
}

// verifyWarmParity requires the manifest's recorded cached prefix (its non-turn
// entries) to reproduce byte-identically as a leading run of the warm request's
// non-turn entries. Warm may carry MORE trailing history blocks than the last
// turn recorded (at rest the just-completed exchange is now history), so the
// check is prefix-containment, not full equality — but every block the last
// turn actually cached must match in kind and content hash.
func verifyWarmParity(manifest *RequestManifest, warmEntries []RequestManifestEntry) error {
	mPrefix := nonTurnEntries(manifest.Entries)
	wPrefix := nonTurnEntries(warmEntries)
	if len(wPrefix) < len(mPrefix) {
		return fmt.Errorf(
			"prefix bytes diverged since the last turn (warm assembled %d cached blocks, the last turn cached %d) — warming would cold-write; run the next turn normally",
			len(wPrefix), len(mPrefix))
	}
	for i, me := range mPrefix {
		we := wPrefix[i]
		if we.Kind != me.Kind || we.ContentHash != me.ContentHash {
			return fmt.Errorf(
				"prefix bytes diverged since the last turn (block %d %s changed) — warming would cold-write; run the next turn normally",
				i, me.Kind)
		}
	}
	return nil
}

// nonTurnEntries returns the cached (non-volatile) entries in order.
func nonTurnEntries(entries []RequestManifestEntry) []RequestManifestEntry {
	out := make([]RequestManifestEntry, 0, len(entries))
	for _, e := range entries {
		if e.Kind == anthropic.RequestBlockTurn {
			continue
		}
		out = append(out, e)
	}
	return out
}

// writeWarmReceipt writes the warm receipt atomically (temp + rename) under the
// job's artifact dir as warm-<unixts>.json.
func writeWarmReceipt(planDir, jobID string, r WarmReceipt) (string, error) {
	dir := filepath.Join(planDir, ".artifacts", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // artifact dir
		return "", err
	}
	dest := filepath.Join(dir, fmt.Sprintf("warm-%d.json", r.CreatedAt.Unix()))
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}
