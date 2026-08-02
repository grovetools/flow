package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/grovetools/core/git"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"

	"github.com/grovetools/flow/pkg/planops"
)

// The review packet is the durable home of a plan's review.
//
// Everything the local review system records — per-file review marks
// (review:<repo>/<path>) and agent-written checklists (checklist:<id>) — lives
// in the worktree registry's SessionState: machine-local, unsynced, and
// STRIPPED by the tombstone at `flow plan finish`. A review therefore used to
// evaporate with the worktree that carried it.
//
// This file builds the packet: one notebook note per plan carrying (a) the
// plan's scope (per-job commit ranges from .artifacts/<job>/commits.json),
// (b) its disposition (a read-only planops land preview), (c) its notebook
// links, and (d) a versioned SNAPSHOT of the live review state, checkpointed
// into the note's frontmatter.
//
// Ownership split, following nb's existing convention: everything this file
// renders is machine-owned and sits ABOVE the sync marker; anything a human or
// agent writes below the marker is preserved verbatim across refreshes.
//
// CONCURRENCY (deliberately narrow, see the plan's adversarial review §7): this
// wave is single-machine, single-writer, whole-snapshot replace. A multi-writer
// merge model — append-only, identity-keyed evidence records with the snapshot
// derived as a projection — is deferred. The snapshot is written as an
// explicitly versioned key so that model can replace this one without a
// migration: a reader that does not recognize the version ignores it.
const (
	// ReviewPacketSchemaVersion is the rendered shape of the packet BODY. It is
	// emitted in the marker comment on the note's first line so a later reader
	// can tell which renderer produced it. Bump on any change a parser would
	// trip over.
	ReviewPacketSchemaVersion = 1

	// ReviewSnapshotSchemaVersion is the shape of the frontmatter snapshot
	// (D6). It is independent of the body version: the projection can gain
	// fields without re-rendering every packet body, and vice versa.
	ReviewSnapshotSchemaVersion = 1

	// reviewPacketMarker identifies a note as a review packet notebook-wide,
	// regardless of its filename or title.
	reviewPacketMarker = "grove-review-packet"

	// ReviewSnapshotKey is the packet's frontmatter key holding the snapshot.
	// nb does not model it; nb's frontmatter catch-all (Extra, rendered by
	// nb/pkg/frontmatter) round-trips unknown keys verbatim, so an nb-side
	// rewrite of the note preserves it.
	ReviewSnapshotKey = "review_snapshot"

	// reviewPacketSyncMarker is nb's sync-marker literal (nb/pkg/sync/syncer.go
	// syncMarker). It is duplicated rather than imported because flow does not
	// depend on nb as a Go module — it shells out to the `nb` CLI, which has no
	// verb that exposes the constant. The convention is what matters: content
	// above the marker is machine-owned and rebuilt on every refresh, content
	// below it belongs to whoever wrote it.
	reviewPacketSyncMarker = "<!-- nb-sync-marker -->"
)

// Packet trigger values. The trigger is recorded in the snapshot so a reader
// can tell a review-time checkpoint from the final one taken at finish.
const (
	ReviewTriggerReview = "review"
	ReviewTriggerFinish = "finish"
)

// Derived per-file review states, mirroring the three-state model in
// git-viewer/pkg/tui/viewer/review.go. The two extra values are honesty about
// what a snapshot taken outside the TUI can and cannot see.
const (
	// ReviewStateReviewedCurrent: the file's current blob hash equals the hash
	// it was reviewed at.
	ReviewStateReviewedCurrent = "reviewed_current"
	// ReviewStateChangedSinceReview: the file was re-edited after its review.
	ReviewStateChangedSinceReview = "changed_since_review"
	// ReviewStateGone: the reviewed path no longer exists in the checkout.
	ReviewStateGone = "gone"
	// ReviewStateUnknown: the current hash could not be read (repo checkout
	// unavailable, hashing failed). NEVER conflated with reviewed or changed —
	// an unknown state must not read as either verdict.
	ReviewStateUnknown = "unknown"
)

