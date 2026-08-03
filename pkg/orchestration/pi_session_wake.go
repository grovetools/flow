package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// pi_session_wake.go implements the wake protocol between Flow (the sender of
// turns) and the Phase 3 Pi-side extension (the receiver).
//
// # Why a sentinel file rather than just watching the chat .md
//
// The chat file IS the truth — every reconciliation reads it, and a missed
// wake costs latency, never correctness. But it is a poor wake SIGNAL:
// AppendChatUserTurn writes it through a temp-file + rename, which replaces the
// inode. A watcher registered on the old inode (the common default for
// single-file watches, including Node's fs.watch on macOS) silently stops
// firing after the first turn — the exact failure that looks like "the oracle
// ignored me".
//
// The sentinel decouples the two concerns. wake.json is small, is written the
// same atomic way, and carries a monotonic sequence plus the chat file's digest
// so the receiver can tell a real new turn from a re-notification without
// parsing anything. The receiver may watch the DIRECTORY (which survives
// rename-replace), poll the file, or do both; all three converge on the same
// answer because reconciliation always re-reads the chat file.
//
// Daemon publish (the satellite case, where the sender cannot touch the
// oracle's filesystem at all) is deliberately deferred: it is a second
// transport for the same signal, and the file remains truth under both.

// PiSessionWakeReason names what produced a wake nudge.
const (
	// WakeReasonSay — a user turn was appended by `flow plan say`.
	WakeReasonSay = "say"
	// WakeReasonLaunch — the session was just launched or re-attached; the
	// receiver should reconcile the whole chat file from scratch.
	WakeReasonLaunch = "launch"
	// WakeReasonComplete — the chat was completed; the session should stop
	// expecting turns.
	WakeReasonComplete = "complete"
)

// PiSessionWake is the payload of the wake sentinel.
type PiSessionWake struct {
	// Seq increases by one per nudge. It exists so a receiver that missed a
	// filesystem event can detect the gap; it is NOT a turn counter.
	Seq int64 `json:"seq"`
	// Reason is one of the WakeReason* constants.
	Reason string `json:"reason"`
	// JobID / ChatFile bind the nudge to its chat, so a receiver that watches
	// several never has to infer which one moved.
	JobID    string `json:"job_id"`
	ChatFile string `json:"chat_file"`
	// ChatSHA256 is the digest of the chat file's bytes at nudge time. A
	// receiver that has already reconciled this exact digest can drop the nudge
	// as a duplicate without re-parsing — this is what makes repeated nudges
	// idempotent rather than merely harmless.
	ChatSHA256 string `json:"chat_sha256"`
	// At is the nudge timestamp (RFC3339Nano, UTC).
	At string `json:"at"`
}

// PiSessionDirName is the per-job artifact subdirectory holding everything the
// pi-session responder owns that is not the transcript itself.
const PiSessionDirName = "pi-session"

// PiSessionArtifactDir is the Flow-owned directory for a pi-session chat's
// control files. It sits under the job's .artifacts root (never under the plan
// dir proper) so it stays out of notebook document sync, exactly like the
// session transcripts beside it.
func PiSessionArtifactDir(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, PiSessionDirName)
}

// PiSessionWakePath is the wake sentinel's path. The Phase 3 extension derives
// it from the same two inputs (plan dir + job id) it already receives through
// GROVE_FLOW_JOB_PATH and GROVE_FLOW_JOB_ID, so no new environment plumbing is
// required to find it.
func PiSessionWakePath(planDir, jobID string) string {
	return filepath.Join(PiSessionArtifactDir(planDir, jobID), "wake.json")
}

// PiSessionDescriptorPath is the path of the launch descriptor: the one file a
// receiver reads to learn every other path it needs.
func PiSessionDescriptorPath(planDir, jobID string) string {
	return filepath.Join(PiSessionArtifactDir(planDir, jobID), "session.json")
}

