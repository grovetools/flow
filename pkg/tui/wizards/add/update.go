package add

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/keymap"

	"github.com/grovetools/flow/pkg/orchestration"
)

// goTop implements the GoTop action: jump to the first item of the current
// list, or the first visible slot otherwise. Shared by the "home" key and the
// hand-rolled "gg" chord (see the Update key switch).
func (m Model) goTop(inList bool) (tea.Model, tea.Cmd) {
	if inList {
		// NOTE: the slot-2 case operates on templateList even when the skills
		// list is shown, preserving the exact pre-refactor behavior
		// (keystroke-identical). Do not substitute activeList() here — that
		// would change what gg/G move on the skills slot.
		switch m.currentSlot().id {
		case slotJobType:
			m.jobTypeList.Select(0)
		case slotTemplateOrSkill:
			m.templateList.Select(0)
		case slotDeps:
			m.depList.Select(0)
		}
		return m, nil
	}
	m.focusIndex = m.firstVisibleSlot()
	return m.updateFocus(), nil
}

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
		inTextInput := !m.unfocused && m.currentSlotKind() == slotText
		// Check if we're in a list that needs arrow keys
		inList := !m.unfocused && m.currentSlotKind() == slotList

		// Also treat an active list filter input as text input so
		// single-char keys (like q) are typed into the filter
		// instead of firing navigation/quit actions.
		if !inTextInput && inList {
			inTextInput = m.isListFiltering()
		}

		// ── Mode guard + chord seam ──────────────────────────────────────
		// The guard runs BEFORE ProcessChord (sign-off E3): the c…/t…
		// namespaces arm only outside a text field, so typing "c" or "t"
		// into the title/prompt still inserts the character. An already-armed
		// chord keeps its continuation key (Armed()), so a chord started in
		// nav mode always completes. The hand-rolled "gg" timer below is left
		// alone — no namespace begins with "g", so the two never interact.
		if !inTextInput || m.whichKey.Armed() {
			res, matched, chordCmd := m.whichKey.ProcessChord(msg)
			switch res {
			case keymap.ChordMatched:
				// Re-synthesize the resolved chord's canonical key so the
				// switch below resolves it via key.Matches — but only when the
				// pressed key is not already one of the binding's keys, or a
				// binding that retains a flat alternate alongside its chord
				// would have the flat press rewritten to the chord and lost.
				if len(matched.Keys()) > 0 && !key.Matches(msg, matched) {
					msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(matched.Keys()[0])}
				}
			case keymap.ChordPending:
				return m, chordCmd
			case keymap.ChordConsumed:
				return m, nil
			case keymap.ChordNone:
				// Not a chord — fall through unchanged.
			}
		}

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

		case key.Matches(msg, m.keys.ToggleClaw):
			// Toggle claw — only for interactive_agent
			if selected := m.jobTypeList.SelectedItem(); selected != nil && string(selected.(item)) == "interactive_agent" {
				m.clawEnabled = !m.clawEnabled
			}
			return m, nil

		case key.Matches(msg, m.keys.Next):
			// Tab moves to next field
			m.focusIndex = m.nextVisibleSlot(m.focusIndex)
			return m.updateFocus(), nil

		case key.Matches(msg, m.keys.Prev):
			// Shift+tab moves to previous field
			m.focusIndex = m.prevVisibleSlot(m.focusIndex)
			return m.updateFocus(), nil

		case key.Matches(msg, m.keys.Toggle):
			// Space toggles selection in dependency list
			if m.currentSlot().id == slotDeps {
				if selectedItem := m.depList.SelectedItem(); selectedItem != nil {
					if depItem, ok := selectedItem.(dependencyItem); ok {
						m.selectedDeps[depItem.job.ID] = !m.selectedDeps[depItem.job.ID]
					}
				}
				return m, nil
			}

		case !inTextInput && msg.String() == "g":
			// "gg" chord (go to top). The wizard has no Sequence engine, so
			// hand-roll the two-press timer here (mirrors grove-config); this
			// is what makes advertising "gg" on GoTop truthful.
			if time.Since(m.lastGPress) < 500*time.Millisecond {
				m.lastGPress = time.Time{}
				return m.goTop(inList)
			}
			m.lastGPress = time.Now()
			return m, nil

		case key.Matches(msg, m.keys.GoTop):
			if inTextInput {
				break
			}
			return m.goTop(inList)

		case key.Matches(msg, m.keys.GoBottom):
			if inTextInput {
				break
			}
			if inList {
				// See the GoTop note: slot-2 stays on templateList to
				// preserve pre-refactor keystroke behavior.
				switch m.currentSlot().id {
				case slotJobType:
					m.jobTypeList.Select(len(m.jobTypeList.Items()) - 1)
				case slotTemplateOrSkill:
					m.templateList.Select(len(m.templateList.Items()) - 1)
				case slotDeps:
					m.depList.Select(len(m.depList.Items()) - 1)
				}
			} else {
				m.focusIndex = m.lastVisibleSlot()
				return m.updateFocus(), nil
			}
			return m, nil

		case key.Matches(msg, m.keys.PageUp):
			if inList {
				// See the GoTop note: slot-2 stays on templateList to
				// preserve pre-refactor keystroke behavior.
				switch m.currentSlot().id {
				case slotJobType:
					current := m.jobTypeList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.jobTypeList.Select(newIndex)
				case slotTemplateOrSkill:
					current := m.templateList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.templateList.Select(newIndex)
				case slotDeps:
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
				// See the GoTop note: slot-2 stays on templateList to
				// preserve pre-refactor keystroke behavior.
				switch m.currentSlot().id {
				case slotJobType:
					current := m.jobTypeList.Index()
					newIndex := current + 5
					if newIndex >= len(m.jobTypeList.Items()) {
						newIndex = len(m.jobTypeList.Items()) - 1
					}
					m.jobTypeList.Select(newIndex)
				case slotTemplateOrSkill:
					current := m.templateList.Index()
					newIndex := current + 5
					if newIndex >= len(m.templateList.Items()) {
						newIndex = len(m.templateList.Items()) - 1
					}
					m.templateList.Select(newIndex)
				case slotDeps:
					current := m.depList.Index()
					newIndex := current + 5
					if newIndex >= len(m.depList.Items()) {
						newIndex = len(m.depList.Items()) - 1
					}
					m.depList.Select(newIndex)
				}
			}
			return m, nil

		case key.Matches(msg, m.keys.QuickChat):
			// Quick chat setup - only in NORMAL mode and NOT on a text field.
			// Guard adopts the base's slot-kind check (the former raw "c"
			// handler was refactored to currentSlotKind()==slotList) so typing
			// "c" into the title/prompt fields still inserts the char.
			if m.unfocused && m.currentSlotKind() == slotList {
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

		case key.Matches(msg, m.keys.QuickAgent):
			// Quick agent setup - only in NORMAL mode and NOT on a text field.
			if m.unfocused && m.currentSlotKind() == slotList {
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

		case key.Matches(msg, m.keys.Confirm):
			// Confirm & advance. Guard (inList || unfocused) carried verbatim
			// from the former raw "enter" handler so enter typed in a text
			// field falls through to the input component.
			if inList {
				m.unfocused = false
				m.focusIndex = m.nextVisibleSlot(m.focusIndex)
				return m.updateFocus(), nil
			} else if m.unfocused {
				m.unfocused = false
				return m.updateFocus(), nil
			}
		}

		// Handle non-configurable keys via switch on string
		switch msg.String() {
		case "esc", "escape":
			// ESC unfocuses any focused field (text inputs or lists)
			m.unfocused = true
			m.titleInput.Blur()
			m.promptInput.Blur()
			m.modelInput.Blur()
			return m, nil

		case "ctrl+c":
			return m, doneCancelled()

		case "down", "j":
			if (!inList || m.unfocused) && !inTextInput {
				m.focusIndex = m.nextVisibleSlot(m.focusIndex)
				return m.updateFocus(), nil
			}

		case "up", "k":
			if (!inList || m.unfocused) && !inTextInput {
				m.focusIndex = m.prevVisibleSlot(m.focusIndex)
				return m.updateFocus(), nil
			}

		case "left", "h":
			if m.unfocused && !inTextInput {
				m.focusIndex = m.prevVisibleSlot(m.focusIndex)
				return m.updateFocus(), nil
			}

		case "right", "l":
			if m.unfocused && !inTextInput {
				m.focusIndex = m.nextVisibleSlot(m.focusIndex)
				return m.updateFocus(), nil
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
		}
	}

	// Delegate to the focused component only if in insert mode.
	// When unfocused (navigation mode), components should not
	// receive key events — prevents lists from capturing keys
	// meant for wizard-level navigation.
	if !m.unfocused {
		switch m.currentSlot().id {
		case slotTitle: // Title input
			m.titleInput, cmd = m.titleInput.Update(msg)
		case slotJobType: // Job type list
			prevModelKind := slotModelKind(&m)
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
				// Job type changes switch provider axes and must not retain an
				// agent provider as an API-provider filter (or vice versa).
				m.providerList = buildProviderList(selectedJobType)
				m.resetModelWidget()
				if slotModelKind(&m) != prevModelKind {
					m.resetModelWidget()
				}
			}
		case slotTemplateOrSkill: // Slot 2: Skills or Template list
			if m.slot2IsSkills {
				m.skillList, cmd = m.skillList.Update(msg)
			} else {
				m.templateList, cmd = m.templateList.Update(msg)
			}
		case slotDeps: // Dependency list
			m.depList, cmd = m.depList.Update(msg)
		case slotPrompt: // Prompt textarea
			m.promptInput, cmd = m.promptInput.Update(msg)
		case slotProvider: // Provider picker
			prevModelKind := slotModelKind(&m)
			prevSelection := m.providerList.SelectedItem()
			m.providerList, cmd = m.providerList.Update(msg)
			// Rebuild on every provider selection change: direct-API provider
			// changes alter list contents even though its widget kind stays list.
			if prevSelection != m.providerList.SelectedItem() {
				if isAgentJobType(m.selectedJobType()) {
					if m.effectiveProvider() == "claude" {
						m.modelList = newModelList()
					}
				} else if selected := m.providerList.SelectedItem(); selected != nil {
					m.modelList = buildLLMModelList(string(selected.(item)))
				}
				m.resetModelWidget()
				_ = prevModelKind
			}
		case slotModel: // Model picker (list for claude, text otherwise)
			if m.currentSlotKind() == slotList {
				m.modelList, cmd = m.modelList.Update(msg)
			} else {
				m.modelInput, cmd = m.modelInput.Update(msg)
			}
		}
	}

	return m, cmd
}

// resetModelWidget clears the model slot's value on both backing
// widgets, so a stale selection doesn't survive a provider/job-type
// change that flips the widget kind between the claude picker and the
// free-form input.
func (m *Model) resetModelWidget() {
	m.modelList.Select(0)
	m.modelInput.SetValue("")
}

// updateFocus updates focus state for all components based on the
// current focusIndex and unfocused flag.
func (m Model) updateFocus() Model {
	// Blur all text inputs
	m.titleInput.Blur()
	m.promptInput.Blur()
	m.modelInput.Blur()

	// Only focus if not in unfocused state
	if !m.unfocused {
		switch m.currentSlot().id {
		case slotTitle:
			m.titleInput.Focus()
		case slotPrompt:
			m.promptInput.Focus()
		case slotModel:
			// The model slot only owns a focusable text input when its
			// provider-dependent kind is slotText; the claude picker is
			// a list and needs no text focus.
			if m.currentSlotKind() == slotText {
				m.modelInput.Focus()
			}
		}
	}

	return m
}
