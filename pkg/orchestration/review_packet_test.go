package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"
)

// Every test in this file is hermetic by construction: GROVE_HOME is sandboxed
// so registry reads/writes never see the owner's real state, and the notebook
// is a fake backed by a temp dir so nothing can ever create a note in the
// owner's real notebook (D7).

// sandboxGroveHome points every registry read/write at a temp dir.
func sandboxGroveHome(t *testing.T) {
	t.Helper()
	t.Setenv("GROVE_HOME", t.TempDir())
}

// fakeNotebook implements ReviewPacketNotebook against a temp directory.
type fakeNotebook struct {
	dir string
	// notes is what ListPlanNotes returns; CreateNote appends to it, exactly
	// as nb would start reporting a note once it exists.
	notes []PlanNote
	// listErr / createErr inject failures.
	listErr   error
	createErr error
	// omitCreatedPath simulates nb creating the note but not reporting where.
	omitCreatedPath bool
	// creates counts CreateNote calls, so a test can prove a refresh did not
	// silently create a second packet.
	creates int
	// writes counts WriteNote calls, so a test can prove an idempotent refresh
	// did not touch the file at all.
	writes int
}

func newFakeNotebook(t *testing.T) *fakeNotebook {
	t.Helper()
	return &fakeNotebook{dir: t.TempDir()}
}

func (f *fakeNotebook) ListPlanNotes(planDir, planName string) ([]PlanNote, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]PlanNote(nil), f.notes...), nil
}

func (f *fakeNotebook) CreateNote(planDir, planName, title, body string) (string, []string, error) {
	f.creates++
	if f.createErr != nil {
		return "", nil, f.createErr
	}
	slug := strings.ReplaceAll(strings.ToLower(title), " ", "-")
	slug = strings.ReplaceAll(slug, ":", "")
	path := filepath.Join(f.dir, "review", slug+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", nil, err
	}
	// nb-shaped frontmatter: the packet writer must add its key to this
	// without disturbing anything else.
	content := "---\n" +
		"id: 20260802-review-packet\n" +
		"title: " + strconv.Quote(title) + "\n" +
		"aliases: []\n" +
		"tags: []\n" +
		"created: 2026-08-02T12:00:00Z\n" +
		"modified: 2026-08-02T12:00:00Z\n" +
		"plan_ref: " + planRefFor(planName) + "\n" +
		"---\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", nil, err
	}
	// nb reports the FILENAME in `title` and the note's own title in
	// `frontmatter_title`; the fake mirrors that so the lookup is exercised
	// the way production sees it.
	f.notes = append(f.notes, PlanNote{
		Path: path, Title: filepath.Base(path), FrontmatterTitle: title,
		PlanRef: planRefFor(planName), Type: "review",
	})
	if f.omitCreatedPath {
		return "", nil, nil
	}
	return path, nil, nil
}

func (f *fakeNotebook) ReadNote(path string) ([]byte, error) { return os.ReadFile(path) }

func (f *fakeNotebook) WriteNote(path string, data []byte) error {
	f.writes++
	return os.WriteFile(path, data, 0o644)
}

// stubReviewBlobHashes makes state derivation deterministic without a git repo.
func stubReviewBlobHashes(t *testing.T, hashes map[string]string) {
	t.Helper()
	prev := reviewBlobHashes
	reviewBlobHashes = func(repoPath string, paths []string) (map[string]string, error) {
		out := map[string]string{}
		for _, p := range paths {
			if h, ok := hashes[p]; ok {
				out[p] = h
			}
		}
		return out, nil
	}
	t.Cleanup(func() { reviewBlobHashes = prev })
}

// packetFixture is a plan + registered worktree + review state on disk.
type packetFixture struct {
	planDir   string
	container string
	plan      *Plan
	nb        *fakeNotebook
}

