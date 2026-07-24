package view

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/grovetools/core/tui"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/embed"
	core_theme "github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
	"github.com/grovetools/flow/pkg/tui/wizards/add"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

// Tab indices for the 5 flow pages.
const (
	tabJobs       = 0
	tabAddJob     = 1
	tabPlans      = 2
	tabAddPlan    = 3
	tabFinishPlan = 4
)

// ---------- statusPage (tab 0: Jobs) ----------

type statusPage struct {
	s      *viewState
	width  int
	height int
}

func (p *statusPage) Name() string { return "Jobs" }
func (p *statusPage) Title() string {
	if p.s.statusModel != nil {
		return p.s.statusModel.PlanTitle()
	}
	if p.s.statusLoadingPlan != "" {
		return core_theme.IconPlan + " Plan Status: " + p.s.statusLoadingPlan
	}
	return ""
}
func (p *statusPage) Init() tea.Cmd { return nil }
func (p *statusPage) View() (out string) {
	if p.s.statusModel == nil {
		if p.s.statusLoadError != "" {
			return core_theme.DefaultTheme.Error.Render("Unable to load jobs: " + p.s.statusLoadError)
		}
		return ""
	}
	defer tui.RecoverView(&out)
	return p.s.statusModel.View()
}

func (p *statusPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if p.s.statusModel == nil {
		return p, nil
	}
	updated, cmd := p.s.statusModel.Update(msg)
	if sm, ok := updated.(status.Model); ok {
		*p.s.statusModel = sm
	}
	return p, cmd
}

