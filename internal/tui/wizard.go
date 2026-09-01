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
	screen     wizardScreen
	cursor     int          // core index within nodeCores(s, node)
	selected   map[int]bool // thread ids selected on the manual screen
	stagedHash string
	status     string // transient warning shown on the manual screen only
}

// newWizard opens a wizard for vm, seeded from proposal (screen 1) with
// stagedHash carrying the domain XML hash to stamp on the eventual op.
func newWizard(vm string, proposal *model.Proposal, stagedHash string) *wizard {
	return &wizard{
		vm:         vm,
		node:       proposal.Node,
		proposal:   proposal,
		screen:     proposalScreen,
		stagedHash: stagedHash,
	}
}

// vcpus is the number of vcpus this VM needs pinned (one thread each), per
// the proposal Propose already built.
func (w *wizard) vcpus() int { return len(w.proposal.Pins) }

// update handles one key on whichever screen is active. done reports the
// wizard should close; when done and op is non-nil, the caller stages it,
// else the wizard was cancelled.
func (w *wizard) update(msg tea.KeyMsg, projected *model.Snapshot) (bool, *model.PendingOp) {
	switch w.screen {
	case proposalScreen:
		return w.updateProposal(msg)
	default:
		return w.updateManual(msg, projected)
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

func (w *wizard) updateManual(msg tea.KeyMsg, projected *model.Snapshot) (bool, *model.PendingOp) {
	cores := nodeCores(projected, w.node)

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
		w.screen = proposalScreen
		w.status = ""
		return false, nil
	}
	return false, nil
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
	summary := fmt.Sprintf("%s: pin %d vcpus -> node %d threads %s; memory -> node %d",
		w.vm, len(pins), w.node, threads, w.node)
	return model.PendingOp{
		Kind:       model.OpPin,
		VM:         w.vm,
		Pins:       copyPinsMap(pins),
		MemNode:    w.node,
		StagedHash: w.stagedHash,
		Summary:    summary,
	}
}

// view renders the active screen against projected (the App's current
// projected snapshot, used for the node map's live pin state).
func (w *wizard) view(projected *model.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pin %s (%d vcpus) -> node %d\n\n", w.vm, w.vcpus(), w.node)

	switch w.screen {
	case proposalScreen:
		highlight := threadSet(assignedThreads(w.proposal.Pins))
		b.WriteString(renderNodeMap(projected, w.node, highlight, -1))
		b.WriteString("\n")
		for _, r := range w.proposal.Rationale {
			b.WriteString(r)
			b.WriteString("\n")
		}
		for _, wm := range w.proposal.Warnings {
			b.WriteString(warningStyle.Render(wm))
			b.WriteString("\n")
		}
		b.WriteString("\nenter accept  m manual  esc cancel")
	case manualScreen:
		b.WriteString(renderNodeMap(projected, w.node, w.selected, w.cursor))
		fmt.Fprintf(&b, "\nselected %d/%d\n", len(w.selected), w.vcpus())
		if w.status != "" {
			b.WriteString(w.status)
			b.WriteString("\n")
		}
		b.WriteString("\nenter accept  esc back")
	}
	return b.String()
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

// renderNodeMap renders node's cores as a grid of two-glyph cells (mirrors
// renderCPUMap, but scoped to one node): threads in highlight render in the
// wizard highlight style, the core at cursor (a nodeCores index, -1 for
// none) renders reverse-video instead.
func renderNodeMap(s *model.Snapshot, node int, highlight map[int]bool, cursor int) string {
	var b strings.Builder
	col := 0
	for i, core := range nodeCores(s, node) {
		if col == coresPerRow {
			b.WriteString("\n")
			col = 0
		}
		if col > 0 {
			b.WriteString(" ")
		}
		b.WriteString(nodeMapCell(s, core, highlight, i == cursor))
		col++
	}
	b.WriteString("\n")
	return b.String()
}

// nodeMapCell renders one core's two-glyph cell for the wizard's node map:
// cursor takes precedence over highlight, which takes precedence over the
// thread's plain pinned/free/shared glyph.
func nodeMapCell(s *model.Snapshot, core hostinfo.Core, highlight map[int]bool, isCursor bool) string {
	var glyphs strings.Builder
	for _, t := range core.Threads {
		glyph := glyphChar(s, t)
		switch {
		case isCursor:
			glyph = cursorStyle.Render(glyph)
		case highlight[t]:
			glyph = wizardHighlightStyle.Render(glyph)
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
