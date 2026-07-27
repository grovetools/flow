package orchestration

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"syscall"
)

// SessionRegistrationMode describes how a provider's sessions become visible
// to grove's session tracking after launch.
type SessionRegistrationMode string

const (
	// SessionRegistrationDaemon: the launcher pre-registers a session intent
	// with the daemon and later confirms it with the discovered PID/transcript
	// (claude; also the shape the groveterm meta-provider uses for everyone).
	SessionRegistrationDaemon SessionRegistrationMode = "daemon"
	// SessionRegistrationFSOnly: the launcher writes a record straight into the
	// filesystem session registry at launch time; no daemon intent/confirm.
	SessionRegistrationFSOnly SessionRegistrationMode = "fs-only"
	// SessionRegistrationPlugin: the launcher registers a stub (PID 0, empty
	// native id) and an in-agent plugin enriches it later (opencode).
	SessionRegistrationPlugin SessionRegistrationMode = "plugin-deferred"
)

// AgentProviderSpec declares the capabilities and launch shape of one
// coding-agent CLI provider. It is the single place a provider's behavior is
// described: the executors (interactive / isolated / headless) and the
// groveterm meta-provider dispatch through the registry instead of switching
// on provider-name strings. A new provider lands as one registry entry (plus
// its tmux provider implementation), not another switch arm.
type AgentProviderSpec struct {
	// Name is the registry key and the value used for config lookup
	// (flow.interactive_provider, [flow.providers.<name>]) and session records.
	Name string
	// Binary is the executable launched for this provider.
	Binary string

	// SupportsHeadless reports whether the provider has a non-interactive
	// execution mode usable by the headless agent executor.
	SupportsHeadless bool
	// NewHeadlessCommand builds the exec.Cmd for the provider's headless mode.
	// nil when SupportsHeadless is false. Uses exec.Command (not
	// CommandContext) and Setpgid so the agent detaches from the daemon.
	NewHeadlessCommand func(prompt string, agentArgs []string) *exec.Cmd

	// BuildShellCommand builds the full shell command string that launches the
	// provider with the given briefing instruction, for the interactive
	// (tmux / groveterm PTY) and isolated launch paths. The instruction is
	// already shell-escaped (see buildBriefingInstruction).
	BuildShellCommand func(agentArgs []string, instruction string) string
	// BuildResumeShellCommand builds the provider-specific command that resumes
	// a native session. nil means the provider does not support resume. Launch
	// ownership (intent, env, PID capture, and confirmation) remains with the
	// normal interactive provider lifecycle; callers must not execute this raw.
	BuildResumeShellCommand func(agentArgs []string, nativeSessionID string) (string, error)

	// ModelFlag is the CLI flag used to pass a per-job model ("" = the
	// provider takes no per-job model flag and job.Model is rejected).
	ModelFlag string
	// EffortFlag is the CLI flag used to pass a per-job effort level ("" = the
	// provider takes no effort flag and job.Effort is rejected).
	EffortFlag string
	// ValidateJobModel validates a non-empty job model for this provider.
	// nil means the provider accepts any model string (the CLI owns
	// validation); shell safety is still enforced at arg-append time.
	ValidateJobModel func(model string) error
	// BackfillJobModel optionally records the model the agent will actually
	// run with into the job frontmatter after args are resolved. nil = no-op.
	// workDir and agentEnv are the launch context, so a provider whose CLI
	// self-selects a model can read the same configuration the CLI will.
	BackfillJobModel func(job *Job, workDir string, agentEnv map[string]string)

	// DefaultInputMode is the input mode assumed when sending text into the
	// provider's interactive UI ("vim" or "standard"), overridable per
	// provider via [flow.providers.<name>].input_mode.
	DefaultInputMode string

	// SessionRegistration describes how launched sessions get registered.
	SessionRegistration SessionRegistrationMode
	// ProviderEnv, when non-empty, is exported as GROVE_AGENT_PROVIDER to the
	// agent process so grove hooks/plugins can identify the provider. Empty
	// for claude (hooks default to "claude").
	ProviderEnv string
	// HeadlessTranscriptDiscovery reports whether the headless executor should
	// attempt agentstream transcript discovery after launch.
	HeadlessTranscriptDiscovery bool

	// PiRuntime parameterizes providers that use Pi's launch/session protocol.
	// This lets rebranded distros share the implementation without inheriting
	// stock Pi's binary or config roots.
	PiRuntime *PiRuntimeDescriptor

	// newTmuxProvider constructs the provider's legacy tmux-based
	// InteractiveAgentProvider with flow.agent_env injected.
	newTmuxProvider func(agentEnv map[string]string) InteractiveAgentProvider
}

