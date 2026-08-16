package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/grovetools/agentlogs/pkg/transcript"
	"github.com/grovetools/core/config"
	coredaemon "github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// This file asserts each provider's lifecycle BEYOND launch, end to end:
//
//	registered (intent in the filesystem session registry)
//	  → visible in `flow agent list`
//	  → outcome transition to idle (provider event, simulated at the
//	    hooks/registry boundary — see simulateOutcomeEvent)
//	  → `flow plan complete` teardown + artifact archival
//
// Hermeticity note: the real idle transition is driven by the grove hooks
// binary/plugins (codex: the `hooks codex install` notify hook translating
// agent-turn-complete; pi: the stop/agent_end pipeline; opencode: the
// grove-opencode plugin v2 shelling out to `grove hooks ...`). That binary is
// outside this suite (its `grove` is a mock), and in the tend sandbox flow
// runs daemonless (LocalClient), where every one of those pipelines reduces
// to the same operation: an update of the session's metadata.json in the
// filesystem registry (core/pkg/sessions FileSystemRegistry — see
// LocalClient.ConfirmSession/UpdateSessionStatus). The scenarios therefore
// deliver the provider-side events by performing exactly those registry
// writes, then assert the flow binary's read-side behavior (agent list,
// idle-not-complete, completion teardown/archival) for real.

// hooksSessionsDir returns the sandboxed filesystem session registry root
// ($XDG_STATE_HOME/grove/hooks/sessions — core/pkg/sessions.NewFileSystemRegistry).
func hooksSessionsDir(ctx *harness.Context) string {
	return filepath.Join(ctx.StateDir(), "grove", "hooks", "sessions")
}

// jobFrontmatterString extracts one scalar frontmatter value from a job file.
func jobFrontmatterString(jobPath, field string) (string, error) {
	content, err := fs.ReadString(jobPath)
	if err != nil {
		return "", fmt.Errorf("reading job file: %w", err)
	}
	prefix := field + ": "
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), nil
		}
	}
	return "", fmt.Errorf("could not find %s in %s", field, jobPath)
}

func jobIDFromFile(jobPath string) (string, error) { return jobFrontmatterString(jobPath, "id") }

