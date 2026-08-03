package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/config"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/tui/theme"
	grovecontext "github.com/grovetools/cx/pkg/context"

	"github.com/google/uuid"
)

// pi_session_chat.go is the launch half of `responder: pi-session` — the
// one-job design from the pi-as-oracle proposal (discussion round 2).
//
// The shape, in one paragraph: the chat `.md` is the canonical record and lives
// at plan level exactly like an oracle chat; `flow plan run` is the LAUNCH verb
// rather than a turn dispatch; the first launch freezes the rules sweep through
// the ordinary layer engine, synthesizes a Pi session file holding those frozen
// bytes, and starts ONE persistent Pi process against it through the normal
// interactive-provider lifecycle; every later turn arrives through `flow plan
// say` (append + wake nudge) and every response comes back through `flow plan
// respond`. Flow never dispatches this chat to an LLM API.
//
// Three invariants are load-bearing and each is enforced in exactly one place
// below:
//
//   - The seed is written ONCE per job. Re-running an already-seeded chat
//     re-attaches or resumes the SAME session file; it never re-seeds, because
//     re-seeding would silently discard the dialogue that is the chat's value.
//   - The layer store is frozen ONCE, at first launch, and is the truthful
//     record of what the session was given. The seed embeds the rendered layer
//     bytes, so the manifest and the session agree by construction.
//   - Nothing launches until the preflight gates pass. A session that boots
//     with an empty, broken, or oversized context is worse than one that never
//     booted: it looks authoritative and answers from nothing.

// DefaultPiSessionProvider is the agent provider a pi-session chat uses when
// its frontmatter names none. This responder is defined by Pi's session
// protocol, so it never falls back to flow.interactive_provider (which would
// hand a synthesized Pi session file to claude).
const DefaultPiSessionProvider = "pi"

// piSessionLiveness reports whether a job's agent process is still alive. It is
// a package var so lifecycle tests can drive the re-attach/resume branches
// without a daemon or a real process.
var piSessionLiveness = AgentProcessAlive

// piSessionLauncher launches the prepared provider command. A package var for
// the same reason: the argv-shape test observes what would be launched without
// launching it.
var piSessionLauncher = func(ctx context.Context, provider InteractiveAgentProvider, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	return provider.Launch(ctx, job, plan, workDir, agentArgs, briefingFilePath)
}

// RunPiSessionChat is the `flow plan run` behavior for a pi-session chat. It is
// idempotent by design — a coordinator may run it on every pass:
//
//	no session yet  → preflight, freeze, seed, launch          (status: running)
//	session + alive → no-op plus a wake nudge                  (status: unchanged)
//	session + dead  → relaunch against the SAME session file    (status: running)
func RunPiSessionChat(ctx context.Context, job *Job, plan *Plan) error {
	if job == nil || plan == nil {
		return fmt.Errorf("pi-session chat: nil job or plan")
	}
	if !job.IsPiSessionResponded() {
		return fmt.Errorf("pi-session chat: job %s is responder %q, not %q", chatJobRef(job), job.Responder, ResponderPiSession)
	}

	writer := grovelogging.GetWriter(ctx)

	desc, err := ReadPiSessionDescriptor(plan.Directory, job.ID)
	if err != nil {
		return fmt.Errorf("pi-session chat: reading launch descriptor: %w", err)
	}

	// Already launched: decide between re-attach (alive) and resume (dead)
	// WITHOUT touching the seed or the layer store.
	if desc != nil && desc.SessionFile != "" {
		if _, statErr := os.Stat(desc.SessionFile); statErr == nil {
			return reattachOrResumePiSession(ctx, job, plan, desc)
		}
		// The descriptor points at a session file that is gone. Refuse rather
		// than silently re-seeding: the transcript is the only copy of the
		// dialogue, and quietly starting a fresh one would present an amnesiac
		// oracle as the same oracle.
		return fmt.Errorf("pi-session chat %s was launched against session file %s, which no longer exists — refusing to silently re-seed a new session over an existing dialogue. Delete %s to start this chat over from a fresh seed",
			chatJobRef(job), desc.SessionFile, PiSessionDescriptorPath(plan.Directory, job.ID))
	}

	fmt.Fprintf(writer, "Launching seeded pi session for %s\n", chatJobRef(job))
	return launchPiSessionChat(ctx, job, plan)
}

