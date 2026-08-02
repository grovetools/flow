package planops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	coreplan "github.com/grovetools/core/pkg/plan"
)

// planTargetFor builds a plan-scoped target whose PlanDir actually exists, so
// receipts have somewhere to land. The plan dir is a temp dir — no test may
// write into the owner's real notebook.
func planTargetFor(t *testing.T, repos ...coreplan.RepoTarget) (coreplan.PlanActionTarget, string) {
	t.Helper()
	planDir := filepath.Join(t.TempDir(), "plans", "scratch-plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := targetFor(repos...)
	target.PlanDir = planDir
	target.RegistryID = "scratch/registry"
	return target, planDir
}

// TestLandWritesReceiptMatchingRevList is the core acceptance: a land of a
// scratch plan records a receipt whose landed_range is exactly the range git
// itself reports for what main gained, and whose reviewed_head_sha is the
// pre-rebase branch head — the SHA a rebase destroys and ancestry can therefore
// never recover.
func TestLandWritesReceiptMatchingRevList(t *testing.T) {
	main, worktree := repoFixture(t, "main")
	// Advance main so the land must rebase first: that rewrites the branch head
	// and makes reviewed_head_sha genuinely different from what lands.
	commitFile(t, main, "main.txt", "main advance")
	commitFile(t, worktree, "feature.txt", "feature work")

	target, planDir := planTargetFor(t, coreplan.RepoTarget{Name: "scratch", Path: worktree})
	mainBefore := gitOut(t, main, "rev-parse", "main")
	reviewedHead := gitOut(t, worktree, "rev-parse", "HEAD")

	preview, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}
	result := Execute(context.Background(), preview)
	if result.Failed() {
		t.Fatalf("land failed: %#v", result)
	}
	if result.Receipt == nil || result.Receipt.Error != "" || result.Receipt.Path == "" {
		t.Fatalf("land did not record a receipt: %#v", result.Receipt)
	}
	if len(result.Receipt.Warnings) != 0 {
		t.Errorf("unexpected receipt warnings: %v", result.Receipt.Warnings)
	}

	receipts, err := ReadReceipts(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 {
		t.Fatalf("expected exactly one receipt, got %d", len(receipts))
	}
	receipt := receipts[0]
	if receipt.SchemaVersion != ReceiptSchemaVersion {
		t.Errorf("schema_version = %d, want %d", receipt.SchemaVersion, ReceiptSchemaVersion)
	}
	if receipt.Plan != "scratch-plan" {
		t.Errorf("plan = %q, want scratch-plan", receipt.Plan)
	}
	if receipt.Operation != OperationLand {
		t.Errorf("operation = %q, want land", receipt.Operation)
	}
	if receipt.Fingerprint != preview.Fingerprint {
		t.Errorf("fingerprint = %q, want the confirmed preview's %q", receipt.Fingerprint, preview.Fingerprint)
	}
	if receipt.WorktreePath != target.ContainerPath || receipt.RegistryID != target.RegistryID {
		t.Errorf("receipt lost its worktree identity: %#v", receipt)
	}
	if receipt.SourcePath != result.Receipt.Path {
		t.Errorf("reader source path %q != written path %q", receipt.SourcePath, result.Receipt.Path)
	}
	if len(receipt.Repos) != 1 {
		t.Fatalf("expected one landed repo, got %#v", receipt.Repos)
	}

	landed := receipt.Repos[0]
	if landed.Repo != "scratch" || landed.Branch != "feature" || landed.Onto != "main" {
		t.Errorf("unexpected repo identity: %#v", landed)
	}
	if landed.ReviewedHeadSHA != reviewedHead {
		t.Errorf("reviewed_head_sha = %q, want the pre-rebase head %q", landed.ReviewedHeadSHA, reviewedHead)
	}
	if landed.LandedRange.Start != mainBefore {
		t.Errorf("landed_range.start = %q, want main before advance %q", landed.LandedRange.Start, mainBefore)
	}
	if want := gitOut(t, main, "rev-parse", "main"); landed.LandedRange.End != want {
		t.Errorf("landed_range.end = %q, want main after advance %q", landed.LandedRange.End, want)
	}
	// The rebase rewrote the branch, so the reviewed head is NOT what landed.
	// This is the whole reason closure must be receipt-driven.
	if landed.ReviewedHeadSHA == landed.LandedRange.End {
		t.Error("fixture did not exercise a rebase: reviewed head equals landed head")
	}

	// landed_range is a real git range: exactly the commits main gained.
	gained := strings.Fields(gitOut(t, main, "rev-list", landed.LandedRange.Start+".."+landed.LandedRange.End))
	if len(gained) != 1 {
		t.Fatalf("expected the range to contain exactly the landed commit, got %v", gained)
	}
	if gained[0] != landed.LandedRange.End {
		t.Errorf("range head %s is not landed_range.end %s", gained[0], landed.LandedRange.End)
	}
}

// TestSecondLandAppendsSecondReceipt pins append-only: a re-land never edits or
// replaces the first receipt, and the reader returns both in timestamp order.
func TestSecondLandAppendsSecondReceipt(t *testing.T) {
	_, worktree := repoFixture(t, "main")
	commitFile(t, worktree, "one.txt", "first")
	target, planDir := planTargetFor(t, coreplan.RepoTarget{Name: "scratch", Path: worktree})

	land := func() string {
		t.Helper()
		preview, err := Preview(context.Background(), target, OperationLand)
		if err != nil {
			t.Fatal(err)
		}
		result := Execute(context.Background(), preview)
		if result.Failed() {
			t.Fatalf("land failed: %#v", result)
		}
		if result.Receipt == nil || result.Receipt.Path == "" || result.Receipt.Error != "" {
			t.Fatalf("no receipt recorded: %#v", result.Receipt)
		}
		return result.Receipt.Path
	}

	firstPath := land()
	firstBody, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}

	// A fresh commit on the branch, then land again.
	runGit(t, worktree, "checkout", "-b", "feature-two")
	commitFile(t, worktree, "two.txt", "second")
	secondPath := land()

	if secondPath == firstPath {
		t.Fatalf("second land reused the first receipt file %s", secondPath)
	}
	afterBody, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("first receipt no longer readable: %v", err)
	}
	if string(afterBody) != string(firstBody) {
		t.Error("first receipt was mutated by the second land")
	}

	receipts, err := ReadReceipts(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 {
		t.Fatalf("expected two receipts, got %d", len(receipts))
	}
	if receipts[0].SourcePath != firstPath || receipts[1].SourcePath != secondPath {
		t.Errorf("receipts are not in timestamp order: %q then %q", receipts[0].SourcePath, receipts[1].SourcePath)
	}
	if receipts[0].Timestamp.After(receipts[1].Timestamp) {
		t.Errorf("timestamps out of order: %s then %s", receipts[0].Timestamp, receipts[1].Timestamp)
	}
	if receipts[0].Repos[0].Branch != "feature" || receipts[1].Repos[0].Branch != "feature-two" {
		t.Errorf("receipts do not describe their own land: %q, %q", receipts[0].Repos[0].Branch, receipts[1].Repos[0].Branch)
	}
}