func (p *statusPage) Focus() tea.Cmd {
	if p.s.statusModel == nil {
		return nil
	}
	var cmds []tea.Cmd
	if p.width > 0 && p.height > 0 {
		sized, c := p.s.statusModel.Update(tea.WindowSizeMsg{Width: p.width, Height: p.height})
		if sm, ok := sized.(status.Model); ok {
			*p.s.statusModel = sm
		}
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	focused, c := p.s.statusModel.Update(embed.FocusMsg{})
	if sm, ok := focused.(status.Model); ok {
		*p.s.statusModel = sm
	}
	if c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (p *statusPage) Blur() {
	if p.s.statusModel == nil {
		return
	}
	updated, _ := p.s.statusModel.Update(embed.BlurMsg{})
	if sm, ok := updated.(status.Model); ok {
		*p.s.statusModel = sm
	}
}

// SetSize caches dimensions. The pager follows with Update(WindowSizeMsg)
// for the active page (which returns cmds properly). Inactive pages
// receive the cached size via Focus() when they become active.
func (p *statusPage) SetSize(w, h int) {
	p.width = w
	p.height = h
}

func (p *statusPage) Enabled() bool {
	return p.s.statusModel != nil || p.s.statusLoading || p.s.statusLoadError != ""
}

func (p *statusPage) Ready() (bool, string) {
	return !p.s.statusLoading, "Loading jobs…"
}

func (p *statusPage) IsTextEntryActive() bool {
	return p.s.statusModel != nil && p.s.statusModel.IsTextEntryActive()
}

// Compile-time checks.
var (
	_ pager.Page              = (*statusPage)(nil)
	_ pager.PageWithTitle     = (*statusPage)(nil)
	_ pager.PageWithEnabled   = (*statusPage)(nil)
	_ pager.PageWithReady     = (*statusPage)(nil)
	_ pager.PageWithTextInput = (*statusPage)(nil)
)

// ---------- addJobPage (tab 1: Add Job) ----------

type addJobPage struct {
	s      *viewState
	width  int
	height int
}

func (p *addJobPage) Name() string { return "Add Job" }
func (p *addJobPage) Title() string {
	return core_theme.IconFilePlus + " Add Job"
}
func (p *addJobPage) Init() tea.Cmd { return nil }
func (p *addJobPage) View() (out string) {
	if p.s.wizardModel == nil {
		return ""
	}
	defer tui.RecoverView(&out)
	return p.s.wizardModel.View()
}

func (p *addJobPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if p.s.wizardModel == nil {
		return p, nil
	}
	updated, cmd := p.s.wizardModel.Update(msg)
	if wm, ok := updated.(add.Model); ok {
		*p.s.wizardModel = wm
	}
	return p, cmd
}

func (p *addJobPage) Focus() tea.Cmd {
	if p.s.wizardModel == nil {
		return nil
	}
	var cmds []tea.Cmd
	if p.width > 0 && p.height > 0 {
		sized, c := p.s.wizardModel.Update(tea.WindowSizeMsg{Width: p.width, Height: p.height})
		if wm, ok := sized.(add.Model); ok {
			*p.s.wizardModel = wm
		}
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	focused, c := p.s.wizardModel.Update(embed.FocusMsg{})
	if wm, ok := focused.(add.Model); ok {
		*p.s.wizardModel = wm
	}
	if c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (p *addJobPage) Blur() {
	if p.s.wizardModel == nil {
		return
	}
	updated, _ := p.s.wizardModel.Update(embed.BlurMsg{})
	if wm, ok := updated.(add.Model); ok {
		*p.s.wizardModel = wm
	}
}

func (p *addJobPage) SetSize(w, h int) {
	p.width = w
	p.height = h
}

func (p *addJobPage) Enabled() bool         { return p.s.statusModel != nil }
func (p *addJobPage) Ready() (bool, string) { return p.s.wizardModel != nil, "Loading wizard…" }
func (p *addJobPage) IsTextEntryActive() bool {
	return p.s.wizardModel != nil && p.s.wizardModel.IsTextEntryActive()
}

func (p *addJobPage) Footer() string {
	if p.s.wizardModel == nil {
		return ""
	}
	return p.s.wizardModel.FooterView()
}

var (
	_ pager.Page              = (*addJobPage)(nil)
	_ pager.PageWithTitle     = (*addJobPage)(nil)
	_ pager.PageWithEnabled   = (*addJobPage)(nil)
	_ pager.PageWithReady     = (*addJobPage)(nil)
	_ pager.PageWithTextInput = (*addJobPage)(nil)
	_ pager.PageWithFooter    = (*addJobPage)(nil)
)

// ---------- plansPage (tab 2: Plans) ----------

type plansPage struct {
	s      *viewState
	width  int
	height int
}

func (p *plansPage) Name() string { return "Plans" }
func (p *plansPage) Title() string {
	ws := filepath.Base(p.s.cfg.WorkspaceDir)
	if ws == "" || ws == "." || ws == "/" {
		return core_theme.DefaultTheme.Bold.Render("󰠡 Plans")
	}
	return core_theme.DefaultTheme.Bold.Render(fmt.Sprintf("󰠡 Plans in the %s workspace", ws))
}
func (p *plansPage) Init() tea.Cmd { return p.s.browserModel.Init() }
func (p *plansPage) View() string  { return p.s.browserModel.View() }

func (p *plansPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	updated, cmd := p.s.browserModel.Update(msg)
	if bm, ok := updated.(browser.Model); ok {
		p.s.browserModel = bm
	}
	return p, cmd
}

func (p *plansPage) Focus() tea.Cmd {
	var cmds []tea.Cmd
	if p.width > 0 && p.height > 0 {
		sized, c := p.s.browserModel.Update(tea.WindowSizeMsg{Width: p.width, Height: p.height})
		if bm, ok := sized.(browser.Model); ok {
			p.s.browserModel = bm
		}
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	focused, c := p.s.browserModel.Update(embed.FocusMsg{})
	if bm, ok := focused.(browser.Model); ok {
		p.s.browserModel = bm
	}
	if c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (p *plansPage) Blur() {
	updated, _ := p.s.browserModel.Update(embed.BlurMsg{})
	if bm, ok := updated.(browser.Model); ok {
		p.s.browserModel = bm
	}
}

func (p *plansPage) SetSize(w, h int) {
	p.width = w
	p.height = h
}

func (p *plansPage) Footer() string {
	return p.s.browserModel.Footer()
}

var (
	_ pager.Page           = (*plansPage)(nil)
	_ pager.PageWithTitle  = (*plansPage)(nil)
	_ pager.PageWithFooter = (*plansPage)(nil)
)

// ---------- addPlanPage (tab 3: Add Plan) ----------

type addPlanPage struct {
	s      *viewState
	width  int
	height int
}

func (p *addPlanPage) Name() string  { return "Add Plan" }
func (p *addPlanPage) Title() string { return "󰠡 Create New Plan" }
func (p *addPlanPage) Init() tea.Cmd { return nil }
func (p *addPlanPage) View() (out string) {
	if p.s.initProgress != "" {
		output := strings.TrimRight(p.s.initOutput, "\n")
		if output == "" {
			output = "Waiting for flow plan init output…"
		}
		maxLines := p.height - 9
		if maxLines < 6 {
			maxLines = 6
		}
		lines := strings.Split(output, "\n")
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		popupWidth := p.width - 6
		if popupWidth < 40 {
			popupWidth = 40
		}
		outputBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(core_theme.DefaultColors.Border).
			Padding(0, 1).
			Width(popupWidth).
			Render(core_theme.DefaultTheme.Bold.Render("Creation Output") + "\n" + strings.Join(lines, "\n"))
		return core_theme.DefaultTheme.Info.Render(p.s.initProgress) + "\n\n" + outputBox
	}
	if p.s.initWizardModel == nil {
		return core_theme.DefaultTheme.Info.Render("Loading plan wizard…")
	}
	defer tui.RecoverView(&out)
	return p.s.initWizardModel.View()
}

func (p *addPlanPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if p.s.initWizardModel == nil {
		return p, nil
	}
	updated, cmd := p.s.initWizardModel.Update(msg)
	if im, ok := updated.(planinit.Model); ok {
		*p.s.initWizardModel = im
	}
	return p, cmd
}

func (p *addPlanPage) Focus() tea.Cmd {
	if p.s.initWizardModel == nil {
		return nil
	}
	var cmds []tea.Cmd
	if p.width > 0 && p.height > 0 {
		sized, c := p.s.initWizardModel.Update(tea.WindowSizeMsg{Width: p.width, Height: p.height})
		if im, ok := sized.(planinit.Model); ok {
			*p.s.initWizardModel = im
		}
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	focused, c := p.s.initWizardModel.Update(embed.FocusMsg{})
	if im, ok := focused.(planinit.Model); ok {
		*p.s.initWizardModel = im
	}
	if c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (p *addPlanPage) Blur() {
	if p.s.initWizardModel == nil {
		return
	}
	updated, _ := p.s.initWizardModel.Update(embed.BlurMsg{})
	if im, ok := updated.(planinit.Model); ok {
		*p.s.initWizardModel = im
	}
}

func (p *addPlanPage) SetSize(w, h int) {
	p.width = w
	p.height = h
}

func (p *addPlanPage) Ready() (bool, string) { return p.s.initWizardModel != nil, "Loading wizard…" }
func (p *addPlanPage) IsTextEntryActive() bool {
	return p.s.initWizardModel != nil && p.s.initWizardModel.IsTextEntryActive()
}

func (p *addPlanPage) Footer() string {
	if p.s.initWizardModel == nil {
		return ""
	}
	return p.s.initWizardModel.FooterView()
}

var (
	_ pager.Page              = (*addPlanPage)(nil)
	_ pager.PageWithTitle     = (*addPlanPage)(nil)
	_ pager.PageWithReady     = (*addPlanPage)(nil)
	_ pager.PageWithTextInput = (*addPlanPage)(nil)
	_ pager.PageWithFooter    = (*addPlanPage)(nil)
)

// ---------- finishPlanPage (tab 4: Finish Plan) ----------

type finishPlanPage struct {
	s      *viewState
	width  int
	height int
}

func (p *finishPlanPage) Name() string  { return "Finish Plan" }
func (p *finishPlanPage) Title() string { return "󰄬 Finish Plan" }
func (p *finishPlanPage) Init() tea.Cmd { return nil }
func (p *finishPlanPage) View() (out string) {
	if p.s.finishWizardModel == nil {
		return ""
	}
	defer tui.RecoverView(&out)
	return p.s.finishWizardModel.View()
}

func (p *finishPlanPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	if p.s.finishWizardModel == nil {
		return p, nil
	}
	updated, cmd := p.s.finishWizardModel.Update(msg)
	if fm, ok := updated.(finish.Model); ok {
		*p.s.finishWizardModel = fm
	}
	return p, cmd
}

func (p *finishPlanPage) Focus() tea.Cmd {
	if p.s.finishWizardModel == nil {
		return nil
	}
	var cmds []tea.Cmd
	if p.width > 0 && p.height > 0 {
		sized, c := p.s.finishWizardModel.Update(tea.WindowSizeMsg{Width: p.width, Height: p.height})
		if fm, ok := sized.(finish.Model); ok {
			*p.s.finishWizardModel = fm
		}
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	focused, c := p.s.finishWizardModel.Update(embed.FocusMsg{})
	if fm, ok := focused.(finish.Model); ok {
		*p.s.finishWizardModel = fm
	}
	if c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (p *finishPlanPage) Blur() {
	if p.s.finishWizardModel == nil {
		return
	}
	updated, _ := p.s.finishWizardModel.Update(embed.BlurMsg{})
	if fm, ok := updated.(finish.Model); ok {
		*p.s.finishWizardModel = fm
	}
}

func (p *finishPlanPage) SetSize(w, h int) {
	p.width = w
	p.height = h
}

func (p *finishPlanPage) Enabled() bool { return p.s.statusModel != nil }
func (p *finishPlanPage) Ready() (bool, string) {
	return p.s.finishWizardModel != nil, "Loading wizard…"
}

func (p *finishPlanPage) Footer() string {
	if p.s.finishWizardModel == nil {
		return ""
	}
	return p.s.finishWizardModel.FooterView()
}

var (
	_ pager.Page            = (*finishPlanPage)(nil)
	_ pager.PageWithTitle   = (*finishPlanPage)(nil)
	_ pager.PageWithEnabled = (*finishPlanPage)(nil)
	_ pager.PageWithReady   = (*finishPlanPage)(nil)
	_ pager.PageWithFooter  = (*finishPlanPage)(nil)
)
