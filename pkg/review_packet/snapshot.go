package review_packet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/worktreeregistry"
)

// The checkpoint is a PROJECTION of the live review state that git-viewer keeps
// in the worktree registry's SessionState. Two key families are captured:
//
//	review:<repo>/<rel-path>  ->  {"lastReviewedBlobHash": "<sha>"}
//	checklist:<id>            ->  {"id","title","items":[{"id","text","checked","note"}], ...}
//
// The shapes are git-viewer's (pkg/tui/viewer/review.go, checklist.go). Flow
// cannot import them — git-viewer imports flow, so the dependency only runs one
// way — so they are decoded here through a JSON round-trip against the SAME
// json tags git-viewer writes. That keeps the wire contract explicit: a field
// git-viewer renames is a decode that starts coming back empty, not a silent
// type error.
//
// Nothing here writes to SessionState. The checkpoint is strictly read-only:
// it must never prune, migrate or "tidy" live review state on its way past.

// SnapshotSchemaVersion is the persisted shape of the frontmatter checkpoint
// (D6). Readers must tolerate an older version rather than reject it.
const SnapshotSchemaVersion = 1

// SnapshotKey is the packet frontmatter key the checkpoint is written under.
// It is a versioned KEY in the D6 sense: the value carries schema_version, and
// a future evidence model that cannot be expressed as a whole-snapshot replace
// gets its own key rather than redefining this one.
const SnapshotKey = "review_snapshot"

// SessionState key prefixes, mirroring git-viewer's reviewKeyPrefix and
// checklistKeyPrefix.
const (
	reviewKeyPrefix    = "review:"
	checklistKeyPrefix = "checklist:"
)

// Derived per-file review states. These are the wire names for git-viewer's
// Unreviewed / ReviewedCurrent / ChangedSinceReview, plus StateUnknown, which
// has no git-viewer equivalent: in the TUI a file whose hash cannot be read is
// simply not listed, but a checkpoint that dropped it would look like the file
// was never reviewed. "Unknown" says we could not look, which is a different
// claim from "not reviewed".
const (
	StateReviewedCurrent    = "reviewed_current"
	StateChangedSinceReview = "changed_since_review"
	StateUnknown            = "unknown"
)

// ReviewedFile is one per-file review record as checkpointed.
type ReviewedFile struct {
	Repo string `json:"repo" yaml:"repo"`
	Path string `json:"path" yaml:"path"`
	// LastReviewedBlob is the blob hash that was actually read at review time.
	// It is the durable half of the record: the answer to "reviewed WHAT",
	// which survives every later edit to the file.
	LastReviewedBlob string `json:"last_reviewed_blob" yaml:"last_reviewed_blob"`
	// State is the derivation at checkpoint time, comparing LastReviewedBlob
	// against the file's current blob hash.
	State string `json:"state" yaml:"state"`
	// CurrentBlob is the hash the comparison was made against. Empty when the
	// file could not be hashed (State is then unknown).
	CurrentBlob string `json:"current_blob,omitempty" yaml:"current_blob,omitempty"`
	// Legacy marks a record recovered from the pre-record `review:<path>@<hash>`
	// marker scheme, so a reader can tell a migrated value from a native one.
	Legacy bool `json:"legacy,omitempty" yaml:"legacy,omitempty"`
}

// ChecklistItem mirrors git-viewer's ChecklistItem.
type ChecklistItem struct {
	ID      string `json:"id" yaml:"id"`
	Text    string `json:"text" yaml:"text"`
	Checked bool   `json:"checked" yaml:"checked"`
	Note    string `json:"note,omitempty" yaml:"note,omitempty"`
}