// TestUpdateOnlyWritesNoReceipt: update-only lands nothing, so it must leave no
// receipt at all — not even an empty one.
func TestUpdateOnlyWritesNoReceipt(t *testing.T) {
	main, worktree := repoFixture(t, "main")
	commitFile(t, main, "main.txt", "main advance")
	commitFile(t, worktree, "feature.txt", "feature")
	target, planDir := planTargetFor(t, coreplan.RepoTarget{Name: "scratch", Path: worktree})

	preview, err := Preview(context.Background(), target, OperationUpdateOnly)
	if err != nil {
		t.Fatal(err)
	}
	result := Execute(context.Background(), preview)
	if result.Failed() {
		t.Fatalf("update failed: %#v", result)
	}
	if result.Receipt != nil {
		t.Errorf("update-only must not report a receipt: %#v", result.Receipt)
	}
	if _, err := os.Stat(ReceiptsDir(planDir)); !os.IsNotExist(err) {
		t.Errorf("update-only created a receipts directory (stat err: %v)", err)
	}
	receipts, err := ReadReceipts(planDir)
	if err != nil {
		t.Fatalf("reading an empty plan must not error: %v", err)
	}
	if len(receipts) != 0 {
		t.Errorf("expected no receipts, got %#v", receipts)
	}
}

