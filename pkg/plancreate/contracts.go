// Package plancreate defines the read-only validation and mutation-review
// contracts for Flow-owned plan creation. Execution remains in `flow plan init`.
package plancreate

import "time"

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Check struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	OK       bool     `json:"ok"`
	Detail   string   `json:"detail"`
}

type ValidationReport struct {
	CheckedAt time.Time `json:"checked_at"`
	Checks    []Check   `json:"checks"`
}

func (r ValidationReport) Valid() bool {
	for _, check := range r.Checks {
		if !check.OK && check.Severity == SeverityError {
			return false
		}
	}
	return true
}

type MutationStep struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Reversible bool   `json:"reversible"`
}

type MutationManifest struct {
	Steps []MutationStep `json:"steps"`
}

type Request struct {
	TargetWorkspace string
	PlansDir        string
	PlanName        string
	WorktreeName    string
	BaseBranch      string
	Anchor          string
	Layout          string
	RunInitHooks    bool
	Force           bool
}
