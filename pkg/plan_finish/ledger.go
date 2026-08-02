package plan_finish

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/worktreeregistry"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planops"
)

// The plan ledger is the second half of "finish promotes before tearing down".
// A plan directory is archived and its worktree retired at finish; everything
// that says WHAT the work was — per-job commit ranges (.artifacts/<job>/
// commits.json), landing receipts, and the state each repo was left in — then
// survives only inside an archived directory nobody greps. The ledger renders
// those three sources into one markdown note in the notebook, which replicates
// to every machine and is the thing an agent or a human actually searches later.
//
// Rendering is a pure function of collected data so the format is testable
// without a plan, a notebook, or a worktree on disk.

// LedgerSchemaVersion is the ledger note's rendered shape. It is emitted as a
// marker comment in the note body so a later reader can tell which renderer
// produced it. Bump on any change that would break a parser.
const LedgerSchemaVersion = 1

// ledgerMarker prefixes the machine-readable marker line. Notebook-wide, this
// is what identifies a note as a plan ledger regardless of its filename.
const ledgerMarker = "grove-ledger"

// LedgerJob is one job's commit capture, paired with the job it came from.
// Record is nil when the job never wrote a commits.json (it never ran, or it
// predates the sidecar) — rendered as an explicit "no capture" row rather than
// omitted, because a silently missing job reads as "this job produced nothing".
type LedgerJob struct {
	JobID   string
	JobFile string
	Title   string
	Status  string
	Record  *orchestration.JobCommitsRecord
	Err     string
}

// Ledger is everything the note is rendered from.
type Ledger struct {
	Plan          string
	Worktree      string
	ContainerPath string
	Repos         []string
	GeneratedAt   time.Time

	// Jobs is in plan job order.
	Jobs []LedgerJob
	// Receipts is oldest-first, as ReadReceipts returns them.
	Receipts []planops.LandingReceipt
	// Preview is the read-only planops land preview taken at finish time: the
	// explicit disposition of whatever was NOT landed. Nil when it could not
	// be computed, with the reason in PreviewErr.
	Preview    *planops.OperationPreview
	PreviewErr string
	// ReceiptsErr records a failure to read the receipts directory. An absent
	// directory is not an error — it means the plan never landed.
	ReceiptsErr string
}

// CollectLedger gathers the ledger inputs. It performs local reads only — plan
// artifacts, receipt files, and read-only git status through planops.Preview.
// Nothing here mutates a repository or touches the network, so it is safe on
// the finish path and offline by construction.
func CollectLedger(plan *orchestration.Plan, planPath, containerPath string, now time.Time) Ledger {
	l := Ledger{
		Plan:          filepath.Base(planPath),
		ContainerPath: containerPath,
		GeneratedAt:   now.UTC(),
	}
	if plan != nil && plan.Config != nil {
		l.Worktree = plan.Config.Worktree
		l.Repos = append([]string(nil), plan.Config.Repos...)
	}

	if plan != nil {
		for _, job := range plan.Jobs {
			if job == nil {
				continue
			}
			lj := LedgerJob{
				JobID:   job.ID,
				JobFile: filepath.Base(job.FilePath),
				Title:   job.Title,
				Status:  string(job.Status),
			}
			rec, err := orchestration.ReadJobCommits(plan, job)
			if err == nil {
				lj.Record = rec
			} else {
				lj.Err = err.Error()
			}
			l.Jobs = append(l.Jobs, lj)
		}
	}

	receipts, err := planops.ReadReceipts(planPath)
	if err != nil {
		l.ReceiptsErr = err.Error()
	}
	l.Receipts = receipts

	preview, err := previewAtFinish(containerPath, l.Repos)
	switch {
	case err != nil:
		l.PreviewErr = err.Error()
	default:
		l.Preview = &preview
	}
	return l
}

// previewAtFinish runs planops' land preview over the worktree's own checkouts.
// Preview is a preflight: it reads git status and divergence and mutates
// nothing, which is exactly the "what is left here" snapshot the ledger wants.
func previewAtFinish(containerPath string, repos []string) (planops.OperationPreview, error) {
	if containerPath == "" {
		return planops.OperationPreview{}, fmt.Errorf("worktree container path unknown")
	}
	target := coreplan.PlanActionTarget{ContainerPath: containerPath}
	for _, repo := range repos {
		path := filepath.Join(containerPath, repo)
		if !isDir(path) {
			continue
		}
		target.Repos = append(target.Repos, coreplan.RepoTarget{Name: repo, Path: path})
	}
	if len(target.Repos) == 0 {
		// A single-repo (non-ecosystem) worktree IS the checkout.
		if isDir(containerPath) {
			target.Repos = append(target.Repos, coreplan.RepoTarget{
				Name: filepath.Base(containerPath), Path: containerPath,
			})
		}
	}
	if len(target.Repos) == 0 {
		return planops.OperationPreview{}, fmt.Errorf("no repository checkouts found under %s", containerPath)
	}
	return planops.Preview(context.Background(), target, planops.OperationLand)
}

