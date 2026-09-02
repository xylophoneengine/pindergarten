package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// wizardScreen is one of the two screens the pin wizard can show.
type wizardScreen int

const (
	proposalScreen wizardScreen = iota
	manualScreen
)

// wizard is the pin-wizard state machine. App holds one (nil when closed);
// while non-nil, App routes key input to update and renders view in place
// of the tab body.
type wizard struct {
	vm         string
	node       int
	proposal   *model.Proposal
	base       *model.Snapshot // snapshot Propose was run against: the VM's own current pins/membind projected away, so its own threads read as free rather than occupied
	screen     wizardScreen
	cursor     int          // core index within nodeCores(base, node)
	selected   map[int]bool // thread ids selected on the manual screen
	stagedHash string
	stagedXML  string // domain XML at open time (same XML stagedHash hashes), for the drift screen's diff
	status     string // transient warning shown on the manual screen only
}

// newWizard opens a wizard for vm, seeded from proposal (screen 1) with
// stagedHash/stagedXML carrying the domain XML hash/text to stamp on the
// eventual op, and base the self-stripped snapshot Propose ran against
// (openWizard's projection excluding vm's own current pins/membind) --
// view and updateManual render against base, not the App's plain
// projection, so the node map agrees with the proposal it is illustrating.
func newWizard(vm string, proposal *model.Proposal, stagedHash, stagedXML string, base *model.Snapshot) *wizard {
	return &wizard{
		vm:         vm,
		node:       proposal.Node,
		proposal:   proposal,
		base:       base,
		screen:     proposalScreen,
		stagedHash: stagedHash,
		stagedXML:  stagedXML,
	}
}

// vcpus is the number of vcpus this VM needs pinned (one thread each), per
// the proposal Propose already built.
func (w *wizard) vcpus() int { return len(w.proposal.Pins) }

// update handles one key on whichever screen is active. done reports the
// wizard should close; when done and op is non-nil, the caller stages it,
// else the wizard was cancelled.
func (w *wizard) update(msg tea.KeyMsg) (bool, *model.PendingOp) {
	switch w.screen {
	case proposalScreen:
		return w.updateProposal(msg)
	default:
		return w.updateManual(msg)
	}
}

func (w *wizard) updateProposal(msg tea.KeyMsg) (bool, *model.PendingOp) {
	switch {
	case msg.Type == tea.KeyEnter:
		op := w.buildOp(w.proposal.Pins)
		return true, &op
	case isRune(msg, 'm'):
		w.screen = manualScreen
		w.cursor = 0
		w.selected = threadSet(assignedThreads(w.proposal.Pins))
		w.status = ""
		return false, nil
	case msg.Type == tea.KeyEsc:
		return true, nil
	}
	return false, nil
}

func (w *wizard) updateManual(msg tea.KeyMsg) (bool, *model.PendingOp) {
	cores := nodeCores(w.base, w.node)

	switch {
	case msg.Type == tea.KeyLeft, isRune(msg, 'h'):
		if w.cursor > 0 {
			w.cursor--
		}
		return false, nil
	case msg.Type == tea.KeyRight, isRune(msg, 'l'):
		if w.cursor < len(cores)-1 {
			w.cursor++
		}
		return false, nil
	case msg.Type == tea.KeyUp, isRune(msg, 'k'):
		if w.cursor-coresPerRow >= 0 {
			w.cursor -= coresPerRow
		}
		return false, nil
	case msg.Type == tea.KeyDown, isRune(msg, 'j'):
		if w.cursor+coresPerRow < len(cores) {
			w.cursor += coresPerRow
		}
		return false, nil
	case isRune(msg, 'n'):
		w.cycleNode()
		return false, nil
	case msg.Type == tea.KeySpace:
		w.toggleCore(cores)
		return false, nil
	case msg.Type == tea.KeyEnter:
		n := w.vcpus()
		if len(w.selected) != n {
			w.status = fmt.Sprintf("select exactly %d threads (%d selected)", n, len(w.selected))
			return false, nil
		}
		op := w.buildOp(assignSelected(w.selected))
		return true, &op
	case msg.Type == tea.KeyEsc:
		// Reset to the proposal's own node: its Pins are specific thread
		// IDs on that node, so accepting the proposal screen after cycling
		// away in manual (without staging from there) must not leave a
		// mismatched node/threads pair.
		w.screen = proposalScreen
		w.node = w.proposal.Node
		w.status = ""
		return false, nil
	}
	return false, nil
}

