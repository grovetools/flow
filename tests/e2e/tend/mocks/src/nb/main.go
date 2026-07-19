package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Mock nb command for flow's tend e2e scenarios.
//
// It implements the four nb invocations flow makes through its single note-link
// seam (flow/pkg/orchestration/note_link.go):
//
//	nb new <title> --type <type> --no-edit
//	nb list --plan-ref plans/<name> --json --workspaces
//	nb move <path> <group> --force
//	nb internal update-frontmatter --path <p> --field <f> --value <v>
//
// It is filesystem-backed: every verb reads and writes real note files in the
// scenario's sandbox notebook. Frontmatter handling is intentionally minimal
// (flat `key: value` lines) — enough for the note<->plan linkage fields, not a
// general YAML implementation. The mock is standalone (stdlib only) because
// tend compiles each mocks/src/<name>/ directory on its own.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "[MOCK NB] No command provided\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "new":
		handleNew()
		return
	case "list":
		handleList()
		return
	case "move":
		handleMove()
		return
	case "internal":
		if len(os.Args) > 2 && os.Args[2] == "update-frontmatter" {
			handleUpdateFrontmatter()
			return
		}
	}

	fmt.Fprintf(os.Stderr, "[MOCK NB] Unhandled command: %s\n", strings.Join(os.Args[1:], " "))
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// nb new
// ---------------------------------------------------------------------------

func handleNew() {
	// Parse args: nb new <title> --type <type> --no-edit
	var title string
	var noteType string

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--type", "-t":
			if i+1 < len(args) {
				noteType = args[i+1]
				i++
			}
		case "--no-edit":
			// skip
		default:
			if !strings.HasPrefix(args[i], "-") && title == "" {
				title = args[i]
			}
		}
	}

	if noteType == "" {
		noteType = "inbox"
	}

	// Read stdin for body content
	var body string
	stdinContent, err := io.ReadAll(os.Stdin)
	if err == nil && len(stdinContent) > 0 {
		body = string(stdinContent)
	}

	// Sanitize title for filename
	slug := strings.ToLower(title)
	slug = strings.ReplaceAll(slug, " ", "-")
	sanitized := make([]byte, 0, len(slug))
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			sanitized = append(sanitized, c)
		}
	}

	datestamp := time.Now().Format("20060102")
	filename := fmt.Sprintf("%s-%s.md", datestamp, string(sanitized))

	// Create the note in <cwd>/<noteType>/
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Error getting cwd: %v\n", err)
		os.Exit(1)
	}

	noteDir := filepath.Join(cwd, noteType)
	if err := os.MkdirAll(noteDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Error creating note dir: %v\n", err)
		os.Exit(1)
	}

	notePath := filepath.Join(noteDir, filename)

	// Build note content with frontmatter
	content := fmt.Sprintf("---\ntitle: %s\ntype: %s\n---\n", title, noteType)
	if body != "" {
		content += "\n" + body + "\n"
	}

	if err := os.WriteFile(notePath, []byte(content), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Error writing note: %v\n", err)
		os.Exit(1)
	}

	// Print path to stdout so parseNotePathFromOutput can find it
	fmt.Println(notePath)

	fmt.Fprintf(os.Stderr, "[MOCK NB] Created note: %s\n", notePath)
}

// ---------------------------------------------------------------------------
// nb list
// ---------------------------------------------------------------------------

// mockNote mirrors the subset of nb's Note JSON that flow's PlanNote decodes.
type mockNote struct {
	Path      string `json:"path"`
	PlanRef   string `json:"plan_ref"`
	PlanJob   string `json:"plan_job"`
	Workspace string `json:"workspace"`
	Title     string `json:"title"`
	Type      string `json:"type"`
}

// lifecycleGroups are the notebook directories a note may live in. The mock
// treats any directory with one of these names as a note group.
var lifecycleGroups = []string{"inbox", "in_progress", "review", "completed"}

func isLifecycleGroup(name string) bool {
	for _, g := range lifecycleGroups {
		if name == g {
			return true
		}
	}
	return false
}