// reattachOrResumePiSession handles a chat whose session file already exists.
func reattachOrResumePiSession(ctx context.Context, job *Job, plan *Plan, desc *PiSessionDescriptor) error {
	writer := grovelogging.GetWriter(ctx)

	if alive, pid := piSessionLiveness(job.ID); alive {
		// The session is up. Running it again is a request to make sure it has
		// seen the file, which is precisely what a wake nudge means.
		if err := NudgePiSessionWake(plan.Directory, job, WakeReasonLaunch); err != nil {
			ulog.Warn("Failed to write pi-session wake sentinel").Err(err).Field("job_id", job.ID).Log(ctx)
		}
		fmt.Fprintf(writer, "pi session for %s is already running (pid %d) — nudged it to reconcile the chat file. Send turns with: flow plan say %s\n",
			chatJobRef(job), pid, job.Filename)
		ulog.Info("pi-session chat already running — nudged instead of relaunching").
			Field("job_id", job.ID).
			Field("pid", pid).
			Log(ctx)
		return nil
	}

	fmt.Fprintf(writer, "pi session for %s is not running — resuming its existing session file (no re-seed)\n", chatJobRef(job))
	ulog.Info("Resuming pi-session chat against its existing session file").
		Field("job_id", job.ID).
		Field("session_file", desc.SessionFile).
		Log(ctx)
	return startPiSessionProcess(ctx, job, plan, *desc)
}