// cycleNode advances w.node to the next node in topology order (wrapping),
// resetting the manual screen's cursor and thread selection since both are
// scoped to the previous node's cores. GPU locality is a soft preference,
// never enforced here: crossesGPUWarning (surfaced in view and the staged
// Summary) is the only consequence of picking a node other than the VM's
// GPU node.
func (w *wizard) cycleNode() {
	nodes := w.base.Topo.Nodes
	if len(nodes) == 0 {
		return
	}
	idx := 0
	for i, n := range nodes {
		if n.ID == w.node {
			idx = i
			break
		}
	}
	w.node = nodes[(idx+1)%len(nodes)].ID
	w.cursor = 0
	w.selected = map[int]bool{}
}

// vmGPUDevice returns vm's first passthrough device with a known NUMA
// node, or nil if it has none. Mirrors VM.GPUNode's own selection rule,
// but keeps the PCI address around for crossesGPUWarning's message.
func vmGPUDevice(vm *model.VM) *model.Device {
	for i := range vm.Devices {
		if vm.Devices[i].Node != -1 {
			return &vm.Devices[i]
		}
	}
	return nil
}

// crossesGPUWarning returns the manual screen's non-blocking warning when
// w.node differs from the VM's GPU node, or "" when the VM has no GPU (or
// an unresolved one) or w.node already matches it.
func (w *wizard) crossesGPUWarning() string {
	vm := w.base.VM(w.vm)
	if vm == nil {
		return ""
	}
	gpu := vmGPUDevice(vm)
	if gpu == nil || gpu.Node == w.node {
		return ""
	}
	return fmt.Sprintf("GPU at %s is on node %d; placing vCPUs/memory on node %d crosses the interconnect",
		gpu.Addr, gpu.Node, w.node)
}

// toggleCore toggles every thread of the cursor's core in w.selected: if
// any of them is unselected, it selects all of them; if all are already
// selected, it deselects all of them.
func (w *wizard) toggleCore(cores []hostinfo.Core) {
	if w.cursor < 0 || w.cursor >= len(cores) {
		return
	}
	core := cores[w.cursor]

	allSelected := true
	for _, t := range core.Threads {
		if !w.selected[t] {
			allSelected = false
			break
		}
	}
	for _, t := range core.Threads {
		if allSelected {
			delete(w.selected, t)
		} else {
			w.selected[t] = true
		}
	}
}

// buildOp stages an OpPin from pins: assigns memory to w.node and carries
// the wizard's StagedHash, formatting Summary per the fixed convention.
func (w *wizard) buildOp(pins map[int][]int) model.PendingOp {
	threads := hostinfo.FormatCPUList(assignedThreads(pins))
	summary := fmt.Sprintf("%s: pin %d vcpus -> node %d threads %s; memory -> node %d (strict)",
		w.vm, len(pins), w.node, threads, w.node)
	if w.crossesGPUWarning() != "" {
		summary += " (crosses GPU node)"
	}
	return model.PendingOp{
		Kind:       model.OpPin,
		VM:         w.vm,
		Pins:       copyPinsMap(pins),
		MemNode:    w.node,
		StagedHash: w.stagedHash,
		StagedXML:  w.stagedXML,
		Summary:    summary,
	}
}