// TestLandWithNothingToLandSkipsReceipt: a land whose repos are all skipped
// advanced nothing, so there is nothing to receipt.
func TestLandWithNothingToLandSkipsReceipt(t *testing.T) {
	_, worktree := repoFixture(t, "main")
	// No commits ahead of main — preflight will skip the repo.
	target, planDir := planTargetFor(t, coreplan.RepoTarget{Name: "scratch", Path: worktree})

	preview, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Repos[0].Disposition != DispositionSkipped {
		t.Fatalf("fixture should be skipped, got %#v", preview.Repos[0])
	}
	result := Execute(context.Background(), preview)
	if result.Failed() {
		t.Fatalf("land failed: %#v", result)
	}
	if result.Receipt == nil || result.Receipt.Skipped == "" {
		t.Fatalf("expected a skipped receipt outcome, got %#v", result.Receipt)
	}
	if result.Receipt.Error != "" || result.Receipt.Path != "" {
		t.Errorf("nothing landed, so nothing should have been written: %#v", result.Receipt)
	}
	if receipts, _ := ReadReceipts(planDir); len(receipts) != 0 {
		t.Errorf("expected no receipts on disk, got %#v", receipts)
	}
}

// TestReceiptWriteFailureDoesNotFailTheLand is the guard from the job brief: a
// receipt that cannot be written after a successful advance must leave the land
// standing and complain loudly instead.
func TestReceiptWriteFailureDoesNotFailTheLand(t *testing.T) {
	main, worktree := repoFixture(t, "main")
	commitFile(t, worktree, "feature.txt", "feature")

	target := targetFor(coreplan.RepoTarget{Name: "scratch", Path: worktree})
	// A plan directory that does not exist: planops will not conjure a plan
	// tree, so the receipt write fails.
	target.PlanDir = filepath.Join(t.TempDir(), "no-such-plan")

	preview, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}
	branchHead := gitOut(t, worktree, "rev-parse", "HEAD")
	result := Execute(context.Background(), preview)

	if result.Failed() {
		t.Fatalf("a receipt failure must not fail the land: %#v", result)
	}
	if result.Results[0].Outcome != OutcomeSucceeded {
		t.Errorf("repo outcome should still be succeeded: %#v", result.Results[0])
	}
	if got := gitOut(t, main, "rev-parse", "main"); got != branchHead {
		t.Errorf("the land was rolled back: main is %s, want %s", got, branchHead)
	}
	if result.Receipt == nil || result.Receipt.Error == "" {
		t.Fatalf("the receipt failure was not surfaced: %#v", result.Receipt)
	}
}

// TestUnscopedTargetSkipsReceipt: git-viewer lands an explicit directory scope
// with no plan behind it. That files no receipt, and it is not an error.
func TestUnscopedTargetSkipsReceipt(t *testing.T) {
	_, worktree := repoFixture(t, "main")
	commitFile(t, worktree, "feature.txt", "feature")
	target := targetFor(coreplan.RepoTarget{Name: "scratch", Path: worktree})
	target.PlanDir = ""

	preview, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}
	result := Execute(context.Background(), preview)
	if result.Failed() {
		t.Fatalf("land failed: %#v", result)
	}
	if result.Receipt == nil || result.Receipt.Skipped == "" {
		t.Fatalf("expected a skipped receipt outcome, got %#v", result.Receipt)
	}
	if result.Receipt.Error != "" {
		t.Errorf("an unscoped target is not a receipt failure: %#v", result.Receipt)
	}
}