// launchPiSessionChat is the first-launch path: preflight, freeze, seed, start.
func launchPiSessionChat(ctx context.Context, job *Job, plan *Plan) error {
	writer := grovelogging.GetWriter(ctx)

	contextDir, err := resolvePiSessionContextDir(ctx, job, plan)
	if err != nil {
		return err
	}

	// --- Preflight gate 1: the rules file must resolve, lint clean, and
	// select something. ---
	if job.RulesFile == "" {
		return fmt.Errorf("pi-session chat %s declares no rules_file — a seeded session with no curated context is just a bare agent, and the whole responder exists to avoid that. Add a rules_file to the job frontmatter", chatJobRef(job))
	}
	rulesPath, err := ResolveJobRulesFilePath(plan, job, contextDir)
	if err != nil {
		return fmt.Errorf("pi-session chat %s: %w", chatJobRef(job), err)
	}

	preflight, err := PreflightPiSessionContext(ctx, job, contextDir, rulesPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(writer, "%s\n", preflight.FormatLine())

	// --- Preflight gate 2: the seed must fit the model's window. ---
	gate, err := GatePiSeedWindow(job.Model, preflight.CXTokens, job.Filename)
	if err != nil {
		return err
	}
	fmt.Fprintf(writer, "%s\n", gate.FormatLine())
	if gate.Advisory != "" {
		fmt.Fprintf(writer, "Seed window advisory: %s\n", gate.Advisory)
		ulog.Warn("pi-session seed window advisory").
			Field("job_id", job.ID).
			Field("advisory", gate.Advisory).
			Log(ctx)
	}

	// --- Freeze the context through the ordinary layer engine. ---
	// A pi-session chat gets the SAME artifacts an oracle chat gets —
	// context-layers/00-base.xml, layers.json, snapshot.json — because the
	// audit story ("what exactly was in this agent's head?") must not depend on
	// which engine answered the turns. The refresh verbs never apply: the seed
	// is written once, so a later delta layer could not reach the session.
	turnID := piSessionTurnID(job.ID)
	layerResult, err := PrepareContextLayers(ctx, LayerEngineParams{
		PlanDir:         plan.Directory,
		JobID:           job.ID,
		ContextDir:      contextDir,
		RulesPath:       rulesPath,
		TurnID:          turnID,
		StripComments:   job.IsStripCommentsEnabled(),
		SnapshotEnabled: job.IsContextSnapshotEnabled(),
		Refresh:         LayerRefreshNone,
		Layout:          "ladder",
	})
	if err != nil {
		return fmt.Errorf("pi-session chat %s: freezing context layers: %w", chatJobRef(job), err)
	}
	// The same empty-freeze gate the oracle path runs, for the same reason: a
	// freeze that captured nothing must fail the launch, not seed an empty
	// session that answers confidently from an empty bundle.
	if err := validateFrozenContextCoverage(ctx, plan.Directory, job, contextDir, rulesPath); err != nil {
		return err
	}

	bundle, err := readLayerBundle(layerResult.LayerPaths)
	if err != nil {
		return fmt.Errorf("pi-session chat %s: %w", chatJobRef(job), err)
	}

	// --- Synthesize the session. ---
	sessionDir, err := preparePiJobSessionDir(plan.Directory, job.ID)
	if err != nil {
		return err
	}
	now := time.Now()
	sessionID, err := newPiSeedSessionID()
	if err != nil {
		return fmt.Errorf("pi-session chat %s: %w", chatJobRef(job), err)
	}
	sessionPath := filepath.Join(sessionDir, piSeedSessionFileName(sessionID, now))

	seedResult, err := WritePiSessionSeed(sessionPath, PiSessionSeed{
		SessionID: sessionID,
		CWD:       contextDir,
		Now:       now,
		Stamp: map[string]any{
			"job_id":     job.ID,
			"job_title":  job.Title,
			"plan_name":  plan.Name,
			"chat_file":  job.FilePath,
			"rules_file": rulesPath,
			"layers":     layerResult.LayerPaths,
			"seeded_at":  piSeedTimestamp(now),
		},
		Messages: []PiSeedMessage{
			{CustomType: PiSeedFramingType, Content: piSeedFraming(), Display: true},
			{CustomType: PiSeedBundleType, Content: bundle, Display: false},
			{CustomType: PiSeedContractType, Content: piSeedContract(job, plan), Display: true},
		},
	})
	if err != nil {
		return fmt.Errorf("pi-session chat %s: %w", chatJobRef(job), err)
	}
	fmt.Fprintf(writer, "Seeded pi session %s (%s, %d in-context entries) at %s\n",
		seedResult.SessionID, formatByteCount(seedResult.Bytes), len(seedResult.EntryIDs), seedResult.Path)
	ulog.Success("Synthesized pi session seed").
		Field("job_id", job.ID).
		Field("session_id", seedResult.SessionID).
		Field("seed_bytes", seedResult.Bytes).
		Field("seed_tokens", gate.ModelTokens).
		Log(ctx)

	// Version probe: compare our pinned format version against whatever the
	// installed runtime last wrote in this job's session directory. Advisory —
	// see ProbePiSessionFormat for why this must not be a gate.
	if advisory := ProbePiSessionFormat(sessionDir, seedResult.Path); advisory != "" {
		fmt.Fprintf(writer, "Seed format advisory: %s\n", advisory)
		ulog.Warn("pi session seed format advisory").Field("job_id", job.ID).Field("advisory", advisory).Log(ctx)
	}

	desc := PiSessionDescriptor{
		JobID:       job.ID,
		JobTitle:    job.Title,
		PlanName:    plan.Name,
		PlanDir:     plan.Directory,
		ChatFile:    job.FilePath,
		WakeFile:    PiSessionWakePath(plan.Directory, job.ID),
		SessionFile: seedResult.Path,
		SessionID:   seedResult.SessionID,
		ContextDir:  contextDir,
		RulesFile:   rulesPath,
		LayerPaths:  layerResult.LayerPaths,
		Model:       job.Model,
		SeedTokens:  gate.ModelTokens,
		SeedBudget:  gate.Budget,
		SeedFamily:  gate.Profile.Family,
		LaunchedAt:  now.UTC().Format(time.RFC3339Nano),
	}
	return startPiSessionProcess(ctx, job, plan, desc)
}

// startPiSessionProcess launches (or relaunches) the Pi process for a
// pi-session chat through the ordinary interactive-provider lifecycle, so
// daemon session intent, GROVE_FLOW_* environment, deterministic PID capture,
// transcript confirmation, and agent-target routing all behave exactly as they
// do for an interactive_agent job. Nothing about this responder justifies a
// second, subtly different launch path.
func startPiSessionProcess(ctx context.Context, job *Job, plan *Plan, desc PiSessionDescriptor) error {
	spec, flowCfg, err := resolvePiSessionProviderSpec(job)
	if err != nil {
		return err
	}
	desc.Provider = spec.Name

	provider, err := newInteractiveProviderForPlan(spec, plan, flowCfg)
	if err != nil {
		return err
	}

	agentArgs := resolveProviderArgs(flowCfg, spec.Name)
	agentArgs, err = appendProviderJobArgs(spec, agentArgs, job)
	if err != nil {
		return err
	}
	// --session <path> is appended AFTER the configured provider args and the
	// per-job model flag, and before the provider appends its own
	// --session-dir, so no operator-configured arg can redirect the session
	// away from the file Flow just seeded. pi resolves an argument containing a
	// path separator (or ending .jsonl) as a literal file rather than a session
	// id — see resolveSessionPath in pi's main.ts — which is what lets Flow
	// hand it a synthesized transcript instead of a registered session.
	if !isShellSafeArgValue(desc.SessionFile) {
		return fmt.Errorf("pi-session chat %s: session file path %q contains characters that are unsafe in a shell command", chatJobRef(job), desc.SessionFile)
	}
	agentArgs = append(agentArgs, "--session", desc.SessionFile)

	briefingPath, err := WriteBriefingFile(plan, job, piSessionBriefing(job, plan, desc), "")
	if err != nil {
		return fmt.Errorf("pi-session chat %s: writing kickoff briefing: %w", chatJobRef(job), err)
	}

	// Persist the descriptor BEFORE the launch: it is how a wake nudge, a
	// re-run, or the Phase 3 extension finds the session, and a process that
	// started without a discoverable descriptor is a process nobody can talk to.
	if err := WritePiSessionDescriptor(plan.Directory, desc); err != nil {
		return fmt.Errorf("pi-session chat %s: %w", chatJobRef(job), err)
	}
	if err := NudgePiSessionWake(plan.Directory, job, WakeReasonLaunch); err != nil {
		ulog.Warn("Failed to write pi-session wake sentinel").Err(err).Field("job_id", job.ID).Log(ctx)
	}

	// The process cwd is the resolved context dir — the same directory the
	// rules resolved against and the same one stamped into the session header,
	// so pi's own cwd, its session header, and the layer manifest's root pin
	// all name one place.
	if err := piSessionLauncher(ctx, provider, job, plan, desc.ContextDir, agentArgs, briefingPath); err != nil {
		return err
	}

	ulog.Success("Launched seeded pi session").
		Field("job_id", job.ID).
		Field("provider", spec.Name).
		Field("session_file", desc.SessionFile).
		Log(ctx)
	fmt.Fprintf(grovelogging.GetWriter(ctx),
		"%s pi session live. Send a turn from anywhere with: flow plan say %s\n", theme.IconSuccess, job.Filename)
	return nil
}

// resolvePiSessionProviderSpec resolves the Pi-family provider for the job and
// refuses anything else. A non-Pi agent cannot open a Pi session file, so an
// inherited flow.interactive_provider (claude, by default) must fail here with
// an actionable message rather than at launch with a confusing one.
func resolvePiSessionProviderSpec(job *Job) (*AgentProviderSpec, FlowConfig, error) {
	var flowCfg FlowConfig
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}
	if parsed, cfgErr := FlowConfigFrom(coreCfg); cfgErr == nil && parsed != nil {
		flowCfg = *parsed
	}

	name := job.Provider
	if name == "" {
		name = DefaultPiSessionProvider
	}
	spec, ok := LookupAgentProvider(name)
	if !ok {
		return nil, flowCfg, unknownAgentProviderError(name)
	}
	if spec.PiRuntime == nil {
		return nil, flowCfg, fmt.Errorf("responder: pi-session requires a Pi-family provider (pi or grove-agent), but this job resolves to provider %q — only Pi can open the synthesized session file Flow seeds. Set provider: pi in the job frontmatter", spec.Name)
	}
	return spec, flowCfg, nil
}

