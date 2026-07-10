package model

import (
	"sort"
	"strings"

	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	geminimodels "github.com/grovetools/grove-gemini/pkg/models"
	openroutermodels "github.com/grovetools/grove-openrouter/pkg/models"
	"github.com/grovetools/grove-openrouter/pkg/openrouter"
)

// ModelInfo is a displayable, flow-facing LLM model. ID is always suitable for
// persisting in a chat or oneshot job.
type ModelInfo struct {
	ID       string
	Provider string
	Note     string
}

// LLMProviderNames returns the enumerable direct-API provider axis. This is
// deliberately separate from orchestration.AgentProviderNames: the latter
// names coding-agent CLIs, not Flow API dispatchers.
func LLMProviderNames() []string {
	return []string{ProviderAnthropic, ProviderGoogle, ProviderOpenRouter}
}

// ModelsForProvider returns picker models for a direct-API provider. OpenRouter
// additionally includes its on-disk catalog cache; this never performs network
// I/O, so it is safe to call from a TUI render/update path.
func ModelsForProvider(provider string) []ModelInfo {
	var result []ModelInfo
	switch provider {
	case ProviderAnthropic:
		for _, m := range anthropicmodels.CurrentModels() {
			id := m.ID
			if m.Alias != "" {
				id = m.Alias
			}
			result = append(result, ModelInfo{ID: id, Provider: provider, Note: m.Note})
		}
	case ProviderGoogle:
		for _, m := range geminimodels.CurrentModels() {
			// The registries do not yet expose a capability/kind field.
			if strings.Contains(strings.ToLower(m.ID), "embedding") {
				continue
			}
			result = append(result, ModelInfo{ID: m.ID, Provider: provider, Note: m.Note})
		}
	case ProviderOpenRouter:
		for _, m := range openroutermodels.CurrentModels() {
			result = append(result, ModelInfo{ID: m.ID, Provider: provider, Note: m.Note})
		}
		if catalog, err := openrouter.LoadCatalogCache(); err == nil && catalog != nil {
			result = mergeOpenRouterCatalog(result, catalog)
		}
	}
	return result
}

// AllModels returns every direct-API picker model. In particular it includes
// the cached OpenRouter catalog, keeping the wizard's "all" filter as
// comprehensive as an OpenRouter-specific selection without blocking on a
// refresh.
func AllModels() []ModelInfo {
	var result []ModelInfo
	for _, provider := range LLMProviderNames() {
		result = append(result, ModelsForProvider(provider)...)
	}
	return result
}

func mergeOpenRouterCatalog(curated []ModelInfo, catalog *openrouter.Catalog) []ModelInfo {
	seen := make(map[string]bool, len(curated))
	for _, m := range curated {
		seen[m.ID] = true
	}
	var appended []ModelInfo
	for _, m := range catalog.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if !openroutermodels.HasPrefix(id) {
			id = openroutermodels.Prefix + id
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		note := m.Name
		if note == "" {
			note = "OpenRouter catalog"
		}
		appended = append(appended, ModelInfo{ID: id, Provider: ProviderOpenRouter, Note: note})
	}
	sort.Slice(appended, func(i, j int) bool { return appended[i].ID < appended[j].ID })
	return append(curated, appended...)
}
