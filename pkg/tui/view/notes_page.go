package view

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/tui/components/pager"
	coretheme "github.com/grovetools/core/tui/theme"
	corefm "github.com/grovetools/core/util/frontmatter"

	"github.com/grovetools/flow/pkg/orchestration"
)

type notesScope int

const (
	notesThisPlan notesScope = iota
	notesThisNotespace
	notesUnlinked
	notesAll
)

func (s notesScope) String() string {
	return [...]string{"This plan", "This notespace", "Unlinked", "All notespaces"}[s]
}

type notesLoadedMsg struct {
	notes []*models.NoteIndexEntry
	err   error
}
type notesActionMsg struct{ err error }
type notesPollMsg struct{}

// loadNotesIndex is a test seam. The daemon index is preferred; LocalClient's
// documented nil result and unavailable daemons fall back to nb's
// filesystem-authoritative cross-notespace list, then a bounded direct scan.
var loadNotesIndex = func(s *viewState) ([]*models.NoteIndexEntry, error) {
	if s.cfg.DaemonClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		notes, err := s.cfg.DaemonClient.GetNoteIndex(ctx, "")
		cancel()
		if err == nil && notes != nil {
			return notes, nil
		}
	}
	// nb's list path is the filesystem-authoritative cross-notespace fallback;
	// it works without groved and preserves the meaning of "All notespaces".
	if notes, err := listNotesFromFilesystem(); err == nil {
		return notes, nil
	}
	// Keep a final dependency-free fallback for installations where nb is not
	// on PATH. It is intentionally bounded to this notespace.
	return scanNotespace(filepath.Dir(filepath.Clean(s.cfg.PlansDir)))
}