// newInteractiveProviderForPlan builds the launcher for a plan's resolved agent
// target. It mirrors InteractiveAgentExecutor.Execute's routing so a pi-session
// chat lands in the same pane surface an interactive agent would.
func newInteractiveProviderForPlan(spec *AgentProviderSpec, plan *Plan, flowCfg FlowConfig) (InteractiveAgentProvider, error) {
	target := ""
	if plan.Orchestration != nil {
		target = plan.Orchestration.AgentTarget
	}
	switch target {
	case "native", "tuimux":
		gp := NewGrovetermAgentProvider(spec, false, target)
		gp.agentEnv = flowCfg.AgentEnv
		return gp, nil
	case "tmux":
		return spec.newTmuxProvider(flowCfg.AgentEnv), nil
	default:
		return nil, fmt.Errorf("agent_target not set: job submitted without routing context — this is a bug in the submission path (CLI or TUI should always tag jobs)")
	}
}

// resolvePiSessionContextDir prepares the job's worktree (if any) and returns
// the directory the rules resolve against — which is also the session's cwd and
// the layer store's pinned root. One directory, three roles, resolved once.
func resolvePiSessionContextDir(ctx context.Context, job *Job, plan *Plan) (string, error) {
	var workDir string
	if job.Worktree != "" {
		prepared, err := prepareJobWorktree(ctx, job, plan)
		if err != nil {
			return "", fmt.Errorf("pi-session chat %s: preparing worktree: %w", chatJobRef(job), err)
		}
		workDir = prepared
	} else {
		root, err := GetProjectGitRoot(plan.Directory)
		if err != nil {
			root = plan.Directory
		}
		workDir = root
	}
	return ScopeToSubProject(workDir, job), nil
}

