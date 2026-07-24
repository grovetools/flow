package planinit

import (
	"strings"
	"testing"
)

func TestNewDefaultsRecipeAndModelToConfigResolution(t *testing.T) {
	m := New(Config{
		WorkspaceDir:       t.TempDir(),
		DefaultModel:       "configured-model",
		AnchorRepositories: []string{},
	})

	if got := string(m.recipeList.SelectedItem().(item)); got != "none" {
		t.Fatalf("default recipe = %q, want none", got)
	}
	selectedModel := m.modelList.SelectedItem().(modelItem)
	if !selectedModel.IsDefault || selectedModel.ID != "(default: configured-model)" {
		t.Fatalf("default model item = %+v", selectedModel)
	}
	if req := m.toRequest(); req.Recipe != "" || req.Model != "" {
		t.Fatalf("default request should defer recipe/model resolution: %+v", req)
	}
}

func TestMainFormDoesNotOfferRedundantOpenSessionToggle(t *testing.T) {
	m := New(Config{WorkspaceDir: t.TempDir(), AnchorRepositories: []string{}})
	if got := m.renderMainScreen(); strings.Contains(got, "Open Session") {
		t.Fatalf("main form still offers redundant session launch: %q", got)
	}
	if m.getMaxFocusIndex() != 4 {
		t.Fatalf("main focus range still includes removed toggle: %d", m.getMaxFocusIndex())
	}
}

func TestAnchorRepositoryPickerEmitsSelectedCanonicalName(t *testing.T) {
	m := New(Config{
		WorkspaceDir:       t.TempDir(),
		AnchorRepositories: []string{"zeta", "alpha", "alpha"},
	})

	items := m.anchorList.Items()
	if len(items) != 3 || string(items[0].(item)) != "(auto-infer)" || string(items[1].(item)) != "alpha" {
		t.Fatalf("anchor items were not sorted and deduplicated: %#v", items)
	}
	m.anchorList.Select(1)
	if got := m.toRequest().Anchor; got != "alpha" {
		t.Fatalf("request anchor = %q, want alpha", got)
	}
}
