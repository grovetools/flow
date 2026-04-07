package planinit

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"
)

// doneWithRequest returns a tea.Cmd that emits embed.DoneMsg carrying
// the freshly-built *Request as its Result.
func doneWithRequest(req *Request) tea.Cmd {
	return func() tea.Msg {
		return embed.DoneMsg{Result: req}
	}
}

// doneCancelled returns a tea.Cmd that emits embed.DoneMsg with a nil
// Result, signaling that the user dismissed the wizard.
func doneCancelled() tea.Cmd {
	return func() tea.Msg {
		return embed.DoneMsg{Result: nil}
	}
}

// Update routes tea messages to the focused form component after
// handling global wizard concerns.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case embed.SetWorkspaceMsg:
		// Workspace changed under us; cancel the wizard so the host
		// returns to its previous view in the new workspace.
		return m, doneCancelled()

	case embed.FocusMsg, embed.BlurMsg:
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help, _ = m.help.Update(msg)

	case tea.KeyMsg:
		if m.help.ShowAll {
			var helpCmd tea.Cmd
			m.help, helpCmd = m.help.Update(msg)
			return m, helpCmd
		}

		// Figure out whether we're in a field that wants to
		// consume keys verbatim.
		inTextInput := false
		if !m.unfocused {
			if m.currentScreen == MainScreen && m.focusIndex == 0 {
				inTextInput = true
			} else if m.currentScreen == AdvancedScreen && m.focusIndex >= 1 {
				inTextInput = true
			}
		}
		inList := !m.unfocused && m.currentScreen == MainScreen && (m.focusIndex == 1 || m.focusIndex == 2)

		// Navigate to advanced screen (only from main screen; and
		// only when not actively typing in a text input).
		if key.Matches(msg, m.keys.ToggleAdvanced) {
			if m.currentScreen == MainScreen && (!inTextInput || m.unfocused) {
				m.currentScreen = AdvancedScreen
				m.focusIndex = 0
				m.unfocused = false
				return m.updateFocus(), nil
			}
		}

		switch msg.String() {
		case "esc", "escape":
			if m.currentScreen == AdvancedScreen {
				m.currentScreen = MainScreen
				m.focusIndex = 0
				m.unfocused = false
				return m.updateFocus(), nil
			}
			m.unfocused = true
			m.nameInput.Blur()
			m.worktreeInput.Blur()
			m.extractFromInput.Blur()
			m.noteTargetFileInput.Blur()
			return m, nil

		case "i":
			if m.unfocused {
				m.unfocused = false
				return m.updateFocus(), nil
			}

		case "?":
			m.help.Toggle()
			return m, nil

		case "ctrl+c":
			return m, doneCancelled()

		case "b":
			if m.currentScreen == AdvancedScreen && m.unfocused {
				m.currentScreen = MainScreen
				m.focusIndex = 0
				return m.updateFocus(), nil
			}

		case "q":
			if !inTextInput || m.unfocused {
				return m, doneCancelled()
			}

		case "tab":
			m.focusIndex++
			maxIndex := m.getMaxFocusIndex()
			if m.focusIndex > maxIndex {
				m.focusIndex = 0
			}
			if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
				m.highestFocusIndex = m.focusIndex
			}
			return m.updateFocus(), nil

		case "shift+tab":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = m.getMaxFocusIndex()
			}
			if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
				m.highestFocusIndex = m.focusIndex
			}
			return m.updateFocus(), nil

		case "h":
			if m.unfocused && !inTextInput {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = m.getMaxFocusIndex()
				}
				if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case "l":
			if m.unfocused && !inTextInput {
				m.focusIndex++
				if m.focusIndex > m.getMaxFocusIndex() {
					m.focusIndex = 0
				}
				if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case " ":
			if m.currentScreen == MainScreen {
				switch m.focusIndex {
				case 3:
					m.withWorktree = !m.withWorktree
					if m.withWorktree {
						m.worktreeInput.SetValue("")
					}
					return m, nil
				case 4:
					m.openSession = !m.openSession
					return m, nil
				}
			} else if m.currentScreen == AdvancedScreen {
				if m.focusIndex == 0 {
					m.runInit = !m.runInit
					return m, nil
				}
			}

		case "enter":
			if inList {
				m.unfocused = false
				m.focusIndex++
				if m.focusIndex > m.getMaxFocusIndex() {
					m.focusIndex = 0
				}
				if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			} else if m.unfocused {
				m.unfocused = false
				return m.updateFocus(), nil
			} else {
				if m.nameInput.Value() == "" {
					m.err = fmt.Errorf("plan name cannot be empty")
					return m, nil
				}
				return m, doneWithRequest(m.toRequest())
			}
		}
	}

	// Delegate to the focused component only if in insert mode.
	if !m.unfocused {
		switch m.currentScreen {
		case MainScreen:
			switch m.focusIndex {
			case 0:
				m.nameInput, cmd = m.nameInput.Update(msg)
			case 1:
				prevSelection := m.recipeList.SelectedItem()
				m.recipeList, cmd = m.recipeList.Update(msg)
				newSelection := m.recipeList.SelectedItem()

				if prevSelection != newSelection && newSelection != nil {
					if it, ok := newSelection.(item); ok {
						selectedRecipeName := string(it)
						defaultTarget := getDefaultNoteTargetFile(selectedRecipeName, m.getRecipeCmd)
						m.noteTargetFileInput.SetValue(defaultTarget)
						if defaultTarget != "" {
							m.noteTargetFileInput.Placeholder = defaultTarget
						} else {
							m.noteTargetFileInput.Placeholder = "job-filename.md"
						}
					}
				}
			case 2:
				m.modelList, cmd = m.modelList.Update(msg)
			}
		case AdvancedScreen:
			switch m.focusIndex {
			case 0:
				// Run Init Actions checkbox - no text input
			case 1:
				if !m.withWorktree {
					m.worktreeInput, cmd = m.worktreeInput.Update(msg)
				}
			case 2:
				m.extractFromInput, cmd = m.extractFromInput.Update(msg)
			case 3:
				m.noteTargetFileInput, cmd = m.noteTargetFileInput.Update(msg)
			}
		}
	}

	return m, cmd
}
