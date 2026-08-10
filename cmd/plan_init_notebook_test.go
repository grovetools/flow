package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreconfig "github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/coderoot"
)

// TestMain stubs the nb materialization exec for the whole cmd package:
// executePlanInit is a note-writing action that would otherwise shell out to
// the real `nb` binary and mutate live config.
func TestMain(m *testing.M) {
	ensureDefaultNotebookFn = func(context.Context) ([]byte, []byte, error) {
		return []byte("null\n"), nil, nil
	}
	os.Exit(m.Run())
}

func stubEnsureNotebook(t *testing.T, stdout, stderr []byte, err error) {
	t.Helper()
	prev := ensureDefaultNotebookFn
	ensureDefaultNotebookFn = func(context.Context) ([]byte, []byte, error) {
		return stdout, stderr, err
	}
	t.Cleanup(func() { ensureDefaultNotebookFn = prev })
}

func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = prev }()
	callErr := fn()
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return string(data), callErr
}

func TestEnsureDefaultNotebook_MissingNbIsFatal(t *testing.T) {
	stubEnsureNotebook(t, nil, nil, errors.New("exec: nb: not found"))
	_, err := captureStderr(t, func() error { return ensureDefaultNotebook(context.Background()) })
	if err == nil || !strings.Contains(err.Error(), "nb: not found") {
		t.Fatalf("expected missing nb error, got %v", err)
	}
}

func TestEnsureDefaultNotebook_NoOpOnNull(t *testing.T) {
	stubEnsureNotebook(t, []byte("null\n"), nil, nil)
	out, err := captureStderr(t, func() error { return ensureDefaultNotebook(context.Background()) })
	if err != nil {
		t.Fatalf("ensureDefaultNotebook: %v", err)
	}
	if out != "" {
		t.Fatalf("expected no announcement on a no-op pass, got %q", out)
	}
}

func TestEnsureDefaultNotebook_RejectsNonJSONStdout(t *testing.T) {
	stubEnsureNotebook(t, []byte("pretty logger output\n{\"root_dir\":\"/tmp/nb\",\"created\":true,\"recorded\":true}\n"), nil, nil)
	_, err := captureStderr(t, func() error { return ensureDefaultNotebook(context.Background()) })
	if err == nil || !strings.Contains(err.Error(), "invalid nb ensure-notebook protocol") {
		t.Fatalf("expected strict protocol error, got %v", err)
	}
	if !strings.Contains(err.Error(), "pretty logger output") {
		t.Fatalf("protocol error should retain stdout context, got %v", err)
	}
}

func TestEnsureDefaultNotebook_RejectsUnknownProtocolField(t *testing.T) {
	stubEnsureNotebook(t, []byte(`{"root_dir":"/tmp/nb","created":true,"future_field":true}`), nil, nil)
	_, err := captureStderr(t, func() error { return ensureDefaultNotebook(context.Background()) })
	if err == nil || !strings.Contains(err.Error(), `unknown field "future_field"`) {
		t.Fatalf("expected unknown protocol field error, got %v", err)
	}
}

func TestEnsureDefaultNotebook_ForwardsStderrAndAnnouncesMaterialization(t *testing.T) {
	stubEnsureNotebook(t,
		[]byte(`{"notebook_name":"personal","root_dir":"/tmp/home/notebooks/nb","created":false,"marker_created":true,"recorded":false}`),
		[]byte("WARN notebook.materialized\n"), nil)
	out, err := captureStderr(t, func() error { return ensureDefaultNotebook(context.Background()) })
	if err != nil {
		t.Fatalf("ensureDefaultNotebook: %v", err)
	}
	for _, want := range []string{"WARN notebook.materialized", "/tmp/home/notebooks/nb"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stderr %q does not contain %q", out, want)
		}
	}
}

func TestRunNbEnsureNotebook_PreservesNonzeroAndStderr(t *testing.T) {
	installFakeNb(t, "#!/bin/sh\necho materialization-broke >&2\nexit 9\n")
	_, stderr, err := runNbEnsureNotebook(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exit status 9") {
		t.Fatalf("expected exit status context, got %v", err)
	}
	if got := string(stderr); !strings.Contains(got, "materialization-broke") {
		t.Fatalf("stderr = %q", got)
	}

	stubEnsureNotebook(t, nil, stderr, err)
	_, ensureErr := captureStderr(t, func() error { return ensureDefaultNotebook(context.Background()) })
	if ensureErr == nil || !strings.Contains(ensureErr.Error(), "materialization-broke") || !strings.Contains(ensureErr.Error(), "exit status 9") {
		t.Fatalf("ensure error did not preserve child context: %v", ensureErr)
	}
}

func TestRunNbEnsureNotebook_TimesOutAndPreservesStderr(t *testing.T) {
	installFakeNb(t, "#!/bin/sh\necho started-materialization >&2\nexec sleep 5\n")
	prev := defaultNotebookEnsureTimeout
	defaultNotebookEnsureTimeout = 500 * time.Millisecond
	t.Cleanup(func() { defaultNotebookEnsureTimeout = prev })

	_, stderr, err := runNbEnsureNotebook(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if !strings.Contains(string(stderr), "started-materialization") {
		t.Fatalf("timeout lost child stderr: %q", stderr)
	}
}

func TestApplyRecordedNotebooksRoutesThroughAuthoritativeTable(t *testing.T) {
	legacy := &coreconfig.Config{Notebooks: &coreconfig.NotebooksConfig{
		Definitions: map[string]*coreconfig.Notebook{"legacy": {RootDir: "/legacy/fallback"}},
		Rules:       &coreconfig.NotebookRules{Default: "legacy"},
	}}
	table := coderoot.Table{
		NotebooksFilePath: "/config/notebooks.toml",
		Default:           "work",
		Notebooks: map[string]coderoot.Notebook{
			"work": {Root: "/recorded/custom-root"},
		},
	}

	got := applyRecordedNotebooks(legacy, table)
	if got.Notebooks == nil || got.Notebooks.Rules == nil {
		t.Fatal("recorded notebook projection is missing")
	}
	if got.Notebooks.Rules.Default != "work" {
		t.Fatalf("default = %q, want work", got.Notebooks.Rules.Default)
	}
	if root := got.Notebooks.Definitions["work"].RootDir; root != "/recorded/custom-root" {
		t.Fatalf("recorded root = %q", root)
	}
	if _, exists := got.Notebooks.Definitions["legacy"]; exists {
		t.Fatal("legacy fallback survived authoritative notebooks.toml projection")
	}
}

func TestExecutePlanInit_AbortsWhenNotebookMaterializationFails(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "must-not-exist")
	sentinel := errors.New("materializer failed")
	stubEnsureNotebook(t, nil, []byte("recording denied"), sentinel)

	_, err := executePlanInit(&PlanInitCmd{Dir: planPath})
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("expected plan init to return materialization failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "recording denied") {
		t.Fatalf("expected stderr context, got %v", err)
	}
	if _, statErr := os.Stat(planPath); !os.IsNotExist(statErr) {
		t.Fatalf("plan path was touched despite materialization failure: %v", statErr)
	}
}

func installFakeNb(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nb")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake nb: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