// TestPartialFailureStillReceiptsWhatLanded: when a later repo fails, the
// earlier repos really did advance main. Their provenance must survive.
func TestPartialFailureStillReceiptsWhatLanded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim requires a POSIX shell")
	}
	alphaMain, alpha := repoFixture(t, "main")
	betaMain, beta := repoFixture(t, "main")
	commitFile(t, alpha, "alpha.txt", "alpha work")
	commitFile(t, beta, "beta.txt", "beta work")

	target, planDir := planTargetFor(t,
		coreplan.RepoTarget{Name: "a-alpha", Path: alpha},
		coreplan.RepoTarget{Name: "b-beta", Path: beta},
	)
	preview, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}

	// Fail only beta's ff-only advance, via a shim that leaves every read
	// preflight performs untouched — so the fresh preview still matches and the
	// failure lands mid-execution rather than as a stale refusal.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	betaMainPath, err := filepath.EvalSymlinks(betaMain)
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	shim := "#!/bin/sh\n" +
		"if [ \"$1\" = merge ] && [ \"$(pwd -P)\" = \"" + betaMainPath + "\" ]; then exit 86; fi\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := Execute(context.Background(), preview)
	if !result.Failed() {
		t.Fatalf("expected beta to fail: %#v", result)
	}
	if result.Results[0].Outcome != OutcomeSucceeded {
		t.Fatalf("alpha should have landed: %#v", result.Results[0])
	}
	if got, want := gitOut(t, alphaMain, "rev-parse", "main"), gitOut(t, alpha, "rev-parse", "HEAD"); got != want {
		t.Fatalf("alpha did not actually land: %s != %s", got, want)
	}

	receipts, err := ReadReceipts(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 {
		t.Fatalf("a partial land must still leave one receipt, got %d", len(receipts))
	}
	if len(receipts[0].Repos) != 1 || receipts[0].Repos[0].Repo != "a-alpha" {
		t.Errorf("receipt must record only what landed: %#v", receipts[0].Repos)
	}
}

// TestReceiptRoundTripsThroughJSON pins the persisted shape: every field a
// downstream reader (job 05's ledger and tombstone) depends on survives a write
// and a read unchanged.
func TestReceiptRoundTripsThroughJSON(t *testing.T) {
	planDir := t.TempDir()
	want := LandingReceipt{
		SchemaVersion: ReceiptSchemaVersion,
		Plan:          "hosted-git-and-prs",
		PlanDir:       planDir,
		WorktreePath:  "/containers/hosted-git-and-prs",
		RegistryID:    "grovetools/hosted-git-and-prs",
		Operation:     OperationLand,
		Fingerprint:   "0123456789abcdef0123456789abcdef",
		Timestamp:     time.Date(2026, 8, 2, 12, 30, 45, 123000000, time.UTC),
		Repos: []RepoLanding{{
			Repo:            "flow",
			Branch:          "feature",
			Onto:            "main",
			Path:            "/containers/hosted-git-and-prs/flow",
			ReviewedHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			LandedRange:     LandedRange{Start: "bbbbbbbb", End: "cccccccc"},
		}},
	}

	path, err := WriteReceipt(planDir, want)
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(path); !strings.HasPrefix(base, "land-") || !strings.HasSuffix(base, ".json") {
		t.Errorf("receipt name %q does not follow land-<timestamp>-<shortsha>.json", base)
	}
	if !strings.Contains(filepath.Base(path), "20260802T123045.123Z") {
		t.Errorf("receipt name %q does not carry its UTC timestamp", filepath.Base(path))
	}
	if !strings.Contains(filepath.Base(path), want.Fingerprint[:12]) {
		t.Errorf("receipt name %q does not carry its short fingerprint", filepath.Base(path))
	}

	got, err := ReadReceipts(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one receipt, got %d", len(got))
	}
	roundTripped := got[0]
	if roundTripped.SourcePath != path {
		t.Errorf("SourcePath = %q, want %q", roundTripped.SourcePath, path)
	}
	roundTripped.SourcePath = ""
	if !roundTripped.Timestamp.Equal(want.Timestamp) {
		t.Errorf("timestamp = %s, want %s", roundTripped.Timestamp, want.Timestamp)
	}
	roundTripped.Timestamp = want.Timestamp
	if !reflect.DeepEqual(roundTripped, want) {
		t.Errorf("receipt did not round-trip:\n got %#v\nwant %#v", got[0], want)
	}

	// The persisted keys are the contract job 05 reads; pin them by name.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "plan", "plan_dir", "worktree_path", "registry_id", "operation", "fingerprint", "timestamp", "repos"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("persisted receipt is missing key %q", key)
		}
	}
	repos, ok := raw["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("persisted repos has the wrong shape: %#v", raw["repos"])
	}
	repo, _ := repos[0].(map[string]any)
	for _, key := range []string{"repo", "branch", "reviewed_head_sha", "landed_range"} {
		if _, ok := repo[key]; !ok {
			t.Errorf("persisted repo landing is missing key %q", key)
		}
	}
	landedRange, _ := repo["landed_range"].(map[string]any)
	if landedRange["start"] != "bbbbbbbb" || landedRange["end"] != "cccccccc" {
		t.Errorf("persisted landed_range has the wrong shape: %#v", repo["landed_range"])
	}
	if _, ok := raw["SourcePath"]; ok {
		t.Error("SourcePath must not be persisted — a receipt does not record its own location")
	}
}