// newPacketFixture builds a plan with one job (and its commits.json), a
// worktree container holding one repo directory with two files, and a registry
// entry carrying review marks and a checklist.
func newPacketFixture(t *testing.T, sessionState map[string]any) *packetFixture {
	t.Helper()
	sandboxGroveHome(t)

	container := filepath.Join(t.TempDir(), "owner-id", "feature")
	repo := filepath.Join(container, "core")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("package core\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath:      container,
		Repos:        []string{"core"},
		Plan:         "my-plan",
		SessionState: sessionState,
	}); err != nil {
		t.Fatal(err)
	}

	planDir := filepath.Join(t.TempDir(), "my-plan")
	if err := os.MkdirAll(filepath.Join(planDir, ".artifacts", "job-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	commits := `{"schema":1,"job_id":"job-1","job_file":"01-job.md","worktree":"` + container + `",
"started_at":"2026-08-02T12:00:00Z","finished_at":"2026-08-02T12:30:00Z",
"repos":[{"name":"core","path":"` + repo + `","branch":"feature","start_head":"aaaaaaaaaaaaaaaa","end_head":"bbbbbbbbbbbbbbbb","commits":["c1","c2"],"dirty_at_end":false}]}`
	if err := os.WriteFile(filepath.Join(planDir, ".artifacts", "job-1", "commits.json"), []byte(commits), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{
		Name:      "my-plan",
		Directory: planDir,
		Config:    &PlanConfig{Worktree: "feature", Repos: []string{"core"}, Status: "review"},
		Jobs: []*Job{{
			ID: "job-1", Title: "first job", Status: JobStatus("completed"),
			FilePath: filepath.Join(planDir, "01-job.md"),
		}},
	}

	return &packetFixture{planDir: planDir, container: container, plan: plan, nb: newFakeNotebook(t)}
}

func (f *packetFixture) write(t *testing.T, trigger string, now time.Time) ReviewPacketResult {
	t.Helper()
	result, err := WriteReviewPacket(ReviewPacketRequest{
		PlanDir:       f.planDir,
		Plan:          f.plan,
		ContainerPath: f.container,
		Trigger:       trigger,
		Now:           now,
		Notebook:      f.nb,
	})
	if err != nil {
		t.Fatalf("WriteReviewPacket: %v", err)
	}
	return result
}

func reviewedState(hash string) map[string]any {
	return map[string]any{"lastReviewedBlobHash": hash}
}

func checklistState(id, title string, items ...map[string]any) map[string]any {
	return map[string]any{
		"id": id, "title": title,
		"createdAt": "2026-08-02T12:00:00Z", "updatedAt": "2026-08-02T12:05:00Z",
		"agent": "review-agent", "items": items,
	}
}

func checklistItem(id, text string, checked bool, note string) map[string]any {
	return map[string]any{"id": id, "text": text, "checked": checked, "note": note}
}

func readPacketFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func TestWriteReviewPacket_CreatesPacketWithScopeDispositionAndSnapshot(t *testing.T) {
	f := newPacketFixture(t, map[string]any{
		"review:core/a.go": reviewedState("hash-a"),
		"review:core/b.go": reviewedState("hash-b-old"),
		"checklist:cl-1": checklistState("cl-1", "Human verification",
			checklistItem("i1", "run the TUI", true, "looks right"),
			checklistItem("i2", "check the docs", false, ""),
		),
		"active_job": "job-1", // unrelated session key: must not appear
	})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a", "b.go": "hash-b-new"})
	f.nb.notes = append(f.nb.notes, PlanNote{
		Path: "/notes/in_progress/ticket.md", Title: "20260802-ticket.md", FrontmatterTitle: "The ticket",
		PlanRef: "plans/my-plan", PlanJob: "01-job.md", Type: "inbox",
	})

	result := f.write(t, ReviewTriggerReview, time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC))

	if !result.Created || !result.Changed {
		t.Fatalf("first write: created=%v changed=%v, want both true", result.Created, result.Changed)
	}
	content := readPacketFile(t, result.Path)

	// Body: marker, scope from commits.json, the ticket link, the sync marker.
	for _, want := range []string{
		"<!-- grove-review-packet schema_version=1 plan=my-plan worktree=feature -->",
		"# Review packet: my-plan",
		"- **Plan ref:** plans/my-plan",
		"## Scope — per-job commit ranges",
		"`aaaaaaaaaaaa..bbbbbbbbbbbb`",
		"[The ticket](/notes/in_progress/ticket.md)",
		"## Disposition — land preview",
		"## Review state",
		reviewPacketSyncMarker,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("packet body missing %q\n---\n%s", want, content)
		}
	}

	// Snapshot: versioned frontmatter key with both file marks and the
	// checklist, and the derived three-state model.
	for _, want := range []string{
		"review_snapshot:",
		"schema_version: 1",
		"checkpointed_at: \"2026-08-02T13:00:00Z\"",
		"trigger: review",
		"key: review:core/a.go",
		"state: " + ReviewStateReviewedCurrent,
		"key: review:core/b.go",
		"state: " + ReviewStateChangedSinceReview,
		"key: checklist:cl-1",
		"text: run the TUI",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("packet frontmatter missing %q\n---\n%s", want, content)
		}
	}
	if strings.Contains(content, "active_job") {
		t.Error("snapshot leaked an unrelated SessionState key")
	}

	// nb's own frontmatter must survive the surgery untouched.
	for _, want := range []string{"id: 20260802-review-packet", "plan_ref: plans/my-plan", "created: 2026-08-02T12:00:00Z"} {
		if !strings.Contains(content, want) {
			t.Errorf("packet frontmatter lost nb key %q", want)
		}
	}
}

func TestWriteReviewPacket_RefreshIsIdempotent(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})

	first := f.write(t, ReviewTriggerReview, time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC))
	before := readPacketFile(t, first.Path)
	writesAfterCreate := f.nb.writes

	// A later refresh with a LATER clock and nothing else changed must not
	// rewrite the note: checkpointed_at alone may not dirty the packet.
	second := f.write(t, ReviewTriggerReview, time.Date(2026, 8, 2, 19, 30, 0, 0, time.UTC))
	if second.Created {
		t.Error("refresh created a second packet")
	}
	if second.Changed {
		t.Error("refresh reported a change when nothing changed")
	}
	if f.nb.creates != 1 {
		t.Errorf("CreateNote called %d times, want 1", f.nb.creates)
	}
	if f.nb.writes != writesAfterCreate {
		t.Errorf("idempotent refresh wrote the note %d extra time(s)", f.nb.writes-writesAfterCreate)
	}
	if after := readPacketFile(t, first.Path); after != before {
		t.Errorf("idempotent refresh changed the file\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

func TestWriteReviewPacket_RefreshPreservesProseBelowMarkerAndRecheckpoints(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a", "b.go": "hash-b"})

	first := f.write(t, ReviewTriggerReview, time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC))

	// A human writes below the marker.
	const prose = "\n\n## Reviewer notes\n\nThe error path in b.go needs a second look.\n"
	if err := os.WriteFile(first.Path, []byte(readPacketFile(t, first.Path)+strings.TrimPrefix(prose, "\n\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	withProse := readPacketFile(t, first.Path)

	// More review happens, then the packet is refreshed.
	if err := worktreeregistry.Update(pathutil.WorktreeID(f.container), func(e *worktreeregistry.Entry) {
		e.SessionState["review:core/b.go"] = reviewedState("hash-b")
	}); err != nil {
		t.Fatal(err)
	}

	second := f.write(t, ReviewTriggerFinish, time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC))
	if !second.Changed {
		t.Fatal("refresh after new review marks reported no change")
	}
	after := readPacketFile(t, second.Path)

	if !strings.Contains(after, "The error path in b.go needs a second look.") {
		t.Errorf("refresh dropped prose written below the sync marker\n---\n%s", after)
	}
	if strings.Count(after, reviewPacketSyncMarker) != 1 {
		t.Errorf("refresh duplicated the sync marker (%d occurrences)", strings.Count(after, reviewPacketSyncMarker))
	}
	if !strings.Contains(after, "key: review:core/b.go") {
		t.Error("refresh did not checkpoint the newly reviewed file")
	}
	if !strings.Contains(after, "checkpointed_at: \"2026-08-02T14:00:00Z\"") {
		t.Error("refresh did not advance checkpointed_at")
	}
	if !strings.Contains(after, "trigger: finish") {
		t.Error("refresh did not record the finish trigger")
	}
	if after == withProse {
		t.Error("refresh did not rewrite the machine half at all")
	}
}

func TestWriteReviewPacket_EmptyLiveStateNeverErasesAStoredSnapshot(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})
	first := f.write(t, ReviewTriggerReview, time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC))

	// The tombstone has since stripped SessionState (or a second finish ran).
	if err := worktreeregistry.Update(pathutil.WorktreeID(f.container), func(e *worktreeregistry.Entry) {
		e.SessionState = nil
	}); err != nil {
		t.Fatal(err)
	}

	second := f.write(t, ReviewTriggerFinish, time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC))
	if !second.SnapshotPreserved {
		t.Error("an empty live state did not report the stored snapshot as preserved")
	}
	after := readPacketFile(t, second.Path)
	if !strings.Contains(after, "key: review:core/a.go") {
		t.Errorf("re-checkpointing with no live state erased the stored snapshot\n---\n%s", after)
	}
	if !strings.Contains(after, "checkpointed_at: \"2026-08-02T13:00:00Z\"") {
		t.Error("a preserved snapshot must keep the checkpoint time it was actually taken at")
	}
	if first.Path != second.Path {
		t.Errorf("second write landed at %s, want %s", second.Path, first.Path)
	}
}

func TestWriteReviewPacket_RefusesWhenSyncMarkerWasRemoved(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})
	first := f.write(t, ReviewTriggerReview, time.Now())

	stripped := strings.ReplaceAll(readPacketFile(t, first.Path), reviewPacketSyncMarker, "")
	if err := os.WriteFile(first.Path, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReviewPacket(ReviewPacketRequest{
		PlanDir: f.planDir, Plan: f.plan, ContainerPath: f.container,
		Trigger: ReviewTriggerReview, Notebook: f.nb,
	})
	if err == nil {
		t.Fatal("expected an error when the sync marker is missing")
	}
	if !strings.Contains(err.Error(), "sync marker") {
		t.Errorf("error = %v, want it to name the missing sync marker", err)
	}
	if got := readPacketFile(t, first.Path); got != stripped {
		t.Error("the refusal still modified the note")
	}
}

func TestWriteReviewPacket_RefusesToClobberAnUnparseableSnapshot(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})
	first := f.write(t, ReviewTriggerReview, time.Now())

	broken := strings.Replace(readPacketFile(t, first.Path), "review_snapshot:", "review_snapshot: [unterminated", 1)
	if err := os.WriteFile(first.Path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteReviewPacket(ReviewPacketRequest{
		PlanDir: f.planDir, Plan: f.plan, ContainerPath: f.container,
		Trigger: ReviewTriggerReview, Notebook: f.nb,
	})
	if err == nil {
		t.Fatal("expected an error for an unparseable stored snapshot")
	}
	if got := readPacketFile(t, first.Path); got != broken {
		t.Error("the refusal still modified the note")
	}
}

func TestWriteReviewPacket_FailedNotebookQueryNeverCreatesASecondPacket(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})
	f.nb.listErr = fmt.Errorf("nb list: notebook unavailable")

	if _, err := WriteReviewPacket(ReviewPacketRequest{
		PlanDir: f.planDir, Plan: f.plan, ContainerPath: f.container, Notebook: f.nb,
	}); err == nil {
		t.Fatal("expected the write to fail when the plan's notes cannot be listed")
	}
	if f.nb.creates != 0 {
		t.Errorf("CreateNote called %d times after a failed lookup; a blind create would duplicate the packet", f.nb.creates)
	}
}

func TestWriteReviewPacket_UnreportedCreatedPathIsAFailure(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})
	f.nb.omitCreatedPath = true

	_, err := WriteReviewPacket(ReviewPacketRequest{
		PlanDir: f.planDir, Plan: f.plan, ContainerPath: f.container, Notebook: f.nb,
	})
	if err == nil {
		t.Fatal("expected an error when nb does not report the created note's path")
	}
	if !strings.Contains(err.Error(), "not checkpointed") {
		t.Errorf("error = %v, want it to say the snapshot was not checkpointed", err)
	}
}

func TestWriteReviewPacket_FindsTheExistingPacketByMarkerNotTitle(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})
	first := f.write(t, ReviewTriggerReview, time.Now())

	// Somebody retitled the note. The marker is what identifies a packet.
	f.nb.notes[0].FrontmatterTitle = "Some other title"
	renamed := strings.Replace(readPacketFile(t, first.Path), strconv.Quote("Review packet: my-plan"), strconv.Quote("Some other title"), 1)
	if err := os.WriteFile(first.Path, []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}

	second := f.write(t, ReviewTriggerReview, time.Now())
	if second.Created || f.nb.creates != 1 {
		t.Errorf("a retitled packet was not recognized: created=%v creates=%d", second.Created, f.nb.creates)
	}
	if second.Path != first.Path {
		t.Errorf("refresh landed at %s, want %s", second.Path, first.Path)
	}
}

func TestCollectReviewSnapshot_DerivesStatesAndCarriesLegacyMarkers(t *testing.T) {
	f := newPacketFixture(t, map[string]any{
		"review:core/a.go":               reviewedState("hash-a"),
		"review:core/b.go":               reviewedState("stale"),
		"review:core/gone.go":            reviewedState("hash-gone"),
		"review:core/legacy.go@hash-leg": true,
	})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a", "b.go": "hash-b"})

	snap := CollectReviewSnapshot(f.container, []string{"core"}, ReviewTriggerReview, time.Now())

	got := map[string]ReviewSnapshotFile{}
	for _, file := range snap.Files {
		got[file.Key] = file
	}
	if len(got) != 4 {
		t.Fatalf("snapshot has %d files, want 4: %+v", len(got), snap.Files)
	}
	if s := got["review:core/a.go"].State; s != ReviewStateReviewedCurrent {
		t.Errorf("a.go state = %q, want %q", s, ReviewStateReviewedCurrent)
	}
	if s := got["review:core/b.go"].State; s != ReviewStateChangedSinceReview {
		t.Errorf("b.go state = %q, want %q", s, ReviewStateChangedSinceReview)
	}
	// gone.go has no current hash AND no file on disk.
	if s := got["review:core/gone.go"].State; s != ReviewStateGone {
		t.Errorf("gone.go state = %q, want %q", s, ReviewStateGone)
	}
	legacy := got["review:core/legacy.go@hash-leg"]
	if legacy.Source != reviewSourceLegacyMarker {
		t.Errorf("legacy marker source = %q, want %q", legacy.Source, reviewSourceLegacyMarker)
	}
	if legacy.LastReviewedBlobHash != "hash-leg" || legacy.Path != "legacy.go" {
		t.Errorf("legacy marker decoded as path=%q hash=%q", legacy.Path, legacy.LastReviewedBlobHash)
	}
}

func TestCollectReviewSnapshot_UnknownWhenTheCheckoutIsGone(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	if err := os.RemoveAll(filepath.Join(f.container, "core")); err != nil {
		t.Fatal(err)
	}

	snap := CollectReviewSnapshot(f.container, []string{"core"}, ReviewTriggerFinish, time.Now())
	if len(snap.Files) != 1 {
		t.Fatalf("snapshot files = %d, want 1", len(snap.Files))
	}
	// An unreadable checkout must never be reported as a verdict either way.
	if snap.Files[0].State != ReviewStateUnknown {
		t.Errorf("state = %q, want %q", snap.Files[0].State, ReviewStateUnknown)
	}
	if snap.Files[0].LastReviewedBlobHash != "hash-a" {
		t.Error("the recorded review hash was lost when the checkout was unavailable")
	}
}

func TestCollectReviewSnapshot_MissingRegistryEntryIsEmptyWithAReason(t *testing.T) {
	sandboxGroveHome(t)
	container := filepath.Join(t.TempDir(), "never-registered")

	snap := CollectReviewSnapshot(container, nil, ReviewTriggerReview, time.Now())
	if !snap.IsEmpty() {
		t.Errorf("snapshot not empty: %+v", snap)
	}
	if snap.Unreadable == "" {
		t.Error("an empty snapshot from an unreadable registry must say why")
	}
}

func TestLiveReviewStateCounts(t *testing.T) {
	f := newPacketFixture(t, map[string]any{
		"review:core/a.go": reviewedState("hash-a"),
		"review:core/b.go": reviewedState("hash-b"),
		"checklist:cl-1":   checklistState("cl-1", "check"),
		"active_job":       "job-1",
	})

	files, checklists, err := LiveReviewStateCounts(f.container)
	if err != nil {
		t.Fatalf("LiveReviewStateCounts: %v", err)
	}
	if files != 2 || checklists != 1 {
		t.Errorf("counts = (%d, %d), want (2, 1)", files, checklists)
	}

	sandboxGroveHome(t)
	if files, checklists, err = LiveReviewStateCounts(filepath.Join(t.TempDir(), "nope")); err != nil || files != 0 || checklists != 0 {
		t.Errorf("unregistered worktree = (%d, %d, %v), want (0, 0, nil)", files, checklists, err)
	}
}

func TestSetFrontmatterKey_ReplacesOnlyItsOwnBlock(t *testing.T) {
	fm := "id: x\ntitle: T\ntags: []\nreview_snapshot:\n    schema_version: 1\n    files:\n        - key: old\nplan_ref: plans/p\n"
	got := setFrontmatterKey(fm, ReviewSnapshotKey, "review_snapshot:\n    schema_version: 1\n    files:\n        - key: new\n")

	for _, want := range []string{"id: x\n", "title: T\n", "tags: []\n", "plan_ref: plans/p\n", "- key: new\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("result lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- key: old") {
		t.Errorf("old block survived:\n%s", got)
	}
	if strings.Count(got, "review_snapshot:") != 1 {
		t.Errorf("expected exactly one review_snapshot key:\n%s", got)
	}
}

func TestSplitNoteFrontmatterRoundTrip(t *testing.T) {
	raw := "---\nid: x\ntitle: T\n---\n\n# Body\n\ntext\n"
	fm, body, ok := splitNoteFrontmatter(raw)
	if !ok {
		t.Fatal("frontmatter not detected")
	}
	if got := assembleNote(fm, body); got != raw {
		t.Errorf("round trip changed the note:\n%q\n%q", got, raw)
	}
	if _, _, ok := splitNoteFrontmatter("# no frontmatter\n"); ok {
		t.Error("a note without frontmatter was reported as having it")
	}
}

// TestWriteReviewPacket_RendersRealGitDisposition exercises the planops preview
// against an actual checkout so the disposition table is proven to render from
// real git state, not just from the "unavailable" branch.
func TestWriteReviewPacket_RendersRealGitDisposition(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	stubReviewBlobHashes(t, map[string]string{"a.go": "hash-a"})
	repo := filepath.Join(f.container, "core")
	initTestGitRepo(t, repo)

	result := f.write(t, ReviewTriggerReview, time.Now())
	content := readPacketFile(t, result.Path)

	if !strings.Contains(content, "| repo | branch | onto | ahead | behind | dirty | disposition | reason |") {
		t.Errorf("disposition table missing:\n%s", content)
	}
	if !strings.Contains(content, "| core |") {
		t.Errorf("disposition row for the repo missing:\n%s", content)
	}
}

// stubDefaultNotebook installs a fake as the notebook used by IMPLICIT packet
// writes — the one inside MarkPlanReview, which has no argument to thread a
// seam through. Without it those tests would shell out to the real `nb` and
// could create notes in the owner's notebook (D7).
func stubDefaultNotebook(t *testing.T) *fakeNotebook {
	t.Helper()
	nb := newFakeNotebook(t)
	prev := newReviewPacketNotebook
	newReviewPacketNotebook = func() ReviewPacketNotebook { return nb }
	t.Cleanup(func() { newReviewPacketNotebook = prev })
	return nb
}

func TestWriteReviewPacket_NoNotebookOnTheMachineIsASkipNotAFailure(t *testing.T) {
	f := newPacketFixture(t, map[string]any{"review:core/a.go": reviewedState("hash-a")})
	f.nb.listErr = ErrNotebookUnavailable

	result, err := WriteReviewPacket(ReviewPacketRequest{
		PlanDir: f.planDir, Plan: f.plan, ContainerPath: f.container, Notebook: f.nb,
	})
	if err != nil {
		t.Fatalf("a missing nb must not fail the write: %v", err)
	}
	if result.Path != "" || f.nb.creates != 0 {
		t.Errorf("nothing should have been written: path=%q creates=%d", result.Path, f.nb.creates)
	}
	if len(result.Warnings) == 0 {
		t.Error("a skipped packet must say so")
	}
}