// readSessionMetadata parses a session dir's metadata.json into a generic map.
func readSessionMetadata(sessionDir string) (map[string]interface{}, error) {
	content, err := fs.ReadString(filepath.Join(sessionDir, "metadata.json"))
	if err != nil {
		return nil, fmt.Errorf("reading metadata.json: %w", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return nil, fmt.Errorf("parsing metadata.json: %w", err)
	}
	return m, nil
}

var localClientEnvMu sync.Mutex

// withSandboxLocalClient runs the real core LocalClient against this scenario's
// XDG sandbox. Core resolves paths from process environment, so serialize the
// brief override to keep parallel scenario sandboxes isolated.
func withSandboxLocalClient(ctx *harness.Context, fn func(*coredaemon.LocalClient) error) error {
	localClientEnvMu.Lock()
	defer localClientEnvMu.Unlock()
	type prior struct {
		value string
		set   bool
	}
	old := map[string]prior{}
	defer func() {
		for key, p := range old {
			if p.set {
				_ = os.Setenv(key, p.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}()
	for _, entry := range ctx.SandboxEnv() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		v, set := os.LookupEnv(key)
		old[key] = prior{v, set}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return fn(coredaemon.NewLocalClient())
}

func confirmIntentInPlace(ctx *harness.Context, jobID, attemptID, nativeID, transcriptPath string) error {
	return withSandboxLocalClient(ctx, func(client *coredaemon.LocalClient) error {
		return client.ConfirmSession(context.Background(), coredaemon.SessionConfirmation{
			JobID: jobID, AttemptID: attemptID, NativeID: nativeID, PID: 1, TranscriptPath: transcriptPath,
		})
	})
}

// simulateOutcomeEvent delivers a provider lifecycle event's effect at the
// hooks/registry boundary: the status write `grove hooks` performs via
// UpdateSessionStatus in the daemonless path, joined by exact job+attempt.
func simulateOutcomeEvent(ctx *harness.Context, jobID, attemptID, status string) error {
	return withSandboxLocalClient(ctx, func(client *coredaemon.LocalClient) error {
		return client.UpdateSessionStatus(context.Background(), jobID, attemptID, status)
	})
}

// agentListJSON runs `flow agent list --json` and parses the entries.
func agentListJSON(ctx *harness.Context, projectDir string) ([]agentListEntry, string, error) {
	cmd := ctx.Bin("agent", "list", "--json")
	cmd.Dir(projectDir)
	result := cmd.Run()
	ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
	if result.ExitCode != 0 {
		return nil, result.Stdout, fmt.Errorf("flow agent list failed: %s", result.Stderr)
	}

	// The JSON payload ("null" or a pretty-printed array) is the last thing
	// on stdout; tolerate any leading log lines. Entry fields never contain
	// '[' so the array's opening bracket is the last one printed.
	out := strings.TrimSpace(result.Stdout)
	if idx := strings.LastIndex(out, "["); idx >= 0 {
		var entries []agentListEntry
		if err := json.Unmarshal([]byte(out[idx:]), &entries); err != nil {
			return nil, result.Stdout, fmt.Errorf("parsing agent list JSON (%q): %w", out[idx:], err)
		}
		return entries, result.Stdout, nil
	}
	if out == "" || strings.HasSuffix(out, "null") {
		return nil, result.Stdout, nil
	}
	return nil, result.Stdout, fmt.Errorf("no JSON payload found in agent list output: %q", result.Stdout)
}

type agentListEntry struct {
	PlanName  string `json:"plan_name"`
	JobTitle  string `json:"job_title"`
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
}

// codexNativeIDFromPath extracts the rollout UUID from a codex session
// filename (rollout-<timestamp>-<uuid>.jsonl; the UUID is the trailing 36
// chars — the same parse flow's codex provider uses).
func codexNativeIDFromPath(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if len(base) >= 36 {
		return base[len(base)-36:]
	}
	return base
}

// piNativeIDFromPath extracts the pi session id (segment after the last "_",
// mirroring flow's piNativeSessionID).
func piNativeIDFromPath(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if i := strings.LastIndex(base, "_"); i >= 0 && i+1 < len(base) {
		return base[i+1:]
	}
	return base
}

// providerOutcomeSpec parametrizes provider-specific outcome behavior.
type providerOutcomeSpec struct {
	provider ProviderConfig

	// eventDoc names the real provider event whose effect the scenario
	// simulates (documentation for the scenario listing).
	eventDoc string

	// simulateAgent runs the provider mock in workDir (so it writes its real
	// session-file layout in the sandbox HOME) and returns the discovered
	// native session id + transcript path. transcriptPath == "" means the
	// provider has no single transcript file (opencode).
	simulateAgent func(ctx *harness.Context, workDir string) (nativeID, transcriptPath string, err error)

	// transcriptProbes are substrings the archived transcript.jsonl must
	// contain; empty means NO transcript.jsonl must be archived.
	transcriptProbes []string
}

// simulateCodexAgent runs the codex mock and locates its rollout file via the
// SAME shared glob flow's discovery uses (transcript.CodexSessionsGlob). It
// also guards the P2 nested-layout regression: the file must live under the
// date-nested ~/.codex/sessions/YYYY/MM/DD/ tree and a flat-layout glob must
// find nothing.
func simulateCodexAgent(ctx *harness.Context, workDir string) (string, string, error) {
	home := ctx.HomeDir()

	cmd := ctx.Command("codex", "--full-auto")
	cmd.Dir(workDir)
	result := cmd.Run()
	ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("mock codex exited %d: %s", result.ExitCode, result.Stderr)
	}

	matches, err := filepath.Glob(transcript.CodexSessionsGlob(home, ""))
	if err != nil {
		return "", "", err
	}
	if len(matches) != 1 {
		return "", "", fmt.Errorf("expected exactly 1 codex session via the nested discovery glob, got %d (%v)", len(matches), matches)
	}
	flat, err := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*.jsonl"))
	if err != nil {
		return "", "", err
	}
	if len(flat) != 0 {
		return "", "", fmt.Errorf("codex mock wrote a FLAT session layout (%v); it must write ~/.codex/sessions/YYYY/MM/DD/", flat)
	}
	nested := regexp.MustCompile(`\.codex/sessions/\d{4}/\d{2}/\d{2}/rollout-.+\.jsonl$`)
	if !nested.MatchString(matches[0]) {
		return "", "", fmt.Errorf("codex session path %q is not date-nested (YYYY/MM/DD)", matches[0])
	}

	return codexNativeIDFromPath(matches[0]), matches[0], nil
}

// simulatePiAgent runs the pi mock in the job's workdir and locates its v3
// session file via the SAME shared glob flow's discovery uses
// (transcript.PiSessionsGlob), then cross-checks the per-cwd munged directory
// against transcript.PiSessionsDir for the resolved workdir.
func simulatePiAgent(ctx *harness.Context, workDir string) (string, string, error) {
	home := ctx.HomeDir()

	cmd := ctx.Command("pi", "Simulated interactive first prompt")
	cmd.Dir(workDir)
	result := cmd.Run()
	ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("mock pi exited %d: %s", result.ExitCode, result.Stderr)
	}

	matches, err := filepath.Glob(transcript.PiSessionsGlob(home, ""))
	if err != nil {
		return "", "", err
	}
	if len(matches) != 1 {
		return "", "", fmt.Errorf("expected exactly 1 pi session via the discovery glob, got %d (%v)", len(matches), matches)
	}

	// The per-cwd session dir must be the munged form of the (resolved)
	// workdir — the exact contract PiSessionDirName encodes.
	resolvedWorkDir := workDir
	if r, rerr := filepath.EvalSymlinks(workDir); rerr == nil {
		resolvedWorkDir = r
	}
	wantDir := transcript.PiSessionsDir(home, resolvedWorkDir)
	if filepath.Dir(matches[0]) != wantDir {
		return "", "", fmt.Errorf("pi session dir %q does not match PiSessionsDir(%q) = %q",
			filepath.Dir(matches[0]), resolvedWorkDir, wantDir)
	}

	return piNativeIDFromPath(matches[0]), matches[0], nil
}

// simulateOpencodeAgent runs the opencode mock (headless `run` shape) purely
// to exercise the refreshed mock; opencode has no single transcript file
// (fragmented storage + plugin-registered native id), so the native id is the
// plugin-shaped stub and no transcript path is bound.
func simulateOpencodeAgent(ctx *harness.Context, workDir string) (string, string, error) {
	cmd := ctx.Command("opencode", "run", "simulated prompt")
	cmd.Dir(workDir)
	result := cmd.Run()
	ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("mock opencode exited %d: %s", result.ExitCode, result.Stderr)
	}
	// Modern-layout marker: the refreshed mock must NOT recreate the stale
	// ~/.config/opencode/sessions layout.
	stale, err := filepath.Glob(filepath.Join(ctx.HomeDir(), ".config", "opencode", "sessions", "*"))
	if err != nil {
		return "", "", err
	}
	if len(stale) != 0 {
		return "", "", fmt.Errorf("opencode mock recreated the stale ~/.config/opencode/sessions layout: %v", stale)
	}
	return "ses_mock0123456789abcdef", "", nil
}

// createProviderOutcomeScenario builds the registered → listed → idle →
// completed lifecycle scenario for one provider.
func createProviderOutcomeScenario(o providerOutcomeSpec) *harness.Scenario {
	p := o.provider
	return harness.NewScenario(
		fmt.Sprintf("%s-outcome-lifecycle", p.Name),
		fmt.Sprintf("Tests the %s lifecycle beyond launch: registry intent -> visible in `flow agent list` -> idle on %s (idle must NOT complete the job) -> `flow plan complete` teardown + artifact archival", p.Name, o.eventDoc),
		[]string{"agent", "provider", p.Name, "lifecycle", "outcome"},
		[]harness.Step{
			harness.NewStep(fmt.Sprintf("Setup environment with %s provider", p.Name), func(ctx *harness.Context) error {
				projectName := fmt.Sprintf("%s-outcome-project", p.ProjectSuffix)
				projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
				if err != nil {
					return err
				}

				repo, err := git.SetupTestRepo(projectDir)
				if err != nil {
					return err
				}
				if err := fs.WriteString(filepath.Join(projectDir, "README.md"), fmt.Sprintf("# %s Outcome Test\n", p.Name)); err != nil {
					return err
				}
				if err := repo.AddCommit("Initial commit"); err != nil {
					return err
				}

				groveConfig := &config.Config{
					Name:    projectName,
					Version: "1.0",
					Extensions: map[string]interface{}{
						"flow": map[string]interface{}{
							"interactive_provider": p.Name,
						},
					},
				}
				if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
					return err
				}

				ctx.Set("notebooks_root", notebooksRoot)
				ctx.Set("project_name", projectName)
				return nil
			}),

			harness.SetupMocks(
				harness.Mock{CommandName: "grove"},
				harness.Mock{CommandName: p.MockName},
				harness.Mock{CommandName: "tmux"},
			),

			harness.NewStep("Initialize plan and add interactive_agent job", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				notebooksRoot := ctx.GetString("notebooks_root")
				projectName := ctx.GetString("project_name")

				planName := fmt.Sprintf("%s-outcome-plan", p.Name)
				cmd := ctx.Bin("plan", "init", planName, "--worktree")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				cmd = ctx.Bin("plan", "add", planName,
					"--type", "interactive_agent",
					"--title", fmt.Sprintf("%s Outcome Test", p.Name),
					"-p", fmt.Sprintf("Exercise the %s outcome lifecycle", p.Name))
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
				jobPath := filepath.Join(planPath, fmt.Sprintf("01-%s-outcome-test.md", p.Name))
				ctx.Set("plan_path", planPath)
				ctx.Set("plan_name", planName)
				ctx.Set("job_path", jobPath)

				jobID, err := jobIDFromFile(jobPath)
				if err != nil {
					return err
				}
				ctx.Set("job_id", jobID)
				return nil
			}),

			harness.NewStep("Run job: launch registers a session intent", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")
				jobID := ctx.GetString("job_id")

				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}
				if err := assert.YAMLField(jobPath, "status", "running", "job should be running"); err != nil {
					return err
				}

				// The provider must have registered an attempt-keyed session
				// intent before launch returned.
				attemptID, err := jobFrontmatterString(jobPath, "attempt_id")
				if err != nil {
					return err
				}
				ctx.Set("attempt_id", attemptID)
				intentDir := filepath.Join(hooksSessionsDir(ctx), attemptID)
				m, err := readSessionMetadata(intentDir)
				if err != nil {
					return fmt.Errorf("session intent not registered: %w", err)
				}
				workDir, _ := m["working_directory"].(string)
				if workDir == "" {
					return fmt.Errorf("intent metadata has no working_directory: %v", m)
				}
				ctx.Set("agent_work_dir", workDir)

				return ctx.Verify(func(v *verify.Collector) {
					v.Equal("intent records the provider", p.Name, m["provider"])
					v.Equal("intent records the job id", jobID, m["session_id"])
					v.Equal("intent records the attempt id", attemptID, m["attempt_id"])
					v.Equal("intent type is interactive_agent", "interactive_agent", m["type"])
				})
			}),

			harness.NewStep("Simulate the agent process + hooks confirm (native id, PID, transcript)", func(ctx *harness.Context) error {
				jobID := ctx.GetString("job_id")
				attemptID := ctx.GetString("attempt_id")
				workDir := ctx.GetString("agent_work_dir")

				nativeID, transcriptPath, err := o.simulateAgent(ctx, workDir)
				if err != nil {
					return err
				}
				ctx.Set("native_id", nativeID)
				ctx.Set("transcript_path", transcriptPath)

				if err := confirmIntentInPlace(ctx, jobID, attemptID, nativeID, transcriptPath); err != nil {
					return err
				}
				if err := fs.AssertExists(filepath.Join(hooksSessionsDir(ctx), attemptID, "metadata.json")); err != nil {
					return fmt.Errorf("attempt-keyed record missing after confirm: %w", err)
				}
				if nativeID != attemptID {
					if err := fs.AssertNotExists(filepath.Join(hooksSessionsDir(ctx), nativeID)); err != nil {
						return fmt.Errorf("confirm created duplicate native-keyed record: %w", err)
					}
				}
				return nil
			}),

			harness.NewStep("Agent is visible in `flow agent list`", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				planName := ctx.GetString("plan_name")
				jobID := ctx.GetString("job_id")

				entries, raw, err := agentListJSON(ctx, projectDir)
				if err != nil {
					return err
				}
				for _, e := range entries {
					if e.SessionID == jobID {
						return ctx.Verify(func(v *verify.Collector) {
							v.Equal("listed provider", p.Name, e.Provider)
							v.Equal("listed plan", planName, e.PlanName)
							v.Equal("listed status", "running", e.Status)
						})
					}
				}
				return fmt.Errorf("job %s not visible in `flow agent list` output: %s", jobID, raw)
			}),

			harness.NewStep(fmt.Sprintf("Outcome transition: %s => idle", o.eventDoc), func(ctx *harness.Context) error {
				jobID := ctx.GetString("job_id")
				attemptID := ctx.GetString("attempt_id")
				return simulateOutcomeEvent(ctx, jobID, attemptID, "idle")
			}),

			harness.NewStep("Idle must NOT complete the job, and drops it from the active agent list", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")
				jobID := ctx.GetString("job_id")

				// The provider going idle (turn complete / agent_end / stop)
				// means "awaiting the user", never "job done": the job file
				// must still be running.
				if err := assert.YAMLField(jobPath, "status", "running", "idle agent must not complete the job"); err != nil {
					return err
				}

				entries, _, err := agentListJSON(ctx, projectDir)
				if err != nil {
					return err
				}
				for _, e := range entries {
					if e.SessionID == jobID {
						return fmt.Errorf("idle session %s still listed as active (status %q)", jobID, e.Status)
					}
				}
				return nil
			}),

			harness.NewStep("Complete job: teardown + archival", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				_ = fs.RemoveIfExists(jobPath + ".lock")

				cmd := ctx.Bin("plan", "complete", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}
				return assert.YAMLField(jobPath, "status", "completed", "job should be completed")
			}),

			harness.NewStep("Verify session archival and terminal registry status", func(ctx *harness.Context) error {
				planPath := ctx.GetString("plan_path")
				jobID := ctx.GetString("job_id")
				nativeID := ctx.GetString("native_id")
				attemptID := ctx.GetString("attempt_id")

				artifactDir := filepath.Join(planPath, ".artifacts", jobID)
				archivedMetadata := filepath.Join(artifactDir, "metadata.json")
				if err := fs.AssertExists(archivedMetadata); err != nil {
					return fmt.Errorf("archived metadata.json should exist: %w", err)
				}
				metadataContent, err := fs.ReadString(archivedMetadata)
				if err != nil {
					return err
				}

				archivedTranscript := filepath.Join(artifactDir, "transcript.jsonl")
				var transcriptContent string
				if len(o.transcriptProbes) > 0 {
					if err := fs.AssertExists(archivedTranscript); err != nil {
						return fmt.Errorf("archived transcript.jsonl should exist: %w", err)
					}
					transcriptContent, err = fs.ReadString(archivedTranscript)
					if err != nil {
						return err
					}
				}

				// EndSession must have persisted the terminal status into the
				// (confirmed) registry record.
				sessionMeta, err := readSessionMetadata(filepath.Join(hooksSessionsDir(ctx), attemptID))
				if err != nil {
					return fmt.Errorf("confirmed session record should survive completion for archival: %w", err)
				}

				return ctx.Verify(func(v *verify.Collector) {
					v.Contains("archived metadata carries the native session id", metadataContent, nativeID)
					v.Contains("archived metadata carries the provider", metadataContent, fmt.Sprintf("%q", p.Name))
					for _, probe := range o.transcriptProbes {
						v.Contains("archived transcript carries provider content", transcriptContent, probe)
					}
					if len(o.transcriptProbes) == 0 {
						v.True("no transcript archived for a provider without a transcript file",
							fs.AssertNotExists(archivedTranscript) == nil)
					}
					v.Equal("registry status is terminal after EndSession", "completed", sessionMeta["status"])
				})
			}),
		},
	)
}

