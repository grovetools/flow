package workflowmon

import "github.com/grovetools/core/pkg/workflows"

// The script meta parser moved to core/pkg/workflows so agentlogs (which
// cannot import flow) can share it. These aliases keep workflowmon's
// existing API surface intact.

// ScriptMeta is the display-relevant subset of a workflow script's
// `export const meta = {...}` block.
type ScriptMeta = workflows.ScriptMeta

// PhaseMeta is one entry of the meta block's phases array.
type PhaseMeta = workflows.PhaseMeta

var (
	// ParseScriptMeta extracts the meta block from a persisted workflow script.
	ParseScriptMeta = workflows.ParseScriptMeta
	// FindRunScript locates the persisted orchestration script for a run.
	FindRunScript = workflows.FindRunScript
	// LoadRunMeta finds and parses the script meta for a run.
	LoadRunMeta = workflows.LoadRunMeta
)
