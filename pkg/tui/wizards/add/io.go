package add

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
	skillservice "github.com/grovetools/skills/pkg/service"
	"github.com/grovetools/skills/pkg/skills"
)

// item is a simple string list entry for the job-type, skill-none,
// and template lists.
type item string

func (i item) FilterValue() string { return string(i) }

// dependencyItem represents a job that can be selected as a dependency.
type dependencyItem struct {
	job *orchestration.Job
}

func (d dependencyItem) FilterValue() string { return d.job.Filename + " " + d.job.Title }
func (d dependencyItem) Title() string       { return d.job.Filename }
func (d dependencyItem) Description() string { return d.job.Title }

// skillItem represents a skill in the picker list.
type skillItem struct {
	name        string
	description string
	authorized  bool
}

func (s skillItem) FilterValue() string { return s.name }

// itemDelegate renders item and skillItem entries in a list.Model.
type itemDelegate struct{}

func (d itemDelegate) Height() int                               { return 1 }
func (d itemDelegate) Spacing() int                              { return 0 }
func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	var str string
	cursor := "  "
	if index == m.Index() {
		cursor = theme.DefaultTheme.Highlight.Render(theme.IconArrow + " ")
	}

	switch i := listItem.(type) {
	case item:
		str = fmt.Sprintf("%s%s", cursor, i)
	case skillItem:
		authBadge := theme.DefaultTheme.Muted.Render("○")
		if i.authorized {
			authBadge = theme.DefaultTheme.Success.Render("✓")
		}
		str = fmt.Sprintf("%s%s %s", cursor, i.name, authBadge)
	default:
		return
	}

	fmt.Fprint(w, str)
}

// dependencyDelegate handles rendering for dependency items with checkboxes.
type dependencyDelegate struct {
	selectedDeps *map[string]bool
}

func (d dependencyDelegate) Height() int                               { return 1 }
func (d dependencyDelegate) Spacing() int                              { return 0 }
func (d dependencyDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d dependencyDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	depItem, ok := listItem.(dependencyItem)
	if !ok {
		return
	}

	var str string
	cursor := "  "
	if index == m.Index() {
		cursor = theme.DefaultTheme.Highlight.Render(theme.IconArrow + " ")
	}

	checkbox := theme.IconUnselect
	if (*d.selectedDeps)[depItem.job.ID] {
		checkbox = theme.IconSelect
	}

	// Format the display text
	displayText := fmt.Sprintf("%s (%s)", depItem.job.Filename, depItem.job.Title)

	// Truncate to prevent wrapping (account for cursor and checkbox = 6 chars)
	maxWidth := 35 // Conservative width for 45-char list minus cursor/checkbox
	if len(displayText) > maxWidth {
		displayText = displayText[:maxWidth-3] + "..."
	}

	str = fmt.Sprintf("%s%s %s", cursor, checkbox, displayText)

	if (*d.selectedDeps)[depItem.job.ID] {
		fmt.Fprint(w, theme.DefaultTheme.Bold.Render(str))
	} else {
		fmt.Fprint(w, theme.DefaultTheme.Muted.Render(str))
	}
}

// buildSkillList creates a list of available skills for the picker.
func buildSkillList(svc *skillservice.Service, node *workspace.WorkspaceNode) list.Model {
	var items []list.Item

	// Always add a "none" option first
	items = append(items, item("none"))

	// Get all discoverable skill sources (works with nil service for project/ecosystem skills)
	sources := skills.ListSkillSources(svc, node)
	for name, src := range sources {
		// Skip sub-skills (nested paths contain "/")
		if strings.Contains(src.RelPath, "/") {
			continue
		}
		// Get description from metadata
		desc := ""
		if loaded, err := skills.LoadSkillFromSource(name, src); err == nil {
			if content, ok := loaded.Files["SKILL.md"]; ok {
				if meta, parseErr := skills.ParseSkillFrontmatter(content); parseErr == nil {
					desc = meta.Description
				}
			}
		}
		items = append(items, skillItem{
			name:        name,
			description: desc,
			authorized:  true, // All discovered skills are accessible
		})
	}

	skillList := list.New(items, itemDelegate{}, 20, 7)
	skillList.Title = ""
	skillList.SetShowTitle(false)
	skillList.SetShowStatusBar(false)
	skillList.SetFilteringEnabled(true)
	skillList.SetShowHelp(false)
	skillList.SetShowPagination(true)
	skillList.FilterInput.Prompt = " "
	skillList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	skillList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	return skillList
}

