package status

import (
	"strconv"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// fieldKind selects how the schema-driven field editor renders and commits a
// single mutable job-config field.
type fieldKind int

const (
	fieldEnum   fieldKind = iota // a fixed option list (status, type, provider, …)
	fieldToggle                  // a bool flipped in place — never opens the editor
	fieldText                    // a free-form text input (model, effort)
)

// fieldDescriptor declares one editable frontmatter field: the frontmatter Key
// it writes, a human Label, its editor Kind, the enum Options (fieldEnum only),
// and a Current accessor that reads the job's present value as a string (used to
// preselect the enum cursor / prefill the text input, and to compute a toggle's
// negation). This hand-maintained table is the single source of truth for the
// Change (c…) namespace's field editor; the reflection-based schema-drift guard
// in fieldeditor_test.go asserts the enum Options still match orchestration.Job's
// jsonschema `enum=` tags where those exist.
type fieldDescriptor struct {
	Key     string
	Label   string
	Kind    fieldKind
	Options []string
	Current func(*orchestration.Job) string
}

// jobFields is the descriptor table. It intentionally excludes the bespoke
// mutators — cc (completed, side-effectful CompleteJob), cn (rename, file
// rename via RenameJob), cd (deps, multi-select EditingDeps editor) — which keep
// their own flows. Read-only/derived fields (id, created_at, …) and
// execution-identity fields (worktree/branch/repository, skill*, rules_file,
// channels, inline, …) are excluded by judgment; adding another scalar toggle or
// enum later is a single descriptor line, which is the point of this design.
var jobFields = []fieldDescriptor{
	{
		Key:   "status",
		Label: "Status",
		Kind:  fieldEnum,
		Options: []string{
			string(orchestration.JobStatusPending),
			string(orchestration.JobStatusTodo),
			string(orchestration.JobStatusHold),
			string(orchestration.JobStatusRunning),
			string(orchestration.JobStatusCompleted),
			string(orchestration.JobStatusFailed),
			string(orchestration.JobStatusBlocked),
			string(orchestration.JobStatusNeedsReview),
			string(orchestration.JobStatusAbandoned),
			string(orchestration.JobStatusInterrupted),
			string(orchestration.JobStatusOrphaned),
		},
		Current: func(j *orchestration.Job) string { return string(j.Status) },
	},
	{
		Key:   "type",
		Label: "Type",
		Kind:  fieldEnum,
		Options: []string{
			string(orchestration.JobTypeShell),
			string(orchestration.JobTypeOneshot),
			string(orchestration.JobTypeChat),
			string(orchestration.JobTypeAgent),
			string(orchestration.JobTypeInteractiveAgent),
			string(orchestration.JobTypeIsolatedAgent),
			string(orchestration.JobTypeHeadlessAgent),
			string(orchestration.JobTypeGenerateRecipe),
			string(orchestration.JobTypeFile),
		},
		Current: func(j *orchestration.Job) string { return string(j.Type) },
	},
	{
		Key:     "template",
		Label:   "Template",
		Kind:    fieldEnum,
		Options: []string{"", "agent-xml", "agent-run", "agent-from-chat", "chat"},
		Current: func(j *orchestration.Job) string { return j.Template },
	},
	{
		Key:     "model",
		Label:   "Model",
		Kind:    fieldText,
		Current: func(j *orchestration.Job) string { return j.Model },
	},
	{
		Key:     "provider",
		Label:   "Provider",
		Kind:    fieldEnum,
		Options: []string{"claude", "codex", "opencode", "pi"},
		Current: func(j *orchestration.Job) string { return j.Provider },
	},
	{
		Key:     "effort",
		Label:   "Effort",
		Kind:    fieldText,
		Current: func(j *orchestration.Job) string { return j.Effort },
	},
	{
		Key:     "responder",
		Label:   "Responder",
		Kind:    fieldEnum,
		Options: []string{"oracle", "agent"},
		Current: func(j *orchestration.Job) string { return j.Responder },
	},
	{
		Key:     "cache_ttl",
		Label:   "Cache TTL",
		Kind:    fieldEnum,
		Options: []string{"5m", "1h"},
		Current: func(j *orchestration.Job) string { return j.CacheTTL },
	},
	{
		Key:     "cache_layout",
		Label:   "Cache Layout",
		Kind:    fieldEnum,
		Options: []string{"ladder", "stream"},
		Current: func(j *orchestration.Job) string { return j.CacheLayout },
	},
	{
		Key:     "memory",
		Label:   "Memory",
		Kind:    fieldToggle,
		Current: func(j *orchestration.Job) string { return strconv.FormatBool(j.IsMemoryEnabled()) },
	},
	{
		Key:     "auto_complete",
		Label:   "Auto Complete",
		Kind:    fieldToggle,
		Current: func(j *orchestration.Job) string { return strconv.FormatBool(j.AutoComplete) },
	},
}

// fieldDescriptorByKey looks up a descriptor by its frontmatter key.
func fieldDescriptorByKey(key string) (fieldDescriptor, bool) {
	for _, d := range jobFields {
		if d.Key == key {
			return d, true
		}
	}
	return fieldDescriptor{}, false
}

// fieldEditorState is the single state machine that replaced the three bespoke
// ShowXPicker bool+cursor pairs. desc is the field being edited; cursor indexes
// desc.Options for a fieldEnum; input drives a fieldText. Held as a pointer on
// Model so a nil value means "editor closed".
type fieldEditorState struct {
	desc   fieldDescriptor
	cursor int
	input  textinput.Model
}

// targetJobs resolves the jobs an action applies to: every multi-selected job
// when the selection is non-empty, otherwise the single job under the cursor.
// This extracts the "collect selected jobs or current job" loop that was
// copy-pasted across the three picker Update blocks.
func (m *Model) targetJobs() []*orchestration.Job {
	if len(m.Selected) > 0 {
		var jobs []*orchestration.Job
		for id := range m.Selected {
			for _, job := range m.Jobs {
				if job.ID == id {
					jobs = append(jobs, job)
					break
				}
			}
		}
		return jobs
	}
	if job := m.CurrentJob(); job != nil {
		return []*orchestration.Job{job}
	}
	return nil
}

// openFieldEditor opens the schema-driven editor for the field with the given
// frontmatter key, preselecting the current value (enum) or prefilling it
// (text). It is a no-op when there is no target job, when the key is unknown, or
// when the descriptor is a toggle (toggles dispatch directly via toggleJobField
// and never open the editor). The command returned starts the text-input cursor
// blink for a fieldText.
func (m *Model) openFieldEditor(key string) tea.Cmd {
	desc, ok := fieldDescriptorByKey(key)
	if !ok || desc.Kind == fieldToggle {
		return nil
	}
	target := m.CurrentJob()
	if target == nil && len(m.Selected) == 0 {
		return nil
	}
	current := ""
	if target != nil {
		current = desc.Current(target)
	}

	st := &fieldEditorState{desc: desc}
	switch desc.Kind {
	case fieldEnum:
		for i, opt := range desc.Options {
			if opt == current {
				st.cursor = i
				break
			}
		}
		m.fieldEditor = st
		return nil
	case fieldText:
		ti := textinput.New()
		ti.SetValue(current)
		ti.CursorEnd()
		ti.Focus()
		ti.CharLimit = 200
		ti.Width = 50
		st.input = ti
		m.fieldEditor = st
		return textinput.Blink
	}
	return nil
}

// updateFieldEditor drives the open field editor: enum navigation (up/k, down/j,
// enter to commit, esc to cancel) and text entry (enter to commit, esc to
// cancel, everything else forwarded to the textinput). On commit it resolves
// targets via targetJobs() and dispatches setJobFieldCmd + refreshPlan, then
// closes the editor. Callers guard with `m.fieldEditor != nil`.
func (m Model) updateFieldEditor(msg tea.KeyMsg) (Model, tea.Cmd) {
	desc := m.fieldEditor.desc

	if desc.Kind == fieldText {
		switch msg.String() {
		case "enter":
			value := m.fieldEditor.input.Value()
			targets := m.targetJobs()
			m.fieldEditor = nil
			return m.commitFieldValue(targets, desc.Key, value)
		case "esc", "ctrl+c":
			m.fieldEditor = nil
			return m, nil
		default:
			var cmd tea.Cmd
			m.fieldEditor.input, cmd = m.fieldEditor.input.Update(msg)
			return m, cmd
		}
	}

	// fieldEnum
	switch msg.String() {
	case "up", "k":
		if m.fieldEditor.cursor > 0 {
			m.fieldEditor.cursor--
		}
		return m, nil
	case "down", "j":
		if m.fieldEditor.cursor < len(desc.Options)-1 {
			m.fieldEditor.cursor++
		}
		return m, nil
	case "enter":
		value := desc.Options[m.fieldEditor.cursor]
		targets := m.targetJobs()
		m.fieldEditor = nil
		return m.commitFieldValue(targets, desc.Key, value)
	case "esc", "ctrl+c", "q", "b":
		m.fieldEditor = nil
		return m, nil
	default:
		// Any other key while the editor is open — consume it.
		return m, nil
	}
}

// commitFieldValue dispatches the generic setter for a resolved target set and
// surfaces the mid-run hint when any target is completed/running (config edits
// only take effect on the next run/resume).
func (m Model) commitFieldValue(targets []*orchestration.Job, key string, value any) (Model, tea.Cmd) {
	if len(targets) == 0 {
		return m, nil
	}
	if hint := midRunEditHint(targets); hint != "" {
		m.StatusSummary = hint
	}
	return m, tea.Sequence(
		setJobFieldCmd(targets, key, value),
		refreshPlan(m.PlanDir),
	)
}

// toggleJobField flips a bool config field (memory / auto_complete) in place and
// commits it directly — toggles never open the editor. It negates the current
// value of the cursor job (the first target) and applies the same negated value
// to all targets, matching the multi-select semantics of the enum/text editor.
func (m Model) toggleJobField(key string) (Model, tea.Cmd) {
	desc, ok := fieldDescriptorByKey(key)
	if !ok || desc.Kind != fieldToggle {
		return m, nil
	}
	targets := m.targetJobs()
	if len(targets) == 0 {
		return m, nil
	}
	cur, _ := strconv.ParseBool(desc.Current(targets[0]))
	newVal := !cur
	if hint := midRunEditHint(targets); hint != "" {
		m.StatusSummary = hint
	}
	return m, tea.Sequence(
		setJobFieldCmd(targets, key, newVal),
		refreshPlan(m.PlanDir),
	)
}

// midRunEditHint returns a one-line note when any target job is completed or
// running: config fields (model/provider/effort/responder/cache_*/toggles) only
// affect a future run, so the edit is silently allowed but a no-op until the job
// is re-run or resumed. Empty when every target is idle/pending.
func midRunEditHint(targets []*orchestration.Job) string {
	for _, j := range targets {
		if j.Status == orchestration.JobStatusCompleted || j.Status == orchestration.JobStatusRunning {
			return "Config change takes effect on next run/resume."
		}
	}
	return ""
}
