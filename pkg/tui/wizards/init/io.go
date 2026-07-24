package planinit

import (
	"fmt"
	"io"
	"sort"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/tui/theme"

	flowmodel "github.com/grovetools/flow/pkg/model"
)

// flowConfigSubset captures the fields of the flow grove.yml
// extension that the init wizard cares about. It's deliberately
// narrower than flow/cmd.FlowConfig so the wizard doesn't pull in
// the cmd-layer config type.
type flowConfigSubset struct {
	OneshotModel     string `yaml:"oneshot_model"`
	RunInitByDefault *bool  `yaml:"run_init_by_default"`
}

// LoadFlowDefaults reads grove config from the current working
// directory and returns the wizard-relevant settings:
//   - getRecipeCmd: dynamic recipe command from flow.recipes.get_recipe_cmd
//   - runInitByDefault: flow.run_init_by_default (defaults to true)
//   - defaultModel: flow.oneshot_model (empty means provider fallback)
//
// Both return zero values and no error if the config can't be
// loaded; callers should treat failure as "use built-in defaults."
func LoadFlowDefaults() (getRecipeCmd string, runInitByDefault bool, defaultModel string) {
	return LoadFlowDefaultsAt(".")
}

// LoadFlowDefaultsAt reads the settings that should govern a plan created from
// workspaceDir. Embedded callers use the canonical ecosystem root here so a
// wizard opened from a plan worktree does not accidentally inherit that
// worktree's local targeting.
func LoadFlowDefaultsAt(workspaceDir string) (getRecipeCmd string, runInitByDefault bool, defaultModel string) {
	runInitByDefault = true
	coreCfg, err := config.LoadFrom(workspaceDir)
	if err != nil || coreCfg == nil {
		return "", runInitByDefault, ""
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
		defaultModel = flowCfg.OneshotModel
		if flowCfg.RunInitByDefault != nil {
			runInitByDefault = *flowCfg.RunInitByDefault
		}
	}
	return getRecipeCmd, runInitByDefault, defaultModel
}

// item is a simple string list entry for the recipe list.
type item string

func (i item) FilterValue() string { return string(i) }

// modelInfo describes an LLM model option for the model picker.
type modelInfo struct {
	ID        string
	Provider  string
	Note      string
	IsDefault bool
}

// modelItem wraps modelInfo for use in a list.Model.
type modelItem struct {
	modelInfo
}

func (m modelItem) FilterValue() string { return m.ID }
func (m modelItem) Title() string       { return m.ID }
func (m modelItem) Description() string { return fmt.Sprintf("%s - %s", m.Provider, m.Note) }

func defaultModelItem(configured string) modelItem {
	label := "(default)"
	if configured != "" {
		label = "(default: " + configured + ")"
	}
	return modelItem{modelInfo{ID: label, Provider: "flow config", Note: "flow.oneshot_model", IsDefault: true}}
}

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

// getAvailableModels delegates aggregation to the shared direct-API model
// axis, so init and add expose the same cached OpenRouter catalog.
func getAvailableModels() []modelInfo {
	available := flowmodel.AllModels()
	models := make([]modelInfo, 0, len(available))
	for _, m := range available {
		models = append(models, modelInfo{ID: m.ID, Provider: m.Provider, Note: m.Note})
	}
	return models
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
