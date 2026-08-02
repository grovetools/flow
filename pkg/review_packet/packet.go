// Package review_packet makes a plan's review durable.
//
// The local review system is rich — per-file records anchored to the blob hash
// that was actually read, a three-state model that survives re-edits, and
// agent-authored human-verification checklists — and every byte of it lives in
// the worktree registry's SessionState: machine-local, unsynced, and deleted
// outright when `flow plan finish` tombstones the entry. Reviewing a plan and
// then finishing it has meant losing the record of who reviewed what.
//
// The review packet is the durable home. It is one generated note per plan in
// the notebook, carrying:
//
//   - the plan's SCOPE, from the per-job commits.json sidecars;
//   - its DISPOSITION, from a read-only planops land preview;
//   - its LINKS — plan_ref and the originating ticket notes;
//   - and a versioned CHECKPOINT of the live review state, in frontmatter.
//
// Notebook notes replicate, so the packet outlives the worktree, the branch and
// the machine. Refresh is idempotent and preserves anything written below nb's
// sync marker, which is what makes it safe to re-run on every review.
//
// # Single writer
//
// This wave is deliberately single-machine, single-writer: the checkpoint is a
// whole-snapshot REPLACE, not a merge. The adversarial review is right that a
// last-write-wins snapshot key cannot be shared by several reviewers or
// machines without losing marks — that is why the snapshot is an explicitly
// versioned PROJECTION of state that lives elsewhere (SessionState remains
// canonical while the worktree exists) rather than the record of authority. An
// append-only, identity-keyed evidence model can replace this key wholesale
// later; readers keyed on schema_version will know which one they are looking
// at, and nothing needs migrating because nothing here is the source of truth.
package review_packet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planops"
)

// PacketSchemaVersion is the rendered shape of the packet BODY. It is emitted
// in the marker comment on the note's first line so a later reader can tell
// which renderer produced the prose. Bump on any change a parser would trip on.
//
// It is versioned separately from SnapshotSchemaVersion: the body is for
// humans and can be reformatted freely, while the frontmatter snapshot is the
// machine-readable half and changes on a different clock.
const PacketSchemaVersion = 1

// packetMarker identifies a note as a review packet notebook-wide, regardless
// of its filename — the same trick the plan ledger uses.
const packetMarker = "grove-review-packet"

// Source values for a checkpoint, recording WHICH trigger produced it.
const (
	// SourceReview: `flow plan review` — the plan entered (or is already in)
	// review and the packet was generated or refreshed.
	SourceReview = "review"
	// SourceFinish: the finish checkpoint item, the last chance to capture
	// review state before the registry tombstone strips SessionState.
	SourceFinish = "finish"
)

// PacketJob is one job's commit capture. Record is nil when the job never
// wrote a commits.json — rendered as an explicit "no capture" row rather than
// omitted, since a silently missing job reads as "this job produced nothing".
type PacketJob struct {
	JobID   string
	JobFile string
	Title   string
	Status  string
	Record  *orchestration.JobCommitsRecord
	Err     string
}

// TicketLink is a note joined to this plan through plan_ref. The packet links
// to whatever nb reports; it never guesses at a ticket that is not linked, and
// it never moves one (D5 — the packet is a new note, ticket state is untouched).
type TicketLink struct {
	Path    string
	Title   string
	Type    string
	PlanJob string
}

// Packet is everything the note is rendered from. Collect fills it; Render and
// the snapshot projection are pure functions of it.
type Packet struct {
	Plan          string
	Worktree      string
	ContainerPath string
	RegistryID    string
	Repos         []string
	PlanRef       string
	Status        string
	Source        string

	// CheckpointedAt stamps the snapshot. The writer replays Render with the
	// PREVIOUS note's stamp when deciding whether anything actually changed,
	// so a no-op refresh does not move the timestamp and the file stays byte
	// identical.
	CheckpointedAt time.Time

	// Jobs is in plan job order.
	Jobs []PacketJob

	// Preview is the read-only planops land preview: what is landed, what is
	// still ahead, what is dirty. Nil when it could not be computed.
	Preview    *planops.OperationPreview
	PreviewErr string

	// Tickets are the plan's linked notes, excluding the packet itself.
	Tickets    []TicketLink
	TicketsErr string

	// Snapshot is the checkpoint of live review state.
	Snapshot    Snapshot
	SnapshotErr string
}

// Title is the packet note's title. It doubles as the packet's identity: the
// generator finds its own note again by matching this string against notes'
// frontmatter titles, so it must stay stable for a given plan.
func Title(plan string) string { return "Review packet: " + plan }

