package planops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReceiptSchemaVersion is the current LandingReceipt shape. Readers must
// tolerate other versions rather than refusing them: the receipts directory is
// append-only, so a directory written by a newer grove is a normal thing for an
// older reader to encounter, and dropping those receipts would silently lose
// exactly the provenance they exist to carry.
const ReceiptSchemaVersion = 1

// receiptsSubdir is the plan-relative directory every landing receipt is
// written into.
const receiptsSubdir = ".artifacts/receipts"

// receiptTimestampLayout keeps filenames both filesystem-safe and
// lexically ordered by time. Millisecond precision means two lands of the same
// plan effectively never collide on a name.
const receiptTimestampLayout = "20060102T150405.000Z"

// LandedRange is the movement of one repository's local default branch: main
// as it stood before the fast-forward advance, and main after it. Every commit
// in (Start, End] is what this land put on main.
type LandedRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// RepoLanding is the provenance record for one repository that actually landed.
// ReviewedHeadSHA is the branch head as it stood *before* the catch-up rebase —
// the commits a reviewer (or a forge PR) would have seen. It is deliberately
// kept alongside LandedRange because a rebase makes them different objects with
// the same content, which is precisely why PR closure cannot be inferred from
// ancestry and must be driven from this receipt instead.
type RepoLanding struct {
	Repo            string      `json:"repo"`
	Branch          string      `json:"branch"`
	Onto            string      `json:"onto,omitempty"`
	Path            string      `json:"path,omitempty"`
	ReviewedHeadSHA string      `json:"reviewed_head_sha"`
	LandedRange     LandedRange `json:"landed_range"`
}

// LandingReceipt is the durable, immutable record of one land operation. It is
// written once, after the advance has already succeeded, and is never edited or
// overwritten — re-landing a plan appends another receipt.
type LandingReceipt struct {
	SchemaVersion int           `json:"schema_version"`
	Plan          string        `json:"plan"`
	PlanDir       string        `json:"plan_dir"`
	WorktreePath  string        `json:"worktree_path,omitempty"`
	RegistryID    string        `json:"registry_id,omitempty"`
	Operation     Operation     `json:"operation"`
	Fingerprint   string        `json:"fingerprint"`
	Timestamp     time.Time     `json:"timestamp"`
	Repos         []RepoLanding `json:"repos"`

	// SourcePath is where this receipt was read from. It is populated by
	// ReadReceipts and never serialized — a receipt does not record its own
	// location.
	SourcePath string `json:"-"`
}

// ReceiptOutcome reports what happened to the receipt for one operation. It is
// strictly informational: a land that advanced main has already happened, so
// nothing here ever unwinds it.
//
//   - Error means the land succeeded but its provenance was NOT durably
//     recorded. Callers must surface this loudly.
//   - Skipped means no receipt was applicable (nothing landed, or the target is
//     not plan-scoped). Not a failure.
//   - Warnings means a receipt was written but a field in it could not be
//     resolved.
type ReceiptOutcome struct {
	Path     string   `json:"path,omitempty"`
	Skipped  string   `json:"skipped,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// ReceiptsDir is the receipts directory for a plan.
func ReceiptsDir(planDir string) string {
	return filepath.Join(filepath.Clean(planDir), receiptsSubdir)
}

// WriteReceipt appends one receipt to a plan's receipts directory and returns
// the file it created. The plan directory must already exist — receipts are a
// plan artifact, and planops will not conjure a plan tree to hold one.
//
// The write is append-only by construction: the name is claimed with O_EXCL and
// a taken name is never reused, so no receipt can be overwritten, not even by a
// caller that replays the same fingerprint.
func WriteReceipt(planDir string, receipt LandingReceipt) (string, error) {
	planDir = strings.TrimSpace(planDir)
	if planDir == "" {
		return "", fmt.Errorf("no plan directory")
	}
	info, err := os.Stat(planDir)
	if err != nil {
		return "", fmt.Errorf("plan directory unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("plan directory %s is not a directory", planDir)
	}
	dir := ReceiptsDir(planDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create receipts directory: %w", err)
	}

	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode receipt: %w", err)
	}
	body = append(body, '\n')

	for attempt := 0; attempt < 64; attempt++ {
		path := filepath.Join(dir, receiptFileName(receipt, attempt))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create receipt file: %w", err)
		}
		if _, err := f.Write(body); err != nil {
			f.Close()
			return "", fmt.Errorf("write receipt file: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close receipt file: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("could not claim an unused receipt name in %s", dir)
}

// receiptFileName renders land-<utc-timestamp>-<shortsha>.json, with a
// disambiguating suffix for the (practically unreachable) case of two receipts
// sharing both a millisecond and a fingerprint.
func receiptFileName(receipt LandingReceipt, attempt int) string {
	stamp := receipt.Timestamp.UTC().Format(receiptTimestampLayout)
	short := shortFingerprint(receipt.Fingerprint)
	name := fmt.Sprintf("land-%s-%s", stamp, short)
	if attempt > 0 {
		name = fmt.Sprintf("%s-%d", name, attempt+1)
	}
	return name + ".json"
}

func shortFingerprint(fingerprint string) string {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return "nofingerprint"
	}
	if len(fingerprint) > 12 {
		return fingerprint[:12]
	}
	return fingerprint
}

// ReadReceipts returns every landing receipt recorded for a plan, oldest first.
// A plan with no receipts is not an error — it is a plan that has not landed.
//
// Files that do not parse as a receipt are skipped rather than failing the
// whole read: an append-only directory that one bad file can render unreadable
// is not durable provenance. Receipts whose schema_version this build does not
// know are still returned, so a newer land stays visible to an older reader.
func ReadReceipts(planDir string) ([]LandingReceipt, error) {
	dir := ReceiptsDir(planDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read receipts directory: %w", err)
	}

	receipts := make([]LandingReceipt, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isReceiptFileName(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var receipt LandingReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			continue
		}
		receipt.SourcePath = path
		receipts = append(receipts, receipt)
	}
	sort.SliceStable(receipts, func(i, j int) bool {
		if !receipts[i].Timestamp.Equal(receipts[j].Timestamp) {
			return receipts[i].Timestamp.Before(receipts[j].Timestamp)
		}
		return receipts[i].SourcePath < receipts[j].SourcePath
	})
	return receipts, nil
}

func isReceiptFileName(name string) bool {
	return strings.HasPrefix(name, "land-") && strings.HasSuffix(name, ".json")
}