// handleList implements `nb list [--plan-ref <ref>] [--json] [--workspaces]`.
//
// Only the JSON shape is emitted; --workspaces is accepted and always implied
// (the mock scans every workspace it can find). A non-JSON human listing is not
// emulated because flow never asks for one.
func handleList() {
	var planRef string
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--plan-ref":
			if i+1 < len(args) {
				planRef = args[i+1]
				i++
			}
		case "--json", "--workspaces":
			// accepted; --json is the only output shape the mock produces and
			// --workspaces is always implied.
		}
	}

	notes := make([]mockNote, 0)
	for _, path := range scanNotes() {
		fm, _, err := readNote(path)
		if err != nil {
			continue
		}
		if planRef != "" && fm.get("plan_ref") != planRef {
			continue
		}
		notes = append(notes, mockNote{
			Path:      path,
			PlanRef:   fm.get("plan_ref"),
			PlanJob:   fm.get("plan_job"),
			Workspace: filepath.Base(workspaceRootOf(path)),
			Title:     fm.get("title"),
			Type:      fm.get("type"),
		})
	}

	out, err := json.Marshal(notes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Error marshaling notes: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "[MOCK NB] list --plan-ref %q matched %d note(s)\n", planRef, len(notes))
}

// scanNotes walks every notebook root the mock can discover and returns the
// paths of all .md files sitting directly inside a lifecycle group directory.
func scanNotes() []string {
	var found []string
	seen := map[string]bool{}

	for _, root := range notebookRoots() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // unreadable subtrees are simply skipped
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			if !isLifecycleGroup(filepath.Base(filepath.Dir(path))) {
				return nil
			}
			if !seen[path] {
				seen[path] = true
				found = append(found, path)
			}
			return nil
		})
	}

	sort.Strings(found)
	return found
}

