package add

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/flow/pkg/orchestration"
)

// doneWithJob returns a tea.Cmd that emits embed.DoneMsg carrying the
// freshly-built *orchestration.Job as its Result. Hosts intercept the
// DoneMsg to persist the job and refresh their view.
func doneWithJob(job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		return embed.DoneMsg{Result: job}
	}
}

// doneCancelled returns a tea.Cmd that emits embed.DoneMsg with a nil
// Result, signaling that the user dismissed the wizard without
// submitting.
func doneCancelled() tea.Cmd {
	return func() tea.Msg {
		return embed.DoneMsg{Result: nil}
	}
}

// Update routes tea messages to the focused form component after
// handling global wizard concerns (help toggle, mode switching,
// submit / cancel, embed lifecycle messages).
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case embed.SetWorkspaceMsg:
		// Workspace changed under us. The wizard's plan pointer is
		// now stale — close the wizard by emitting a cancel DoneMsg
		// so the host returns to its previous view in the new
		// workspace context.
		return m, doneCancelled()

	case embed.FocusMsg, embed.BlurMsg:
		// The wizard has nothing async to start/stop on focus, so
		// these are no-ops today.
		return m, nil

	case tea.KeyMsg:
		// If help is visible, it consumes all key presses
		if m.helpModel.ShowAll {
			m.helpModel.Toggle()
			return m, nil
		}

		// Check if we're in a text input field that should capture all keys
		inTextInput := !m.unfocused && (m.focusIndex == 0 || m.focusIndex == 4)
		// Check if we're in a list that needs arrow keys
		inList := !m.unfocused && (m.focusIndex == 1 || m.focusIndex == 2 || m.focusIndex == 3)

		// Handle configurable keybindings using key.Matches (these take precedence)
		switch {
		case key.Matches(msg, m.keys.Help):
			m.helpModel.Toggle()
			return m, nil

		case key.Matches(msg, m.keys.Quit):
			// Only quit if not in text input
			if !inTextInput {
				return m, doneCancelled()
			}

		case key.Matches(msg, m.keys.Submit):
			// Save - extract values and emit DoneMsg
			m.extractValues()
			return m, doneWithJob(m.toJob(m.plan))

		case key.Matches(msg, clawToggleKey):
			// Toggle claw — only for interactive_agent
			if selected := m.jobTypeList.SelectedItem(); selected != nil && string(selected.(item)) == "interactive_agent" {
				m.clawEnabled = !m.clawEnabled
			}
			return m, nil

		case key.Matches(msg, m.keys.Next):
			// Tab moves to next field
			m.focusIndex++
			if m.focusIndex > 4 {
				m.focusIndex = 0
			}
			return m.updateFocus(), nil

		case key.Matches(msg, m.keys.Prev):
			// Shift+tab moves to previous field
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = 4
			}
			return m.updateFocus(), nil

		case key.Matches(msg, m.keys.Toggle):
			// Space toggles selection in dependency list
			if m.focusIndex == 3 {
				if selectedItem := m.depList.SelectedItem(); selectedItem != nil {
					if depItem, ok := selectedItem.(dependencyItem); ok {
						m.selectedDeps[depItem.job.ID] = !m.selectedDeps[depItem.job.ID]
					}
				}
				return m, nil
			}

		case key.Matches(msg, m.keys.GoTop):
			if inTextInput {
				break
			}
			if inList {
				switch m.focusIndex {
				case 1:
					m.jobTypeList.Select(0)
				case 2:
					m.templateList.Select(0)
				case 3:
					m.depList.Select(0)
				}
			} else {
				m.focusIndex = 0
				return m.updateFocus(), nil
			}
			return m, nil

		case key.Matches(msg, m.keys.GoBottom):
			if inTextInput {
				break
			}
			if inList {
				switch m.focusIndex {
				case 1:
					m.jobTypeList.Select(len(m.jobTypeList.Items()) - 1)
				case 2:
					m.templateList.Select(len(m.templateList.Items()) - 1)
				case 3:
					m.depList.Select(len(m.depList.Items()) - 1)
				}
			} else {
				m.focusIndex = 4
				return m.updateFocus(), nil
			}
			return m, nil

		case key.Matches(msg, m.keys.PageUp):
			if inList {
				switch m.focusIndex {
				case 1:
					current := m.jobTypeList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.jobTypeList.Select(newIndex)
				case 2:
					current := m.templateList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.templateList.Select(newIndex)
				case 3:
					current := m.depList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.depList.Select(newIndex)
				}
			}
			return m, nil

		case key.Matches(msg, m.keys.PageDown):
			if inList {
				switch m.focusIndex {
				case 1:
					current := m.jobTypeList.Index()
					newIndex := current + 5
					if newIndex >= len(m.jobTypeList.Items()) {
						newIndex = len(m.jobTypeList.Items()) - 1
					}
					m.jobTypeList.Select(newIndex)
				case 2:
					current := m.templateList.Index()
					newIndex := current + 5
					if newIndex >= len(m.templateList.Items()) {
						newIndex = len(m.templateList.Items()) - 1
					}
					m.templateList.Select(newIndex)
				case 3:
					current := m.depList.Index()
					newIndex := current + 5
					if newIndex >= len(m.depList.Items()) {
						newIndex = len(m.depList.Items()) - 1
					}
					m.depList.Select(newIndex)
				}
			}
			return m, nil
		}

		// Handle non-configurable keys via switch on string
		switch msg.String() {
		case "esc", "escape":
			// ESC unfocuses any focused field (text inputs or lists)
			m.unfocused = true
			m.titleInput.Blur()
			m.promptInput.Blur()
			return m, nil

		case "ctrl+c":
			return m, doneCancelled()

		case "down", "j":
			if (!inList || m.unfocused) && !inTextInput {
				m.focusIndex++
				if m.focusIndex > 4 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			}

		case "up", "k":
			if (!inList || m.unfocused) && !inTextInput {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = 4
				}
				return m.updateFocus(), nil
			}

		case "left", "h":
			if m.unfocused && !inTextInput {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = 4
				}
				return m.updateFocus(), nil
			}

		case "right", "l":
			if m.unfocused && !inTextInput {
				m.focusIndex++
				if m.focusIndex > 4 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			}

		case "c":
			// Quick chat setup - only when in NORMAL mode and NOT on a text field
			if m.unfocused && m.focusIndex != 0 && m.focusIndex != 4 {
				for i, listItem := range m.jobTypeList.Items() {
					if string(listItem.(item)) == "chat" {
						m.jobTypeList.Select(i)
						break
					}
				}
				m.templateList = m.buildTemplateList("chat")
				m.slot2IsSkills = false
				for i, listItem := range m.templateList.Items() {
					if string(listItem.(item)) == "chat" {
						m.templateList.Select(i)
						break
					}
				}
				return m, nil
			}

		case "a":
			// Quick agent setup - only when in NORMAL mode and NOT on a text field
			if m.unfocused && m.focusIndex != 0 && m.focusIndex != 4 {
				for i, listItem := range m.jobTypeList.Items() {
					if string(listItem.(item)) == "interactive_agent" {
						m.jobTypeList.Select(i)
						break
					}
				}
				m.templateList = m.buildTemplateList("interactive_agent")
				m.slot2IsSkills = true
				return m, nil
			}

		case ":wq":
			// Vim-style save and quit
			m.extractValues()
			return m, doneWithJob(m.toJob(m.plan))

		case "i":
			// Insert mode - refocus current field (like vim)
			if m.unfocused {
				m.unfocused = false
				return m.updateFocus(), nil
			}

		case "enter":
			if m.focusIndex == 4 {
				// On the last field (prompt), enter confirms the form
				m.extractValues()
				return m, doneWithJob(m.toJob(m.plan))
			} else if inList {
				// For lists, enter confirms selection and moves to next field
				m.unfocused = false
				m.focusIndex++
				if m.focusIndex > 4 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			} else if m.unfocused {
				// If unfocused, enter refocuses current field
				m.unfocused = false
				return m.updateFocus(), nil
			}
		}
	}

	// Delegate to the focused component
	switch m.focusIndex {
	case 0: // Title input
		m.titleInput, cmd = m.titleInput.Update(msg)
	case 1: // Job type list
		prevSelection := m.jobTypeList.SelectedItem()
		m.jobTypeList, cmd = m.jobTypeList.Update(msg)
		// Check if job type selection changed
		newSelection := m.jobTypeList.SelectedItem()
		if prevSelection != newSelection && newSelection != nil {
			selectedJobType := string(newSelection.(item))
			// Switch slot 2 between skills (agent types) and templates (chat/oneshot)
			switch selectedJobType {
			case "interactive_agent", "isolated_agent", "headless_agent":
				m.slot2IsSkills = true
			default:
				m.slot2IsSkills = false
				m.templateList = m.buildTemplateList(selectedJobType)
			}
		}
	case 2: // Slot 2: Skills or Template list
		if m.slot2IsSkills {
			m.skillList, cmd = m.skillList.Update(msg)
		} else {
			m.templateList, cmd = m.templateList.Update(msg)
		}
	case 3: // Dependency list
		m.depList, cmd = m.depList.Update(msg)
	case 4: // Prompt textarea
		m.promptInput, cmd = m.promptInput.Update(msg)
	}

	return m, cmd
}

// updateFocus updates focus state for all components based on the
// current focusIndex and unfocused flag.
func (m Model) updateFocus() Model {
	// Blur all text inputs
	m.titleInput.Blur()
	m.promptInput.Blur()

	// Only focus if not in unfocused state
	if !m.unfocused {
		switch m.focusIndex {
		case 0:
			m.titleInput.Focus()
		case 4:
			m.promptInput.Focus()
		}
	}

	return m
}
