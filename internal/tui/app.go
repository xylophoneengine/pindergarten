// Package tui is the root Bubble Tea application: tabs, the read-only/edit
// badge, and the status bar. Tab bodies beyond one-line placeholders are
// filled in by later tasks.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// ScanFn scans the host and libvirt connection into a fresh Snapshot plus
// per-VM raw domain configs (keyed by name, for drift checks later).
type ScanFn func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error)

// numTabs is len(tabNames), fixed as a const so it can size tabRanges.
const numTabs = 5

// tabNames are the tabs, in order. The active one is rendered wrapped in
// brackets, e.g. "[VMs]" -- Tasks 12-14 must keep that marker stable since
// tests key off it.
var tabNames = [numTabs]string{"Overview", "CPU Map", "VMs", "Pending", "Backups"}

// confirm is a pending yes/no prompt. While set, Update handles only
// y/n/esc; yes runs the confirmed action and returns its Cmd.
type confirm struct {
	prompt string
	yes    func() tea.Cmd
}

// scanDoneMsg carries the result of a ScanFn run back into Update.
type scanDoneMsg struct {
	snap *model.Snapshot
	doms map[string]*libvirtio.DomainConfig
	err  error
}

// App is the root Bubble Tea model.
type App struct {
	hv        libvirtio.Hypervisor
	scan      ScanFn
	backupDir string
	version   string

	tab       int
	editMode  bool
	queue     model.Queue
	snap      *model.Snapshot
	doms      map[string]*libvirtio.DomainConfig
	scanErr   error
	status    string
	confirm   *confirm
	width     int
	height    int
	tabRanges [numTabs][2]int // X ranges recorded during the last render, row 0
}

var _ tea.Model = (*App)(nil)

// New constructs an App. Call its Init to kick off the first scan.
func New(hv libvirtio.Hypervisor, scan ScanFn, backupDir, version string) *App {
	return &App{hv: hv, scan: scan, backupDir: backupDir, version: version}
}

// Init issues the first scan.
func (a *App) Init() tea.Cmd {
	return a.scanCmd()
}

func (a *App) scanCmd() tea.Cmd {
	return func() tea.Msg {
		snap, doms, err := a.scan()
		return scanDoneMsg{snap: snap, doms: doms, err: err}
	}
}

// Update handles messages and routes key/mouse input.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil
	case scanDoneMsg:
		a.snap, a.doms, a.scanErr = msg.snap, msg.doms, msg.err
		if msg.err != nil {
			a.status = fmt.Sprintf("scan error: %v", msg.err)
		} else {
			a.status = ""
		}
		return a, nil
	case tea.MouseMsg:
		return a.handleMouse(msg)
	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return a, nil
}

func (a *App) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if a.confirm != nil {
		return a, nil
	}
	if msg.Y != 0 || msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return a, nil
	}
	for i, rng := range a.tabRanges {
		if msg.X >= rng[0] && msg.X < rng[1] {
			a.tab = i
			break
		}
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.confirm != nil {
		return a.handleConfirmKey(msg)
	}

	switch {
	case msg.Type == tea.KeyCtrlC, isRune(msg, 'q'):
		return a.requestQuit()
	case isRune(msg, 'e'):
		a.toggleEdit()
		return a, nil
	case isRune(msg, 'r'):
		a.status = "rescanning..."
		return a, a.scanCmd()
	case msg.Type == tea.KeyTab:
		a.tab = (a.tab + 1) % len(tabNames)
		return a, nil
	case msg.Type == tea.KeyShiftTab:
		a.tab = (a.tab - 1 + len(tabNames)) % len(tabNames)
		return a, nil
	case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '5':
		a.tab = int(msg.Runes[0] - '1')
		return a, nil
	}
	return a, nil
}

func (a *App) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case isRune(msg, 'y'):
		yes := a.confirm.yes
		a.confirm = nil
		return a, yes()
	case isRune(msg, 'n'), msg.Type == tea.KeyEsc:
		a.confirm = nil
		return a, nil
	}
	return a, nil
}

// requestQuit quits directly when the queue is empty, else asks for
// confirmation since applying is the only way to keep staged ops.
func (a *App) requestQuit() (tea.Model, tea.Cmd) {
	n := a.queue.Len()
	if n == 0 {
		return a, tea.Quit
	}
	a.confirm = &confirm{
		prompt: fmt.Sprintf("Discard %d pending ops and quit? [y/n]", n),
		yes:    func() tea.Cmd { return tea.Quit },
	}
	return a, nil
}

