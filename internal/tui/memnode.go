package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// memNodePicker is the tiny node-picker state for the 'n' (set memory
// node) VMs-tab action. App holds one (nil when closed); while non-nil,
// App routes key input to handleMemNodeKey and renders view in place of
// the tab body, mirroring the wizard.
type memNodePicker struct {
	vm         string
	pins       map[int][]int // copy of the VM's current pins; left unchanged by this action
	gpuNode    int           // -1 if the VM has no passthrough device with a known node
	pinNode    int           // node vm's pins all sit on; -1 if unpinned or pinned cross-node
	stagedHash string
	stagedXML  string          // domain XML at open time (same XML stagedHash hashes), for the drift screen's diff
	snap       *model.Snapshot // for rendering each node's free memory
}

// newMemNodePicker builds a picker for vm, computing gpuNode/pinNode from
// vm's current state so the warning check has something to compare a pick
// against.
func newMemNodePicker(vm *model.VM, stagedHash, stagedXML string, snap *model.Snapshot) *memNodePicker {
	return &memNodePicker{
		vm:         vm.Name,
		pins:       copyPinsMap(vm.Pins),
		gpuNode:    vm.GPUNode(),
		pinNode:    pinsNode(snap.Topo, vm.Pins),
		stagedHash: stagedHash,
		stagedXML:  stagedXML,
		snap:       snap,
	}
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
func (p *memNodePicker) buildOp(node int) model.PendingOp {
	return model.PendingOp{
		Kind:       model.OpPin,
		VM:         p.vm,
		Pins:       copyPinsMap(p.pins),
		MemNode:    node,
		StagedHash: p.stagedHash,
		StagedXML:  p.stagedXML,
		Summary:    fmt.Sprintf("%s: memory -> node %d (strict); vcpu pinning unchanged", p.vm, node),
	}
}

// view renders the node list (with free memory) and the VM's GPU node, if
// any, inside a titled panel. Alongside the string it returns one
// "memnode" hit per node line, screen-absolute (0-based relative to the
// picker body's own top-left corner), indexed by node ID (so a click maps
// straight onto the same pickMemNode path as its digit key).
func (p *memNodePicker) view(width int) (string, []hit) {
	var b strings.Builder
	var hits []hit
	for i, n := range p.snap.Topo.Nodes {
		fmt.Fprintf(&b, "%d) node %d  free %s\n", n.ID, n.ID, fmtKiB(model.FreeMemKiB(p.snap, n.ID)))
		hits = append(hits, hit{y0: i, y1: i + 1, x0: 0, x1: hitWide, kind: "memnode", index: n.ID})
	}
	if p.gpuNode != -1 {
		fmt.Fprintf(&b, "\nGPU is on node %d\n", p.gpuNode)
	}
	b.WriteString("\ndigit/click: choose node  esc cancel")

	title := "set memory node for " + p.vm
	return panelWrap(title, strings.TrimRight(b.String(), "\n"), width), offsetHits(hits, 1, 1)
}

// statusBarHint returns the status bar's replacement content while the
// picker is open.
func (p *memNodePicker) statusBarHint() string {
	return "digit: choose node  esc cancel"
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

// handleMemNodeKey routes a key to the open picker: esc cancels, a digit
// 0-9 naming an existing node picks it (via pickMemNode). Anything else is
// ignored.
func (a *App) handleMemNodeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc {
		a.memPicker = nil
		a.status = "cancelled"
		return a, nil
	}
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 || msg.Runes[0] < '0' || msg.Runes[0] > '9' {
		return a, nil
	}
	a.pickMemNode(int(msg.Runes[0] - '0'))
	return a, nil
}

// pickMemNode stages a memory-node op for the open picker's VM if node
// names an existing topology node (appending its (non-blocking) warning
// line, styled, to the status), closing the picker either way once staged.
// Shared by the digit-key handler and the node-line mouse click.
func (a *App) pickMemNode(node int) {
	if !a.memPicker.hasNode(node) {
		return
	}
	op := a.memPicker.buildOp(node)
	status := "staged: " + op.Summary
	if warn := a.memPicker.warning(node); warn != "" {
		status += "\n" + warningStyle.Render(warn)
	}
	a.queue.Add(op)
	a.status = status
	a.memPicker = nil
}

// handleMemNodeMouse routes a left click on a node line to pickMemNode,
// same as pressing its digit.
func (a *App) handleMemNodeMouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return
	}
	for _, h := range a.hits {
		if h.kind == "memnode" && msg.Y >= h.y0 && msg.Y < h.y1 && msg.X >= h.x0 && msg.X < h.x1 {
			a.pickMemNode(h.index)
			return
		}
	}
}