// TestReadReceiptsToleratesForeignFiles: the receipts directory is append-only
// and shared with future writers. One unreadable file must not blind the reader
// to every other receipt, and a receipt from a newer schema must still be
// returned rather than silently dropped.
func TestReadReceiptsToleratesForeignFiles(t *testing.T) {
	planDir := t.TempDir()
	older := LandingReceipt{
		SchemaVersion: ReceiptSchemaVersion,
		Plan:          "p",
		Operation:     OperationLand,
		Fingerprint:   "aaaaaaaaaaaaaaaa",
		Timestamp:     time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		Repos:         []RepoLanding{{Repo: "flow"}},
	}
	newer := older
	newer.SchemaVersion = ReceiptSchemaVersion + 7
	newer.Fingerprint = "bbbbbbbbbbbbbbbb"
	newer.Timestamp = time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	for _, r := range []LandingReceipt{newer, older} {
		if _, err := WriteReceipt(planDir, r); err != nil {
			t.Fatal(err)
		}
	}

	dir := ReceiptsDir(planDir)
	if err := os.WriteFile(filepath.Join(dir, "land-garbage.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a receipt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "land-subdir.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	receipts, err := ReadReceipts(planDir)
	if err != nil {
		t.Fatalf("an unreadable neighbour must not fail the read: %v", err)
	}
	if len(receipts) != 2 {
		t.Fatalf("expected both parseable receipts, got %d", len(receipts))
	}
	if !receipts[0].Timestamp.Before(receipts[1].Timestamp) {
		t.Errorf("receipts are not oldest-first: %s then %s", receipts[0].Timestamp, receipts[1].Timestamp)
	}
	if receipts[1].SchemaVersion != ReceiptSchemaVersion+7 {
		t.Errorf("a future-schema receipt was dropped or rewritten: %#v", receipts[1])
	}
}

// TestWriteReceiptNeverOverwrites pins the append-only guarantee directly: two
// writes that agree on both timestamp and fingerprint still produce two files.
func TestWriteReceiptNeverOverwrites(t *testing.T) {
	planDir := t.TempDir()
	receipt := LandingReceipt{
		SchemaVersion: ReceiptSchemaVersion,
		Plan:          "p",
		Operation:     OperationLand,
		Fingerprint:   "cafebabecafebabe",
		Timestamp:     time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
		Repos:         []RepoLanding{{Repo: "flow"}},
	}
	first, err := WriteReceipt(planDir, receipt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteReceipt(planDir, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("identical receipts collapsed onto one file: %s", first)
	}
	entries, err := os.ReadDir(ReceiptsDir(planDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two receipt files, got %d", len(entries))
	}
}

// TestWriteReceiptRefusesAbsentPlanDir: receipts are a plan artifact. planops
// must not materialize a plan tree just to hold one.
func TestWriteReceiptRefusesAbsentPlanDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-plan")
	if _, err := WriteReceipt(missing, LandingReceipt{Plan: "p"}); err == nil {
		t.Fatal("expected a missing plan directory to be refused")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("a refused write created %s (stat err: %v)", missing, err)
	}
	if _, err := WriteReceipt("", LandingReceipt{Plan: "p"}); err == nil {
		t.Fatal("expected an empty plan directory to be refused")
	}
}
