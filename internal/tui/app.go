// Package tui is the root Bubble Tea application: tabs, the read-only/edit
// badge, the status bar, and every tab body.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// ScanFn scans the host and libvirt connection into a fresh Snapshot plus
// per-VM raw domain configs (keyed by name, for drift checks later).
type ScanFn func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error)

// numTabs is len(tabNames), fixed as a const so it can size tabRanges.
const numTabs = 5

// tabNames are the tabs, in order. The active one is rendered wrapped in
// brackets, e.g. "[VMs]" -- keep that marker stable, since tests key off
// it.
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

	tab        int
	cursor     int    // selected core index (s.Topo.Cores) on the CPU Map tab
	vmSel      int    // selected row (s.VMs) on the VMs tab
	pendingSel int    // selected row (queue.Ops) on the Pending tab
	backupsSel int    // selected row on the Backups tab
	diffView   string // set by 'enter' on the Backups tab; non-empty shows it instead of the list
	editMode   bool
	queue      model.Queue
	snap       *model.Snapshot
	doms       map[string]*libvirtio.DomainConfig
	scanErr    error
	status     string
	confirm    *confirm
	wizard     *wizard
	memPicker  *memNodePicker
	flow       *applyFlow
	// flowGen guards against a stale driftCheckedMsg/applyDoneMsg landing
	// on the wrong round: it only ever increases (never reset per-flow),
	// so a leftover message from an earlier flow can never collide with a
	// later, unrelated one the way a counter restarting at zero on every
	// new applyFlow could.
	flowGen   int
	reopenVM  string // set by the drift screen's 'w' key; consumed by the next scanDoneMsg
	width     int
	height    int
	tabRanges [numTabs][2]int // X ranges recorded during the last render, row 0
	hits      []hit           // clickable body regions recorded during the last render
}

// bodyY0 is the number of lines rendered above the tab body in View: the
// tab row (row 0) and the header line.
const bodyY0 = 2

// hitWide is used as a hit's x1 for rows meant to be clickable across their
// whole width (VM/Pending/Backups/mem-node-picker rows), rather than one
// bounded to an exact rendered column span.
const hitWide = 1 << 20

// hit is one clickable body region recorded during the last render, in
// screen-absolute rows/columns (y0/x0 inclusive, y1/x1 exclusive). kind
// names which list/grid it belongs to; index is the row/core/node index
// within it, in the units that field's owner expects (e.g. a.vmSel for
// "vm", a.cursor for "core").
type hit struct {
	y0, y1, x0, x1 int
	kind           string
	index          int
}

// offsetHits returns a copy of hits with dy/dx added to every y0/y1/x0/x1,
// used to translate a nested screen's own 0-based rows/columns (e.g. a
// panel's content, offset by its own border and header lines) into
// screen-absolute ones.
func offsetHits(hits []hit, dy, dx int) []hit {
	out := make([]hit, len(hits))
	for i, h := range hits {
		h.y0 += dy
		h.y1 += dy
		h.x0 += dx
		h.x1 += dx
		out[i] = h
	}
	return out
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
		a.clampVMSel()
		if a.reopenVM != "" {
			vm := a.reopenVM
			a.reopenVM = ""
			a.openWizardFor(vm)
		}
		return a, nil
	case driftCheckedMsg:
		return a.handleDriftChecked(msg)
	case applyDoneMsg:
		return a.handleApplyDone(msg)
	case tea.MouseMsg:
		return a.handleMouse(msg)
	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return a, nil
}