// Collect gathers everything the packet is rendered from. It performs LOCAL
// READS ONLY — plan artifacts, the worktree registry, read-only git status via
// planops.Preview, blob hashing, and one `nb list` query. Nothing here mutates
// a repository, a registry entry or a note, and nothing touches the network,
// which is what lets the finish path depend on it offline.
//
// containerPath may be empty, in which case the worktree container is resolved
// from the registry by plan name. Every input degrades independently: a missing
// commits.json, an unreadable worktree and an absent registry entry each
// produce an explicit "not available" in the packet rather than an error, so a
// partial packet is always better than none.
func Collect(plan *orchestration.Plan, planPath, containerPath, source string, now time.Time) Packet {
	planName := filepath.Base(planPath)
	p := Packet{
		Plan:           planName,
		ContainerPath:  containerPath,
		PlanRef:        "plans/" + planName,
		Source:         source,
		CheckpointedAt: now.UTC(),
	}
	if plan != nil && plan.Config != nil {
		p.Worktree = plan.Config.Worktree
		p.Repos = append([]string(nil), plan.Config.Repos...)
		p.Status = plan.Config.Status
	}

	entry := resolveEntry(planName, &p)

	if plan != nil {
		for _, job := range plan.Jobs {
			if job == nil {
				continue
			}
			pj := PacketJob{
				JobID:   job.ID,
				JobFile: filepath.Base(job.FilePath),
				Title:   job.Title,
				Status:  string(job.Status),
			}
			rec, err := orchestration.ReadJobCommits(plan, job)
			if err == nil {
				pj.Record = rec
			} else if !os.IsNotExist(err) {
				pj.Err = err.Error()
			}
			p.Jobs = append(p.Jobs, pj)
		}
	}

	preview, err := previewAt(p.ContainerPath, p.Repos)
	if err != nil {
		p.PreviewErr = err.Error()
	} else {
		p.Preview = &preview
	}

	p.Tickets, p.TicketsErr = collectTickets(planName)

	p.Snapshot, p.SnapshotErr = CollectSnapshot(entry, p, now)
	return p
}

// resolveEntry locates the worktree registry entry that owns the plan's
// SessionState, filling ContainerPath and RegistryID on the packet. A finished
// (tombstoned) entry is deliberately invisible: its SessionState is already
// gone by design, so there is nothing to checkpoint and reporting it as missing
// is the accurate answer.
func resolveEntry(planName string, p *Packet) *worktreeregistry.Entry {
	if p.ContainerPath != "" {
		id := pathutil.WorktreeID(p.ContainerPath)
		entry, err := worktreeregistry.Load(id)
		if err != nil || entry == nil || entry.IsFinished() {
			return nil
		}
		p.RegistryID = id
		return entry
	}
	entry, err := worktreeregistry.FindByRef(planName)
	if err != nil || entry == nil {
		return nil
	}
	p.ContainerPath = entry.AbsPath
	p.RegistryID = pathutil.WorktreeID(entry.AbsPath)
	return entry
}

// previewAt runs planops' land preview over the worktree's checkouts. Preview
// is a preflight: it reads status and divergence and mutates nothing, which is
// exactly the "where does this plan stand" snapshot the packet wants.
func previewAt(containerPath string, repos []string) (planops.OperationPreview, error) {
	if containerPath == "" {
		return planops.OperationPreview{}, fmt.Errorf("worktree container path unknown")
	}
	target := coreplan.PlanActionTarget{ContainerPath: containerPath}
	for _, repo := range repos {
		path := filepath.Join(containerPath, repo)
		if isDir(path) {
			target.Repos = append(target.Repos, coreplan.RepoTarget{Name: repo, Path: path})
		}
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

// planNotes is the nb query seam, replaced in tests so no test can reach the
// owner's real notebook.
var planNotes = orchestration.PlanNotes

// collectTickets lists the plan's linked notes, dropping the packet's own note
// so the packet does not link to itself.
func collectTickets(planName string) ([]TicketLink, string) {
	notes, err := planNotes(planName)
	if err != nil {
		return nil, err.Error()
	}
	packetTitle := Title(planName)
	var out []TicketLink
	for _, n := range notes {
		if n.FrontmatterTitle == packetTitle {
			continue
		}
		out = append(out, TicketLink{
			Path:    n.Path,
			Title:   strings.TrimSpace(n.FrontmatterTitle),
			Type:    n.Type,
			PlanJob: n.PlanJob,
		})
	}
	return out, ""
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