// PiRuntimeDescriptor is the product-specific portion of a Pi-family
// provider. ConfigDirName is both the project resource directory and the
// basename of the global ~/.<name>/agent root. ManagedCodexAuth is reserved
// for the stock guest profile provisioned by grove satellite auth.
type PiRuntimeDescriptor struct {
	Name             string
	Binary           string
	ConfigDirName    string
	ManagedCodexAuth bool
}

func newPiProviderSpec(runtime PiRuntimeDescriptor) *AgentProviderSpec {
	headless := func(prompt string, agentArgs []string) *exec.Cmd {
		// Pi print mode consumes piped stdin. Append -p after configured args so
		// it cannot consume a following flag value as a positional prompt.
		args := append([]string{}, agentArgs...)
		args = append(args, "-p")
		cmd := exec.Command(runtime.Binary, args...)
		cmd.Stdin = strings.NewReader(prompt)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}
	return &AgentProviderSpec{
		Name:               runtime.Name,
		Binary:             runtime.Binary,
		SupportsHeadless:   true,
		NewHeadlessCommand: headless,
		BuildShellCommand:  positionalShellCommand(runtime.Binary),
		// Pi resumes in place: `--session <uuid>` reopens the job's existing
		// transcript (unlike claude, which forks a new session file). The
		// Flow-owned `--session-dir` is appended by the resume preparer so the
		// uuid resolves inside the job's artifact directory.
		BuildResumeShellCommand:     resumeShellCommand(runtime.Binary, "--session"),
		ModelFlag:                   "--model",
		EffortFlag:                  "",
		DefaultInputMode:            "standard",
		SessionRegistration:         SessionRegistrationDaemon,
		ProviderEnv:                 runtime.Name,
		HeadlessTranscriptDiscovery: true,
		PiRuntime:                   &runtime,
		newTmuxProvider: func(agentEnv map[string]string) InteractiveAgentProvider {
			p := newPiAgentProvider(runtime)
			p.agentEnv = agentEnv
			return p
		},
	}
}

// defaultAgentProviderName is intentionally unchanged: product runtimes are
// selected explicitly by job or flow profile and never alter the ecosystem
// fallback.
const defaultAgentProviderName = "claude"

