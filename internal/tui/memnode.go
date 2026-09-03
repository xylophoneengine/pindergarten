package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// memNodePicker is the node-picker state for the 'n' (set memory node)
// VMs-tab action. App holds one (nil when closed); while non-nil, App
// routes key input to handleMemNodeKey and renders view in place of the
// tab body. It borrows the pin wizard's own idioms: a reverse-video cursor
// row (arrows/j k, or a click), a pending-colored chosen row (space, or a
// double-click), and the shared [A]pply/[C]ancel buttons -- a digit still
// picks that node outright, as before.
type memNodePicker struct {
	vm         string
	pins       map[int][]int // copy of the VM's current pins; left unchanged by this action
	gpuNode    int           // -1 if the VM has no passthrough device with a known node
	pinNode    int           // node vm's pins all sit on; -1 if unpinned or pinned cross-node
	memNodes   map[int]bool  // nodes the VM's memory is currently bound to (nil/empty if unbound)
	stagedHash string
	stagedXML  string          // domain XML at open time (same XML stagedHash hashes), for the drift screen's diff
	snap       *model.Snapshot // for rendering each node's free memory

	cursor int     // index into snap.Topo.Nodes
	chosen int     // node id marked by space/double-click, -1 for none; apply falls back to the cursor's node
	clicks clicker // double-click detection for the node rows
}

// newMemNodePicker builds a picker for vm, computing gpuNode/pinNode from
// vm's current state so the warning check has something to compare a pick
// against. The cursor starts on the VM's GPU node when it has one (the
// recommended pick), else on its current pin node, else the first node.
func newMemNodePicker(vm *model.VM, stagedHash, stagedXML string, snap *model.Snapshot) *memNodePicker {
	p := &memNodePicker{
		vm:         vm.Name,
		pins:       copyPinsMap(vm.Pins),
		gpuNode:    vm.GPUNode(),
		pinNode:    pinsNode(snap.Topo, vm.Pins),
		memNodes:   threadSet(vm.MemNodes),
		stagedHash: stagedHash,
		stagedXML:  stagedXML,
		snap:       snap,
		chosen:     -1,
	}
	if i := p.nodeIndex(p.gpuNode); i >= 0 {
		p.cursor = i
	} else if i := p.nodeIndex(p.pinNode); i >= 0 {
		p.cursor = i
	}
	return p
}

// nodeIndex returns node's index within snap.Topo.Nodes, or -1.
func (p *memNodePicker) nodeIndex(node int) int {
	for i, n := range p.snap.Topo.Nodes {
		if n.ID == node {
			return i
		}
	}
	return -1
}

// cursorNode returns the node id under the cursor (-1 with no nodes).
func (p *memNodePicker) cursorNode() int {
	if p.cursor < 0 || p.cursor >= len(p.snap.Topo.Nodes) {
		return -1
	}
	return p.snap.Topo.Nodes[p.cursor].ID
}

// target is the node apply acts on: the chosen one, else the cursor's.
func (p *memNodePicker) target() int {
	if p.chosen != -1 {
		return p.chosen
	}
	return p.cursorNode()
}

// tags renders node's inline markers -- "(GPU, vcpus, memory)" for
// whichever of the VM's GPU, current vcpu pins, and current memory binding
// live there -- or "" when none do.
func (p *memNodePicker) tags(node int) string {
	var t []string
	if node == p.gpuNode {
		t = append(t, "GPU")
	}
	if node == p.pinNode {
		t = append(t, "vcpus")
	}
	if p.memNodes[node] {
		t = append(t, "memory")
	}
	if len(t) == 0 {
		return ""
	}
	return "(" + strings.Join(t, ", ") + ")"
}

// crossesGPU reports whether picking node would cross the VM's GPU node
// -- the one warning reason (of warning's two) that gets the loud,
// confirm-via-App.confirm treatment (see App.pickMemNode); "differs from
// the current pin node" stays a plain, immediate warning, since it isn't
// a locality concern.
func (p *memNodePicker) crossesGPU(node int) bool {
	return p.gpuNode != -1 && node != p.gpuNode
}

// pinsNode returns the single node pins all sit on, or -1 if pins is empty
// or spans more than one node.
func pinsNode(topo *hostinfo.Topology, pins map[int][]int) int {
	node := -1
	for _, threads := range pins {
		for _, t := range threads {
			th, ok := topo.Threads[t]
			if !ok {
				continue
			}
			if node == -1 {
				node = th.Node
			} else if node != th.Node {
				return -1
			}
		}
	}
	return node
}