// handleMouse routes a mouse event: only a left click on the tab row (row
// 0) does anything, and only when no modal/screen (confirm, wizard,
// mem-node picker, apply flow) is open.
func (a *App) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if a.confirm != nil || a.wizard != nil || a.memPicker != nil || a.flow != nil {
		return a, nil
	}
	if msg.Y != 0 || msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return a, nil
	}
	for i, rng := range a.tabRanges {
		if msg.X >= rng[0] && msg.X < rng[1] {
			if i != a.tab {
				a.tab = i
				a.status = ""
			}
			break
		}
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		// Hard quit from anywhere, including every modal/screen (confirm,
		// wizard, mem-node picker, apply flow, even flowRunning): a backup
		// is always written before any Define, and Define itself is
		// atomic, so there is nothing ctrl+c could leave half-written.
		// 'q' still asks first when ops are pending; ctrl+c never does.
		return a, tea.Quit
	}
	if a.confirm != nil {
		return a.handleConfirmKey(msg)
	}
	if a.wizard != nil {
		return a.handleWizardKey(msg)
	}
	if a.memPicker != nil {
		return a.handleMemNodeKey(msg)
	}
	if a.flow != nil {
		return a.handleFlowKey(msg)
	}
	if a.tab == 4 && a.diffView != "" {
		// Any key dismisses the diff view first, before any other handling
		// (tab-switch digits included) can see it -- otherwise a stale
		// diff reappears if the user later returns to the Backups tab
		// without having dismissed it (regression: see the test exercising
		// '1' while a diff is open). ctrl+c already returned above, so it
		// still quits instead of just closing the diff.
		a.diffView = ""
		return a, nil
	}

	switch {
	case isRune(msg, 'q'):
		return a.requestQuit()
	case isRune(msg, 'e'):
		a.toggleEdit()
		return a, nil
	case isRune(msg, 'r'):
		a.status = "rescanning..."
		return a, a.scanCmd()
	case isRune(msg, 'a'):
		return a.openApplyFlow()
	case msg.Type == tea.KeyTab:
		a.tab = (a.tab + 1) % len(tabNames)
		a.status = ""
		return a, nil
	case msg.Type == tea.KeyShiftTab:
		a.tab = (a.tab - 1 + len(tabNames)) % len(tabNames)
		a.status = ""
		return a, nil
	case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '5':
		if next := int(msg.Runes[0] - '1'); next != a.tab {
			a.tab = next
			a.status = ""
		}
		return a, nil
	case a.tab == 1 && (msg.Type == tea.KeyLeft || isRune(msg, 'h')):
		a.moveCursor(-1)
		return a, nil
	case a.tab == 1 && (msg.Type == tea.KeyRight || isRune(msg, 'l')):
		a.moveCursor(1)
		return a, nil
	case a.tab == 1 && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.moveCursor(-coresPerRow)
		return a, nil
	case a.tab == 1 && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.moveCursor(coresPerRow)
		return a, nil
	case a.tab == 2 && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.moveVMSel(-1)
		return a, nil
	case a.tab == 2 && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.moveVMSel(1)
		return a, nil
	case a.tab == 2 && isRune(msg, 'p'):
		return a.openWizard()
	case a.tab == 2 && isRune(msg, 's'):
		return a.stageStrip()
	case a.tab == 2 && isRune(msg, 'n'):
		return a.openMemNodePicker()
	case a.tab == 3 && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.movePendingSel(-1)
		return a, nil
	case a.tab == 3 && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.movePendingSel(1)
		return a, nil
	case a.tab == 3 && isRune(msg, 'x'):
		return a.removeSelectedPendingOp()
	case a.tab == 3 && isRune(msg, 'd'):
		return a.discardAllPending()
	case a.tab == 4 && (msg.Type == tea.KeyUp || msg.Type == tea.KeyDown || isRune(msg, 'j') || isRune(msg, 'k')):
		n := 0
		if entries, err := backup.List(a.backupDir); err == nil {
			n = len(entries)
		}
		a.handleBackupsKey(msg, &a.backupsSel, n)
		return a, nil
	case a.tab == 4 && msg.Type == tea.KeyEnter:
		if diff, err := a.backupsDiff(a.backupsSel); err != nil {
			a.status = err.Error()
		} else {
			a.diffView = diff
		}
		return a, nil
	case a.tab == 4 && isRune(msg, 'R'):
		a.status = a.stageRestore(a.backupsSel)
		return a, nil
	}
	return a, nil
}

// moveVMSel shifts the VMs tab selection by delta rows, clamped to
// [0, len(VMs)-1]. A no-op before the first scan or with no VMs.
func (a *App) moveVMSel(delta int) {
	if a.snap == nil || len(a.snap.VMs) == 0 {
		return
	}
	a.vmSel += delta
	a.clampVMSel()
}

// clampVMSel clamps a.vmSel into [0, len(VMs)-1] (0 with no VMs or before
// the first scan), so a rescan that shrinks or empties the VM list never
// leaves it pointing past the end.
func (a *App) clampVMSel() {
	if a.snap == nil || len(a.snap.VMs) == 0 {
		a.vmSel = 0
		return
	}
	if a.vmSel < 0 {
		a.vmSel = 0
	}
	if max := len(a.snap.VMs) - 1; a.vmSel > max {
		a.vmSel = max
	}
}

// selectedVM returns the VM at a.vmSel in the raw (unprojected) snapshot,
// or nil before the first scan or with no VMs. Name and Unsupported are
// identical between the raw and projected snapshots (Project never adds,
// removes, or renames VMs), so indexing the raw snapshot here stays in
// sync with the projected one the VMs tab renders.
func (a *App) selectedVM() *model.VM {
	if a.snap == nil || a.vmSel < 0 || a.vmSel >= len(a.snap.VMs) {
		return nil
	}
	return &a.snap.VMs[a.vmSel]
}