func listNotesFromFilesystem() ([]*models.NoteIndexEntry, error) {
	out, err := exec.Command("nb", "list", "--json", "--workspaces", "--all").Output()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Path             string    `json:"path"`
		Title            string    `json:"title"`
		FrontmatterTitle string    `json:"frontmatter_title"`
		Group            string    `json:"group"`
		Workspace        string    `json:"workspace"`
		PlanRef          string    `json:"plan_ref"`
		PlanJob          string    `json:"plan_job"`
		Priority         string    `json:"priority"`
		ModifiedAt       time.Time `json:"modified_at"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	notes := make([]*models.NoteIndexEntry, 0, len(rows))
	for _, row := range rows {
		title := row.FrontmatterTitle
		if title == "" {
			title = row.Title
		}
		notes = append(notes, &models.NoteIndexEntry{Path: row.Path, Name: filepath.Base(row.Path), Title: title, Group: row.Group, Workspace: row.Workspace, PlanRef: row.PlanRef, PlanJob: row.PlanJob, Priority: row.Priority, ModTime: row.ModifiedAt, Type: "note", ContentDir: "notes"})
	}
	return notes, nil
}

func scanNotespace(root string) ([]*models.NoteIndexEntry, error) {
	if root == "" || root == "." || root == string(filepath.Separator) {
		return nil, fmt.Errorf("notespace root is unavailable")
	}
	workspace := filepath.Base(root)
	var notes []*models.NoteIndexEntry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "plans" || name == "chats" || name == "concepts" || name == "context" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || filepath.Ext(d.Name()) != ".md" {
			return nil
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		meta, parseErr := corefm.Parse(f)
		_ = f.Close()
		if parseErr != nil {
			return parseErr
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		title := meta.Title
		if title == "" {
			title = strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		}
		notes = append(notes, &models.NoteIndexEntry{Path: path, Name: d.Name(), Title: title, ID: meta.ID, PlanRef: meta.PlanRef, PlanJob: meta.PlanJob, Priority: meta.Priority, Created: meta.Created, ModTime: info.ModTime(), Type: "note", Group: filepath.ToSlash(rel), Workspace: workspace, ContentDir: "notes"})
		return nil
	})
	return notes, err
}

func notesLoadCmd(s *viewState) tea.Cmd {
	return func() tea.Msg {
		notes, err := loadNotesIndex(s)
		return notesLoadedMsg{notes: notes, err: err}
	}
}

func notesPollCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return notesPollMsg{} })
}

type notesPage struct {
	s        *viewState
	width    int
	height   int
	scope    notesScope
	notes    []*models.NoteIndexEntry
	visible  []*models.NoteIndexEntry
	cursor   int
	preview  bool
	content  string
	loading  bool
	status   string
	creating bool
	newTitle string
	polling  bool
}

func (p *notesPage) Name() string  { return "Notes" }
func (p *notesPage) Title() string { return "󰎞 Notes · " + p.scope.String() }
func (p *notesPage) Init() tea.Cmd {
	p.loading, p.polling = true, true
	return tea.Batch(notesLoadCmd(p.s), notesPollCmd())
}
func (p *notesPage) SetSize(w, h int) { p.width, p.height = w, h }
func (p *notesPage) Focus() tea.Cmd {
	p.loading = true
	if !p.polling {
		p.polling = true
		return tea.Batch(notesLoadCmd(p.s), notesPollCmd())
	}
	return notesLoadCmd(p.s)
}
func (p *notesPage) Blur()                   {}
func (p *notesPage) IsTextEntryActive() bool { return p.creating }

func (p *notesPage) currentPlan() *orchestration.Plan {
	if p.s.statusModel != nil && p.s.statusModel.Plan != nil {
		return p.s.statusModel.Plan
	}
	return p.s.browserModel.CurrentPlan()
}

func (p *notesPage) applyScope() {
	planName := ""
	if plan := p.currentPlan(); plan != nil {
		planName = plan.Name
	}
	root := filepath.Clean(filepath.Dir(p.s.cfg.PlansDir)) + string(filepath.Separator)
	workspace := filepath.Base(filepath.Dir(filepath.Clean(p.s.cfg.PlansDir)))
	p.visible = p.visible[:0]
	for _, n := range p.notes {
		if n == nil || n.ContentDir == "plans" {
			continue
		}
		match := false
		switch p.scope {
		case notesThisPlan:
			match = planName != "" && orchestration.NoteLinkedToPlan(n.PlanRef, planName)
		case notesThisNotespace:
			match = n.Workspace == workspace || strings.HasPrefix(filepath.Clean(n.Path), root)
		case notesUnlinked:
			match = n.PlanRef == ""
		case notesAll:
			match = true
		}
		if match {
			p.visible = append(p.visible, n)
		}
	}
	sort.SliceStable(p.visible, func(i, j int) bool {
		if !p.visible[i].ModTime.Equal(p.visible[j].ModTime) {
			return p.visible[i].ModTime.After(p.visible[j].ModTime)
		}
		return p.visible[i].Path < p.visible[j].Path
	})
	if p.cursor >= len(p.visible) {
		p.cursor = len(p.visible) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	p.loadPreview()
}

func (p *notesPage) selected() *models.NoteIndexEntry {
	if p.cursor < 0 || p.cursor >= len(p.visible) {
		return nil
	}
	return p.visible[p.cursor]
}

func (p *notesPage) loadPreview() {
	p.content = ""
	if n := p.selected(); n != nil {
		b, err := os.ReadFile(n.Path)
		if err != nil {
			p.content = "Unable to preview: " + err.Error()
		} else {
			p.content = string(b)
		}
	}
}

func notesActionCmd(fn func() error) tea.Cmd {
	return func() tea.Msg { return notesActionMsg{err: fn()} }
}

func (p *notesPage) runAction(key string) tea.Cmd {
	n := p.selected()
	if n == nil {
		p.status = "No note selected"
		return nil
	}
	plan := p.currentPlan()
	switch key {
	case "p":
		if plan == nil {
			p.status = "Select a plan before promoting"
			return nil
		}
		return notesActionCmd(func() error { return exec.Command("nb", "promote", n.Path, "--plan", plan.Directory).Run() }) //nolint:gosec
	case "u":
		return notesActionCmd(func() error { return orchestration.ClearNoteLink(n.Path) })
	case "e":
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		parts := strings.Fields(editor)
		cmd := exec.Command(parts[0], append(parts[1:], n.Path)...)
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return notesActionMsg{err: err} })
	case "d":
		linkedPlan := strings.TrimPrefix(n.PlanRef, "plans/")
		if n.PlanJob == "" || linkedPlan == "" || linkedPlan == n.PlanRef || filepath.Base(linkedPlan) != linkedPlan {
			p.status = "Selected note is not linked to a job"
			return nil
		}
		// Demote the note's linked job, not a similarly named job in whichever
		// plan happens to be selected while viewing the all-notespaces scope.
		jobPath := filepath.Join(p.s.cfg.PlansDir, linkedPlan, filepath.Base(n.PlanJob))
		return notesActionCmd(func() error { return exec.Command("flow", "plan", "demote", jobPath).Run() }) //nolint:gosec
	}
	return nil
}

func (p *notesPage) Update(msg tea.Msg) (pager.Page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.SetSize(msg.Width, msg.Height)
	case notesLoadedMsg:
		p.loading = false
		if msg.err != nil {
			p.status = msg.err.Error()
		} else {
			p.notes = msg.notes
			p.status = ""
			p.applyScope()
		}
	case notesPollMsg:
		return p, tea.Batch(notesLoadCmd(p.s), notesPollCmd())
	case notesActionMsg:
		if msg.err != nil {
			p.status = msg.err.Error()
		} else {
			p.status = "Done"
		}
		return p, notesLoadCmd(p.s)
	case tea.KeyMsg:
		if p.creating {
			switch msg.String() {
			case "esc":
				p.creating, p.newTitle = false, ""
			case "backspace":
				if p.newTitle != "" {
					_, size := utf8.DecodeLastRuneInString(p.newTitle)
					p.newTitle = p.newTitle[:len(p.newTitle)-size]
				}
			case "enter":
				title := strings.TrimSpace(p.newTitle)
				p.creating, p.newTitle = false, ""
				plan := p.currentPlan()
				if title == "" || plan == nil {
					p.status = "A title and selected plan are required"
					return p, nil
				}
				return p, notesActionCmd(func() error { _, err := orchestration.CreatePlanNote(title, plan.Name, ""); return err })
			default:
				if msg.Type == tea.KeyRunes {
					p.newTitle += string(msg.Runes)
				}
			}
			return p, nil
		}
		switch msg.String() {
		case "j", "down":
			if p.cursor+1 < len(p.visible) {
				p.cursor++
				p.loadPreview()
			}
		case "k", "up":
			if p.cursor > 0 {
				p.cursor--
				p.loadPreview()
			}
		case "[":
			p.scope = (p.scope + 3) % 4
			p.applyScope()
		case "]", "s":
			p.scope = (p.scope + 1) % 4
			p.applyScope()
		case "enter":
			p.preview = !p.preview
			p.loadPreview()
		case "n":
			p.creating, p.newTitle = true, ""
		case "p", "u", "e", "d":
			return p, p.runAction(msg.String())
		case "r":
			p.loading = true
			return p, notesLoadCmd(p.s)
		}
	}
	return p, nil
}

func clipLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if max > 0 && len(lines) > max {
		lines = lines[:max]
	}
	return strings.Join(lines, "\n")
}

func (p *notesPage) View() string {
	if p.creating {
		return fmt.Sprintf("New linked note title: %s_\n\nenter create · esc cancel", p.newTitle)
	}
	if p.loading && len(p.notes) == 0 {
		return "Loading notes…"
	}
	if len(p.visible) == 0 {
		msg := "No notes in " + p.scope.String()
		if p.scope == notesThisPlan && p.currentPlan() == nil {
			msg = "Select a plan to see its notes"
		}
		if p.status != "" {
			msg += "\n\n" + p.status
		}
		return msg
	}
	listWidth := p.width
	if p.preview && p.width > 50 {
		listWidth = p.width / 3
	}
	rows := make([]string, 0, len(p.visible))
	maxRows := p.height - 3
	if maxRows < 1 {
		maxRows = len(p.visible)
	}
	start := 0
	if p.cursor >= maxRows {
		start = p.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(p.visible) {
		end = len(p.visible)
	}
	for i := start; i < end; i++ {
		n := p.visible[i]
		marker := "  "
		if i == p.cursor {
			marker = "› "
		}
		title := n.Title
		if title == "" {
			title = n.Name
		}
		link := ""
		if n.PlanRef != "" {
			link = "  " + n.PlanRef
			if n.PlanJob != "" {
				link += "/" + n.PlanJob
			}
		}
		row := marker + title + link
		if lipgloss.Width(row) > listWidth {
			row = lipgloss.NewStyle().MaxWidth(listWidth).Render(row)
		}
		if i == p.cursor {
			row = coretheme.DefaultTheme.Selected.Render(row)
		}
		rows = append(rows, row)
	}
	list := strings.Join(rows, "\n")
	if p.preview && p.width > 50 {
		previewWidth := p.width - listWidth - 3
		preview := clipLines(p.content, p.height-1)
		list = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(listWidth).Render(list), " │ ", lipgloss.NewStyle().Width(previewWidth).Render(preview))
	}
	footer := "s scope · enter preview · p promote · n new · u unlink · e edit · d demote · r refresh"
	if p.status != "" {
		footer = p.status + " · " + footer
	}
	return list + "\n\n" + coretheme.DefaultTheme.Muted.Render(footer)
}

func (p *notesPage) Footer() string { return "" }

var (
	_ pager.Page              = (*notesPage)(nil)
	_ pager.PageWithTitle     = (*notesPage)(nil)
	_ pager.PageWithTextInput = (*notesPage)(nil)
	_ pager.PageWithFooter    = (*notesPage)(nil)
)