// agentProviderRegistry holds all known agent providers, keyed by name.
var agentProviderRegistry = map[string]*AgentProviderSpec{
	"claude": {
		Name:             "claude",
		Binary:           "claude",
		SupportsHeadless: true,
		NewHeadlessCommand: func(prompt string, agentArgs []string) *exec.Cmd {
			// Claude Code headless: prompt is piped via stdin. Flags are
			// derived entirely from providers.claude.args (resolved upstream);
			// no flag — including --dangerously-skip-permissions — is
			// hardcoded here.
			var args []string
			args = append(args, agentArgs...)
			cmd := exec.Command("claude", args...)
			cmd.Stdin = strings.NewReader(prompt)
			// Detach the process: place it in its own process group so signals
			// sent to the daemon don't propagate to the agent.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			return cmd
		},
		BuildShellCommand:           positionalShellCommand("claude"),
		BuildResumeShellCommand:     resumeShellCommand("claude", "--resume"),
		ModelFlag:                   "--model",
		EffortFlag:                  "--effort",
		ValidateJobModel:            validateClaudeAgentModel,
		BackfillJobModel:            backfillClaudeAgentModel,
		DefaultInputMode:            "vim",
		SessionRegistration:         SessionRegistrationDaemon,
		HeadlessTranscriptDiscovery: true,
		newTmuxProvider: func(agentEnv map[string]string) InteractiveAgentProvider {
			p := NewClaudeAgentProvider()
			p.agentEnv = agentEnv
			return p
		},
	},
	"codex": {
		Name:                    "codex",
		Binary:                  "codex",
		SupportsHeadless:        false,
		BuildShellCommand:       positionalShellCommand("codex"),
		BuildResumeShellCommand: resumeShellCommand("codex", "resume"),
		ModelFlag:               "--model",
		// codex has no --effort flag (reasoning effort is a -c config
		// override, not a per-job CLI flag we pass through today).
		EffortFlag:          "",
		ValidateJobModel:    nil, // codex owns its model names (gpt-*, o*, ...)
		DefaultInputMode:    "standard",
		SessionRegistration: SessionRegistrationDaemon,
		ProviderEnv:         "codex",
		newTmuxProvider: func(agentEnv map[string]string) InteractiveAgentProvider {
			p := NewCodexAgentProvider()
			p.agentEnv = agentEnv
			return p
		},
	},
	"pi":          newPiProviderSpec(PiRuntimeDescriptor{Name: "pi", Binary: "pi", ConfigDirName: ".pi", ManagedCodexAuth: true}),
	"grove-agent": newPiProviderSpec(PiRuntimeDescriptor{Name: "grove-agent", Binary: "grove-agent", ConfigDirName: ".grove-agent"}),
	"opencode": {
		Name:             "opencode",
		Binary:           "opencode",
		SupportsHeadless: true,
		NewHeadlessCommand: func(prompt string, agentArgs []string) *exec.Cmd {
			// Opencode headless: 'opencode run' executes the prompt and exits
			// (same invocation OpencodeAgentProvider.buildAgentCommand uses
			// for non-interactive jobs).
			args := []string{"run"}
			args = append(args, agentArgs...)
			args = append(args, prompt)
			cmd := exec.Command("opencode", args...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			return cmd
		},
		BuildShellCommand: func(agentArgs []string, instruction string) string {
			// opencode takes the prompt via --prompt so the TUI stays open for
			// continued interaction.
			cmdParts := append([]string{"opencode"}, agentArgs...)
			return fmt.Sprintf("%s --prompt \"%s\"", strings.Join(cmdParts, " "), instruction)
		},
		ModelFlag:           "--model", // provider/model format, e.g. anthropic/claude-sonnet-4-5
		EffortFlag:          "",
		ValidateJobModel:    nil,
		DefaultInputMode:    "standard",
		SessionRegistration: SessionRegistrationPlugin,
		ProviderEnv:         "opencode",
		newTmuxProvider: func(agentEnv map[string]string) InteractiveAgentProvider {
			p := NewOpencodeAgentProvider()
			p.agentEnv = agentEnv
			return p
		},
	},
}

// positionalShellCommand returns a BuildShellCommand that passes the briefing
// instruction as the last positional argument, double-quoted (the claude/codex
// command shape shared by the tmux, groveterm, and isolated launch paths).
func positionalShellCommand(binary string) func(agentArgs []string, instruction string) string {
	return func(agentArgs []string, instruction string) string {
		cmdParts := append([]string{binary}, agentArgs...)
		return fmt.Sprintf("%s \"%s\"", strings.Join(cmdParts, " "), instruction)
	}
}

// resumeShellCommand returns the common resume command shape used by Claude
// (`claude <args> --resume <id>`) and Codex (`codex <args> resume <id>`).
// Provider args precede the resume token because Codex treats them as global
// options. Native IDs are intentionally restricted to the same shell-safe
// alphabet used by per-job model values.
func resumeShellCommand(binary, resumeToken string) func([]string, string) (string, error) {
	return func(agentArgs []string, nativeSessionID string) (string, error) {
		if nativeSessionID == "" {
			return "", fmt.Errorf("native session ID is required")
		}
		if !isShellSafeArgValue(nativeSessionID) {
			return "", fmt.Errorf("native session ID %q contains characters that are unsafe in a shell command", nativeSessionID)
		}
		cmdParts := append([]string{binary}, agentArgs...)
		cmdParts = append(cmdParts, resumeToken, nativeSessionID)
		return strings.Join(cmdParts, " "), nil
	}
}

// BuildAgentResumeShellCommand builds the registered provider's resume command.
// It only describes command bytes; the command must still be launched through
// the provider lifecycle so daemon intent, GROVE_FLOW_* env, PID capture,
// session confirmation, and agent-target routing are preserved.
func BuildAgentResumeShellCommand(providerName string, agentArgs []string, nativeSessionID string) (string, error) {
	spec, ok := LookupAgentProvider(providerName)
	if !ok {
		return "", unknownAgentProviderError(providerName)
	}
	if spec.BuildResumeShellCommand == nil {
		return "", fmt.Errorf("agent provider %q does not support session resume", providerName)
	}
	return spec.BuildResumeShellCommand(agentArgs, nativeSessionID)
}

// buildBriefingInstruction assembles the standard "read the briefing file"
// instruction with the briefing path shell-escaped. All launch paths
// (tmux providers, groveterm, isolated) must produce this exact string so the
// pane/PTY command bytes are identical across paths.
func buildBriefingInstruction(briefingFilePath string) string {
	return fmt.Sprintf("Read the briefing file at %s and execute the task.", shellSingleQuote(briefingFilePath))
}

// LookupAgentProvider returns the spec for a provider name.
func LookupAgentProvider(name string) (*AgentProviderSpec, bool) {
	spec, ok := agentProviderRegistry[name]
	return spec, ok
}

// AgentProviderNames returns the sorted names of all registered providers.
func AgentProviderNames() []string {
	names := make([]string, 0, len(agentProviderRegistry))
	for name := range agentProviderRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// headlessAgentProviderNames returns the sorted names of providers that
// support headless execution (for actionable error messages).
func headlessAgentProviderNames() []string {
	var names []string
	for name, spec := range agentProviderRegistry {
		if spec.SupportsHeadless {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// unknownAgentProviderError builds the standard error for a provider name
// missing from the registry.
func unknownAgentProviderError(name string) error {
	return fmt.Errorf("unknown agent provider %q; available providers: %s",
		name, strings.Join(AgentProviderNames(), ", "))
}

// ValidateAgentProviderName checks that a provider name (typically from job
// frontmatter or a CLI flag) is registered. An empty name is valid — it means
// "fall back to flow.interactive_provider / the claude default".
func ValidateAgentProviderName(name string) error {
	if name == "" {
		return nil
	}
	if _, ok := agentProviderRegistry[name]; !ok {
		return unknownAgentProviderError(name)
	}
	return nil
}

// ResolveJobProviderName returns the provider name effective for an agent job:
// job `provider:` frontmatter first, then flow.interactive_provider, then the
// claude default. It does NOT check that the name is registered — use
// resolveJobProviderSpec for a validated lookup.
func ResolveJobProviderName(job *Job, cfg FlowConfig) string {
	if job != nil && job.Provider != "" {
		return job.Provider
	}
	if cfg.InteractiveProvider != "" {
		return cfg.InteractiveProvider
	}
	return defaultAgentProviderName
}

// resolveJobProviderSpec resolves the effective provider for a job and returns
// its registry spec, erroring on unknown names so misconfiguration surfaces
// before any launch work happens.
func resolveJobProviderSpec(job *Job, cfg FlowConfig) (*AgentProviderSpec, error) {
	name := ResolveJobProviderName(job, cfg)
	spec, ok := LookupAgentProvider(name)
	if !ok {
		return nil, unknownAgentProviderError(name)
	}
	return spec, nil
}

// ResolveJobProviderNameFromConfig resolves the effective provider name for a
// job using the flow config loaded from the current directory. A missing or
// malformed config falls back to defaults (this mirrors the executors'
// tolerant config loading).
func ResolveJobProviderNameFromConfig(job *Job) string {
	flowCfg, err := LoadFlowConfig()
	if err != nil || flowCfg == nil {
		flowCfg = &FlowConfig{}
	}
	return ResolveJobProviderName(job, *flowCfg)
}

// ResolveJobProviderSpecFromConfig is ResolveJobProviderNameFromConfig plus a
// validated registry lookup. Used where only the job is at hand (submit-time
// validation, the headless executor's early validation).
func ResolveJobProviderSpecFromConfig(job *Job) (*AgentProviderSpec, error) {
	flowCfg, err := LoadFlowConfig()
	if err != nil || flowCfg == nil {
		flowCfg = &FlowConfig{}
	}
	return resolveJobProviderSpec(job, *flowCfg)
}

// appendProviderJobArgs appends per-job CLI flags (model, effort) to the
// static provider args from grove.toml, according to the provider's spec.
// Values are read from job frontmatter only — plan/global model defaults apply
// exclusively to chat/oneshot jobs (see JobType.InheritsPlanModel). Values are
// passed through without validating against the provider's accepted set (the
// spec's ValidateJobModel hook covers family-level checks), but are rejected
// if they contain characters that would need shell quoting, since several
// launch paths join args into a raw shell command string.
//
// An empty model means "let the agent CLI use its own configured default";
// for claude the executor then records that via the spec's BackfillJobModel.
func appendProviderJobArgs(spec *AgentProviderSpec, agentArgs []string, job *Job) ([]string, error) {
	model := job.Model
	if model != "" && spec.ValidateJobModel != nil {
		if err := spec.ValidateJobModel(model); err != nil {
			return nil, err
		}
	}
	if model == "" && job.Effort == "" {
		return agentArgs, nil
	}

	// Copy so we never mutate the shared provider config slice
	// (jobs may build args concurrently from the same FlowConfig).
	args := make([]string, len(agentArgs), len(agentArgs)+4)
	copy(args, agentArgs)

	if model != "" {
		if spec.ModelFlag == "" {
			return nil, fmt.Errorf("provider %q does not accept a per-job model; remove model: %q from the job frontmatter", spec.Name, model)
		}
		if !isShellSafeArgValue(model) {
			return nil, fmt.Errorf("model %q contains characters that are unsafe in a shell command; use a plain model name/alias", model)
		}
		args = append(args, spec.ModelFlag, model)
	}
	if job.Effort != "" {
		if spec.EffortFlag == "" {
			return nil, fmt.Errorf("provider %q does not accept a per-job effort level; remove effort: %q from the job frontmatter", spec.Name, job.Effort)
		}
		if !isShellSafeArgValue(job.Effort) {
			return nil, fmt.Errorf("effort %q contains characters that are unsafe in a shell command; use a plain effort level", job.Effort)
		}
		args = append(args, spec.EffortFlag, job.Effort)
	}
	return args, nil
}
