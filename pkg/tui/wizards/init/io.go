package planinit

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/theme"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	geminimodels "github.com/grovetools/grove-gemini/pkg/models"
)

// flowConfigSubset captures the fields of the flow grove.yml
// extension that the init wizard cares about. It's deliberately
// narrower than flow/cmd.FlowConfig so the wizard doesn't pull in
// the cmd-layer config type.
type flowConfigSubset struct {
	RunInitByDefault *bool `yaml:"run_init_by_default"`
}

// LoadFlowDefaults reads grove config from the current working
// directory and returns the two wizard-relevant settings:
//   - getRecipeCmd: dynamic recipe command from flow.recipes.get_recipe_cmd
//   - runInitByDefault: flow.run_init_by_default (defaults to true)
//
// Both return zero values and no error if the config can't be
// loaded; callers should treat failure as "use built-in defaults."
func LoadFlowDefaults() (getRecipeCmd string, runInitByDefault bool) {
	runInitByDefault = true
	coreCfg, err := config.LoadFrom(".")
	if err != nil || coreCfg == nil {
		return "", runInitByDefault
	}

	// Pull get_recipe_cmd out of the generic flow map.
	var rawFlowConfig map[string]interface{}
	if err := coreCfg.UnmarshalExtension("flow", &rawFlowConfig); err == nil {
		if recipes, ok := rawFlowConfig["recipes"].(map[string]interface{}); ok {
			if cmdStr, ok := recipes["get_recipe_cmd"].(string); ok {
				getRecipeCmd = cmdStr
			}
		}
	}

	// Pull run_init_by_default out of the typed subset.
	var flowCfg flowConfigSubset
	if err := coreCfg.UnmarshalExtension("flow", &flowCfg); err == nil {
		if flowCfg.RunInitByDefault != nil {
			runInitByDefault = *flowCfg.RunInitByDefault
		}
	}
	return getRecipeCmd, runInitByDefault
}

// item is a simple string list entry for the recipe list.
type item string

func (i item) FilterValue() string { return string(i) }

// modelInfo describes an LLM model option for the model picker.
type modelInfo struct {
	ID       string
	Provider string
	Note     string
}

// modelItem wraps modelInfo for use in a list.Model.
type modelItem struct {
	modelInfo
}

func (m modelItem) FilterValue() string { return m.ID }
func (m modelItem) Title() string       { return m.ID }
func (m modelItem) Description() string { return fmt.Sprintf("%s - %s", m.Provider, m.Note) }

// itemDelegate renders item and modelItem entries in a list.Model.
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
	case modelItem:
		str = fmt.Sprintf("%s%s", cursor, i.ID)
	default:
		return
	}
	fmt.Fprint(w, str)
}

// getAvailableModels returns the list of current, non-legacy LLM
// models used by the plan-init wizard's model picker.
func getAvailableModels() []modelInfo {
	var models []modelInfo
	for _, m := range geminimodels.CurrentModels() {
		models = append(models, modelInfo{
			ID:       m.ID,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}
	for _, m := range anthropicmodels.CurrentModels() {
		id := m.ID
		if m.Alias != "" {
			id = m.Alias
		}
		models = append(models, modelInfo{
			ID:       id,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}
	return models
}