// notebookRoots discovers the sandbox notebook(s) to scan. In tend scenarios
// the notebook lives under $HOME/notebooks/<name>/; the cwd is also walked
// upward so the mock works when invoked from inside a workspace.
func notebookRoots() []string {
	var roots []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			return
		}
		seen[p] = true
		roots = append(roots, p)
	}

	if dir := os.Getenv("NB_NOTEBOOK_DIR"); dir != "" {
		add(dir)
	}
	if dir := os.Getenv("NB_DIR"); dir != "" {
		add(dir)
	}

	// $HOME/notebooks/* — the tend sandbox layout.
	if home, err := os.UserHomeDir(); err == nil {
		notebooks := filepath.Join(home, "notebooks")
		if entries, err := os.ReadDir(notebooks); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					add(filepath.Join(notebooks, e.Name()))
				}
			}
		}
	}

	// Walk up from cwd looking for a directory that contains workspaces/.
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for {
			if info, err := os.Stat(filepath.Join(dir, "workspaces")); err == nil && info.IsDir() {
				add(dir)
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return roots
}

// workspaceRootOf returns the workspace directory containing a note path, i.e.
// the parent of the note's lifecycle group directory.
func workspaceRootOf(notePath string) string {
	groupDir := filepath.Dir(notePath)
	if isLifecycleGroup(filepath.Base(groupDir)) {
		return filepath.Dir(groupDir)
	}
	return groupDir
}

// ---------------------------------------------------------------------------
// nb move
// ---------------------------------------------------------------------------

// handleMove implements both destination forms of `nb move <path> <dest>
// [--force]`, dispatching on <dest> exactly as real nb does (see
// nb/cmd/move.go): a bare lifecycle group name is a note-type move, anything
// else is an explicit destination PATH.
//
//	nb move <path> inbox            -> <own-workspace>/inbox/     ("To:" output)
//	nb move <path> /abs/ws-c/inbox  -> that directory             ("Moved successfully:")
//
// The path form is what flow's --workspace routing rides on: it is the only
// way to land a note in a DIFFERENT workspace while still going through nb.
// Output shapes mirror real nb's two code paths, including the fact that the
// path form does NOT print a "To:" line. The mock still does not emulate note
// index updates or daemon notification.
func handleMove() {
	var positional []string
	for _, a := range os.Args[2:] {
		if strings.HasPrefix(a, "-") {
			// --force (and any other flag) is accepted and ignored: the mock
			// always overwrites the destination.
			continue
		}
		positional = append(positional, a)
	}

	if len(positional) < 2 {
		fmt.Fprintf(os.Stderr, "[MOCK NB] move requires <path> <group>\n")
		os.Exit(1)
	}

	src, rawDest := positional[0], positional[1]
	if !filepath.IsAbs(src) {
		abs, err := filepath.Abs(src)
		if err == nil {
			src = abs
		}
	}

	if _, err := os.Stat(src); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] move: source note not found: %s\n", src)
		os.Exit(1)
	}

	// Real nb treats a bare note-type/group token as a type move and anything
	// else as a literal destination path.
	pathForm := !isLifecycleGroup(rawDest)

	var dest string
	if pathForm {
		dest = rawDest
		if !filepath.IsAbs(dest) {
			if abs, err := filepath.Abs(dest); err == nil {
				dest = abs
			}
		}
		// Mirror real nb (cmd/move.go moveToPath): the source basename is
		// appended ONLY when the destination already exists as a directory.
		// A destination that does not exist yet is a literal file path, and
		// its parent is created. Getting this branch wrong is not academic —
		// treating it as a directory unconditionally would turn a move into
		// "<dir>/<name>.md/<name>.md".
		if info, err := os.Stat(dest); err == nil && info.IsDir() {
			dest = filepath.Join(dest, filepath.Base(src))
		}
	} else {
		dest = filepath.Join(workspaceRootOf(src), rawDest, filepath.Base(src))
	}
	destDir := filepath.Dir(dest)

	if dest == src {
		// Already at the destination — report it as a no-op move, in whichever
		// output shape the invoked form uses.
		if pathForm {
			fmt.Printf("Moved successfully: %s -> %s\n", src, dest)
		} else {
			fmt.Printf("From: %s\nTo: %s\n", src, dest)
		}
		return
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] move: creating %s: %v\n", destDir, err)
		os.Exit(1)
	}

	if err := os.Rename(src, dest); err != nil {
		// Fall back to copy+remove for cross-device moves.
		data, rErr := os.ReadFile(src) //nolint:gosec // sandbox path
		if rErr != nil {
			fmt.Fprintf(os.Stderr, "[MOCK NB] move: reading %s: %v\n", src, rErr)
			os.Exit(1)
		}
		if wErr := os.WriteFile(dest, data, 0o600); wErr != nil {
			fmt.Fprintf(os.Stderr, "[MOCK NB] move: writing %s: %v\n", dest, wErr)
			os.Exit(1)
		}
		if rmErr := os.Remove(src); rmErr != nil {
			fmt.Fprintf(os.Stderr, "[MOCK NB] move: removing %s: %v\n", src, rmErr)
			os.Exit(1)
		}
	}

	if pathForm {
		fmt.Printf("Moved successfully: %s -> %s\n", src, dest)
	} else {
		fmt.Printf("Moved note\nFrom: %s\nTo: %s\n", src, dest)
	}
	fmt.Fprintf(os.Stderr, "[MOCK NB] Moved %s -> %s\n", src, dest)
}

// ---------------------------------------------------------------------------
// nb internal update-frontmatter
// ---------------------------------------------------------------------------