// PiSessionPreflight is the result of the context preflight.
type PiSessionPreflight struct {
	RulesPath string
	Files     int
	Bytes     int64
	CXTokens  int
	// LintIssues are cx lint findings at Warning severity or below; Error-level
	// findings are returned as an error instead.
	LintIssues []string
}

// FormatLine renders the preflight summary written to job.log.
func (p PiSessionPreflight) FormatLine() string {
	return fmt.Sprintf("Seed preflight: %d file(s), %s, cx estimate %s tokens from %s",
		p.Files, formatByteCount(p.Bytes), piTokens(p.CXTokens), p.RulesPath)
}

// PreflightPiSessionContext runs the hard context gates for a pi-session
// launch, from the worktree root:
//
//   - cx lint over the rules file. Error-severity findings refuse the launch:
//     a rules file with a syntax error resolves to something the author did not
//     mean, and there is no later turn in which to notice.
//   - empty resolution. Refused outright — this is the seeded-session analogue
//     of the empty-freeze gate.
//   - size accounting, which feeds the window gate.
//
// Warnings (overly broad patterns, zero-match lines) are surfaced, not fatal:
// they are frequently intentional in a curated bundle.
func PreflightPiSessionContext(ctx context.Context, job *Job, contextDir, rulesPath string) (*PiSessionPreflight, error) {
	mgr := grovecontext.NewManager(contextDir)

	issues, err := mgr.LintRulesFile(rulesPath)
	if err != nil {
		return nil, fmt.Errorf("pi-session preflight: linting %s: %w", rulesPath, err)
	}
	var errorIssues, warnIssues []string
	for _, issue := range issues {
		line := fmt.Sprintf("line %d: %s (%s)", issue.LineNum, issue.Message, strings.TrimSpace(issue.Line))
		if strings.EqualFold(issue.Severity, "Error") {
			errorIssues = append(errorIssues, line)
		} else {
			warnIssues = append(warnIssues, line)
		}
	}
	if len(errorIssues) > 0 {
		return nil, fmt.Errorf("pi-session preflight: cx lint found %d error(s) in %s — the seed would be built from a rules file that does not resolve the way it reads:\n  %s",
			len(errorIssues), rulesPath, strings.Join(errorIssues, "\n  "))
	}

	fileset, err := resolveRulesFileset(contextDir, rulesPath)
	if err != nil {
		return nil, fmt.Errorf("pi-session preflight: resolving %s: %w", rulesPath, err)
	}
	if len(fileset) == 0 {
		return nil, fmt.Errorf("pi-session preflight: rules file %s resolved 0 files from %s — the session would launch holding nothing, which is exactly the failure this responder exists to prevent. Fix the rules file (cx lint --job %s), then re-run",
			rulesPath, contextDir, job.Filename)
	}

	preflight := &PiSessionPreflight{RulesPath: rulesPath, Files: len(fileset), LintIssues: warnIssues}
	for _, rel := range fileset {
		path := rel
		if !filepath.IsAbs(path) {
			path = filepath.Join(contextDir, path)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			// The layer engine treats an unreadable file the rules demand as a
			// hard failure; the preflight says so first, with the better error.
			return nil, fmt.Errorf("pi-session preflight: rules file %s resolves %s, which cannot be read: %w", rulesPath, rel, statErr)
		}
		preflight.Bytes += info.Size()
		// EstimateTokens is cx's own content-class-aware estimator (code ≈
		// bytes/2, prose ≈ bytes/4), so the number the gate converts is the
		// same number `cx stats` would print for this rules file.
		preflight.CXTokens += grovecontext.EstimateTokens(path, info.Size())
	}

	for _, warn := range warnIssues {
		ulog.Warn("pi-session preflight: cx lint warning").
			Field("job_id", job.ID).
			Field("issue", warn).
			Log(ctx)
	}
	return preflight, nil
}

