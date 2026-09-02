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

	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// ScanFn scans the host and libvirt connection into a fresh Snapshot plus
// per-VM raw domain configs (keyed by name, for drift checks later).
type ScanFn func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error)

// numTabs is len(tabNames), fixed as a const so it can size tabRanges.
const numTabs = 6

// Tab indices, in on-screen order -- named so a.tab comparisons/switches
// read by intent instead of a bare number that silently rots the moment
// tabNames/the digit-key order changes underneath it (as happened when
// Topology moved from last to second: every literal "5" meaning Topology,
// and every literal "1" meaning CPU Map, had to be found and updated by
// hand). Digit keys ('1'-'6' in handleKey) still map positionally off
// tabNames itself, so they need no changes here at all.
const (
	tabOverview = iota
	tabTopology
	tabCPUMap
	tabVMs
	tabPending
	tabBackups
)

// tabNames are the tabs, in order (see the tab* constants above). The
// active one renders as a filled accent pill (see renderTabs/
// tabActiveStyle); tests check which is active via a.tab / activeTabName,
// not by grepping a text marker.
var tabNames = [numTabs]string{"Overview", "Topology", "CPU Map", "VMs", "Pending", "Backups"}

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

	tab            int
	cursor         int    // selected core index (s.Topo.Cores) on the CPU Map tab
	vmSel          int    // selected row (s.VMs) on the VMs tab
	pendingSel     int    // selected row (queue.Ops) on the Pending tab
	backupsSel     int    // selected row on the Backups tab
	overviewScroll int    // first NUMA node card shown on the Overview tab, when stacked; up/down/wheel-driven
	topologyScroll int    // scroll offset into the Topology tab's drawing; up/down/wheel-driven
	diffView       string // set by 'enter' on the Backups tab; non-empty shows it instead of the list
	help           bool   // toggled by '?'/F1; any other key closes it again
	helpScroll     int    // scroll offset into the help overlay; up/down/wheel-driven, reset on close
	editMode       bool
	queue          model.Queue
	snap           *model.Snapshot
	doms           map[string]*libvirtio.DomainConfig
	scanErr        error
	status         string
	confirm        *confirm
	wizard         *wizard
	memPicker      *memNodePicker
	flow           *applyFlow
	// flowGen guards against a stale driftCheckedMsg/applyDoneMsg landing
	// on the wrong round: it only ever increases (never reset per-flow),
	// so a leftover message from an earlier flow can never collide with a
	// later, unrelated one the way a counter restarting at zero on every
	// new applyFlow could.
	flowGen    int
	reopenVM   string // set by the drift screen's 'w' key; consumed by the next scanDoneMsg
	width      int
	height     int
	tabRanges  [numTabs][2]int // X ranges recorded during the last render, row 0
	hits       []hit           // clickable body regions recorded during the last render
	diffScroll int             // scroll offset into a.diffView, the Backups tab's long-text diff

	// diffCache/diffCacheWidth/diffCacheFor memoize colorDiff+truncateLines
	// over a.diffView: both renderDiffView and clampDiffScroll need those
	// lines every wheel tick, and colorDiff walks the whole diff, so
	// recomputing it on every scroll would make scrolling a large diff
	// janky. Recomputed only when the diff text or the width it was wrapped
	// at has changed (diffCacheFor holds the text itself, not just a dirty
	// flag, since the cache is still valid whenever a new diff happens to
	// have identical text). Cleared on dismissal.
	diffCache      []string
	diffCacheWidth int
	diffCacheFor   string
}

// bodyY0 is the number of lines rendered above the tab body in View: the
// tab row (row 0) and the header line.
const bodyY0 = 2

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

// wheelDelta reports whether msg is a mouse wheel event and, if so, the
// selection delta it maps to (-1 up, +1 down) -- mirroring the up/down key.
func wheelDelta(msg tea.MouseMsg) (delta int, ok bool) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return -1, true
	case tea.MouseButtonWheelDown:
		return 1, true
	}
	return 0, false
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
		a.clampOverviewScroll()
		a.clampTopologyScroll()
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