// Provenance of a snapshot file row.
const (
	// reviewSourceRecord: a path-keyed ReviewRecord.
	reviewSourceRecord = "record"
	// reviewSourceLegacyMarker: a legacy review:<repo>/<path>@<hash>=true key
	// that git-viewer has not migrated yet. Recorded rather than dropped —
	// this is a checkpoint, not a cleanup.
	reviewSourceLegacyMarker = "legacy_marker"
)

// reviewKeyPrefix / checklistKeyPrefix are the SessionState namespaces owned by
// git-viewer (viewer.reviewKeyPrefix / viewer.checklistKeyPrefix). They are
// duplicated here because the dependency runs the OTHER way — git-viewer
// imports flow/pkg/orchestration — so flow cannot import the viewer package to
// share them. The snapshot therefore reads the raw SessionState shape and does
// not depend on any viewer type.
const (
	reviewKeyPrefix    = "review:"
	checklistKeyPrefix = "checklist:"
)

// ReviewSnapshot is the versioned projection of live review state written into
// the packet's frontmatter. Whole-snapshot replace, single writer.
type ReviewSnapshot struct {
	SchemaVersion  int                       `yaml:"schema_version"`
	CheckpointedAt string                    `yaml:"checkpointed_at"`
	Trigger        string                    `yaml:"trigger,omitempty"`
	RegistryID     string                    `yaml:"registry_id,omitempty"`
	ContainerPath  string                    `yaml:"container_path,omitempty"`
	Files          []ReviewSnapshotFile      `yaml:"files,omitempty"`
	Checklists     []ReviewSnapshotChecklist `yaml:"checklists,omitempty"`
	// Unreadable records WHY a snapshot is empty when the reason is that the
	// live state could not be read. An empty snapshot with an empty Unreadable
	// means the plan genuinely has no review state.
	Unreadable string `yaml:"unreadable,omitempty"`
}

// IsEmpty reports whether the snapshot carries no review evidence at all. An
// empty snapshot must never replace a non-empty stored one (see
// mergeReviewSnapshot): SessionState is gone after the tombstone, so a
// re-checkpoint would otherwise erase the very thing that was preserved.
func (s ReviewSnapshot) IsEmpty() bool {
	return len(s.Files) == 0 && len(s.Checklists) == 0
}

// ReviewSnapshotFile is one per-file review mark at checkpoint time.
type ReviewSnapshotFile struct {
	Key                  string `yaml:"key"`
	Repo                 string `yaml:"repo,omitempty"`
	Path                 string `yaml:"path,omitempty"`
	LastReviewedBlobHash string `yaml:"last_reviewed_blob_hash,omitempty"`
	CurrentBlobHash      string `yaml:"current_blob_hash,omitempty"`
	State                string `yaml:"state"`
	Source               string `yaml:"source,omitempty"`
	Note                 string `yaml:"note,omitempty"`
}

// ReviewSnapshotChecklist is one agent-written checklist at checkpoint time.
type ReviewSnapshotChecklist struct {
	Key       string                        `yaml:"key"`
	ID        string                        `yaml:"id,omitempty"`
	Title     string                        `yaml:"title,omitempty"`
	CreatedAt string                        `yaml:"created_at,omitempty"`
	UpdatedAt string                        `yaml:"updated_at,omitempty"`
	Agent     string                        `yaml:"agent,omitempty"`
	JobRef    string                        `yaml:"job_ref,omitempty"`
	Items     []ReviewSnapshotChecklistItem `yaml:"items,omitempty"`
	Note      string                        `yaml:"note,omitempty"`
}

// ReviewSnapshotChecklistItem mirrors viewer.ChecklistItem's persisted shape.
type ReviewSnapshotChecklistItem struct {
	ID      string `yaml:"id,omitempty"`
	Text    string `yaml:"text,omitempty"`
	Checked bool   `yaml:"checked"`
	Note    string `yaml:"note,omitempty"`
}

// Done reports how many of a checklist's items are ticked.
func (c ReviewSnapshotChecklist) Done() int {
	n := 0
	for _, item := range c.Items {
		if item.Checked {
			n++
		}
	}
	return n
}