// view renders the active screen against w.base (the self-stripped snapshot
// Propose ran against, used for the node map's live pin state) as a
// centered dialog (dialogWidth(width) wide): a titled node-map panel (grid
// content, so it truncates rather than wraps) and an info panel below it
// (prose, so it wraps), both trimmed to fit budget. Alongside the string
// it returns the manual screen's per-core hits, screen-absolute (0-based
// relative to the wizard body's own top-left corner, already shifted by
// the centering offset) -- the proposal screen's map isn't clickable, so
// it reports none.
func (w *wizard) view(width, budget int) (string, []hit) {
	dw := dialogWidth(width)
	title := fmt.Sprintf("pin %s (%d vcpus) -> node %d", w.vm, w.vcpus(), w.node)

	var grid string
	var gridHits []hit
	var info strings.Builder
	switch w.screen {
	case proposalScreen:
		highlight := threadSet(assignedThreads(w.proposal.Pins))
		grid, _ = renderNodeMap(w.base, w.node, highlight, -1, "wizardcore")
		for _, r := range w.proposal.Rationale {
			info.WriteString(r)
			info.WriteString("\n")
		}
		for _, wm := range w.proposal.Warnings {
			info.WriteString(warningStyle.Render(wm))
			info.WriteString("\n")
		}
	case manualScreen:
		grid, gridHits = renderNodeMap(w.base, w.node, w.selected, w.cursor, "wizardcore")
		fmt.Fprintf(&info, "selected %d/%d\n", len(w.selected), w.vcpus())
		if warn := w.crossesGPUWarning(); warn != "" {
			info.WriteString(warningStyle.Render(warn))
			info.WriteString("\n")
		}
		if w.status != "" {
			info.WriteString(w.status)
			info.WriteString("\n")
		}
	}

	gridBudget, infoBudget := splitStackedBudget(budget, lineCount(grid))
	gridPanel, kept := panelH(title, grid, dw, gridBudget, false)
	hits := offsetHits(clipHitsToWindow(gridHits, kept, dw-2), 1, 1)
	infoPanel := ""
	if infoBudget > 0 {
		infoPanel, _ = panelWrapH("info", strings.TrimRight(info.String(), "\n"), dw, infoBudget, false)
	}
	out := gridPanel
	if infoPanel != "" {
		out = gridPanel + "\n" + infoPanel
	}
	centered, xOff := centerDialog(out, width)
	return centered, offsetHits(hits, 0, xOff)
}

// statusBarHint returns the status bar's replacement content while the
// wizard is open: its own keys, since edit/quit/pin/strip are inert while
// a wizard is capturing all key input.
func (w *wizard) statusBarHint() string {
	if w.screen == manualScreen {
		return "[h/l/j/k/up/down] move  [n] node  [space] toggle  [enter] accept  [esc] back"
	}
	return "[enter] accept  [m] manual  [esc] cancel"
}

// openWizard implements the 'p' key on the VMs tab: after the shared
// edit-mode/supported gates and fetching the domain's current XML (for
// StagedHash), it builds a Propose-ready projection that excludes the VM's
// own current pins/membind -- so re-pinning an already-pinned VM does not
// see its own claim as occupied -- and opens the wizard on the result. The
// wizard keeps that same projection (base) for its own rendering, so the
// node map it draws never disagrees with the proposal it is illustrating.
func (a *App) openWizard() (tea.Model, tea.Cmd) {
	vm := a.gateVMAction()
	if vm == nil {
		return a, nil
	}
	xml, err := a.hv.DomainXML(vm.Name)
	if err != nil {
		a.status = fmt.Sprintf("%s: %v", vm.Name, err)
		return a, nil
	}
	hash := model.HashXML(xml)

	ops := append(append([]model.PendingOp(nil), a.queue.Ops...), model.PendingOp{Kind: model.OpStrip, VM: vm.Name})
	base := model.Project(a.snap, a.doms, ops)
	proposal, err := model.Propose(base, vm.Name)
	if err != nil {
		a.status = err.Error()
		return a, nil
	}

	a.wizard = newWizard(vm.Name, proposal, hash, xml, base)
	a.status = ""
	return a, nil
}

// handleWizardKey routes a key to the open wizard, staging or discarding it
// once the wizard reports done.
func (a *App) handleWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	done, op := a.wizard.update(msg)
	if done {
		if op != nil {
			a.queue.Add(*op)
			a.status = "staged: " + op.Summary
		} else {
			a.status = "cancelled"
		}
		a.wizard = nil
	}
	return a, nil
}

// handleWizardMouse routes a left click on the manual screen's node map: it
// moves the cursor to the clicked core and toggles it, same as moving there
// with the arrow keys and pressing space. Ignored on the proposal screen
// (its map isn't clickable) and for anything but a left press.
func (a *App) handleWizardMouse(msg tea.MouseMsg) {
	if a.wizard.screen != manualScreen || msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return
	}
	cores := nodeCores(a.wizard.base, a.wizard.node)
	for _, h := range a.hits {
		if h.kind == "wizardcore" && msg.Y >= h.y0 && msg.Y < h.y1 && msg.X >= h.x0 && msg.X < h.x1 {
			a.wizard.cursor = h.index
			a.wizard.toggleCore(cores)
			return
		}
	}
}