// Checklist mirrors git-viewer's Checklist: a human-verification checklist an
// agent recorded for the user, with the user's ticks and annotations.
type Checklist struct {
	ID        string          `json:"id" yaml:"id"`
	Title     string          `json:"title" yaml:"title"`
	CreatedAt string          `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	Agent     string          `json:"agent,omitempty" yaml:"agent,omitempty"`
	JobRef    string          `json:"job_ref,omitempty" yaml:"job_ref,omitempty"`
	Items     []ChecklistItem `json:"items,omitempty" yaml:"items,omitempty"`
}

// Snapshot is the whole checkpoint, written as one frontmatter value.
type Snapshot struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	Plan          string `json:"plan" yaml:"plan"`
	Worktree      string `json:"worktree,omitempty" yaml:"worktree,omitempty"`
	RegistryID    string `json:"registry_id,omitempty" yaml:"registry_id,omitempty"`
	// Source records the trigger (review / finish) that produced this
	// checkpoint — useful when reading a packet later to know whether the last
	// word came from an active review or from teardown.
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	// CheckpointedAt moves only when the snapshot's CONTENT changes; an
	// unchanged refresh leaves it alone so the note stays byte identical.
	CheckpointedAt string `json:"checkpointed_at" yaml:"checkpointed_at"`
	// Available is false when there was no registry entry to read — the
	// worktree is gone, or was never registered. It distinguishes "nothing was
	// reviewed" from "we could not look", which a bare empty Files would not.
	Available  bool           `json:"available" yaml:"available"`
	Files      []ReviewedFile `json:"files,omitempty" yaml:"files,omitempty"`
	Checklists []Checklist    `json:"checklists,omitempty" yaml:"checklists,omitempty"`
}

// Empty reports whether the snapshot captured no review state at all. A finish
// checkpoint uses it to avoid creating a packet for a plan nobody ever
// reviewed.
func (s Snapshot) Empty() bool { return len(s.Files) == 0 && len(s.Checklists) == 0 }

// ReviewedCount reports how many files are still reviewed at their current
// content.
func (s Snapshot) ReviewedCount() int {
	n := 0
	for _, f := range s.Files {
		if f.State == StateReviewedCurrent {
			n++
		}
	}
	return n
}

// hashBlob is the blob hasher, a package var so state derivation is testable
// without a git repository.
var hashBlob = git.GetBlobHash

// CollectSnapshot projects a registry entry's SessionState into a Snapshot. A
// nil entry yields an explicit "not available" snapshot rather than an error:
// a plan whose worktree is already gone has no live review state to lose, and
// failing the checkpoint over it would block a finish for no reason.
func CollectSnapshot(entry *worktreeregistry.Entry, p Packet, now time.Time) (Snapshot, string) {
	snap := Snapshot{
		SchemaVersion:  SnapshotSchemaVersion,
		Plan:           p.Plan,
		Worktree:       p.Worktree,
		RegistryID:     p.RegistryID,
		Source:         p.Source,
		CheckpointedAt: now.UTC().Format(time.RFC3339),
	}
	if entry == nil {
		return snap, ""
	}
	snap.Available = true

	var problems []string
	files, checklists := splitSessionState(entry.SessionState)

	for _, rec := range files {
		rec.State, rec.CurrentBlob = deriveState(p.ContainerPath, rec)
		snap.Files = append(snap.Files, rec)
	}
	sort.SliceStable(snap.Files, func(i, j int) bool {
		if snap.Files[i].Repo != snap.Files[j].Repo {
			return snap.Files[i].Repo < snap.Files[j].Repo
		}
		return snap.Files[i].Path < snap.Files[j].Path
	})

	for _, raw := range checklists {
		cl, err := decodeChecklist(raw.value)
		if err != nil {
			problems = append(problems, fmt.Sprintf("checklist %s: %v", raw.key, err))
			continue
		}
		if cl.ID == "" {
			cl.ID = strings.TrimPrefix(raw.key, checklistKeyPrefix)
		}
		snap.Checklists = append(snap.Checklists, cl)
	}
	sort.SliceStable(snap.Checklists, func(i, j int) bool {
		if snap.Checklists[i].CreatedAt != snap.Checklists[j].CreatedAt {
			return snap.Checklists[i].CreatedAt < snap.Checklists[j].CreatedAt
		}
		return snap.Checklists[i].ID < snap.Checklists[j].ID
	})

	return snap, strings.Join(problems, "; ")
}

type rawEntry struct {
	key   string
	value any
}

// splitSessionState partitions the SessionState map into per-file review
// records and checklist entries. Both current path-keyed records and the legacy
// `review:<repo>/<path>@<hash>` markers are recognized: readers tolerate the
// old shape (D6), and a checkpoint that ignored legacy markers would silently
// drop the review state of anyone who has not opened the Changes page since the
// record format changed.
func splitSessionState(state map[string]any) ([]ReviewedFile, []rawEntry) {
	var files []ReviewedFile
	var checklists []rawEntry

	for key, value := range state {
		switch {
		case strings.HasPrefix(key, reviewKeyPrefix):
			if rec, ok := decodeReviewKey(key, value); ok {
				files = append(files, rec)
			}
		case strings.HasPrefix(key, checklistKeyPrefix):
			checklists = append(checklists, rawEntry{key: key, value: value})
		}
	}
	sort.SliceStable(checklists, func(i, j int) bool { return checklists[i].key < checklists[j].key })
	return files, checklists
}

// decodeReviewKey turns one review:* SessionState entry into a ReviewedFile.
// ok is false for a key that carries no usable hash — an unparseable value, or
// a legacy marker whose flag is not truthy (git-viewer only ever honoured
// truthy ones).
func decodeReviewKey(key string, value any) (ReviewedFile, bool) {
	path := strings.TrimPrefix(key, reviewKeyPrefix)

	// Current shape: a ReviewRecord under a path-only key.
	var rec struct {
		LastReviewedBlobHash string `json:"lastReviewedBlobHash"`
	}
	if err := remarshal(value, &rec); err == nil && rec.LastReviewedBlobHash != "" {
		repo, rel := splitRepoPath(path)
		return ReviewedFile{Repo: repo, Path: rel, LastReviewedBlob: rec.LastReviewedBlobHash}, true
	}

	// Legacy shape: review:<repo>/<path>@<hash> = true.
	if at := strings.LastIndex(path, "@"); at > 0 && truthy(value) {
		repo, rel := splitRepoPath(path[:at])
		return ReviewedFile{
			Repo:             repo,
			Path:             rel,
			LastReviewedBlob: path[at+1:],
			Legacy:           true,
		}, true
	}
	return ReviewedFile{}, false
}

// splitRepoPath splits `<repo>/<rel-path>` on the FIRST separator: the repo is
// one path element, the rest is a repo-relative path that may itself contain
// separators.
func splitRepoPath(s string) (repo, rel string) {
	repo, rel, found := strings.Cut(s, "/")
	if !found {
		return "", s
	}
	return repo, rel
}

// truthy interprets a legacy marker value the way git-viewer's isTruthy does: a
// JSON round-trip can surface the original bool as other types.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	default:
		return v != nil
	}
}

// deriveState compares a record's last-reviewed hash against the file's blob
// hash right now. A file that cannot be hashed — deleted, committed away, a
// repo that is gone, or a directory-shaped gitlink — is reported unknown rather
// than assumed changed: the durable claim is the recorded hash, and guessing at
// its current status would put a wrong verdict into a permanent record.
func deriveState(containerPath string, rec ReviewedFile) (state, currentBlob string) {
	repoPath := repoCheckoutPath(containerPath, rec.Repo)
	if repoPath == "" || rec.Path == "" {
		return StateUnknown, ""
	}
	full := filepath.Join(repoPath, rec.Path)
	if info, err := os.Lstat(full); err != nil || info.IsDir() {
		return StateUnknown, ""
	}
	hash, err := hashBlob(repoPath, rec.Path)
	if err != nil || hash == "" {
		return StateUnknown, ""
	}
	if hash == rec.LastReviewedBlob {
		return StateReviewedCurrent, hash
	}
	return StateChangedSinceReview, hash
}

// repoCheckoutPath resolves a record's repo name to a checkout inside the
// container, tolerating both layouts: an ecosystem worktree holds each repo in
// a subdirectory, while a single-repo worktree IS the checkout.
func repoCheckoutPath(containerPath, repo string) string {
	if containerPath == "" {
		return ""
	}
	if repo != "" {
		if candidate := filepath.Join(containerPath, repo); isDir(candidate) {
			return candidate
		}
		if filepath.Base(containerPath) != repo {
			return ""
		}
	}
	if isDir(containerPath) {
		return containerPath
	}
	return ""
}

// decodeChecklist decodes a checklist:* SessionState value. git-viewer writes
// camelCase json tags (createdAt, jobRef); the snapshot re-keys them to the
// snake_case the rest of the packet uses, so the decode is explicit rather than
// a straight re-serialization of git-viewer's struct.
func decodeChecklist(value any) (Checklist, error) {
	var wire struct {
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
	if err := remarshal(value, &wire); err != nil {
		return Checklist{}, err
	}
	cl := Checklist{
		ID:        wire.ID,
		Title:     wire.Title,
		CreatedAt: wire.CreatedAt,
		UpdatedAt: wire.UpdatedAt,
		Agent:     wire.Agent,
		JobRef:    wire.JobRef,
	}
	for _, it := range wire.Items {
		cl.Items = append(cl.Items, ChecklistItem{ID: it.ID, Text: it.Text, Checked: it.Checked, Note: it.Note})
	}
	return cl, nil
}

// remarshal decodes a SessionState value (a map[string]any after the JSON round
// trip through worktreeregistry.Load) into a typed struct by re-encoding it.
func remarshal(value, into any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}