// handleMouse routes a mouse event. A wheel event is handled first and
// uniformly (it works even over the confirm/wizard/mem-node-picker/
// apply-flow screens, on whatever that screen's own scrollable content
// is); everything else -- a confirm modal or the apply flow swallow every
// click (nothing else there is clickable); an open wizard or mem-node
// picker route to their own click handling instead of the tab bar/body
// below. A left click on row 0 switches tabs, and a left click elsewhere
// is hit-tested against a.hits (refreshed by the last View call).
func (a *App) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if a.tooSmall() {
		return a, nil
	}
	if delta, ok := wheelDelta(msg); ok {
		a.scrollWheel(delta)
		return a, nil
	}
	if a.help || a.confirm != nil || a.flow != nil {
		return a, nil
	}
	if a.wizard != nil {
		a.handleWizardMouse(msg)
		return a, nil
	}
	if a.memPicker != nil {
		a.handleMemNodeMouse(msg)
		return a, nil
	}

	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return a, nil
	}
	if msg.Y == 0 {
		for i, rng := range a.tabRanges {
			if msg.X >= rng[0] && msg.X < rng[1] {
				if i != a.tab {
					a.tab = i
					a.status = ""
				}
				return a, nil
			}
		}
		return a, nil
	}
	a.handleBodyClick(msg)
	return a, nil
}

// scrollWheel implements the mouse-wheel-as-up/down-key contract: on the
// drift screen it moves the selected drifted op; on the results screen (or
// the Backups tab's diff view) it scrolls that long-text view; a confirm
// modal has nothing scrollable; otherwise it falls through to whichever
// tab/screen is active.
func (a *App) scrollWheel(delta int) {
	if a.help {
		a.helpScroll += delta
		a.clampHelpScroll()
		return
	}
	if a.confirm != nil {
		return
	}
	if a.flow != nil {
		switch a.flow.screen {
		case flowDrift:
			a.moveFlowSel(delta)
		case flowResults:
			a.flow.resultsScroll += delta
			a.clampResultsScroll()
		}
		return
	}
	if a.wizard != nil || a.memPicker != nil {
		return
	}
	if a.tab == tabBackups && a.diffView != "" {
		a.diffScroll += delta
		a.clampDiffScroll()
		return
	}
	a.scrollActive(delta)
}

// scrollActive implements the mouse-wheel-as-up/down-key contract for
// whichever tab is active (only reached once the apply flow, diff view,
// wizard, and mem-node picker have all had their own say -- see
// scrollWheel).
func (a *App) scrollActive(delta int) {
	switch a.tab {
	case tabOverview:
		a.overviewScroll += delta
		a.clampOverviewScroll()
	case tabCPUMap:
		a.moveCursor(delta * coresPerRow)
	case tabVMs:
		a.moveVMSel(delta)
	case tabPending:
		a.movePendingSel(delta)
	case tabBackups:
		kt := tea.KeyDown
		if delta < 0 {
			kt = tea.KeyUp
		}
		a.handleBackupsKey(tea.KeyMsg{Type: kt}, &a.backupsSel, a.backupsCount())
	case tabTopology:
		a.topologyScroll += delta
		a.clampTopologyScroll()
	}
}