// warning returns a non-blocking warning line when node differs from the
// VM's GPU node or from the node its pins currently live on, or "" when
// neither applies.
func (p *memNodePicker) warning(node int) string {
	var reasons []string
	if p.gpuNode != -1 && node != p.gpuNode {
		reasons = append(reasons, fmt.Sprintf("the GPU is on node %d", p.gpuNode))
	}
	if p.pinNode != -1 && node != p.pinNode {
		reasons = append(reasons, fmt.Sprintf("vcpus are pinned on node %d", p.pinNode))
	}
	if len(reasons) == 0 {
		return ""
	}
	return fmt.Sprintf("warning: node %d differs from where %s", node, strings.Join(reasons, " and "))
}

// buildOp stages an OpPin that only touches numatune: Pins carries the
// VM's own current pins back in unchanged (SetPinning leaves cputune
// untouched when Pins is empty, so an unpinned VM stays unpinned).
// Summary gets the same " (crosses GPU node)" suffix the pin wizard's
// form uses when crossesGPU(node), so the Pending tab (pendingCrossesGPU,
// pending.go) can flag this op's row too.
func (p *memNodePicker) buildOp(node int) model.PendingOp {
	summary := fmt.Sprintf("%s: memory -> node %d (strict); vcpu pinning unchanged", p.vm, node)
	if p.crossesGPU(node) {
		summary += " (crosses GPU node)"
	}
	return model.PendingOp{
		Kind:       model.OpPin,
		VM:         p.vm,
		Pins:       copyPinsMap(p.pins),
		MemNode:    node,
		StagedHash: p.stagedHash,
		StagedXML:  p.stagedXML,
		Summary:    summary,
	}
}

// view renders one "[x] node N  free M  (tags)" row per node -- the cursor
// row reverse-video, the chosen row in the wizard's pending highlight, both
// combined when they coincide (same rule as nodeMapCell) -- then the
// target node's warning, if any, and the shared centered [A]pply/[C]ancel
// buttons, inside a titled panel dw wide, top-down truncated to fit
// budget. Alongside the string it returns one "memnode" hit per node row
// (indexed by node ID) and the buttons' "dialogbtn" hits, 0-based relative
// to the picker panel's own top-left corner.
func (p *memNodePicker) view(dw, budget int) (string, []hit) {
	inner := dw - 2
	if inner < 1 {
		inner = 1
	}
	var lines []string
	var hits []hit
	for i, n := range p.snap.Topo.Nodes {
		mark := "[ ]"
		if n.ID == p.chosen {
			mark = "[x]"
		}
		row := fmt.Sprintf("%s node %d  free %s", mark, n.ID, fmtKiB(model.FreeMemKiB(p.snap, n.ID)))
		if tags := p.tags(n.ID); tags != "" {
			row += "  " + tags
		}
		switch {
		case i == p.cursor && n.ID == p.chosen:
			row = wizardHighlightStyle.Reverse(true).Render(row)
		case i == p.cursor:
			row = cursorStyle.Render(row)
		case n.ID == p.chosen:
			row = wizardHighlightStyle.Render(row)
		}
		hits = append(hits, hit{y0: len(lines), y1: len(lines) + 1, x0: 0, x1: inner, kind: "memnode", index: n.ID})
		lines = append(lines, row)
	}
	if warn := p.warning(p.target()); warn != "" {
		lines = append(lines, "")
		lines = append(lines, strings.Split(lipgloss.NewStyle().Width(inner).Render(warningStyle.Render(warn)), "\n")...)
	}
	lines = append(lines, "")
	btnLine, btnHits := buttonRow(inner, "[A]pply", "[C]ancel")
	hits = append(hits, offsetHits(btnHits, len(lines), 0)...)
	lines = append(lines, btnLine)

	kept := len(lines)
	if contentBudget := budget - 2; contentBudget >= 1 && kept > contentBudget {
		lines = lines[:contentBudget]
		kept = contentBudget
	}
	body := truncateLines(strings.Join(lines, "\n"), inner)
	panel := panelInner("set memory node for "+p.vm, body, dw, 0)
	return panel, offsetHits(clipHitsToWindow(hits, kept, inner), 1, 1)
}

// statusBarHint returns the status bar's replacement content while the
// picker is open.
func (p *memNodePicker) statusBarHint() string {
	return "[arrows] move  [space] choose  [0-9] pick  [A] apply  [C] cancel"
}

// hasNode reports whether node is one of the topology's node IDs.
func (p *memNodePicker) hasNode(node int) bool {
	for _, n := range p.snap.Topo.Nodes {
		if n.ID == node {
			return true
		}
	}
	return false
}

