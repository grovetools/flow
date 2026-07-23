package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupManagedPiRuntime(t *testing.T, helperOutput string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := filepath.Join(home, ".local", "share", "grove", "pi-packages", "sha256", strings.Repeat("a", 64))
	if err := os.MkdirAll(filepath.Join(store, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(store, "bin", "pi-codex-auth.mjs")
	script := "process.stdout.write(" + string(mustJSON(t, helperOutput)) + ");\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	metadataDir := filepath.Join(home, ".config", "grove", "pi-runtime")
	if err := os.MkdirAll(metadataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeModule := filepath.Join(home, "lib", "node_modules", "@earendil-works", "pi-coding-agent", "dist", "index.js")
	if err := os.MkdirAll(filepath.Dir(runtimeModule), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeModule, []byte("export {};\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"schema_version": 1, "version": "0.80.10", "auth_path": filepath.Join(home, ".pi", "agent", "auth.json"), "auth_helper": helper, "auth_runtime_module": runtimeModule, "isolation_boundary": "tart-vm"}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(metadataDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	policy := `{"version":1,"bash":{"deny":["\\brm\\s+-rf\\b"]},"paths":{"confineWritesToCwd":true,"protect":[".git/"]},"onViolation":"deny"}`
	if err := os.WriteFile(filepath.Join(metadataDir, "grove-policy.json"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRequireManagedPiCodexAuthIsInertOffSatellite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := requireManagedPiCodexAuth(); err != nil {
		t.Fatal(err)
	}
}

func TestRequireManagedPiCodexAuthGuidesMissingLogin(t *testing.T) {
	setupManagedPiRuntime(t, `{"provider":"openai-codex","present":false}`+"\n")
	err := requireManagedPiCodexAuth()
	if err == nil || !strings.Contains(err.Error(), "grove satellite auth pi-codex login <name>") {
		t.Fatalf("error = %v", err)
	}
}

func TestRequireManagedPiCodexAuthAcceptsMetadataOnlyPresence(t *testing.T) {
	setupManagedPiRuntime(t, `{"provider":"openai-codex","present":true,"type":"oauth"}`+"\n")
	if err := requireManagedPiCodexAuth(); err != nil {
		t.Fatal(err)
	}
}

func TestRequireManagedPiCodexAuthReportsMalformedPolicyAsCapabilityUnhealthy(t *testing.T) {
	setupManagedPiRuntime(t, `{"provider":"openai-codex","present":true,"type":"oauth"}`+"\n")
	home, _ := os.UserHomeDir()
	if err := os.WriteFile(filepath.Join(home, ".config", "grove", "pi-runtime", "grove-policy.json"), []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := requireManagedPiCodexAuth()
	if err == nil || !strings.Contains(err.Error(), "policy/guard capability is unhealthy") || !strings.Contains(err.Error(), "VM remains the isolation boundary") {
		t.Fatalf("error = %v", err)
	}
}

func TestRequireManagedPiCodexAuthNeverEchoesHelperValues(t *testing.T) {
	canary := "GROVE_SECRET_CANARY_DO_NOT_LOG"
	setupManagedPiRuntime(t, `{"provider":"openai-codex","present":true,"access":"`+canary+`"}`+"\n")
	err := requireManagedPiCodexAuth()
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("redaction error = %v", err)
	}
}
