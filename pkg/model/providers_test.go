package model

import (
	"testing"
	"time"

	"github.com/grovetools/grove-openrouter/pkg/openrouter"
)

func TestLLMProviderNamesAndModels(t *testing.T) {
	want := []string{ProviderAnthropic, ProviderGoogle, ProviderOpenRouter}
	got := LLMProviderNames()
	if len(got) != len(want) {
		t.Fatalf("providers = %v, want %v", got, want)
	}
	for i, provider := range want {
		if got[i] != provider {
			t.Errorf("provider[%d] = %q, want %q", i, got[i], provider)
		}
		models := ModelsForProvider(provider)
		if len(models) == 0 {
			t.Errorf("ModelsForProvider(%q) is empty", provider)
		}
		for _, model := range models {
			resolved, ok := LookupModelProvider(model.ID)
			if !ok || resolved != provider {
				t.Errorf("%q resolved to %q, %v; want %q, true", model.ID, resolved, ok, provider)
			}
		}
	}
}

func TestMergeOpenRouterCatalogPrefixesDedupesAndSorts(t *testing.T) {
	curated := []ModelInfo{{ID: "openrouter/a/one", Provider: ProviderOpenRouter}}
	catalog := &openrouter.Catalog{FetchedAt: time.Now(), Models: []openrouter.CatalogModel{{ID: "z/last"}, {ID: "a/one"}, {ID: "b/first"}}}
	got := mergeOpenRouterCatalog(curated, catalog)
	if len(got) != 3 {
		t.Fatalf("models = %#v, want three", got)
	}
	if got[0].ID != "openrouter/a/one" || got[1].ID != "openrouter/b/first" || got[2].ID != "openrouter/z/last" {
		t.Errorf("unexpected merge order: %#v", got)
	}
}