// createCodexNestedDiscoveryScenario is the focused P2 regression guard: the
// codex mock must write the date-nested rollout layout and the SHARED
// discovery glob (agentlogs transcript.CodexSessionsGlob — the single
// definition flow's transcript discovery, session scanning, and path lookup
// all use) must find it, while the pre-P2 flat layout finds nothing.
func createCodexNestedDiscoveryScenario() *harness.Scenario {
	return harness.NewScenario(
		"codex-nested-session-discovery",
		"Guards the P2 codex discovery-glob fix: mock codex writes ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl and the shared CodexSessionsGlob discovers it (flat layout matches nothing)",
		[]string{"agent", "provider", "codex", "discovery", "regression"},
		[]harness.Step{
			harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
				_, _, err := setupDefaultEnvironment(ctx, "codex-discovery-project")
				return err
			}),

			harness.SetupMocks(
				harness.Mock{CommandName: "codex"},
			),

			harness.NewStep("Run mock codex and verify nested layout + discovery glob", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				nativeID, transcriptPath, err := simulateCodexAgent(ctx, projectDir)
				if err != nil {
					return err
				}

				content, err := fs.ReadString(transcriptPath)
				if err != nil {
					return err
				}

				uuidShape := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
				return ctx.Verify(func(v *verify.Collector) {
					v.True("rollout filename ends in a parseable UUID", uuidShape.MatchString(nativeID))
					v.Contains("rollout log carries session_meta", content, "session_meta")
					v.Contains("rollout log carries the session id", content, nativeID)
				})
			}),
		},
	)
}

