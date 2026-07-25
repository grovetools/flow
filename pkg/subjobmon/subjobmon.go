// Package subjobmon derives and reconciles Pi Flow child report events.
package subjobmon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/flow/pkg/orchestration"
)

const MaxReportBytes = 64 << 10

type finalReport struct {
	SchemaVersion int               `json:"schema_version"`
	ChildJobID    string            `json:"child_job_id"`
	ParentJobID   string            `json:"parent_job_id"`
	Summary       string            `json:"summary"`
	Artifacts     map[string]string `json:"artifacts"`
	CreatedAt     string            `json:"created_at"`
}

type Output struct {
	SchemaVersion int                     `json:"schema_version"`
	Kind          string                  `json:"kind"`
	PlanKey       string                  `json:"plan_key"`
	ParentJobID   string                  `json:"parent_job_id"`
	ChildJobID    string                  `json:"child_job_id"`
	ChildTitle    string                  `json:"child_title"`
	ReportSHA256  string                  `json:"report_sha256,omitempty"`
	ReportSummary string                  `json:"report_summary,omitempty"`
	JobStatus     orchestration.JobStatus `json:"job_status,omitempty"`
	ObservedAt    time.Time               `json:"observed_at"`
}

func CanonicalPlan(dir string) (string, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(filepath.Join(real, ".grove-plan.yml"))
	if err != nil || info.IsDir() {
		return "", "", fmt.Errorf("not a Flow plan: %s", real)
	}
	h := sha256.Sum256([]byte(real))
	return real, hex.EncodeToString(h[:]), nil
}

func readFinalReport(planDir string, child *orchestration.Job) ([]byte, *finalReport, error) {
	path := filepath.Join(planDir, ".artifacts", child.ID, "final-report.json")
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxReportBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > MaxReportBytes {
		return nil, nil, fmt.Errorf("final report exceeds %d bytes", MaxReportBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r finalReport
	if err := dec.Decode(&r); err != nil {
		return nil, nil, fmt.Errorf("invalid final report: %w", err)
	}
	if r.SchemaVersion != 1 || r.ChildJobID != child.ID || r.ParentJobID != child.ParentJobID || r.Summary == "" || r.CreatedAt == "" || r.Artifacts == nil {
		return nil, nil, errors.New("final report schema or lineage mismatch")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, nil, errors.New("invalid trailing report data")
	}
	return data, &r, nil
}

func BuildEvent(planDir string, child *orchestration.Job, kind models.SubjobEventKind) (*models.SubjobEvent, error) {
	canonical, key, err := CanonicalPlan(planDir)
	if err != nil {
		return nil, err
	}
	if child == nil || child.ParentJobID == "" {
		return nil, errors.New("job is not a parent-owned child")
	}
	data, _, err := readFinalReport(canonical, child)
	if err != nil {
		return nil, err
	}
	if kind == models.SubjobJoined && child.Status != orchestration.JobStatusCompleted {
		return nil, errors.New("joined publication requires completed Flow status")
	}
	if kind != models.SubjobReportReady && kind != models.SubjobJoined {
		return nil, errors.New("invalid subjob event kind")
	}
	h := sha256.Sum256(data)
	return &models.SubjobEvent{SchemaVersion: 1, Kind: kind, PlanKey: key, ParentJobID: child.ParentJobID, ChildJobID: child.ID, ReportSHA256: hex.EncodeToString(h[:]), Timestamp: time.Now().UTC()}, nil
}

type Client interface {
	PublishSubjobEvent(context.Context, models.SubjobEvent) error
	GetSubjobSnapshot(context.Context, string, string) (*models.SubjobSnapshot, error)
	StreamState(context.Context) (<-chan daemon.StateUpdate, error)
}

func terminal(s orchestration.JobStatus) bool {
	return s == orchestration.JobStatusCompleted || s == orchestration.JobStatusFailed || s == orchestration.JobStatusAbandoned
}

// Reconcile re-reads disk truth, repairs daemon state, and returns actionable records.
func Reconcile(ctx context.Context, client Client, planDir, parentID string) ([]Output, error) {
	canonical, key, err := CanonicalPlan(planDir)
	if err != nil {
		return nil, err
	}
	plan, err := orchestration.LoadPlan(canonical)
	if err != nil {
		return nil, err
	}
	if _, ok := plan.GetJobByID(parentID); !ok {
		return nil, fmt.Errorf("parent job %s not found", parentID)
	}
	snap, err := client.GetSubjobSnapshot(ctx, key, parentID)
	if err != nil {
		return nil, err
	}
	var out []Output
	for _, child := range plan.Jobs {
		if child.ParentJobID != parentID {
			continue
		}
		ready, reportErr := BuildEvent(canonical, child, models.SubjobReportReady)
		var reportSummary string
		if reportErr == nil {
			_, report, readErr := readFinalReport(canonical, child)
			if readErr != nil {
				reportErr = readErr
			} else {
				reportSummary = report.Summary
			}
		}
		state := snap.Reports[child.ID]
		if child.Status == orchestration.JobStatusCompleted {
			if reportErr == nil && (state == nil || state.State != models.SubjobJoined || state.ReportSHA256 != ready.ReportSHA256) {
				joined := *ready
				joined.Kind = models.SubjobJoined
				_ = client.PublishSubjobEvent(ctx, joined)
			}
			continue
		}
		if reportErr == nil {
			if state == nil || state.State != models.SubjobReportReady || state.ReportSHA256 != ready.ReportSHA256 {
				if err := client.PublishSubjobEvent(ctx, *ready); err != nil {
					return nil, err
				}
			}
			out = append(out, Output{SchemaVersion: 1, Kind: "report_ready", PlanKey: key, ParentJobID: parentID, ChildJobID: child.ID, ChildTitle: child.Title, ReportSHA256: ready.ReportSHA256, ReportSummary: reportSummary, ObservedAt: time.Now().UTC()})
		} else if terminal(child.Status) {
			out = append(out, Output{SchemaVersion: 1, Kind: "terminal_without_report", PlanKey: key, ParentJobID: parentID, ChildJobID: child.ID, ChildTitle: child.Title, JobStatus: child.Status, ObservedAt: time.Now().UTC()})
		}
	}
	return out, nil
}