// FinalStates derives the per-repo final SHAs recorded on the registry
// tombstone. A landing receipt wins over a live branch head: the receipt is
// immutable and asserts the work actually landed, while a branch head is only
// where a checkout happened to be pointing when finish ran. Repos with neither
// are omitted rather than recorded with an empty SHA.
func (l Ledger) FinalStates() []worktreeregistry.RepoFinalState {
	byRepo := map[string]worktreeregistry.RepoFinalState{}

	// Receipts oldest-first, so the last one to mention a repo wins.
	for _, receipt := range l.Receipts {
		for _, repo := range receipt.Repos {
			if repo.Repo == "" || repo.LandedRange.End == "" {
				continue
			}
			byRepo[repo.Repo] = worktreeregistry.RepoFinalState{
				Repo:   repo.Repo,
				Branch: repo.Branch,
				SHA:    repo.LandedRange.End,
				Source: worktreeregistry.SHASourceReceipt,
			}
		}
	}

	for _, repo := range l.repoCheckouts() {
		if _, ok := byRepo[repo.name]; ok {
			continue
		}
		head := gitLine(repo.path, "rev-parse", "--verify", "HEAD")
		if head == "" {
			continue
		}
		byRepo[repo.name] = worktreeregistry.RepoFinalState{
			Repo:   repo.name,
			Branch: gitLine(repo.path, "rev-parse", "--abbrev-ref", "HEAD"),
			SHA:    head,
			Source: worktreeregistry.SHASourceBranchHead,
		}
	}

	names := make([]string, 0, len(byRepo))
	for name := range byRepo {
		names = append(names, name)
	}
	sort.Strings(names)
	states := make([]worktreeregistry.RepoFinalState, 0, len(names))
	for _, name := range names {
		states = append(states, byRepo[name])
	}
	return states
}

type ledgerCheckout struct {
	name string
	path string
}

// repoCheckouts lists the worktree's repository checkouts, mirroring the
// container/single-repo split previewAtFinish uses.
func (l Ledger) repoCheckouts() []ledgerCheckout {
	var out []ledgerCheckout
	for _, repo := range l.Repos {
		path := filepath.Join(l.ContainerPath, repo)
		if isDir(path) {
			out = append(out, ledgerCheckout{name: repo, path: path})
		}
	}
	if len(out) == 0 && l.ContainerPath != "" && isDir(l.ContainerPath) {
		out = append(out, ledgerCheckout{name: filepath.Base(l.ContainerPath), path: l.ContainerPath})
	}
	return out
}

// LedgerTitle is the note title. The "Plan ledger:" prefix is the stable,
// greppable marker in both the title and the derived filename.
func LedgerTitle(plan string) string {
	return "Plan ledger: " + plan
}

// RenderLedger renders the markdown body of the ledger note. It is pure: same
// Ledger in, same bytes out, no filesystem or git access.
func RenderLedger(l Ledger) string {
	var b strings.Builder

	fmt.Fprintf(&b, "<!-- %s schema_version=%d plan=%s worktree=%s -->\n\n",
		ledgerMarker, LedgerSchemaVersion, l.Plan, l.Worktree)

	fmt.Fprintf(&b, "# %s\n\n", LedgerTitle(l.Plan))
	fmt.Fprintf(&b, "- **Plan:** %s\n", l.Plan)
	if l.Worktree != "" {
		fmt.Fprintf(&b, "- **Worktree:** %s\n", l.Worktree)
	}
	if l.ContainerPath != "" {
		fmt.Fprintf(&b, "- **Container:** `%s`\n", l.ContainerPath)
	}
	if len(l.Repos) > 0 {
		fmt.Fprintf(&b, "- **Repos:** %s\n", strings.Join(l.Repos, ", "))
	}
	fmt.Fprintf(&b, "- **Finished:** %s\n", l.GeneratedAt.Format(time.RFC3339))
	b.WriteString("\n")

	renderLedgerReceipts(&b, l)
	renderLedgerJobs(&b, l)
	renderLedgerDisposition(&b, l)

	return b.String()
}