// PiSessionDescriptor is the launch record the Phase 3 extension reads to bind
// itself to its chat. It is rewritten on every launch/re-attach, and its fields
// are the cross-phase contract.
type PiSessionDescriptor struct {
	SchemaVersion int    `json:"schema_version"`
	JobID         string `json:"job_id"`
	JobTitle      string `json:"job_title"`
	PlanName      string `json:"plan_name"`
	PlanDir       string `json:"plan_dir"`
	// ChatFile is the plan-level chat .md — the canonical, human-readable
	// record. It is NOT under .artifacts.
	ChatFile string `json:"chat_file"`
	// WakeFile is the sentinel this descriptor's owner should watch.
	WakeFile string `json:"wake_file"`
	// SessionFile is the synthesized transcript pi was launched against.
	SessionFile string `json:"session_file"`
	// SessionID is the pi session id (the header id).
	SessionID string `json:"session_id"`
	// ContextDir is the resolved worktree/sub-project the rules were resolved
	// against and the session's cwd.
	ContextDir string `json:"context_dir"`
	// RulesFile / LayerPaths record exactly which bytes were seeded, so the
	// manifest — not the transcript — remains the truthful record of what the
	// session has seen.
	RulesFile  string   `json:"rules_file,omitempty"`
	LayerPaths []string `json:"layer_paths,omitempty"`
	// Model is the resolved Pi `--model` value ("" = pi's own default).
	Model string `json:"model,omitempty"`
	// Provider is the Pi-family agent provider used (pi / grove-agent).
	Provider string `json:"provider"`
	// SeedTokens / SeedBudget record the window gate's verdict at launch.
	SeedTokens  int    `json:"seed_tokens"`
	SeedBudget  int    `json:"seed_budget"`
	SeedFamily  string `json:"seed_family"`
	LaunchedAt  string `json:"launched_at"`
	SeedVersion int    `json:"seed_format_version"`
}

// piSessionDescriptorSchema is the descriptor's schema version. Phase 3 reads
// it and must refuse a version it does not understand rather than guessing.
const piSessionDescriptorSchema = 1

// WritePiSessionDescriptor persists the launch descriptor atomically.
func WritePiSessionDescriptor(planDir string, desc PiSessionDescriptor) error {
	desc.SchemaVersion = piSessionDescriptorSchema
	desc.SeedVersion = piSessionFormatVersion
	path := PiSessionDescriptorPath(planDir, desc.JobID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating pi-session artifact directory: %w", err)
	}
	data, err := json.MarshalIndent(desc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi-session descriptor: %w", err)
	}
	return writeFileAtomic0600(path, append(data, '\n'))
}

// ReadPiSessionDescriptor loads the launch descriptor. A missing file returns
// (nil, nil): "this chat has never been launched" is a state, not an error.
func ReadPiSessionDescriptor(planDir, jobID string) (*PiSessionDescriptor, error) {
	data, err := os.ReadFile(PiSessionDescriptorPath(planDir, jobID)) //nolint:gosec // Flow-owned artifact
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var desc PiSessionDescriptor
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("parsing pi-session descriptor for %s: %w", jobID, err)
	}
	return &desc, nil
}

// NudgePiSessionWake writes the wake sentinel for a pi-session chat.
//
// It is safe to call for ANY job: a chat that is not pi-session-responded is a
// silent no-op, so callers on shared paths (`flow plan say`) need no branch.
// It is also safe to call repeatedly — the sequence advances, the digest tells
// the receiver whether anything actually changed, and reconciliation is driven
// off the chat file either way.
//
// Errors are returned but every caller treats them as advisory: a failed nudge
// costs latency (the receiver still reconciles on its next poll or on its next
// wake), while failing the append that preceded it would cost the user's turn.
func NudgePiSessionWake(planDir string, job *Job, reason string) error {
	if job == nil || !job.IsPiSessionResponded() {
		return nil
	}
	path := PiSessionWakePath(planDir, job.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating pi-session artifact directory: %w", err)
	}

	// Continue the sequence from whatever is on disk. A missing or corrupt
	// sentinel restarts at 1 rather than failing: the receiver treats any
	// unexpected sequence as "reconcile from the file", which is exactly the
	// right response to a restarted counter.
	var seq int64
	if prev, err := ReadPiSessionWake(planDir, job.ID); err == nil && prev != nil {
		seq = prev.Seq
	}

	var digest string
	if content, err := os.ReadFile(job.FilePath); err == nil { //nolint:gosec // job file path from the loaded plan
		digest = sha256Hex(content)
	}

	wake := PiSessionWake{
		Seq:        seq + 1,
		Reason:     reason,
		JobID:      job.ID,
		ChatFile:   job.FilePath,
		ChatSHA256: digest,
		At:         time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(wake, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi-session wake: %w", err)
	}
	if err := writeFileAtomic0600(path, append(data, '\n')); err != nil {
		return fmt.Errorf("writing pi-session wake sentinel: %w", err)
	}
	return nil
}

// ReadPiSessionWake reads the current wake sentinel. A missing file returns
// (nil, nil).
func ReadPiSessionWake(planDir, jobID string) (*PiSessionWake, error) {
	data, err := os.ReadFile(PiSessionWakePath(planDir, jobID)) //nolint:gosec // Flow-owned artifact
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var wake PiSessionWake
	if err := json.Unmarshal(data, &wake); err != nil {
		return nil, fmt.Errorf("parsing pi-session wake sentinel for %s: %w", jobID, err)
	}
	return &wake, nil
}