// openMemNodePicker implements the 'n' key on the VMs tab: after the
// shared edit-mode/supported gates and fetching the domain's current XML
// (for StagedHash), opens the node picker against the *projected* VM (raw
// snapshot plus any already-staged ops, mirroring openWizard's own use of
// model.Project) -- so pressing 'n' right after 'p' on the same VM sees
// the just-staged pins, not the stale on-disk ones, and "vcpu pinning
// unchanged" stays true.
func (a *App) openMemNodePicker() (tea.Model, tea.Cmd) {
	vm := a.gateVMAction()
	if vm == nil {
		return a, nil
	}
	xml, err := a.hv.DomainXML(vm.Name)
	if err != nil {
		a.status = fmt.Sprintf("%s: %v", vm.Name, err)
		return a, nil
	}
	projected := model.Project(a.snap, a.doms, a.queue.Ops)
	pv := projected.VM(vm.Name)
	if pv == nil {
		// Project never adds or removes VMs, so this only guards a future change.
		a.status = fmt.Sprintf("%s: not in projected snapshot", vm.Name)
		return a, nil
	}
	a.memPicker = newMemNodePicker(pv, model.HashXML(xml), xml, projected)
	a.status = ""
	return a, nil
}

// handleMemNodeKey routes a key to the open picker: esc/c/C cancel;
// up/down (j/k) move the cursor row; space chooses the cursor's node
// (marked, not yet staged); enter/a/A stage the target (chosen, else
// cursor) via pickMemNode; a digit 0-9 naming an existing node picks it
// outright, as before. Anything else is ignored.
func (a *App) handleMemNodeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.memPicker
	switch {
	case msg.Type == tea.KeyEsc, isRune(msg, 'c'), isRune(msg, 'C'):
		a.memPicker = nil
		a.status = "cancelled"
	case msg.Type == tea.KeyUp, isRune(msg, 'k'):
		if p.cursor > 0 {
			p.cursor--
		}
	case msg.Type == tea.KeyDown, isRune(msg, 'j'):
		if p.cursor < len(p.snap.Topo.Nodes)-1 {
			p.cursor++
		}
	case msg.Type == tea.KeySpace:
		p.chosen = p.cursorNode()
	case msg.Type == tea.KeyEnter, isRune(msg, 'a'), isRune(msg, 'A'):
		a.pickMemNode(p.target())
	case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] >= '0' && msg.Runes[0] <= '9':
		a.pickMemNode(int(msg.Runes[0] - '0'))
	}
	return a, nil
}

// pickMemNode stages a memory-node op for the open picker's VM if node
// names an existing topology node, appending its (non-blocking) plain
// warning line, styled, to the status. A pick that crosses the VM's GPU
// node is never blocked either, but does open the shared App.confirm y/n
// dialog first (mirroring the pin wizard form's own tryStage): the op is
// built right here, from the pick that triggered it, and staged verbatim
// on "y"; "n"/esc just dismiss the confirm, leaving the picker open with
// nothing staged (see App.handleConfirmKey, routed ahead of the picker --
// App.handleKey). The mouse click and the digit key share this same gate,
// since they both funnel through here. A non-crossing pick still closes
// the picker immediately, same as before. Shared by the digit-key handler
// and the node-line mouse click.
func (a *App) pickMemNode(node int) {
	if !a.memPicker.hasNode(node) {
		return
	}
	op := a.memPicker.buildOp(node)
	status := "staged: " + op.Summary
	if warn := a.memPicker.warning(node); warn != "" {
		status += "\n" + warningStyle.Render(warn)
	}
	if a.memPicker.crossesGPU(node) {
		a.confirm = &confirm{
			prompt: "Bind memory across the GPU's node anyway? [y/n]",
			yes: func() tea.Cmd {
				a.queue.Add(op)
				a.status = status
				a.memPicker = nil
				return nil
			},
		}
		return
	}
	a.queue.Add(op)
	a.status = status
	a.memPicker = nil
}

// handleMemNodeMouse routes a left click: on a node row it moves the
// cursor there, and a double-click (see clicker) picks that node, same as
// its digit; on a "dialogbtn" hit it replays 'A'/'C' through
// handleMemNodeKey, same as the wizard's own buttons.
func (a *App) handleMemNodeMouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return
	}
	p := a.memPicker
	for _, h := range a.hits {
		if msg.Y < h.y0 || msg.Y >= h.y1 || msg.X < h.x0 || msg.X >= h.x1 {
			continue
		}
		switch h.kind {
		case "memnode":
			if i := p.nodeIndex(h.index); i >= 0 {
				p.cursor = i
			}
			if p.clicks.double(h.index, time.Now()) {
				a.pickMemNode(h.index)
			}
			return
		case "dialogbtn":
			r := 'A'
			if h.index == 1 {
				r = 'C'
			}
			_, _ = a.handleMemNodeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			return
		}
	}
}
