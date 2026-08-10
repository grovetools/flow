package cmd

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// TestMain stubs the nb materialization exec for the whole cmd package:
// executePlanInit is a note-writing action that would otherwise shell out to
// the real `nb` binary, and on an unconfigured machine that pass mutates live
// config. Tests must stay inside temp homes, so the default is a no-op stub;
// tests that exercise the wiring override it locally.
func TestMain(m *testing.M) {
	ensureDefaultNotebookFn = func() ([]byte, error) { return []byte("null"), nil }
	os.Exit(m.Run())
}

// stubEnsureNotebook swaps the materialization exec for one test.
func stubEnsureNotebook(t *testing.T, out []byte, err error) {
	t.Helper()
	prev := ensureDefaultNotebookFn
	ensureDefaultNotebookFn = func() ([]byte, error) { return out, err }
	t.Cleanup(func() { ensureDefaultNotebookFn = prev })
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = prev }()
	fn()
	w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

func TestEnsureDefaultNotebook_ToleratesMissingNb(t *testing.T) {
	stubEnsureNotebook(t, nil, errors.New("exec: nb: not found"))
	out := captureStderr(t, ensureDefaultNotebook)
	if out != "" {
		t.Fatalf("expected silence when nb is unavailable, got %q", out)
	}
}

func TestEnsureDefaultNotebook_NoOpOnNull(t *testing.T) {
	stubEnsureNotebook(t, []byte("null\n"), nil)
	out := captureStderr(t, ensureDefaultNotebook)
	if out != "" {
		t.Fatalf("expected no announcement on a no-op pass, got %q", out)
	}
}

func TestEnsureDefaultNotebook_ToleratesGarbageOutput(t *testing.T) {
	stubEnsureNotebook(t, []byte("not json at all"), nil)
	out := captureStderr(t, ensureDefaultNotebook)
	if out != "" {
		t.Fatalf("expected silence on unparseable output, got %q", out)
	}
}

func TestEnsureDefaultNotebook_ParsesJSONAfterPrettyOutput(t *testing.T) {
	// nb's unified logger prints its pretty announcement to stdout ahead of
	// the JSON document; the wiring must parse the last line.
	stubEnsureNotebook(t, []byte("Default notebook materialized at /sb/notebooks/nb\n\n{\"root_dir\":\"/sb/notebooks/nb\",\"created\":true,\"recorded\":true}\n"), nil)
	out := captureStderr(t, ensureDefaultNotebook)
	if !strings.Contains(out, "/sb/notebooks/nb") {
		t.Fatalf("expected the materialized root in the announcement, got %q", out)
	}
}

func TestEnsureDefaultNotebook_AnnouncesMaterialization(t *testing.T) {
	stubEnsureNotebook(t, []byte(`{"notebook_name":"personal","root_dir":"/tmp/home/notebooks/nb","created":true,"recorded":true,"config_path":"/tmp/grove/config/grove/grove.toml"}`), nil)
	out := captureStderr(t, ensureDefaultNotebook)
	if !strings.Contains(out, "/tmp/home/notebooks/nb") {
		t.Fatalf("expected the materialized root in the announcement, got %q", out)
	}
}