// ReviewPacketJob is one job's commit capture. Record is nil when the job never
// wrote a commits.json — rendered as an explicit "no capture" row rather than
// omitted, because a silently missing job reads as "this job produced nothing".
type ReviewPacketJob struct {
	JobID   string
	JobFile string
	Title   string
	Status  string
	Record  *JobCommitsRecord
	Err     string
}

// ReviewPacket is everything the packet note is rendered from.
type ReviewPacket struct {
	Plan          string
	Worktree      string
	ContainerPath string
	Repos         []string
	PlanRef       string
	Trigger       string

	// Jobs is in plan job order.
	Jobs []ReviewPacketJob
	// Tickets are the notebook notes joined to this plan by plan_ref, minus
	// the packet itself. Derived from nb's own linkage (see note_link.go); when
	// nb reports none, the section says so rather than guessing at a ticket.
	Tickets    []PlanNote
	TicketsErr string
	// Preview is the read-only planops land preview: what each repo's checkout
	// looks like right now. Nil when it could not be computed.
	Preview    *planops.OperationPreview
	PreviewErr string

	Snapshot ReviewSnapshot
}

// ReviewPacketTitle is the note title. The "Review packet:" prefix is the
// stable, greppable marker in both the title and the derived filename.
func ReviewPacketTitle(plan string) string { return "Review packet: " + plan }

// reviewBlobHashes is the batch blob hasher used to derive per-file review
// state. Indirected so tests can drive state derivation without a git repo.
var reviewBlobHashes = git.GetBlobHashes

// CollectReviewPacket gathers everything the packet is rendered from. It
// performs LOCAL reads only — plan artifacts, the worktree registry, and
// read-only git through planops.Preview and git hash-object. Nothing here
// mutates a repository, moves a note, or touches the network, so it is safe on
// both the review and the finish path and works offline by construction.
func CollectReviewPacket(plan *Plan, planDir, containerPath, trigger string, now time.Time, tickets []PlanNote, ticketsErr string) ReviewPacket {
	p := ReviewPacket{
		Plan:          filepath.Base(planDir),
		ContainerPath: containerPath,
		Trigger:       trigger,
		Tickets:       tickets,
		TicketsErr:    ticketsErr,
	}
	p.PlanRef = planRefFor(p.Plan)
	if plan != nil && plan.Config != nil {
		p.Worktree = plan.Config.Worktree
		p.Repos = append([]string(nil), plan.Config.Repos...)
	}

	if plan != nil {
		for _, job := range plan.Jobs {
			if job == nil {
				continue
			}
			pj := ReviewPacketJob{
				JobID:   job.ID,
				JobFile: filepath.Base(job.FilePath),
				Title:   job.Title,
				Status:  string(job.Status),
			}
			rec, err := ReadJobCommits(plan, job)
			if err == nil {
				pj.Record = rec
			} else {
				pj.Err = err.Error()
			}
			p.Jobs = append(p.Jobs, pj)
		}
	}

	preview, err := previewForPacket(containerPath, p.Repos)
	if err != nil {
		p.PreviewErr = err.Error()
	} else {
		p.Preview = &preview
	}

	p.Snapshot = CollectReviewSnapshot(containerPath, p.Repos, trigger, now)
	return p
}