// nodeCores returns s.Topo.Cores restricted to node, in topology order.
func nodeCores(s *model.Snapshot, node int) []hostinfo.Core {
	var cores []hostinfo.Core
	for _, c := range s.Topo.Cores {
		if c.Node == node {
			cores = append(cores, c)
		}
	}
	return cores
}

// renderNodeMap renders node's cores as a grid of two-glyph cells (also
// used, restricted to one node at a time, for the CPU Map tab's per-node
// panels -- see renderCPUMapTab): threads in highlight render in the
// wizard highlight style, the core at cursor (a nodeCores index, -1 for
// none) renders reverse-video instead. Alongside the string it returns one
// hit of the given kind per cell, 0-based relative to the grid's own
// top-left corner (x0 = the cell's column * 3), indexed by its position in
// nodeCores(s, node) -- the CPU Map tab, whose cursor is a *global*
// s.Topo.Cores index, translates that back via globalCoreIndices.
func renderNodeMap(s *model.Snapshot, node int, highlight map[int]bool, cursor int, kind string) (string, []hit) {
	var b strings.Builder
	var hits []hit
	col := 0
	row := 0
	for i, core := range nodeCores(s, node) {
		if col == coresPerRow {
			b.WriteString("\n")
			row++
			col = 0
		}
		if col > 0 {
			b.WriteString(" ")
		}
		x0 := col * 3
		hits = append(hits, hit{y0: row, y1: row + 1, x0: x0, x1: x0 + 2, kind: kind, index: i})
		b.WriteString(nodeMapCell(s, core, highlight, i == cursor))
		col++
	}
	return b.String(), hits
}

// nodeMapCell renders one core's two-glyph cell: a cursor+highlighted
// thread combines both (highlight style, reverse-video) so the selected
// core under the cursor still shows its selected state; a cursor-only or
// highlight-only thread gets just that style; a pending-only claim (no VM,
// just a staged op) gets the plain pinned glyph in a distinct color;
// otherwise the thread's plain pinned/free/shared glyph. A single-thread
// core (no SMT sibling) renders its one glyph followed by a space.
func nodeMapCell(s *model.Snapshot, core hostinfo.Core, highlight map[int]bool, isCursor bool) string {
	var glyphs strings.Builder
	for _, t := range core.Threads {
		glyph := glyphChar(s, t)
		use := s.Use[t]
		pendingOnly := len(use.VMs) == 0 && len(use.Pending) == 1
		switch {
		case isCursor && highlight[t]:
			glyph = wizardHighlightStyle.Reverse(true).Render(glyph)
		case isCursor:
			glyph = cursorStyle.Render(glyph)
		case highlight[t]:
			glyph = wizardHighlightStyle.Render(glyph)
		case pendingOnly:
			glyph = pendingGlyphStyle.Render(glyph)
		}
		glyphs.WriteString(glyph)
	}
	if len(core.Threads) == 1 {
		glyphs.WriteString(" ")
	}
	return glyphs.String()
}

// assignedThreads returns the threads pins assigns, sorted ascending.
func assignedThreads(pins map[int][]int) []int {
	ids := make([]int, 0, len(pins))
	for _, threads := range pins {
		ids = append(ids, threads[0])
	}
	sort.Ints(ids)
	return ids
}

// threadSet builds a selection set from a slice of thread ids.
func threadSet(ids []int) map[int]bool {
	set := make(map[int]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// assignSelected assigns vcpu i to the i-th selected thread in ascending
// order, per the manual-adjust acceptance rule.
func assignSelected(selected map[int]bool) map[int][]int {
	ids := make([]int, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	pins := make(map[int][]int, len(ids))
	for vcpu, t := range ids {
		pins[vcpu] = []int{t}
	}
	return pins
}

// copyPinsMap returns a shallow-safe copy of pins (each thread slice
// copied), so the staged op never aliases the proposal's or the manual
// selection's own maps.
func copyPinsMap(pins map[int][]int) map[int][]int {
	cp := make(map[int][]int, len(pins))
	for vcpu, threads := range pins {
		cp[vcpu] = append([]int(nil), threads...)
	}
	return cp
}