// Exported outcome-lifecycle scenarios.
var (
	// codex: `hooks codex install` wires a notify hook; its
	// agent-turn-complete event maps to the idle outcome (P2).
	CodexOutcomeLifecycleScenario = createProviderOutcomeScenario(providerOutcomeSpec{
		provider:         ProviderByName("codex"),
		eventDoc:         "codex notify agent-turn-complete",
		simulateAgent:    simulateCodexAgent,
		transcriptProbes: []string{"Mock response from codex"},
	})

	// pi: the stop pipeline's agent_end event maps to idle (P3 decision).
	PiOutcomeLifecycleScenario = createProviderOutcomeScenario(providerOutcomeSpec{
		provider:         ProviderByName("pi"),
		eventDoc:         "pi agent_end",
		simulateAgent:    simulatePiAgent,
		transcriptProbes: []string{`"version":3`, `"cost"`, "Mock response from pi"},
	})

	// opencode: the grove-opencode plugin's stop event maps to idle and must
	// never auto-complete the job (P4 idle-not-complete semantics); opencode
	// has no single transcript file, so completion archives metadata only.
	OpencodeOutcomeLifecycleScenario = createProviderOutcomeScenario(providerOutcomeSpec{
		provider:         ProviderByName("opencode"),
		eventDoc:         "opencode plugin stop",
		simulateAgent:    simulateOpencodeAgent,
		transcriptProbes: nil,
	})

	// Focused codex nested-layout discovery regression guard (P2).
	CodexNestedDiscoveryScenario = createCodexNestedDiscoveryScenario()
)