func renderLedgerReceipts(b *strings.Builder, l Ledger) {
	b.WriteString("## Landing receipts\n\n")
	if l.ReceiptsErr != "" {
		fmt.Fprintf(b, "_Receipts unreadable: %s_\n\n", l.ReceiptsErr)
		return
	}
	if len(l.Receipts) == 0 {
		b.WriteString("_No landing receipts — nothing from this plan was landed through planops._\n\n")
		return
	}
	for _, receipt := range l.Receipts {
		fmt.Fprintf(b, "### %s — %s\n\n", filepath.Base(receipt.SourcePath), receipt.Operation)
		fmt.Fprintf(b, "- **When:** %s\n", receipt.Timestamp.UTC().Format(time.RFC3339))
		if receipt.Fingerprint != "" {
			fmt.Fprintf(b, "- **Fingerprint:** `%s`\n", receipt.Fingerprint)
		}
		b.WriteString("\n")
		if len(receipt.Repos) == 0 {
			b.WriteString("_No repositories recorded._\n\n")
			continue
		}
		b.WriteString("| repo | branch | reviewed head | landed range |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
		for _, repo := range receipt.Repos {
			fmt.Fprintf(b, "| %s | %s | `%s` | `%s..%s` |\n",
				cell(repo.Repo), cell(repo.Branch), shortSHA(repo.ReviewedHeadSHA),
				shortSHA(repo.LandedRange.Start), shortSHA(repo.LandedRange.End))
		}
		b.WriteString("\n")
	}
}

func renderLedgerJobs(b *strings.Builder, l Ledger) {
	b.WriteString("## Per-job commit ranges\n\n")
	if len(l.Jobs) == 0 {
		b.WriteString("_No jobs in this plan._\n\n")
		return
	}
	for _, job := range l.Jobs {
		fmt.Fprintf(b, "### %s", cell(job.JobFile))
		if job.Title != "" {
			fmt.Fprintf(b, " — %s", job.Title)
		}
		if job.Status != "" {
			fmt.Fprintf(b, " (%s)", job.Status)
		}
		b.WriteString("\n\n")

		if job.Record == nil {
			fmt.Fprintf(b, "_No commit capture recorded for this job._\n\n")
			continue
		}
		if len(job.Record.Repos) == 0 {
			b.WriteString("_Commit capture recorded no repositories._\n\n")
			continue
		}
		b.WriteString("| repo | branch | range | commits | dirty at end | note |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		repos := append([]orchestration.JobCommitsRepo(nil), job.Record.Repos...)
		sort.SliceStable(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
		for _, repo := range repos {
			// nil Commits means "could not be computed" (the schema's
			// contractual distinction from an empty list); rendering it as 0
			// would assert the job produced nothing, which is a different claim.
			count := "n/a"
			if repo.Commits != nil {
				count = fmt.Sprintf("%d", len(repo.Commits))
			}
			fmt.Fprintf(b, "| %s | %s | `%s..%s` | %s | %s | %s |\n",
				cell(repo.Name), cell(repo.Branch),
				shortSHA(repo.StartHead), shortSHA(repo.EndHead),
				count, yesNo(repo.DirtyAtEnd), cell(repo.Note))
		}
		b.WriteString("\n")
	}
}

// renderLedgerDisposition is the "what was left behind" section. Unlanded
// commits and uncommitted work are the two things a finish can silently
// discard, so they get an explicit row each rather than being inferred from
// their absence elsewhere.
func renderLedgerDisposition(b *strings.Builder, l Ledger) {
	b.WriteString("## State at finish\n\n")
	if l.Preview == nil {
		reason := l.PreviewErr
		if reason == "" {
			reason = "not computed"
		}
		fmt.Fprintf(b, "_Could not read the worktree's final state: %s_\n\n", reason)
		return
	}
	if len(l.Preview.Repos) == 0 {
		b.WriteString("_No repository checkouts remained to inspect._\n\n")
		return
	}
	b.WriteString("| repo | branch | onto | ahead | behind | dirty | disposition | reason |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	repos := append([]planops.RepoPreview(nil), l.Preview.Repos...)
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	unlanded := 0
	dirty := 0
	for _, repo := range repos {
		if repo.Ahead > 0 {
			unlanded++
		}
		if repo.Dirty {
			dirty++
		}
		fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %s | %s | %s |\n",
			cell(repo.Name), cell(repo.Branch), cell(repo.Onto),
			repo.Ahead, repo.Behind, yesNo(repo.Dirty),
			cell(string(repo.Disposition)), cell(repo.Reason))
	}
	b.WriteString("\n")
	switch {
	case unlanded == 0 && dirty == 0:
		b.WriteString("Every repository was fully landed and clean at finish.\n\n")
	default:
		fmt.Fprintf(b, "**%d repo(s) still ahead of their base branch and %d with uncommitted changes at finish.**\n\n",
			unlanded, dirty)
	}
}

func cell(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	// Pipes would break the markdown table row this lands in.
	return strings.ReplaceAll(s, "|", "\\|")
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "—"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// gitLine runs a read-only git command in dir and returns its first line, or
// "" on any failure. Every caller here treats "unknown" as an acceptable
// answer — a ledger with a missing SHA beats a finish that fails.
func gitLine(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // args are internal
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}