// gateVMAction implements the shared strip/wizard opening gates: edit mode
// must be on and the selected VM must be supported. Returns nil (and sets
// a.status) when the action should be refused.
func (a *App) gateVMAction() *model.VM {
	if !a.editMode {
		a.status = "press e to enter edit mode first"
		return nil
	}
	vm := a.selectedVM()
	if vm == nil {
		a.status = "no VM selected"
		return nil
	}
	if vm.Unsupported {
		a.status = fmt.Sprintf("%s: unsupported config, view only", vm.Name)
		return nil
	}
	return vm
}

// stageStrip implements the 's' key on the VMs tab: stages an OpStrip for
// the selected VM after the shared edit-mode/supported gates, fetching its
// current XML for StagedHash.
func (a *App) stageStrip() (tea.Model, tea.Cmd) {
	vm := a.gateVMAction()
	if vm == nil {
		return a, nil
	}
	xml, err := a.hv.DomainXML(vm.Name)
	if err != nil {
		a.status = fmt.Sprintf("%s: %v", vm.Name, err)
		return a, nil
	}
	op := model.PendingOp{
		Kind:       model.OpStrip,
		VM:         vm.Name,
		StagedHash: model.HashXML(xml),
		StagedXML:  xml,
		Summary:    vm.Name + ": remove all pinning and memory binding",
	}
	a.queue.Add(op)
	a.status = "staged: " + op.Summary
	return a, nil
}

// moveCursor shifts the CPU Map cursor by delta cores, clamped to
// [0, len(Cores)-1]. A no-op before the first scan or on a topology with no
// cores.
func (a *App) moveCursor(delta int) {
	if a.snap == nil || len(a.snap.Topo.Cores) == 0 {
		return
	}
	a.cursor += delta
	if a.cursor < 0 {
		a.cursor = 0
	}
	if max := len(a.snap.Topo.Cores) - 1; a.cursor > max {
		a.cursor = max
	}
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
	defer os.Remove(p) //nolint:errcheck // best-effort cleanup, write succeeded either way
	return nil
}

func isRune(msg tea.KeyMsg, r rune) bool {
	return msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == r
}

// View renders the full screen: tab row (row 0, hit-tested by mouse
// clicks), header, tab body, optional status message and confirm modal,
// then the status bar. Table/grid content never wraps (each render
// function truncates its own lines to fit); status/status-bar text is
// prose and wraps via lipgloss. A final pass truncates (never wraps) any
// residual line that still overflows, as a last-resort safety net.
func (a *App) View() string {
	var b strings.Builder
	b.WriteString(a.renderTabs())
	b.WriteString("\n")
	b.WriteString(a.renderHeader())
	b.WriteString("\n")
	body, hits := a.renderBody()
	a.hits = offsetHits(hits, bodyY0, 0)
	b.WriteString(body)
	b.WriteString("\n")
	if a.status != "" {
		b.WriteString(a.wrapProse(styleStatus(a.status)))
		b.WriteString("\n")
	}
	if a.confirm != nil {
		b.WriteString(panelWrap("Confirm", a.confirm.prompt+"\n\n[y]es  [n]/esc cancel", effectiveWidth(a.width)))
		b.WriteString("\n")
	}
	b.WriteString(a.wrapProse(a.renderStatusBar()))
	return a.clampWidth(b.String())
}

// wrapProse word-wraps s to a.width via lipgloss; a no-op when a.width is
// unset (<= 0).
func (a *App) wrapProse(s string) string {
	if a.width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(a.width).Render(s)
}

// clampWidth is View's last-resort safety net: truncates (never wraps) any
// line still wider than a.width. Every render function is expected to
// already fit on its own; this only catches what slips through (e.g. the
// tab bar on a pathologically narrow terminal). A no-op when a.width is
// unset (<= 0).
func (a *App) clampWidth(s string) string {
	if a.width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = ansiTruncate(l, a.width)
	}
	return strings.Join(lines, "\n")
}

// renderTabs renders the tab row (active tab as a filled, colored pill;
// inactive tabs dimmed) and records each label's X range (in visible
// columns) into a.tabRanges for mouse hit-testing.
func (a *App) renderTabs() string {
	var line strings.Builder
	x := 0
	for i, name := range tabNames {
		label := " " + name + " "
		if i == a.tab {
			label = "[" + name + "]"
		}

		style := tabInactiveStyle
		if i == a.tab {
			style = tabActiveStyle
		}
		rendered := style.Render(label)

		start := x
		x += lipgloss.Width(rendered)
		a.tabRanges[i] = [2]int{start, x}

		line.WriteString(rendered)
	}
	return line.String()
}

