package plan_finish

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/core/pkg/worktreeregistry"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planops"
)

// writeCommitsFixture writes a job's commits.json sidecar exactly where
// orchestration.ReadJobCommits looks for it.
func writeCommitsFixture(t *testing.T, planDir, jobID string, rec orchestration.JobCommitsRecord) {
	t.Helper()
	dir := filepath.Join(planDir, ".artifacts", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commits.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureLedger(t *testing.T) (Ledger, string) {
	t.Helper()
	planDir := filepath.Join(t.TempDir(), "hosted-git-and-prs")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeCommitsFixture(t, planDir, "land-receipts-fff6a7f8", orchestration.JobCommitsRecord{
		Schema:     1,
		JobID:      "land-receipts-fff6a7f8",
		JobFile:    "04-land-receipts.md",
		StartedAt:  "2026-08-02T12:00:00Z",
		FinishedAt: "2026-08-02T12:30:00Z",
		Repos: []orchestration.JobCommitsRepo{
			// Deliberately out of alphabetical order: the renderer sorts.
			{Name: "flow", Path: "/w/flow", Branch: "hosted", StartHead: "1111111111111111", EndHead: "2222222222222222", Commits: []string{"a", "b"}},
			{Name: "core", Path: "/w/core", Branch: "hosted", StartHead: "3333333333333333", EndHead: "4444444444444444", Commits: []string{}, DirtyAtEnd: true},
			{Name: "nb", Path: "/w/nb", Branch: "hosted", StartHead: "", EndHead: "5555555555555555", Commits: nil, Note: "start capture missing"},
		},
	})

	plan := &orchestration.Plan{
		Name:      "hosted-git-and-prs",
		Directory: planDir,
		Config:    &orchestration.PlanConfig{Worktree: "hosted-git-and-prs", Repos: []string{"core", "flow", "nb"}},
		Jobs: []*orchestration.Job{
			{
				ID:       "land-receipts-fff6a7f8",
				Title:    "land-receipts",
				Status:   orchestration.JobStatus("completed"),
				FilePath: filepath.Join(planDir, "04-land-receipts.md"),
			},
			{
				ID:       "never-ran-00000000",
				Title:    "never-ran",
				Status:   orchestration.JobStatus("pending"),
				FilePath: filepath.Join(planDir, "99-never-ran.md"),
			},
		},
	}

	receipt := planops.LandingReceipt{
		SchemaVersion: planops.ReceiptSchemaVersion,
		Plan:          "hosted-git-and-prs",
		PlanDir:       planDir,
		Operation:     planops.OperationLand,
		Fingerprint:   "fingerprint0123456789",
		Timestamp:     time.Date(2026, 8, 2, 12, 30, 0, 0, time.UTC),
		Repos: []planops.RepoLanding{
			{
				Repo: "core", Branch: "hosted", Onto: "main",
				ReviewedHeadSHA: "aaaaaaaaaaaaaaaa",
				LandedRange:     planops.LandedRange{Start: "bbbbbbbbbbbbbbbb", End: "cccccccccccccccc"},
			},
		},
	}
	if _, err := planops.WriteReceipt(planDir, receipt); err != nil {
		t.Fatal(err)
	}

	// containerPath is deliberately a path that does not exist: CollectLedger
	// must degrade to an explicit "could not read final state" rather than
	// failing, and no test may depend on a real worktree.
	l := CollectLedger(plan, planDir, filepath.Join(planDir, "no-such-container"),
		time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC))
	return l, planDir
}

func TestCollectLedgerReadsCommitsAndReceipts(t *testing.T) {
	l, _ := fixtureLedger(t)

	if l.Plan != "hosted-git-and-prs" {
		t.Errorf("plan = %q", l.Plan)
	}
	if got := len(l.Jobs); got != 2 {
		t.Fatalf("jobs = %d, want 2", got)
	}
	if l.Jobs[0].Record == nil {
		t.Fatal("job 0 should have a commits record")
	}
	if got := len(l.Jobs[0].Record.Repos); got != 3 {
		t.Errorf("job 0 repos = %d, want 3", got)
	}
	if l.Jobs[1].Record != nil {
		t.Error("a job that never ran must have no commits record, not an invented one")
	}
	if got := len(l.Receipts); got != 1 {
		t.Fatalf("receipts = %d, want 1", got)
	}
	if l.Preview != nil {
		t.Error("preview over a nonexistent container should be nil, not a fabricated snapshot")
	}
	if l.PreviewErr == "" {
		t.Error("an uncomputable preview must record WHY")
	}
}

func TestRenderLedgerMatchesCommitsAndReceiptFixtures(t *testing.T) {
	l, _ := fixtureLedger(t)
	body := RenderLedger(l)

	for _, want := range []string{
		"<!-- grove-ledger schema_version=1 plan=hosted-git-and-prs worktree=hosted-git-and-prs -->",
		"# Plan ledger: hosted-git-and-prs",
		"- **Repos:** core, flow, nb",
		"- **Finished:** 2026-08-02T13:00:00Z",
		"## Landing receipts",
		"`bbbbbbbbbbbb..cccccccccccc`", // landed range, shortened
		"aaaaaaaaaaaa",                 // reviewed head
		"## Per-job commit ranges",
		"### 04-land-receipts.md — land-receipts (completed)",
		"| core | hosted | `333333333333..444444444444` | 0 | yes | — |",
		"| flow | hosted | `111111111111..222222222222` | 2 | no | — |",
		"| nb | hosted | `—..555555555555` | n/a | no | start capture missing |",
		"_No commit capture recorded for this job._",
		"## State at finish",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ledger body missing %q\n---\n%s", want, body)
		}
	}

	// Repos must render sorted regardless of the order in commits.json, so
	// two finishes of the same plan produce comparable ledgers.
	core := strings.Index(body, "| core | hosted |")
	flow := strings.Index(body, "| flow | hosted |")
	nb := strings.Index(body, "| nb | hosted |")
	if !(core < flow && flow < nb) {
		t.Errorf("per-job repo rows are not sorted: core=%d flow=%d nb=%d", core, flow, nb)
	}
}