// previewForPacket runs planops' land preview over the worktree's own
// checkouts. Preview is a preflight: it reads git status and divergence and
// mutates nothing, which is exactly the "where does this plan stand" snapshot
// the packet wants. Mirrors plan_finish's previewAtFinish.
func previewForPacket(containerPath string, repos []string) (planops.OperationPreview, error) {
	if containerPath == "" {
		return planops.OperationPreview{}, fmt.Errorf("worktree container path unknown")
	}
	target := coreplan.PlanActionTarget{ContainerPath: containerPath}
	for _, repo := range repos {
		path := filepath.Join(containerPath, repo)
		if !packetIsDir(path) {
			continue
		}
		target.Repos = append(target.Repos, coreplan.RepoTarget{Name: repo, Path: path})
	}
	if len(target.Repos) == 0 {
		// A single-repo (non-ecosystem) worktree IS the checkout.
		if packetIsDir(containerPath) {
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

// CollectReviewSnapshot projects the worktree registry's live review state into
// the versioned snapshot. A registry that cannot be read yields an EMPTY
// snapshot carrying the reason in Unreadable — never a partial one that would
// look like "the user reviewed less than they did".
func CollectReviewSnapshot(containerPath string, repos []string, trigger string, now time.Time) ReviewSnapshot {
	snap := ReviewSnapshot{
		SchemaVersion:  ReviewSnapshotSchemaVersion,
		CheckpointedAt: now.UTC().Format(time.RFC3339),
		Trigger:        trigger,
		ContainerPath:  containerPath,
	}
	if containerPath == "" {
		snap.Unreadable = "worktree container path unknown"
		return snap
	}
	snap.RegistryID = pathutil.WorktreeID(containerPath)

	entry, err := worktreeregistry.Load(snap.RegistryID)
	if err != nil {
		if os.IsNotExist(err) {
			snap.Unreadable = "no worktree registry entry for " + containerPath
		} else {
			snap.Unreadable = err.Error()
		}
		return snap
	}
	if entry == nil || len(entry.SessionState) == 0 {
		return snap
	}

	keys := make([]string, 0, len(entry.SessionState))
	for k := range entry.SessionState {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value := entry.SessionState[key]
		switch {
		case strings.HasPrefix(key, reviewKeyPrefix):
			snap.Files = append(snap.Files, snapshotReviewFile(key, value))
		case strings.HasPrefix(key, checklistKeyPrefix):
			snap.Checklists = append(snap.Checklists, snapshotChecklist(key, value))
		}
	}

	resolveSnapshotFileStates(snap.Files, containerPath, repos)
	return snap
}

// LiveReviewStateCounts reports how much review evidence the worktree's
// registry entry currently carries. It is the cheap probe (one registry file
// read, no git, no notebook) a caller uses to decide whether a checkpoint has
// anything to do — the full CollectReviewSnapshot hashes blobs and is Action-
// time work, not Check-time work. A missing registry entry is not an error: it
// means there is nothing to checkpoint.
func LiveReviewStateCounts(containerPath string) (files, checklists int, err error) {
	if containerPath == "" {
		return 0, 0, nil
	}
	entry, err := worktreeregistry.Load(pathutil.WorktreeID(containerPath))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if entry == nil {
		return 0, 0, nil
	}
	for key := range entry.SessionState {
		switch {
		case strings.HasPrefix(key, reviewKeyPrefix):
			files++
		case strings.HasPrefix(key, checklistKeyPrefix):
			checklists++
		}
	}
	return files, checklists, nil
}

// snapshotReviewFile decodes one review:* SessionState entry. Values arrive as
// map[string]interface{} (a JSON round-trip through worktreeregistry), so the
// decode goes back through JSON rather than depending on the viewer's
// mapstructure tags.
func snapshotReviewFile(key string, value interface{}) ReviewSnapshotFile {
	f := ReviewSnapshotFile{Key: key, State: ReviewStateUnknown, Source: reviewSourceRecord}
	rest := strings.TrimPrefix(key, reviewKeyPrefix)
	f.Repo, f.Path, _ = strings.Cut(rest, "/")

	var rec struct {
		LastReviewedBlobHash string `json:"lastReviewedBlobHash"`
	}
	if data, err := json.Marshal(value); err == nil {
		_ = json.Unmarshal(data, &rec)
	}
	if rec.LastReviewedBlobHash != "" {
		f.LastReviewedBlobHash = rec.LastReviewedBlobHash
		return f
	}

	// Legacy review:<repo>/<path>@<hash>=true marker: git-viewer migrates these
	// to path keys on its next fetch, but a checkpoint that ran first must
	// still record them rather than lose the mark.
	if at := strings.LastIndex(f.Path, "@"); at > 0 && packetIsTruthy(value) {
		f.LastReviewedBlobHash = f.Path[at+1:]
		f.Path = f.Path[:at]
		f.Source = reviewSourceLegacyMarker
		return f
	}

	f.Note = "review value not recognized; recorded verbatim key only"
	return f
}

// packetIsTruthy interprets a legacy SessionState review marker. Keys were
// stored as bool true, but a JSON round-trip can surface them as other types.
func packetIsTruthy(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}

// snapshotChecklist decodes one checklist:* SessionState entry into the
// snapshot shape (viewer.Checklist's persisted JSON).
func snapshotChecklist(key string, value interface{}) ReviewSnapshotChecklist {
	c := ReviewSnapshotChecklist{Key: key}
	var raw struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
		Agent     string `json:"agent"`
		JobRef    string `json:"jobRef"`
		Items     []struct {
			ID      string `json:"id"`
			Text    string `json:"text"`
			Checked bool   `json:"checked"`
			Note    string `json:"note"`
		} `json:"items"`
	}
	data, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(data, &raw)
	}
	if err != nil {
		c.Note = "checklist value not recognized; recorded verbatim key only"
		return c
	}
	c.ID = raw.ID
	if c.ID == "" {
		c.ID = strings.TrimPrefix(key, checklistKeyPrefix)
	}
	c.Title = raw.Title
	c.CreatedAt = raw.CreatedAt
	c.UpdatedAt = raw.UpdatedAt
	c.Agent = raw.Agent
	c.JobRef = raw.JobRef
	for _, item := range raw.Items {
		c.Items = append(c.Items, ReviewSnapshotChecklistItem{
			ID: item.ID, Text: item.Text, Checked: item.Checked, Note: item.Note,
		})
	}
	return c
}

// resolveSnapshotFileStates derives each file's state by comparing its recorded
// blob hash against the checkout's CURRENT hash, mirroring git-viewer's
// three-state model. Hashing is batched per repo. Any file whose current hash
// cannot be read stays ReviewStateUnknown: a checkpoint reports what it could
// see, and never downgrades a mark it merely failed to verify.
func resolveSnapshotFileStates(files []ReviewSnapshotFile, containerPath string, repos []string) {
	byRepo := map[string][]int{}
	for i := range files {
		if files[i].Repo == "" || files[i].Path == "" {
			continue
		}
		byRepo[files[i].Repo] = append(byRepo[files[i].Repo], i)
	}

	for repoName, idxs := range byRepo {
		repoPath := packetRepoPath(containerPath, repos, repoName)
		if repoPath == "" {
			for _, i := range idxs {
				files[i].Note = appendNote(files[i].Note, "repo checkout unavailable")
			}
			continue
		}
		paths := make([]string, 0, len(idxs))
		for _, i := range idxs {
			paths = append(paths, files[i].Path)
		}
		hashes, err := reviewBlobHashes(repoPath, paths)
		if err != nil {
			for _, i := range idxs {
				files[i].Note = appendNote(files[i].Note, "blob hashing failed: "+err.Error())
			}
			continue
		}
		for _, i := range idxs {
			current := hashes[files[i].Path]
			switch {
			case current == "":
				// The batch hasher skips paths that are not present on disk.
				if _, statErr := os.Stat(filepath.Join(repoPath, files[i].Path)); os.IsNotExist(statErr) {
					files[i].State = ReviewStateGone
				} else {
					files[i].Note = appendNote(files[i].Note, "current blob hash unavailable")
				}
			case files[i].LastReviewedBlobHash == "":
				files[i].CurrentBlobHash = current
			case current == files[i].LastReviewedBlobHash:
				files[i].CurrentBlobHash = current
				files[i].State = ReviewStateReviewedCurrent
			default:
				files[i].CurrentBlobHash = current
				files[i].State = ReviewStateChangedSinceReview
			}
		}
	}
}

// packetRepoPath resolves a review key's repo NAME (the checkout's base name,
// which is how git-viewer namespaces the keys) to a checkout path under the
// worktree container. Returns "" when no such checkout exists.
func packetRepoPath(containerPath string, repos []string, repoName string) string {
	if containerPath == "" || repoName == "" {
		return ""
	}
	for _, repo := range repos {
		if repo == repoName && packetIsDir(filepath.Join(containerPath, repo)) {
			return filepath.Join(containerPath, repo)
		}
	}
	if candidate := filepath.Join(containerPath, repoName); packetIsDir(candidate) {
		return candidate
	}
	// A single-repo worktree IS the checkout, and its keys are namespaced by
	// the container's own base name.
	if filepath.Base(containerPath) == repoName && packetIsDir(containerPath) {
		return containerPath
	}
	return ""
}

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

func packetIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// RenderReviewPacket renders the MACHINE-OWNED half of the packet body — every
// byte above the sync marker. It is pure: same ReviewPacket in, same bytes out,
// no filesystem or git access, and no timestamp of its own. The packet's single
// mutable timestamp lives in the frontmatter snapshot (checkpointed_at), which
// is what makes an unchanged refresh byte-identical.
func RenderReviewPacket(p ReviewPacket) string {
	var b strings.Builder

	fmt.Fprintf(&b, "<!-- %s schema_version=%d plan=%s worktree=%s -->\n\n",
		reviewPacketMarker, ReviewPacketSchemaVersion, p.Plan, p.Worktree)

	fmt.Fprintf(&b, "# %s\n\n", ReviewPacketTitle(p.Plan))
	fmt.Fprintf(&b, "- **Plan:** %s\n", p.Plan)
	if p.PlanRef != "" {
		fmt.Fprintf(&b, "- **Plan ref:** %s\n", p.PlanRef)
	}
	if p.Worktree != "" {
		fmt.Fprintf(&b, "- **Worktree:** %s\n", p.Worktree)
	}
	if p.ContainerPath != "" {
		fmt.Fprintf(&b, "- **Container:** `%s`\n", p.ContainerPath)
	}
	if len(p.Repos) > 0 {
		fmt.Fprintf(&b, "- **Repos:** %s\n", strings.Join(p.Repos, ", "))
	}
	b.WriteString("\n")
	b.WriteString("_Everything above the sync marker at the bottom of this note is generated by " +
		"`flow plan review` / `flow plan finish` and is replaced on every refresh. Write below the marker._\n\n")

	renderPacketLinks(&b, p)
	renderPacketScope(&b, p)
	renderPacketDisposition(&b, p)
	renderPacketReviewState(&b, p)

	return b.String()
}

func renderPacketLinks(b *strings.Builder, p ReviewPacket) {
	b.WriteString("## Linked notes\n\n")
	if p.TicketsErr != "" {
		fmt.Fprintf(b, "_Notebook links unreadable: %s_\n\n", p.TicketsErr)
		return
	}
	if len(p.Tickets) == 0 {
		fmt.Fprintf(b, "_No notes are linked to %s._\n\n", p.PlanRef)
		return
	}
	b.WriteString("| note | job | type |\n")
	b.WriteString("| --- | --- | --- |\n")
	notes := append([]PlanNote(nil), p.Tickets...)
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].Path < notes[j].Path })
	for _, n := range notes {
		// nb reports the FILENAME as `title`; prefer the note's own title when
		// it has one so the table reads like the notebook does.
		title := n.FrontmatterTitle
		if title == "" {
			title = n.Title
		}
		if title == "" {
			title = filepath.Base(n.Path)
		}
		fmt.Fprintf(b, "| [%s](%s) | %s | %s |\n", packetCell(title), n.Path, packetCell(n.PlanJob), packetCell(n.Type))
	}
	b.WriteString("\n")
}

