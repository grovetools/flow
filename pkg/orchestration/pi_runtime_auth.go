package orchestration

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type managedPiRuntimeMetadata struct {
	SchemaVersion     int    `json:"schema_version"`
	Version           string `json:"version"`
	AuthPath          string `json:"auth_path"`
	AuthHelper        string `json:"auth_helper"`
	IsolationBoundary string `json:"isolation_boundary"`
}

type managedPiPolicy struct {
	Version int `json:"version"`
	Bash    struct {
		Deny []string `json:"deny"`
	} `json:"bash"`
	Paths struct {
		ConfineWritesToCwd bool     `json:"confineWritesToCwd"`
		Protect            []string `json:"protect"`
	} `json:"paths"`
	OnViolation string `json:"onViolation"`
}

type managedPiAuthStatus struct {
	Provider string `json:"provider"`
	Present  bool   `json:"present"`
	Type     string `json:"type,omitempty"`
}

// requireManagedPiCodexAuth is inert on ordinary developer machines. A full
// satellite runtime stamps its metadata file; there, provider-pi is the stock
// Codex profile and must fail before launch with actionable login guidance.
// The helper's stderr is discarded and only a bounded metadata shape is read.
func requireManagedPiCodexAuth() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	metadataPath := filepath.Join(home, ".config", "grove", "pi-runtime", "metadata.json")
	info, err := os.Lstat(metadataPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed Pi runtime metadata is unhealthy")
	}
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return fmt.Errorf("managed Pi runtime metadata is unhealthy")
	}
	var metadata managedPiRuntimeMetadata
	if json.Unmarshal(data, &metadata) != nil || metadata.SchemaVersion != 1 || metadata.Version != "0.80.10" || metadata.IsolationBoundary != "tart-vm" {
		return fmt.Errorf("managed Pi runtime metadata is unhealthy")
	}
	policyPath := filepath.Join(home, ".config", "grove", "pi-runtime", "grove-policy.json")
	policyData, policyErr := os.ReadFile(policyPath)
	var policy managedPiPolicy
	if policyErr != nil || json.Unmarshal(policyData, &policy) != nil || policy.Version != 1 || policy.OnViolation != "deny" || !policy.Paths.ConfineWritesToCwd || len(policy.Paths.Protect) == 0 {
		return fmt.Errorf("managed Pi policy/guard capability is unhealthy; the Tart VM remains the isolation boundary")
	}
	for _, pattern := range policy.Bash.Deny {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("managed Pi policy/guard capability is unhealthy; the Tart VM remains the isolation boundary")
		}
	}
	storePrefix := filepath.Join(home, ".local", "share", "grove", "pi-packages", "sha256") + string(filepath.Separator)
	if !strings.HasPrefix(filepath.Clean(metadata.AuthHelper), storePrefix) || filepath.Clean(metadata.AuthPath) != filepath.Join(home, ".pi", "agent", "auth.json") {
		return fmt.Errorf("managed Pi runtime metadata is unhealthy")
	}
	cmd := exec.Command("node", metadata.AuthHelper, "status", "--auth-path", metadata.AuthPath) //nolint:gosec // path is confined to the managed content store above
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil || len(out) > 1024 {
		return fmt.Errorf("managed Pi authentication health check failed; from the laptop run `grove satellite auth pi-codex status <name>`")
	}
	var status managedPiAuthStatus
	statusDec := json.NewDecoder(strings.NewReader(string(out)))
	statusDec.DisallowUnknownFields()
	if statusDec.Decode(&status) != nil || status.Provider != "openai-codex" || (status.Type != "" && status.Type != "oauth") {
		return fmt.Errorf("managed Pi authentication health check returned invalid metadata")
	}
	if !status.Present {
		return fmt.Errorf("Pi openai-codex login is required; from the laptop run `grove satellite auth pi-codex login <name>`")
	}
	return nil
}