func TestRenderLedgerIsDeterministic(t *testing.T) {
	l, _ := fixtureLedger(t)
	if RenderLedger(l) != RenderLedger(l) {
		t.Error("RenderLedger must be a pure function of its input")
	}
}

// A null Commits list means "could not be computed" — rendering it as 0 would
// assert the job produced nothing, which is a different (and wrong) claim.
func TestRenderLedgerDistinguishesNullFromEmptyCommits(t *testing.T) {
	l, _ := fixtureLedger(t)
	body := RenderLedger(l)
	if !strings.Contains(body, "| nb | hosted | `—..555555555555` | n/a |") {
		t.Errorf("null commits must render as n/a, not 0\n%s", body)
	}
	if !strings.Contains(body, "| core | hosted | `333333333333..444444444444` | 0 |") {
		t.Errorf("an empty (captured, none produced) commit list must render as 0\n%s", body)
	}
}

func TestFinalStatesPrefersReceiptOverBranchHead(t *testing.T) {
	l, _ := fixtureLedger(t)
	states := l.FinalStates()

	// Only core has a receipt; the container does not exist, so no branch
	// heads are readable and nothing else may be invented.
	if len(states) != 1 {
		t.Fatalf("final states = %+v, want exactly the receipted repo", states)
	}
	got := states[0]
	if got.Repo != "core" || got.SHA != "cccccccccccccccc" {
		t.Errorf("final state = %+v, want core at the receipt's landed end", got)
	}
	if got.Source != worktreeregistry.SHASourceReceipt {
		t.Errorf("source = %q, want %q", got.Source, worktreeregistry.SHASourceReceipt)
	}
	if got.Branch != "hosted" {
		t.Errorf("branch = %q", got.Branch)
	}
}

func TestFinalStatesFallsBackToBranchHeads(t *testing.T) {
	container := t.TempDir()
	repo := filepath.Join(container, "core")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepoWithIdentity(t, repo)
	commitFile(t, repo, "a.txt", "a", "first")

	l := Ledger{ContainerPath: container, Repos: []string{"core"}}
	states := l.FinalStates()
	if len(states) != 1 {
		t.Fatalf("final states = %+v, want one", states)
	}
	if states[0].Source != worktreeregistry.SHASourceBranchHead {
		t.Errorf("source = %q, want %q", states[0].Source, worktreeregistry.SHASourceBranchHead)
	}
	head := gitLine(repo, "rev-parse", "HEAD")
	if states[0].SHA != head {
		t.Errorf("sha = %q, want live HEAD %q", states[0].SHA, head)
	}
}

func TestParseNbCreatedPath(t *testing.T) {
	cases := map[string]string{
		"Created: /notes/completed/2026-08-02-plan-ledger.md\n": "/notes/completed/2026-08-02-plan-ledger.md",
		"\x1b[32m* \x1b[0mCreated: /notes/completed/x.md\n\n":   "/notes/completed/x.md",
		"nothing useful here": "",
	}
	for in, want := range cases {
		if got := parseNbCreatedPath(in); got != want {
			t.Errorf("parseNbCreatedPath(%q) = %q, want %q", in, got, want)
		}
	}
}
