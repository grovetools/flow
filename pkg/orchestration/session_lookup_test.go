package orchestration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/sirupsen/logrus"
)

func TestFindAgentSessionInfo_MissingPIDAfterRegistryGCIsDebug(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	// Keep this negative probe away from any daemon serving the developer's
	// real environment so the filesystem fixture is authoritative.
	t.Setenv("GROVE_HOST_DAEMON_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))

	jobID := "lookup-after-registry-gc-7d3b1f92"
	sessionDir := filepath.Join(os.Getenv("GROVE_HOME"), "state", "grove", "hooks", "sessions", "stale-attempt")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{"session_id": jobID, "provider": "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "metadata.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	// Deliberately no pid.lock: registry GC preserves metadata after clearing
	// the stale liveness marker.

	logger := grovelogging.NewLogger("flow.session.lookup").Logger
	oldOut, oldFormatter, oldLevel := logger.Out, logger.Formatter, logger.GetLevel()
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.DebugLevel)
	t.Cleanup(func() {
		logger.SetOutput(oldOut)
		logger.SetFormatter(oldFormatter)
		logger.SetLevel(oldLevel)
	})

	if _, _, err := findAgentSessionInfo(jobID); err == nil {
		t.Fatal("missing pid.lock should remain a negative lookup result")
	}
	got := logs.String()
	if !strings.Contains(got, `"msg":"PID file already cleared for found session"`) ||
		!strings.Contains(got, `"level":"debug"`) {
		t.Fatalf("expected debug diagnostic for GC-cleared pid.lock, logs:\n%s", got)
	}
	if strings.Contains(got, `"msg":"Failed to read PID file for found session"`) {
		t.Fatalf("expected negative probe was still logged as an error:\n%s", got)
	}
}