// handleBodyClick finds the recorded hit (if any) under msg's position and
// applies it to the matching selection field.
func (a *App) handleBodyClick(msg tea.MouseMsg) {
	for _, h := range a.hits {
		if msg.Y >= h.y0 && msg.Y < h.y1 && msg.X >= h.x0 && msg.X < h.x1 {
			switch h.kind {
			case "vm":
				a.vmSel = h.index
			case "pending":
				a.pendingSel = h.index
			case "backup":
				a.backupsSel = h.index
			case "core":
				a.cursor = h.index
			case "topocore":
				// A click on the Topology tab's core box jumps to the
				// same core on the CPU Map tab, per the brief.
				a.tab = tabCPUMap
				a.cursor = h.index
			}
			return
		}
	}
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
	if a.help {
		// up/down/j/k scroll the (possibly long) key list; any other key
		// closes the overlay, not just esc/'?' -- it's a pure reference
		// popup with nothing else to confirm or select.
		switch {
		case msg.Type == tea.KeyUp, isRune(msg, 'k'):
			a.helpScroll--
			a.clampHelpScroll()
		case msg.Type == tea.KeyDown, isRune(msg, 'j'):
			a.helpScroll++
			a.clampHelpScroll()
		default:
			a.help = false
			a.helpScroll = 0
		}
		return a, nil
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
	if a.tab == tabBackups && a.diffView != "" {
		// up/down/j/k scroll a long diff (it can be hundreds of lines);
		// any other key dismisses it, before any other handling (tab-switch
		// digits included) can see it -- otherwise a stale diff reappears
		// if the user later returns to the Backups tab without having
		// dismissed it (regression: see the test exercising '1' while a
		// diff is open). ctrl+c already returned above, so it still quits
		// instead of just closing the diff.
		switch {
		case msg.Type == tea.KeyUp, isRune(msg, 'k'):
			a.diffScroll--
			a.clampDiffScroll()
		case msg.Type == tea.KeyDown, isRune(msg, 'j'):
			a.diffScroll++
			a.clampDiffScroll()
		default:
			a.diffView = ""
			a.diffScroll = 0
			a.diffCache = nil
			a.diffCacheFor = ""
		}
		return a, nil
	}

	switch {
	case isRune(msg, '?'), msg.Type == tea.KeyF1:
		a.help = true
		a.helpScroll = 0
		return a, nil
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
	case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '6':
		if next := int(msg.Runes[0] - '1'); next != a.tab {
			a.tab = next
			a.status = ""
		}
		return a, nil
	case a.tab == tabOverview && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.overviewScroll--
		a.clampOverviewScroll()
		return a, nil
	case a.tab == tabOverview && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.overviewScroll++
		a.clampOverviewScroll()
		return a, nil
	case a.tab == tabCPUMap && (msg.Type == tea.KeyLeft || isRune(msg, 'h')):
		a.moveCursor(-1)
		return a, nil
	case a.tab == tabCPUMap && (msg.Type == tea.KeyRight || isRune(msg, 'l')):
		a.moveCursor(1)
		return a, nil
	case a.tab == tabCPUMap && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.moveCursor(-coresPerRow)
		return a, nil
	case a.tab == tabCPUMap && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.moveCursor(coresPerRow)
		return a, nil
	case a.tab == tabVMs && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.moveVMSel(-1)
		return a, nil
	case a.tab == tabVMs && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.moveVMSel(1)
		return a, nil
	case a.tab == tabVMs && isRune(msg, 'p'):
		return a.openWizard()
	case a.tab == tabVMs && isRune(msg, 's'):
		return a.stageStrip()
	case a.tab == tabVMs && isRune(msg, 'n'):
		return a.openMemNodePicker()
	case a.tab == tabPending && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.movePendingSel(-1)
		return a, nil
	case a.tab == tabPending && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.movePendingSel(1)
		return a, nil
	case a.tab == tabPending && isRune(msg, 'x'):
		return a.removeSelectedPendingOp()
	case a.tab == tabPending && isRune(msg, 'd'):
		return a.discardAllPending()
	case a.tab == tabBackups && (msg.Type == tea.KeyUp || msg.Type == tea.KeyDown || isRune(msg, 'j') || isRune(msg, 'k')):
		a.handleBackupsKey(msg, &a.backupsSel, a.backupsCount())
		return a, nil
	case a.tab == tabBackups && msg.Type == tea.KeyEnter:
		if diff, err := a.backupsDiff(a.backupsSel); err != nil {
			a.status = err.Error()
		} else {
			a.diffView = diff
			a.diffScroll = 0
		}
		return a, nil
	case a.tab == tabBackups && isRune(msg, 'R'):
		a.status = a.stageRestore(a.backupsSel)
		return a, nil
	case a.tab == tabTopology && (msg.Type == tea.KeyUp || isRune(msg, 'k')):
		a.topologyScroll--
		a.clampTopologyScroll()
		return a, nil
	case a.tab == tabTopology && (msg.Type == tea.KeyDown || isRune(msg, 'j')):
		a.topologyScroll++
		a.clampTopologyScroll()
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

// overviewSideBySide reports whether the Overview tab's node cards
// currently lay out side by side (at a.width) rather than stacked --
// there's nothing to scroll in that layout, so both clampOverviewScroll
// and the key bar's scroll hint gate on this.
func (a *App) overviewSideBySide() bool {
	if a.snap == nil {
		return false
	}
	_, sideBySide := equalSplit(effectiveWidth(a.width), len(a.snap.Topo.Nodes), sideCardMinWidth)
	return sideBySide
}

// clampOverviewScroll clamps a.overviewScroll: 0 outright with no nodes,
// before the first scan, or while the cards lay out side by side
// (nothing to scroll there); else to maxStackedScroll's bound, the
// largest start index at which the remaining cards still fill the
// budget (mirroring scrollWindow's own rule) -- so scrolling past the
// point where every remaining card already fits doesn't manufacture a
// "+N more nodes" footer out of an already-complete view. Writes the
// clamped value back immediately (same pattern as clampDiffScroll/
// clampResultsScroll) so an over-scroll doesn't leave the next up/
// wheel-up producing no visible change until several presses "catch up".
func (a *App) clampOverviewScroll() {
	if a.snap == nil || len(a.snap.Topo.Nodes) == 0 {
		a.overviewScroll = 0
		return
	}
	if a.overviewScroll < 0 {
		a.overviewScroll = 0
	}
	if a.overviewSideBySide() {
		a.overviewScroll = 0
		return
	}

	projected := model.Project(a.snap, a.doms, a.queue.Ops)
	cardWidths, _ := equalSplit(effectiveWidth(a.width), len(projected.Topo.Nodes), sideCardMinWidth)
	heights := make([]int, len(projected.Topo.Nodes))
	for i, node := range projected.Topo.Nodes {
		heights[i] = lineCount(overviewNodeCard(projected, node, cardWidths[i])) + 2 // borders
	}
	_, _, _, _, chrome := a.renderChrome()
	_, cardsBudget, _ := overviewCardsLayout(projected, effectiveWidth(a.width), a.bodyBudget(chrome))
	if max := maxStackedScroll(heights, cardsBudget); a.overviewScroll > max {
		a.overviewScroll = max
	}
}

// clampTopologyScroll re-clamps a.topologyScroll to the Topology tab's
// actual valid range at the current width/body-budget, writing the
// clamped value back -- same pattern as clampDiffScroll/
// clampOverviewScroll, so an over-scrolled offset doesn't leave the next
// up/wheel-up producing no visible change until several presses "catch
// up" to render-time-only clamping.
func (a *App) clampTopologyScroll() {
	if a.snap == nil {
		a.topologyScroll = 0
		return
	}
	if a.topologyScroll < 0 {
		a.topologyScroll = 0
	}
	projected := model.Project(a.snap, a.doms, a.queue.Ops)
	_, _, _, _, chrome := a.renderChrome()
	budget := a.bodyBudget(chrome)
	inner, _ := topologyInner(projected, effectiveWidth(a.width))
	total := lineCount(inner) + 2 // the machine box's own border rows, added back by renderTopologyTab
	a.topologyScroll = clampScroll(a.topologyScroll, budget, total)
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
		prompt: fmt.Sprintf("Discard %s and quit? [y/n]", pluralize(n, "pending op")),
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

// Minimum terminal size below which the layout cannot be drawn honestly.
// Like btop, a smaller window shows only a resize notice instead of a
// half-clipped UI.
const (
	minWidth  = 80
	minHeight = 16
)

// tooSmall reports whether the terminal is below the minimum size. An
// unknown size (before the first WindowSizeMsg) is not too small.
func (a *App) tooSmall() bool {
	return (a.width > 0 && a.width < minWidth) || (a.height > 0 && a.height < minHeight)
}

// tooSmallView is the whole screen while the terminal is undersized.
// clampHeight/clampWidth guard it exactly like renderFull's own output --
// the notice's own text is fixed, but a terminal so small even placing it
// centered could in principle overflow (e.g. narrower than the longest
// line), and this is the only other thing View ever returns.
func (a *App) tooSmallView() string {
	msg := fmt.Sprintf("terminal too small: %dx%d, need at least %dx%d\nenlarge the window or zoom out (ctrl+-)\n[q] quit",
		a.width, a.height, minWidth, minHeight)
	msg = warningStyle.Render(msg)
	if a.width > 0 && a.height > 0 {
		msg = lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center, msg)
	}
	return a.clampHeight(a.clampWidth(msg))
}

// View renders the full screen: tab row (row 0, hit-tested by mouse
// clicks), header, tab body, optional status message, then the status
// bar -- any open dialog (confirm, wizard, mem-node picker, apply flow) is
// composited on top afterward, via renderDialog/overlay. Table/grid
// content never wraps (each render function truncates its own lines to
// fit); status/status-bar text is prose and wraps via lipgloss. Chrome --
// the tab row, header, status line, and key bar -- is assembled first, by
// renderChrome, which shrinks it (dropping the status line) until it fits
// a.height on its own, never touching the key bar. The body then gets
// whatever's left (bodyBudget), always the full remaining height (every
// render function fills it -- see panelH/panelWrapH's fill option), and
// is force-clipped to exactly that via clipLinesTo regardless of what it
// rendered internally (e.g. a bordered panel's own height floor) -- so
// the body can never again push chrome past a.height the way an
// unclamped, body-first render used to (bubbletea drops overflow from the
// *top*, which silently shifted the tab row off row 0 and threw every
// recorded mouse-hit y off by the same amount). clampHeight/clampWidth
// remain a final safety net for whatever still slips through (e.g. a
// pathologically short a.height that can't even fit chrome alone).
func (a *App) View() string {
	if a.tooSmall() {
		a.hits = nil
		return a.tooSmallView()
	}
	return a.renderFull()
}

// renderFull draws the complete UI regardless of terminal size. View gates
// it behind the minimum size; tests exercise the layout at small sizes
// through it directly.
func (a *App) renderFull() string {
	tabs, header, statusLine, keyBar, chrome := a.renderChrome()
	w := effectiveWidth(a.width)
	budget := a.bodyBudget(chrome)

	body, hits := a.renderBody(budget)
	body = clipLinesTo(body, budget)

	// Any open dialog is composited on top of the (always fully, normally
	// rendered -- its own layout/scroll state never changes) body, centered
	// within it, rather than replacing it: this is what keeps the body
	// looking like a proper popup overlay instead of a prompt shifting the
	// underlying tab's own content around. Body hits are discarded while a
	// dialog is open -- a click outside it does nothing -- since only the
	// dialog's own hits (if any), offset by the overlay's placement, are
	// meaningful then.
	if dialog, dHits, ok := a.renderDialog(w, budget); ok {
		x, y := centerXY(lipgloss.Width(dialog), lineCount(dialog), w, budget)
		body = overlay(body, dialog, x, y)
		a.hits = offsetHits(dHits, bodyY0+y, x)
	} else {
		a.hits = offsetHits(hits, bodyY0, 0)
	}

	var b strings.Builder
	b.WriteString(tabs)
	b.WriteString("\n")
	b.WriteString(header)
	b.WriteString("\n")
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	if statusLine != "" {
		b.WriteString(statusLine)
		b.WriteString("\n")
	}
	b.WriteString(keyBar)
	return a.clampHeight(a.clampWidth(b.String()))
}

// lineCount reports how many visual lines s spans: 0 for an empty string,
// else its newline count + 1.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// renderChrome renders every part of View() apart from the tab body: the
// tab row, header, status line, and key bar -- plus their total line
// count, so View() and the diff/results scroll clamps (which need to
// re-derive the exact body budget View() will use) share one
// implementation. Any open dialog is no longer part of chrome at all (see
// renderDialog/overlay) -- it's composited onto the body afterward and
// clamped to the body's own budget instead. The tab row, header, and key
// bar are a fixed cost -- never shrunk; when they plus the status line
// don't fit a.height, the status line is dropped.
func (a *App) renderChrome() (tabs, header, statusLine, keyBar string, lines int) {
	tabs = a.renderTabs()
	header = a.renderHeader()
	// renderStatusBar does its own width-aware layout (dropping/wrapping
	// on whole "[key] label" tokens) -- wrapping it again here via
	// wrapProse would risk lipgloss's own naive word-wrap splitting a
	// token's key from its label after all.
	keyBar = a.renderStatusBar()
	fixed := 2 + lineCount(keyBar) // tab row + header + key bar

	if a.status != "" {
		statusLine = a.wrapProse(styleStatus(a.status))
	}

	lines = fixed + lineCount(statusLine)
	if a.height > 0 && lines > a.height {
		statusLine = ""
		lines = fixed
	}
	return
}

// renderDialog renders whichever modal (confirm, wizard, mem-node picker,
// apply flow) is currently open as a single self-contained panel, tight
// to its own content -- no centering baked in, any hits 0-based relative
// to its own top-left corner -- clamped to at most dw = dialogWidth(w,
// ...) wide (sized to its own content when it has an opinion, e.g. the
// confirm prompt; dialogMaxWidth itself, i.e. "as wide as available",
// when it doesn't -- the wizard/mem-node-picker/apply-flow screens want
// all the room they can get for their own grid/prose) and budget lines
// tall (the same budget the tab body renders into, so a dialog never
// grows past the body's own footprint; none of these stretch to fill it
// the way a body panel does -- a popup should stay its own natural size).
// renderFull composites the result via overlay, computing the centering
// offset itself so every dialog type shares one placement rule instead of
// each rolling its own. ok is false when nothing is open.
func (a *App) renderDialog(w, budget int) (panel string, hits []hit, ok bool) {
	switch {
	case a.help:
		dw := dialogWidth(w, dialogMaxWidth)
		return helpPanel(dw, budget, a.helpScroll), nil, true
	case a.confirm != nil:
		body := a.confirm.prompt + "\n\n[y]es  [n]/esc cancel"
		dw := dialogWidth(w, maxLineWidth(body))
		panel, _ = panelWrapH("Confirm", body, dw, budget, false)
		return panel, nil, true
	case a.wizard != nil:
		dw := dialogWidth(w, dialogMaxWidth)
		p, h := a.wizard.view(dw, budget)
		return p, h, true
	case a.memPicker != nil:
		dw := dialogWidth(w, dialogMaxWidth)
		p, h := a.memPicker.view(dw, budget)
		return p, h, true
	case a.flow != nil:
		dw := dialogWidth(w, dialogMaxWidth)
		return a.renderFlowPanel(dw, budget), nil, true
	}
	return "", nil, false
}

// clampDiffScroll re-clamps a.diffScroll to the Backups tab's diff view's
// actual valid range at the current width/body-budget, writing the
// clamped value back -- so an over-scrolled offset (e.g. from many
// wheel-downs past the end) doesn't leave the *next* up/wheel-up
// producing no visible change until several presses "catch up" to
// render-time-only clamping.
func (a *App) clampDiffScroll() {
	if a.diffView == "" {
		return
	}
	w := effectiveWidth(a.width)
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	total := len(a.diffLinesCached(inner))
	_, _, _, _, chrome := a.renderChrome()
	budget := a.bodyBudget(chrome) - 2
	a.diffScroll = clampScroll(a.diffScroll, footerBudget(total, budget), total)
}

// clampHelpScroll re-clamps a.helpScroll to the help overlay's actual
// valid range at the current width/body-budget, writing the clamped
// value back -- same pattern as clampDiffScroll, so an over-scrolled
// offset doesn't leave the next up/wheel-up producing no visible change
// until several presses "catch up" to render-time-only clamping.
func (a *App) clampHelpScroll() {
	if !a.help {
		return
	}
	w := effectiveWidth(a.width)
	dw := dialogWidth(w, dialogMaxWidth)
	inner := dw - 2
	if inner < 1 {
		inner = 1
	}
	total := len(helpLines(inner))
	_, _, _, _, chrome := a.renderChrome()
	budget := a.bodyBudget(chrome) - 2
	a.helpScroll = clampScroll(a.helpScroll, footerBudget(total, budget), total)
}

// diffLinesCached returns a.diffView colored (colorDiff) and truncated to
// inner columns, one entry per line, recomputing only when the diff text
// or inner width has changed since the last call -- colorDiff walks the
// whole diff, and both renderDiffView and clampDiffScroll need this on
// every wheel tick, so a large diff would otherwise make scrolling janky.
func (a *App) diffLinesCached(inner int) []string {
	if a.diffCacheFor != a.diffView || a.diffCacheWidth != inner {
		a.diffCache = strings.Split(truncateLines(colorDiff(a.diffView), inner), "\n")
		a.diffCacheWidth = inner
		a.diffCacheFor = a.diffView
	}
	return a.diffCache
}

// clampResultsScroll is clampDiffScroll's counterpart for the apply flow's
// results screen.
func (a *App) clampResultsScroll() {
	if a.flow == nil || a.flow.screen != flowResults {
		return
	}
	w := effectiveWidth(a.width)
	dw := dialogWidth(w, dialogMaxWidth)
	inner := dw - 2
	if inner < 1 {
		inner = 1
	}
	_, _, _, _, chrome := a.renderChrome()
	budget := a.bodyBudget(chrome)
	total := lineCount(lipgloss.NewStyle().Width(inner).Render(a.flow.view(dw, budget)))
	a.flow.resultsScroll = clampScroll(a.flow.resultsScroll, footerBudget(total, budget-2), total)
}

// bodyBudget returns how many lines the tab body may use: a.height minus
// chrome (the tab row, header, status line, and key bar -- see
// renderChrome; an open dialog is no longer part of chrome, see
// renderDialog), floored at 0 (never negative) since chrome always wins
// the remaining space. a.height of 0 (before the first WindowSizeMsg) is
// treated as unbounded, matching effectiveWidth's own convention -- there's
// nothing sane to budget against yet.
func (a *App) bodyBudget(chrome int) int {
	if a.height <= 0 {
		return fallbackHeight
	}
	budget := a.height - chrome
	if budget < 0 {
		budget = 0
	}
	return budget
}

// clampHeight is View's last-resort safety net: once every render function
// has done its own height-budgeting, this only catches what slips through
// (e.g. an Overview with far more NUMA nodes than fit) by dropping any
// trailing lines beyond a.height. A no-op when a.height is unset (<= 0).
func (a *App) clampHeight(s string) string {
	if a.height <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > a.height {
		lines = lines[:a.height]
	}
	return strings.Join(lines, "\n")
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
		style := tabInactiveStyle
		if i == a.tab {
			style = tabActiveStyle
		}
		rendered := style.Render(name)

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

// renderBody renders the active tab's own body alongside its clickable
// regions (0-based, relative to the body's own top-left corner); it never
// panics ahead of the first scanDoneMsg. budget is how many lines the
// body may use (see bodyBudget); every render function fills it exactly
// (via panelH/panelWrapH's fill option). This always renders the plain
// tab underneath, even while a dialog (confirm/wizard/mem-node picker/
// apply flow) is open -- renderFull composites that on top separately, via
// renderDialog/overlay -- so the active tab's own layout and scroll
// position never change just because a dialog happens to be open over it.
func (a *App) renderBody(budget int) (string, []hit) {
	w := effectiveWidth(a.width)

	if a.snap == nil {
		msg := "scanning..."
		if a.scanErr != nil {
			msg = fmt.Sprintf("scan error: %v", a.scanErr)
		}
		return padLinesTo(msg, budget), nil
	}

	projected := model.Project(a.snap, a.doms, a.queue.Ops)
	switch a.tab {
	case tabOverview:
		return renderOverviewTab(projected, w, budget, a.overviewScroll), nil
	case tabCPUMap:
		return renderCPUMapTab(projected, a.cursor, w, budget)
	case tabVMs:
		return renderVMsTab(projected, a.vmSel, w, budget)
	case tabPending:
		return renderPendingTab(a.queue, a.pendingSel, w, budget)
	case tabBackups:
		if a.diffView != "" {
			return a.renderDiffView(w, budget), nil
		}
		return a.renderBackupsTab(a.backupsSel, w, budget)
	case tabTopology:
		return renderTopologyTab(projected, w, budget, a.topologyScroll)
	}
	return "", nil
}

// renderDiffView renders the Backups tab's long-text diff view: colored,
// scrolled to a.diffScroll (clamped to the actual line count so an
// over-eager scroll never runs past the end), with a "lines N-M of T"
// footer once it doesn't all fit.
func (a *App) renderDiffView(w, budget int) string {
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	lines := a.diffLinesCached(inner)
	contentBudget := budget - 2
	visible, offset, total := windowWithFooter(lines, contentBudget, a.diffScroll)
	body := strings.Join(visible, "\n")
	if footer := scrollFooter(offset, len(visible), total); footer != "" {
		body += "\n" + keyBarLabelStyle.Render(footer)
	}
	return panelInner("diff", body, w, contentBudget)
}

// renderFlowPanel renders the apply flow's active screen inside a titled
// panel, tight to its own content (no centering -- renderDialog/overlay
// handle that, as one of the dialogs it composites over the body). The
// review/drift/running screens are prose (word-wrap); the results screen
// scrolls like renderDiffView (via a.flow.resultsScroll), since it can be
// one line per applied op and there's no other "selected row" to derive a
// window from.
func (a *App) renderFlowPanel(dw, budget int) string {
	if a.flow.screen != flowResults {
		out, _ := panelWrapH(flowTitle(a.flow.screen), a.flow.view(dw, budget), dw, budget, false)
		return out
	}
	inner := dw - 2
	if inner < 1 {
		inner = 1
	}
	lines := strings.Split(lipgloss.NewStyle().Width(inner).Render(a.flow.view(dw, budget)), "\n")
	contentBudget := budget - 2
	visible, offset, total := windowWithFooter(lines, contentBudget, a.flow.resultsScroll)
	body := strings.Join(visible, "\n")
	if footer := scrollFooter(offset, len(visible), total); footer != "" {
		body += "\n" + keyBarLabelStyle.Render(footer)
	}
	return panelInner(flowTitle(a.flow.screen), body, dw, 0)
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
	pending := pluralize(a.queue.Len(), "pending op")

	switch {
	case a.help:
		return statusBarStyle.Render(pending + "  [up/down] scroll  any other key: close")
	case a.wizard != nil:
		return statusBarStyle.Render(pending + "  " + a.wizard.statusBarHint())
	case a.memPicker != nil:
		return statusBarStyle.Render(pending + "  " + a.memPicker.statusBarHint())
	case a.flow != nil:
		return statusBarStyle.Render(pending + "  " + a.flow.statusBarHint())
	}

	// '?' (help) and tab switching (digit keys, Tab/shift+Tab) both work
	// from every tab and aren't otherwise mentioned anywhere on screen
	// (only a row-0 click on a tab label hints at tab-switching visually),
	// so they're always named right after the pending count; rescan/edit/
	// quit always come last. Neither set ever drops, however tight a.width
	// gets -- only the context-specific hints in between (below) do, in
	// priority order, so the bar keeps to one row rather than word-
	// wrapping (which could otherwise split a "[key] label" hint's own key
	// from its label across two lines).
	head := []keyHint{{"?", "help"}, {"1-6", "tabs"}, {"tab", "next"}}
	tail := []keyHint{{"r", "rescan"}, {"e", "edit"}, {"q", "quit"}}

	var context []keyHint
	if a.tab == tabOverview && !a.overviewSideBySide() {
		context = append(context, keyHint{"up/down", "scroll"})
	}
	if a.tab == tabCPUMap {
		context = append(context, keyHint{"arrows/hjkl", "move"})
	}
	if a.tab == tabTopology {
		context = append(context, keyHint{"up/down", "scroll"})
	}
	if a.editMode && a.tab == tabPending {
		context = append(context, keyHint{"x", "remove"})
	}
	if a.editMode && a.queue.Len() > 0 {
		context = append(context, keyHint{"a", "apply"})
	}
	if a.editMode && a.tab == tabPending && a.queue.Len() > 0 {
		// 'd' (discard all) is only routed on the Pending tab.
		context = append(context, keyHint{"d", "discard"})
	}
	if a.editMode && a.tab == tabVMs {
		context = append(context, keyHint{"p", "pin"}, keyHint{"s", "strip"}, keyHint{"n", "mem-node"})
	}
	if a.tab == tabBackups {
		switch {
		case a.diffView != "":
			// Dismissing the diff isn't gated by edit mode either.
			context = append(context, keyHint{"any", "close"})
		case a.editMode:
			context = append(context, keyHint{"R", "restore"}, keyHint{"enter", "diff"})
		default:
			// 'enter' (show diff) isn't gated by edit mode; only R is.
			context = append(context, keyHint{"enter", "diff"})
		}
	}

	pendingTok := keyBarLabelStyle.Render(pending)
	headToks, tailToks := renderKeyHints(head), renderKeyHints(tail)
	w := effectiveWidth(a.width)
	fixedWidth := tokenSetWidth(append([]string{pendingTok}, append(headToks, tailToks...)...))

	// Greedily keep as many context hints (in priority order -- the ones
	// added first above, so the least important tend to be added last and
	// so dropped first here) as still fit alongside the fixed set on one
	// row.
	var kept []string
	used := fixedWidth
	for _, h := range context {
		tok := renderKeyHint(h)
		if add := 2 + lipgloss.Width(tok); used+add <= w {
			kept = append(kept, tok)
			used += add
		} else {
			break
		}
	}

	tokens := append([]string{pendingTok}, headToks...)
	tokens = append(tokens, kept...)
	tokens = append(tokens, tailToks...)
	return wrapKeyBarTokens(tokens, w)
}

// renderKeyHints renders each of hints via renderKeyHint.
func renderKeyHints(hints []keyHint) []string {
	toks := make([]string, len(hints))
	for i, h := range hints {
		toks[i] = renderKeyHint(h)
	}
	return toks
}

// tokenSetWidth returns the total width of toks laid out on one row with
// a two-space separator between each.
func tokenSetWidth(toks []string) int {
	w := 0
	for i, t := range toks {
		if i > 0 {
			w += 2
		}
		w += lipgloss.Width(t)
	}
	return w
}