// handleUpdateFrontmatter implements
// `nb internal update-frontmatter --path <p> --field <f> --value <v>`.
//
// Both --path and --file are accepted as the target flag. An EMPTY --value
// CLEARS the field (removes the line entirely) — flow's demote path depends on
// that clearing semantics. A non-empty value sets the field, appending it to
// the frontmatter block when absent.
func handleUpdateFrontmatter() {
	var path, field, value string
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--path", "--file":
			if i+1 < len(args) {
				path = args[i+1]
				i++
			}
		case "--field":
			if i+1 < len(args) {
				field = args[i+1]
				i++
			}
		case "--value":
			if i+1 < len(args) {
				value = args[i+1]
				i++
			}
		}
	}

	if path == "" || field == "" {
		fmt.Fprintf(os.Stderr, "[MOCK NB] update-frontmatter requires --path and --field\n")
		os.Exit(1)
	}

	fm, body, err := readNote(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] update-frontmatter: %v\n", err)
		os.Exit(1)
	}

	if value == "" {
		fm.clear(field)
	} else {
		fm.set(field, value)
	}

	if err := writeNote(path, fm, body); err != nil {
		fmt.Fprintf(os.Stderr, "[MOCK NB] update-frontmatter: writing %s: %v\n", path, err)
		os.Exit(1)
	}

	if value == "" {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Cleared %s on %s\n", field, path)
	} else {
		fmt.Fprintf(os.Stderr, "[MOCK NB] Set %s=%s on %s\n", field, value, path)
	}
}

// ---------------------------------------------------------------------------
// minimal frontmatter
// ---------------------------------------------------------------------------

// frontmatter is an ordered set of flat `key: value` pairs. It deliberately
// supports only scalar values on one line — enough for title/type/plan_ref/
// plan_job. Nested maps, sequences, and block scalars are preserved verbatim
// only insofar as they round-trip as opaque lines.
type frontmatter struct {
	keys   []string
	values map[string]string
	// raw holds lines that are not simple `key: value` pairs (comments, list
	// items, blank lines) so they survive a rewrite in their original slot.
	raw map[string]string
}

func newFrontmatter() *frontmatter {
	return &frontmatter{values: map[string]string{}, raw: map[string]string{}}
}

func (f *frontmatter) get(key string) string { return f.values[key] }

func (f *frontmatter) set(key, value string) {
	if _, ok := f.values[key]; !ok {
		f.keys = append(f.keys, key)
	}
	f.values[key] = value
}

func (f *frontmatter) clear(key string) {
	if _, ok := f.values[key]; !ok {
		return
	}
	delete(f.values, key)
	kept := f.keys[:0]
	for _, k := range f.keys {
		if k != key {
			kept = append(kept, k)
		}
	}
	f.keys = kept
}

// readNote parses a note file into its frontmatter and body. A note without a
// frontmatter block yields an empty frontmatter and the whole file as body.
func readNote(path string) (*frontmatter, string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // sandbox path
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}
	return parseFrontmatter(string(data))
}

func parseFrontmatter(content string) (*frontmatter, string, error) {
	fm := newFrontmatter()

	if !strings.HasPrefix(content, "---\n") {
		return fm, content, nil
	}

	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			end = len(rest) - len("\n---")
		} else {
			// Unterminated block: treat the whole file as body.
			return fm, content, nil
		}
	}

	block := rest[:end]
	body := ""
	if end+len("\n---\n") <= len(rest) {
		body = rest[end+len("\n---\n"):]
	}

	for i, line := range strings.Split(block, "\n") {
		idx := strings.Index(line, ":")
		key := ""
		if idx > 0 {
			key = strings.TrimSpace(line[:idx])
		}
		if key == "" || strings.HasPrefix(strings.TrimSpace(line), "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-") {
			// Not a simple top-level key: preserve verbatim under a synthetic key.
			rawKey := fmt.Sprintf("\x00raw-%d", i)
			fm.keys = append(fm.keys, rawKey)
			fm.raw[rawKey] = line
			continue
		}
		fm.set(key, strings.TrimSpace(line[idx+1:]))
	}

	return fm, body, nil
}

func writeNote(path string, fm *frontmatter, body string) error {
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range fm.keys {
		if raw, ok := fm.raw[k]; ok {
			b.WriteString(raw)
			b.WriteString("\n")
			continue
		}
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(fm.values[k])
		b.WriteString("\n")
	}
	b.WriteString("---\n")
	b.WriteString(body)

	return os.WriteFile(path, []byte(b.String()), 0o600)
}