// buildTemplateList creates a filtered template list based on the selected job type.
func (m Model) buildTemplateList(jobType string) list.Model {
	// Filter templates based on job type
	var filteredTemplates []*orchestration.JobTemplate

	for _, t := range m.allTemplates {
		// Map job types to template types.
		// Agent types share "Agent templates"; prompt types share "Prompt templates".
		includeTemplate := false

		switch jobType {
		case "interactive_agent", "isolated_agent", "headless_agent", "agent":
			includeTemplate = t.Type == "agent"
		case "oneshot", "chat":
			includeTemplate = t.Type == "oneshot"
		case "shell", "file":
			includeTemplate = false
		default:
			includeTemplate = true
		}

		if includeTemplate {
			filteredTemplates = append(filteredTemplates, t)
		}
	}

	// Build list items
	templateItems := make([]list.Item, len(filteredTemplates)+1)
	templateItems[0] = item("none") // Add a 'none' option
	for i, t := range filteredTemplates {
		templateItems[i+1] = item(t.Name)
	}

	templateList := list.New(templateItems, itemDelegate{}, 20, 7)
	templateList.Title = ""
	templateList.SetShowTitle(false)
	templateList.SetShowStatusBar(false)
	templateList.SetFilteringEnabled(true)
	templateList.SetShowHelp(false)
	templateList.SetShowPagination(true)
	templateList.FilterInput.Prompt = " "
	templateList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	templateList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	return templateList
}

// extractValues pulls the final values from all form components into
// the Model's job* fields. Called before toJob on submit.
func (m *Model) extractValues() {
	m.jobTitle = m.titleInput.Value()

	// Get selected job type
	if selected := m.jobTypeList.SelectedItem(); selected != nil {
		m.jobType = string(selected.(item))
	}

	// Get dependencies from selected items
	var deps []string
	for jobID, selected := range m.selectedDeps {
		if selected {
			// Find the corresponding job to get its filename
			for _, job := range m.plan.Jobs {
				if job.ID == jobID {
					deps = append(deps, job.Filename)
					break
				}
			}
		}
	}
	m.jobDependencies = deps

	// Get selected template or skill based on slot 2 mode
	if m.slot2IsSkills {
		if selected := m.skillList.SelectedItem(); selected != nil {
			switch s := selected.(type) {
			case skillItem:
				m.jobSkill = s.name
				// Resolve skill_sequence from skill metadata
				if m.skillService != nil {
					if loaded, err := skills.LoadSkillBypassingAccessWithService(m.skillService, m.workspaceNode, s.name); err == nil {
						if content, ok := loaded.Files["SKILL.md"]; ok {
							if meta, err := skills.ParseSkillFrontmatter(content); err == nil {
								m.jobSkillSequence = meta.SkillSequence
							}
						}
					}
				}
			case item:
				// "none" selected
				m.jobSkill = ""
			}
		}
	} else {
		if selected := m.templateList.SelectedItem(); selected != nil {
			template := string(selected.(item))
			if template != "none" {
				m.jobTemplate = template
			}
		}
	}

	m.jobPrompt = m.promptInput.Value()
}

// toJob converts the captured form values into a new Job struct.
// The caller is responsible for persisting the job to the plan via
// orchestration.AddJob.
func (m Model) toJob(plan *orchestration.Plan) *orchestration.Job {
	// Generate job ID from title
	jobID := orchestration.GenerateUniqueJobID(plan, m.jobTitle)

	// Default job type if none selected
	jobType := m.jobType
	if jobType == "" {
		jobType = "agent"
	}

	// The prompt body is simply the user's input. The executor will load the template.
	promptBody := m.jobPrompt

	jobStatus := orchestration.JobStatusPending
	if jobType == "chat" {
		jobStatus = orchestration.JobStatusPendingUser
	} else if jobType == "file" {
		jobStatus = orchestration.JobStatusCompleted
	}

	job := &orchestration.Job{
		ID:            jobID,
		Title:         m.jobTitle,
		Type:          orchestration.JobType(jobType),
		Status:        jobStatus,
		DependsOn:     m.jobDependencies,
		PromptBody:    promptBody,
		Template:      m.jobTemplate,
		Skill:         m.jobSkill,
		SkillSequence: m.jobSkillSequence,
	}

	if m.clawEnabled {
		job.Channels = []string{"signal"}
		job.Autonomous = &models.AutonomousConfig{
			Enabled:     true,
			IdleMinutes: 15,
		}
	}

	return job
}