// readLayerBundle concatenates the frozen layer artifacts in order. These bytes
// — not a re-render — become the seed's bundle message, so the session's
// context and layers.json describe the same thing by construction rather than
// by convention.
func readLayerBundle(layerPaths []string) (string, error) {
	if len(layerPaths) == 0 {
		return "", fmt.Errorf("no frozen context layers to seed")
	}
	var buf strings.Builder
	for _, path := range layerPaths {
		data, err := os.ReadFile(path) //nolint:gosec // Flow-owned frozen layer artifact
		if err != nil {
			return "", fmt.Errorf("reading frozen layer %s: %w", path, err)
		}
		buf.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.String(), nil
}

// piSessionTurnID names the freeze turn in layer filenames and the manifest.
// A pi-session chat freezes exactly once, so the id is derived from the job
// rather than randomized: a re-run that somehow reached the freeze again would
// produce the same name instead of a confusing second layer.
func piSessionTurnID(jobID string) string {
	return "seed-" + sha256Hex([]byte(jobID))[:6]
}

// newPiSeedSessionID mints the pi session id for a seed: a uuidv7, the same
// shape pi's own createSessionId produces, so nothing downstream (groved's
// native-id record, agentstream discovery, pi's own session list) can tell a
// seeded session from a natural one by its id.
func newPiSeedSessionID() (string, error) {
	generated, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generating pi session id: %w", err)
	}
	return generated.String(), nil
}

// finalizePiSessionArtifacts closes out a pi-session chat's control files at
// completion: one last wake nudge telling the (possibly still-draining) session
// the dialogue is over, so a watcher that is mid-reconcile stops expecting
// turns rather than sitting on a file that will never move again.
//
// The descriptor and the seed are deliberately LEFT in place. They are the
// audit record of what the session was given — the same reason the layer store
// survives completion — and deleting them would make a completed pi-session
// chat the one chat flavor whose context cannot be reconstructed afterwards.
func finalizePiSessionArtifacts(job *Job, plan *Plan, silent bool) {
	if err := NudgePiSessionWake(plan.Directory, job, WakeReasonComplete); err != nil && !silent {
		fmt.Printf("  Note: could not write the final pi-session wake sentinel: %v\n", err)
	}
}

// prepareJobWorktree prepares a job's worktree using the shared interactive
// preparation path, so a pi-session chat materializes its worktree exactly the
// way an interactive agent job in the same plan would.
func prepareJobWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	executor := NewInteractiveAgentExecutor(nil, nil, true)
	return executor.determineWorkDir(ctx, job, plan)
}