// renderHeader renders "pindergarten <version>  <uri>" (version and uri
// dimmed) with the read-only/edit badge right-aligned.
func (a *App) renderHeader() string {
	name := "pindergarten"
	rest := fmt.Sprintf(" %s  %s", a.version, a.hv.URI())
	title := lipgloss.NewStyle().Bold(true).Render(name) + keyBarLabelStyle.Render(rest)

	badge := "READ ONLY"
	badgeStyle := badgeReadOnlyStyle
	if a.editMode {
		badge = "EDIT"
		badgeStyle = badgeEditStyle
	}
	rendered := badgeStyle.Render(badge)

	w := effectiveWidth(a.width)
	gap := w - lipgloss.Width(title) - lipgloss.Width(rendered)
	if gap < 1 {
		gap = 1
	}
	return title + strings.Repeat(" ", gap) + rendered
}

// renderBody renders the active tab's body (or the wizard/apply-flow
// screen in its place) alongside its clickable regions (0-based, relative
// to the body's own top-left corner); it never panics ahead of the first
// scanDoneMsg.
func (a *App) renderBody() (string, []hit) {
	w := effectiveWidth(a.width)

	if a.snap == nil {
		if a.scanErr != nil {
			return fmt.Sprintf("scan error: %v", a.scanErr), nil
		}
		return "scanning...", nil
	}

	if a.wizard != nil {
		return a.wizard.view(w)
	}
	if a.memPicker != nil {
		return a.memPicker.view(w)
	}
	if a.flow != nil {
		return panelWrap(flowTitle(a.flow.screen), a.flow.view(w, a.height), w), nil
	}
	projected := model.Project(a.snap, a.doms, a.queue.Ops)
	switch a.tab {
	case 0:
		return renderOverviewTab(projected, w), nil
	case 1:
		return renderCPUMapTab(projected, a.cursor, w)
	case 2:
		return renderVMsTab(projected, a.vmSel, w)
	case 3:
		return renderPendingTab(a.queue, a.pendingSel, w)
	case 4:
		if a.diffView != "" {
			return panel("diff", colorDiff(a.diffView), w), nil
		}
		return a.renderBackupsTab(a.backupsSel, w)
	}
	return "", nil
}

// flowTitle names the apply-flow panel by its current screen.
func flowTitle(screen flowScreen) string {
	switch screen {
	case flowReview:
		return "Apply"
	case flowRunning:
		return "Working"
	case flowDrift:
		return "Drift Detected"
	case flowResults:
		return "Results"
	}
	return "Apply"
}

// renderStatusBar renders the bottom key bar: "N pending ops" followed by
// "[key] label" hints (bold key, dim label) for whatever's clickable/keyable
// right now. While a wizard/mem-node-picker/apply-flow screen is open, its
// own (unstyled) hint line replaces the default set.
func (a *App) renderStatusBar() string {
	pending := fmt.Sprintf("%d pending ops", a.queue.Len())

	switch {
	case a.wizard != nil:
		return statusBarStyle.Render(pending + "  " + a.wizard.statusBarHint())
	case a.memPicker != nil:
		return statusBarStyle.Render(pending + "  " + a.memPicker.statusBarHint())
	case a.flow != nil:
		return statusBarStyle.Render(pending + "  " + a.flow.statusBarHint())
	}

	var hints []keyHint
	if a.tab == 1 {
		hints = append(hints, keyHint{"arrows/hjkl", "move"})
	}
	if a.editMode && a.tab == 3 {
		hints = append(hints, keyHint{"x", "remove"})
	}
	if a.editMode && a.queue.Len() > 0 {
		hints = append(hints, keyHint{"a", "apply"})
	}
	if a.editMode && a.tab == 3 && a.queue.Len() > 0 {
		// 'd' (discard all) is only routed on the Pending tab.
		hints = append(hints, keyHint{"d", "discard"})
	}
	if a.editMode && a.tab == 2 {
		hints = append(hints, keyHint{"p", "pin"}, keyHint{"s", "strip"}, keyHint{"n", "mem-node"})
	}
	if a.tab == 4 {
		switch {
		case a.diffView != "":
			// Dismissing the diff isn't gated by edit mode either.
			hints = append(hints, keyHint{"any", "close"})
		case a.editMode:
			hints = append(hints, keyHint{"R", "restore"}, keyHint{"enter", "diff"})
		default:
			// 'enter' (show diff) isn't gated by edit mode; only R is.
			hints = append(hints, keyHint{"enter", "diff"})
		}
	}
	hints = append(hints, keyHint{"r", "rescan"}, keyHint{"e", "edit"}, keyHint{"q", "quit"})

	parts := make([]string, 0, len(hints)+1)
	parts = append(parts, keyBarLabelStyle.Render(pending))
	for _, h := range hints {
		parts = append(parts, renderKeyHint(h))
	}
	return strings.Join(parts, "  ")
}