func renderPacketScope(b *strings.Builder, p ReviewPacket) {
	b.WriteString("## Scope — per-job commit ranges\n\n")
	if len(p.Jobs) == 0 {
		b.WriteString("_No jobs in this plan._\n\n")
		return
	}
	for _, job := range p.Jobs {
		fmt.Fprintf(b, "### %s", packetCell(job.JobFile))
		if job.Title != "" {
			fmt.Fprintf(b, " — %s", job.Title)
		}
		if job.Status != "" {
			fmt.Fprintf(b, " (%s)", job.Status)
		}
		b.WriteString("\n\n")

		if job.Record == nil {
			b.WriteString("_No commit capture recorded for this job._\n\n")
			continue
		}
		if len(job.Record.Repos) == 0 {
			b.WriteString("_Commit capture recorded no repositories._\n\n")
			continue
		}
		b.WriteString("| repo | branch | range | commits | dirty at end | note |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		repos := append([]JobCommitsRepo(nil), job.Record.Repos...)
		sort.SliceStable(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
		for _, repo := range repos {
			// nil Commits means "could not be computed" (the schema's
			// contractual distinction from an empty list); rendering it as 0
			// would assert the job produced nothing, a different claim.
			count := "n/a"
			if repo.Commits != nil {
				count = fmt.Sprintf("%d", len(repo.Commits))
			}
			fmt.Fprintf(b, "| %s | %s | `%s..%s` | %s | %s | %s |\n",
				packetCell(repo.Name), packetCell(repo.Branch),
				packetShortSHA(repo.StartHead), packetShortSHA(repo.EndHead),
				count, packetYesNo(repo.DirtyAtEnd), packetCell(repo.Note))
		}
		b.WriteString("\n")
	}
}

func renderPacketDisposition(b *strings.Builder, p ReviewPacket) {
	b.WriteString("## Disposition — land preview\n\n")
	if p.Preview == nil {
		reason := p.PreviewErr
		if reason == "" {
			reason = "not computed"
		}
		fmt.Fprintf(b, "_Could not read the worktree's state: %s_\n\n", reason)
		return
	}
	if len(p.Preview.Repos) == 0 {
		b.WriteString("_No repository checkouts to inspect._\n\n")
		return
	}
	b.WriteString("| repo | branch | onto | ahead | behind | dirty | disposition | reason |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	repos := append([]planops.RepoPreview(nil), p.Preview.Repos...)
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	for _, repo := range repos {
		fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %s | %s | %s |\n",
			packetCell(repo.Name), packetCell(repo.Branch), packetCell(repo.Onto),
			repo.Ahead, repo.Behind, packetYesNo(repo.Dirty),
			packetCell(string(repo.Disposition)), packetCell(repo.Reason))
	}
	b.WriteString("\n")
}

// renderPacketReviewState renders the snapshot in human-readable form. The
// authoritative copy is the frontmatter key; this is the half a person reads.
func renderPacketReviewState(b *strings.Builder, p ReviewPacket) {
	s := p.Snapshot
	b.WriteString("## Review state\n\n")
	if s.Unreadable != "" {
		fmt.Fprintf(b, "_Live review state could not be read: %s_\n\n", s.Unreadable)
	}
	if s.IsEmpty() && s.Unreadable == "" {
		b.WriteString("_No files marked reviewed and no checklists recorded._\n\n")
	}

	if len(s.Files) > 0 {
		counts := map[string]int{}
		for _, f := range s.Files {
			counts[f.State]++
		}
		fmt.Fprintf(b, "%d file mark(s): %d reviewed at current blob, %d changed since review, %d gone, %d unknown.\n\n",
			len(s.Files),
			counts[ReviewStateReviewedCurrent], counts[ReviewStateChangedSinceReview],
			counts[ReviewStateGone], counts[ReviewStateUnknown])
		b.WriteString("| repo | path | state | reviewed at | current |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, f := range s.Files {
			fmt.Fprintf(b, "| %s | %s | %s | `%s` | `%s` |\n",
				packetCell(f.Repo), packetCell(f.Path), packetCell(f.State),
				packetShortSHA(f.LastReviewedBlobHash), packetShortSHA(f.CurrentBlobHash))
		}
		b.WriteString("\n")
	}

	if len(s.Checklists) > 0 {
		b.WriteString("### Checklists\n\n")
		for _, c := range s.Checklists {
			title := c.Title
			if title == "" {
				title = c.ID
			}
			fmt.Fprintf(b, "**%s** (%d/%d)", packetCell(title), c.Done(), len(c.Items))
			if c.Agent != "" {
				fmt.Fprintf(b, " — recorded by %s", packetCell(c.Agent))
			}
			b.WriteString("\n\n")
			for _, item := range c.Items {
				box := " "
				if item.Checked {
					box = "x"
				}
				fmt.Fprintf(b, "- [%s] %s", box, packetCell(item.Text))
				if item.Note != "" {
					fmt.Fprintf(b, " — _%s_", packetCell(item.Note))
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
}

func packetCell(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	// Pipes would break the markdown table row this lands in.
	return strings.ReplaceAll(s, "|", "\\|")
}

func packetShortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "—"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func packetYesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
