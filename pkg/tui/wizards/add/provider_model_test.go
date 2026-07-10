package add

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
)

// selectItem selects the list entry whose string value matches name.
// It is a no-op if the item is absent.
func selectItem(l *list.Model, name string) {
	for i, it := range l.Items() {
		if string(it.(item)) == name {
			l.Select(i)
			return
		}
	}
}

// newTestModel builds a minimal wizard Model with just the widgets the
// Phase 4b provider/model logic reads — the job-type, provider, and
// model pickers — without New()'s config/skill-service side effects.
func newTestModel(jobType, provider string) Model {
	m := Model{
		defaultProviderName: "claude",
		selectedDeps:        map[string]bool{},
	}
	jobTypes := []list.Item{
		item("interactive_agent"), item("isolated_agent"), item("headless_agent"),
		item("oneshot"), item("shell"), item("chat"), item("file"),
	}
	m.jobTypeList = list.New(jobTypes, itemDelegate{}, 20, 7)
	selectItem(&m.jobTypeList, jobType)

	m.providerList = newProviderList()
	selectItem(&m.providerList, provider)
	m.modelList = newModelList()
	if jobType == "oneshot" || jobType == "chat" {
		m.providerList = buildProviderList(jobType)
		selectItem(&m.providerList, provider)
		m.modelList = buildLLMModelList(provider)
	}
	m.modelInput = newModelInput()
	return m
}

// TestToJob_ProviderModelFrontmatter locks in the load-bearing Phase 4b
// outcome: an agent job with an explicit provider + model writes
// provider:/model: frontmatter, while the "default"/"(default)"
// sentinels produce empty strings (config/claude fallback downstream).
func TestToJob_ProviderModelFrontmatter(t *testing.T) {
	// codex uses the free-form text input (non-claude provider kind).
	m := newTestModel("interactive_agent", "codex")
	m.modelInput.SetValue("gpt-5.5")
	m.extractValues()
	job := m.toJob(nil)
	if job.Provider != "codex" {
		t.Errorf("Provider = %q, want %q", job.Provider, "codex")
	}
	if job.Model != "gpt-5.5" {
		t.Errorf("Model = %q, want %q", job.Model, "gpt-5.5")
	}

	// default provider + (default) model → both empty.
	m2 := newTestModel("interactive_agent", "default")
	m2.extractValues()
	job2 := m2.toJob(nil)
	if job2.Provider != "" {
		t.Errorf("Provider = %q, want empty", job2.Provider)
	}
	if job2.Model != "" {
		t.Errorf("Model = %q, want empty", job2.Model)
	}
}

// TestModelSlotKind_ProviderDependent asserts the model slot is a
// claude-family list only for the claude/default provider on an agent
// job, and a free-form text input for every other provider.
func TestModelSlotKind_ProviderDependent(t *testing.T) {
	cases := []struct {
		provider string
		want     slotKind
	}{
		{"default", slotList}, // default resolves to claude
		{"claude", slotList},
		{"codex", slotText},
		{"pi", slotText},
		{"opencode", slotText},
	}
	for _, tc := range cases {
		m := newTestModel("interactive_agent", tc.provider)
		if got := slotModelKind(&m); got != tc.want {
			t.Errorf("provider %q: slotModelKind = %v, want %v", tc.provider, got, tc.want)
		}
	}

	// Direct-API jobs use a provider-filtered list; their provider is only a
	// UI filter and is not persisted.
	mo := newTestModel("oneshot", "(all)")
	if got := slotModelKind(&mo); got != slotList {
		t.Errorf("oneshot: slotModelKind = %v, want %v", got, slotList)
	}
}

// TestSlotVisibility_ShellHidesBoth verifies shell/file hide both new
// slots and that navigation skips the hidden slots (wrapping past them
// rather than landing on them).
func TestSlotVisibility_ShellHidesBoth(t *testing.T) {
	m := newTestModel("shell", "default")
	if slotProviderVisible(&m) {
		t.Error("provider slot must be hidden for shell")
	}
	if slotModelVisible(&m) {
		t.Error("model slot must be hidden for shell")
	}

	// From prompt (index 4), the next visible slot wraps past the
	// hidden provider(5)/model(6) back to title(0).
	if got := m.nextVisibleSlot(4); got != 0 {
		t.Errorf("nextVisibleSlot(4) = %d, want 0 (skips hidden provider/model)", got)
	}
	// From title(0), the previous visible slot wraps back to prompt(4),
	// again skipping the hidden slots.
	if got := m.prevVisibleSlot(0); got != 4 {
		t.Errorf("prevVisibleSlot(0) = %d, want 4 (skips hidden provider/model)", got)
	}

	// Oneshot has an API-provider filter as well as its model picker.
	mo := newTestModel("oneshot", "(all)")
	if !slotProviderVisible(&mo) {
		t.Error("provider slot must be visible for oneshot")
	}
	if !slotModelVisible(&mo) {
		t.Error("model slot must be visible for oneshot")
	}
}