// toggleEdit implements the edit-mode toggle contract: leaving edit mode is
// blocked while ops are staged; entering it is blocked by a read-only
// connection or an unwritable backup dir, else confirmed.
func (a *App) toggleEdit() {
	if a.editMode {
		if a.queue.Len() == 0 {
			a.editMode = false
			a.status = ""
		} else {
			a.status = "discard or apply pending ops first"
		}
		return
	}

	if ro, reason := a.hv.ReadOnly(); ro {
		a.status = reason
		return
	}
	if err := probeWritable(a.backupDir); err != nil {
		a.status = fmt.Sprintf("backup dir not writable: %v", err)
		return
	}

	a.confirm = &confirm{
		prompt: "Enable edit mode? [y/n]",
		yes: func() tea.Cmd {
			a.editMode = true
			a.status = ""
			return nil
		},
	}
}

// probeWritable reports whether dir accepts a write, by creating and
// removing a probe file in it.
func probeWritable(dir string) error {
	p := filepath.Join(dir, ".probe")
	if err := os.WriteFile(p, []byte("probe"), 0o600); err != nil {
		return err
	}
	return os.Remove(p)
}

func isRune(msg tea.KeyMsg, r rune) bool {
	return msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == r
}

// View renders the full screen: tab row (row 0, hit-tested by mouse
// clicks), header, tab body, optional status message and confirm modal,
// then the status bar.
func (a *App) View() string {
	var b strings.Builder
	b.WriteString(a.renderTabs())
	b.WriteString("\n")
	b.WriteString(a.renderHeader())
	b.WriteString("\n")
	b.WriteString(a.renderBody())
	b.WriteString("\n")
	if a.status != "" {
		b.WriteString(a.status)
		b.WriteString("\n")
	}
	if a.confirm != nil {
		b.WriteString(modalStyle.Render(a.confirm.prompt))
		b.WriteString("\n")
	}
	b.WriteString(a.renderStatusBar())
	return b.String()
}

// renderTabs renders the tab row and records each label's X range (in
// visible columns) into a.tabRanges for mouse hit-testing.
func (a *App) renderTabs() string {
	var line strings.Builder
	x := 0
	for i, name := range tabNames {
		label := " " + name + " "
		if i == a.tab {
			label = "[" + name + "]"
		}

		start := x
		x += len(label)
		a.tabRanges[i] = [2]int{start, x}

		style := tabInactiveStyle
		if i == a.tab {
			style = tabActiveStyle
		}
		line.WriteString(style.Render(label))
	}
	return line.String()
}

func (a *App) renderHeader() string {
	title := fmt.Sprintf("pindergarten %s  %s", a.version, a.hv.URI())

	badge := "READ ONLY"
	badgeStyle := badgeReadOnlyStyle
	if a.editMode {
		badge = "EDIT"
		badgeStyle = badgeEditStyle
	}
	rendered := badgeStyle.Render(badge)

	gap := a.width - len(title) - len(badge)
	if gap < 1 {
		gap = 1
	}
	return title + strings.Repeat(" ", gap) + rendered
}

// renderBody renders the active tab's body. Tabs 0-4 are placeholders here
// (Tasks 12-14 replace Overview/CPU Map/VMs/Pending/Backups with real
// views); it never panics ahead of the first scanDoneMsg.
func (a *App) renderBody() string {
	if a.snap == nil {
		if a.scanErr != nil {
			return fmt.Sprintf("scan error: %v", a.scanErr)
		}
		return "scanning..."
	}

	projected := model.Project(a.snap, a.doms, a.queue.Ops)
	switch a.tab {
	case 0:
		return fmt.Sprintf("Overview: %d VMs, %d nodes", len(projected.VMs), len(projected.Topo.Nodes))
	case 1:
		return fmt.Sprintf("CPU Map: %d nodes", len(projected.Topo.Nodes))
	case 2:
		return fmt.Sprintf("VMs: %d", len(projected.VMs))
	case 3:
		return fmt.Sprintf("(pending ops: %d)", a.queue.Len())
	case 4:
		return "(no backups)"
	}
	return ""
}

func (a *App) renderStatusBar() string {
	parts := []string{fmt.Sprintf("%d pending ops", a.queue.Len())}
	if a.editMode && a.queue.Len() > 0 {
		parts = append(parts, "[a]pply", "[d]iscard")
	}
	parts = append(parts, "[e]dit", "[q]uit")
	return statusBarStyle.Render(strings.Join(parts, "  "))
}
