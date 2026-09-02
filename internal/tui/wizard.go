package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// wizardScreen is one of the two screens the pin wizard can show.
type wizardScreen int

const (
	formScreen wizardScreen = iota
	manualScreen
)

// wizardFormField is one of the form screen's four navigable fields, in
// on-screen order.
type wizardFormField int

const (
	fieldNode wizardFormField = iota
	fieldWithin
	fieldThreads
	fieldMemNode
	numFormFields
)

// wizard is the pin-wizard v2 state machine: one form screen (node,
// within, threads, memory node, a live preview) plus the existing
// per-core manual grid as an alternative thread-list editor, entered via
// 'm' and returning to the form with its selection written into the
// threads field. App holds one (nil when closed); while non-nil, App
// routes key input to update and renders view in place of the tab body.
type wizard struct {
	vm         string
	base       *model.Snapshot // snapshot Propose ran against: the VM's own current pins/membind projected away, so its own threads read as free rather than occupied
	screen     wizardScreen
	stagedHash string
	stagedXML  string // domain XML at open time (same XML stagedHash hashes), for the drift screen's diff
	vcpus      int    // the VM's own vcpu count, fixed for the life of the wizard

	// Form screen state.
	node         int             // current node choice
	within       int             // -1 = "any", else an L3Domain.ID -- the form's thread-source filter
	threadsText  string          // editable cpulist text
	threadsCaret int             // caret position within threadsText, 0..len(threadsText)
	memSel       int             // -2 = "same as node", -1 = "leave", else an explicit node id
	field        wizardFormField // focused field
	proposal     *model.Proposal // last successful ProposeWithin(node, within) result; nil if that combination has no valid proposal (e.g. an L3 filter too small for VCPUs) -- 'a' and the preview/summary's Warnings fall back to nothing when nil

	// Manual screen state (an alternative editor for the form's threads
	// field, scoped to the form's current node -- see 'm').
	cursor   int          // core index within nodeCores(base, node)
	selected map[int]bool // thread ids selected
}

// newWizard opens a wizard for vm (vcpus its own vcpu count), seeded from
// proposal (Propose's own default: automatic node, any thread) --
// stagedHash/stagedXML carry the domain XML hash/text to stamp on the
// eventual op, and base is the self-stripped snapshot Propose ran
// against (openWizard's projection excluding vm's own current pins/
// membind) -- every render and re-propose uses base, not the App's plain
// projection, so the form/preview agree with the proposal they're built
// from.
func newWizard(vm string, proposal *model.Proposal, vcpus int, stagedHash, stagedXML string, base *model.Snapshot) *wizard {
	w := &wizard{
		vm:         vm,
		base:       base,
		screen:     formScreen,
		stagedHash: stagedHash,
		stagedXML:  stagedXML,
		vcpus:      vcpus,
		node:       proposal.Node,
		within:     -1,
		memSel:     -2,
		proposal:   proposal,
	}
	w.threadsText = formatCPURanges(assignedThreads(proposal.Pins))
	w.threadsCaret = len(w.threadsText)
	return w
}

// update handles one key on whichever screen is active. done reports the
// wizard should close; when done and op is non-nil, the caller stages it,
// else the wizard was cancelled. When confirmPrompt is non-empty, done is
// always false (the wizard stays open, unmodified) and the caller must
// instead open a y/n confirm with that prompt: a "yes" answer stages op
// verbatim and closes the wizard itself (see App.handleWizardKey) -- this
// is how a GPU-node-crossing stage is confirmed, replacing the old
// press-enter-twice softening with App's shared confirm dialog.
func (w *wizard) update(msg tea.KeyMsg) (done bool, op *model.PendingOp, confirmPrompt string) {
	if w.screen == manualScreen {
		return w.updateManual(msg)
	}
	return w.updateForm(msg)
}

// isThreadsChar reports whether r is a character the threads field
// accepts: digits, comma, dash -- exactly hostinfo.ParseCPUList's own
// cpulist grammar.
func isThreadsChar(r rune) bool {
	return (r >= '0' && r <= '9') || r == ',' || r == '-'
}

// updateForm handles one key on the form screen: up/down (or j/k)
// always move the focused field; left/right edit the caret when
// fieldThreads is focused, else cycle that field's value; 'a' re-fills
// threadsText from the last proposal; 'm' opens the manual grid; enter
// validates and either stages outright or (see tryStage) hands back a
// confirmPrompt for the caller to gate on instead; esc cancels the whole
// wizard.
func (w *wizard) updateForm(msg tea.KeyMsg) (bool, *model.PendingOp, string) {
	if w.field == fieldThreads {
		switch {
		case msg.Type == tea.KeyLeft:
			if w.threadsCaret > 0 {
				w.threadsCaret--
			}
			return false, nil, ""
		case msg.Type == tea.KeyRight:
			if w.threadsCaret < len(w.threadsText) {
				w.threadsCaret++
			}
			return false, nil, ""
		case msg.Type == tea.KeyBackspace:
			if w.threadsCaret > 0 {
				w.threadsText = w.threadsText[:w.threadsCaret-1] + w.threadsText[w.threadsCaret:]
				w.threadsCaret--
			}
			return false, nil, ""
		case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && isThreadsChar(msg.Runes[0]):
			r := string(msg.Runes[0])
			w.threadsText = w.threadsText[:w.threadsCaret] + r + w.threadsText[w.threadsCaret:]
			w.threadsCaret++
			return false, nil, ""
		}
	}

	switch {
	case msg.Type == tea.KeyUp, isRune(msg, 'k'):
		w.field = (w.field - 1 + numFormFields) % numFormFields
		return false, nil, ""
	case msg.Type == tea.KeyDown, isRune(msg, 'j'):
		w.field = (w.field + 1) % numFormFields
		return false, nil, ""
	case msg.Type == tea.KeyLeft, msg.Type == tea.KeyRight:
		delta := 1
		if msg.Type == tea.KeyLeft {
			delta = -1
		}
		switch w.field {
		case fieldNode:
			w.cycleNode(delta)
		case fieldWithin:
			w.cycleWithin(delta)
		case fieldMemNode:
			w.cycleMemNode(delta)
		}
		return false, nil, ""
	case isRune(msg, 'a'):
		w.autofillThreads()
		return false, nil, ""
	case isRune(msg, 'm'):
		w.screen = manualScreen
		w.cursor = 0
		ids, _ := w.parseThreads()
		w.selected = threadSet(ids)
		return false, nil, ""
	case msg.Type == tea.KeyEnter:
		return w.tryStage()
	case msg.Type == tea.KeyEsc:
		return true, nil, ""
	}
	return false, nil, ""
}

// tryStage validates the current form: an invalid thread list never
// stages (the error already shows under the field via view()) and
// returns no confirm prompt either. A valid list that crosses the GPU
// node builds the op right here, from the current field values, and hands
// it back with the confirm prompt the caller (App.handleWizardKey) must
// put to the operator via the shared App.confirm dialog -- the op it
// stages on "yes" is exactly this one, snapshotted at the moment enter was
// pressed, not re-read later; cycleNode/cycleWithin/cycleMemNode need no
// "reset the armed confirm" bookkeeping of their own because of this (the
// old crossConfirmed flag they used to clear is gone). A valid,
// non-crossing list always stages outright on enter, same as before.
func (w *wizard) tryStage() (bool, *model.PendingOp, string) {
	ids, errMsg := w.parseThreads()
	if errMsg != "" {
		return false, nil, ""
	}
	op := w.buildOp(ids)
	if w.crossesGPUWarning() != "" {
		return false, &op, "Pin across the GPU's node anyway? [y/n]"
	}
	return true, &op, ""
}

// cycleNode advances w.node by delta (wrapping) in topology node-ID
// order, resetting within to "any" (an L3 domain id from the old node
// may not even exist on the new one) and re-proposing within it --
// GPU locality is a soft preference, never enforced: crossesGPUWarning
// (surfaced in view and the staged Summary) is the only consequence of
// picking a node other than the VM's GPU node.
func (w *wizard) cycleNode(delta int) {
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
	idx = (idx + delta + len(nodes)) % len(nodes)
	w.node = nodes[idx].ID
	w.within = -1
	w.reproposeAndFill()
}

// withinOptions returns the "within" field's cycle order: -1 ("any")
// first, then every L3Domain.ID actually on w.node, in topology order.
func (w *wizard) withinOptions() []int {
	opts := []int{-1}
	for _, l3 := range w.base.Topo.L3Domains {
		if l3.Node == w.node {
			opts = append(opts, l3.ID)
		}
	}
	return opts
}

// cycleWithin advances the "within" field by delta (wrapping) and
// re-proposes with the new filter.
func (w *wizard) cycleWithin(delta int) {
	opts := w.withinOptions()
	idx := 0
	for i, o := range opts {
		if o == w.within {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(opts)) % len(opts)
	w.within = opts[idx]
	w.reproposeAndFill()
}

// memNodeOptions returns the "memory node" field's cycle order: "same as
// node" (-2) first, then every topology node id, then "leave" (-1) --
// the brief's own "< same as node | node 0 | node 1 | leave >" order.
func (w *wizard) memNodeOptions() []int {
	opts := []int{-2}
	for _, n := range w.base.Topo.Nodes {
		opts = append(opts, n.ID)
	}
	return append(opts, -1)
}

// cycleMemNode advances the "memory node" field by delta (wrapping).
// Independent of node/within -- it never re-proposes. It needs no
// "confirm" bookkeeping of its own: tryStage builds the confirm's op from
// whatever memSel (and every other field) reads at the moment enter is
// pressed, so any earlier cycleMemNode call is already reflected in it.
func (w *wizard) cycleMemNode(delta int) {
	opts := w.memNodeOptions()
	idx := 0
	for i, o := range opts {
		if o == w.memSel {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(opts)) % len(opts)
	w.memSel = opts[idx]
}

// resolvedMemNode turns memSel into the actual node id OpPin.MemNode
// expects: -2 ("same as node") resolves to w.node itself; -1 ("leave")
// and an explicit node id pass through unchanged.
func (w *wizard) resolvedMemNode() int {
	if w.memSel == -2 {
		return w.node
	}
	return w.memSel
}

// l3ThreadsOnNode returns the thread ids of every core on node whose L3
// domain is l3ID.
func l3ThreadsOnNode(s *model.Snapshot, node, l3ID int) []int {
	var threads []int
	for _, c := range s.Topo.Cores {
		if c.Node == node && c.L3 == l3ID {
			threads = append(threads, c.Threads...)
		}
	}
	return threads
}

// reproposeAndFill re-runs ProposeWithin for the form's current node/
// within and, on success, overwrites threadsText with the fresh
// proposal's own thread list (formatCPURanges, matching the field's
// display convention) -- called after every node/within change, per the
// brief ("re-proposes on change"). A failure (e.g. an L3 filter with
// fewer threads than VCPUs) clears w.proposal and leaves threadsText as
// the operator last had it; the field's own live validation reports the
// resulting error, same as if they'd typed something invalid by hand.
func (w *wizard) reproposeAndFill() {
	var allowed []int
	if w.within >= 0 {
		allowed = l3ThreadsOnNode(w.base, w.node, w.within)
	}
	p, err := model.ProposeWithin(w.base, w.vm, w.node, allowed)
	if err != nil {
		w.proposal = nil
		return
	}
	w.proposal = p
	w.threadsText = formatCPURanges(assignedThreads(p.Pins))
	w.threadsCaret = len(w.threadsText)
}

// autofillThreads implements 'a': re-fills threadsText from the current
// proposal, if there is one (see reproposeAndFill's failure case).
func (w *wizard) autofillThreads() {
	if w.proposal == nil {
		return
	}
	w.threadsText = formatCPURanges(assignedThreads(w.proposal.Pins))
	w.threadsCaret = len(w.threadsText)
}

// parseThreads validates threadsText: a cpulist (hostinfo.ParseCPUList)
// naming exactly vcpus threads, every one on w.node, actually online
// (present in base.Topo.Threads -- an offline CPU has no Thread entry at
// all, same as one that never existed), and -- when the "within" field
// has an L3 filter set (w.within >= 0) -- inside that L3 domain too, so a
// hand-typed list can't quietly slip in a thread from a different L3
// domain on the same node while the field still shows the narrower
// filter. Returns the parsed, sorted thread ids and "" on success, or nil
// and the first problem found.
func (w *wizard) parseThreads() ([]int, string) {
	ids, err := hostinfo.ParseCPUList(w.threadsText)
	if err != nil {
		return nil, "invalid thread list (digits, commas, dashes only)"
	}
	if len(ids) != w.vcpus {
		return nil, fmt.Sprintf("%d threads given, need %d", len(ids), w.vcpus)
	}
	var allowed map[int]bool
	if w.within >= 0 {
		allowed = threadSet(l3ThreadsOnNode(w.base, w.node, w.within))
	}
	for _, t := range ids {
		th, ok := w.base.Topo.Threads[t]
		if !ok {
			return nil, fmt.Sprintf("thread %d is offline or does not exist", t)
		}
		if th.Node != w.node {
			return nil, fmt.Sprintf("thread %d is on node %d", t, th.Node)
		}
		if allowed != nil && !allowed[t] {
			return nil, fmt.Sprintf("thread %d not in L3 #%d", t, w.within)
		}
	}
	return ids, ""
}

// siblingHints returns one "core N shares with: <vm>" line per core in
// ids that is only half-selected (one of its two threads is in ids, the
// other isn't) whose OTHER thread is already claimed by some VM --
// naming which one, so pinning ids doesn't quietly leave a core split
// between two VMs without warning.
func (w *wizard) siblingHints(ids []int) []string {
	selected := threadSet(ids)
	var hints []string
	reported := map[int]bool{}
	for _, t := range ids {
		th, ok := w.base.Topo.Threads[t]
		if !ok || th.Sibling < 0 || reported[t] || reported[th.Sibling] {
			continue
		}
		reported[t], reported[th.Sibling] = true, true
		if selected[th.Sibling] {
			continue // both siblings selected: not half-used
		}
		use := w.base.Use[th.Sibling]
		if len(use.VMs) > 0 {
			hints = append(hints, fmt.Sprintf("core %d shares with: %s", th.Core, strings.Join(use.VMs, ", ")))
		}
	}
	return hints
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

// crossesGPUWarning returns the form's non-blocking warning when w.node
// differs from the VM's GPU node, or "" when the VM has no GPU (or an
// unresolved one) or w.node already matches it. Never blocks staging on
// its own -- see tryStage's confirm-on-first-enter softening instead.
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

// nodeHint renders the node field's per-node hint: the VM's own GPU
// location when node is it ("... (recommended)"), else how free node
// currently is.
func (w *wizard) nodeHint(node int) string {
	vm := w.base.VM(w.vm)
	if vm != nil {
		if gpu := vmGPUDevice(vm); gpu != nil && gpu.Node == node {
			return fmt.Sprintf("GPU %s on node %d (recommended)", gpu.Addr, node)
		}
	}
	return fmt.Sprintf("free: %d cores, %s", model.FullyFreeCoreCount(w.base, node), fmtKiB(model.FreeMemKiB(w.base, node)))
}

// withinLabel renders the "within" field's current value: "any" for -1,
// else "L3 #<id>".
func withinLabel(id int) string {
	if id < 0 {
		return "any"
	}
	return fmt.Sprintf("L3 #%d", id)
}

// memSelLabel renders the "memory node" field's current value.
func memSelLabel(sel int) string {
	switch sel {
	case -2:
		return "same as node"
	case -1:
		return "leave"
	default:
		return fmt.Sprintf("node %d", sel)
	}
}

// buildOp stages an OpPin from ids (vcpu i -> ids[i] in ascending thread
// order, matching every other pin path in this package): assigns memory
// per resolvedMemNode and carries the wizard's StagedHash, formatting
// Summary per the fixed convention -- " (crosses GPU node)" appended
// when crossesGPUWarning is non-empty, so the Pending tab (and the apply
// review) can flag it too (see pendingCrossesGPU in pending.go).
func (w *wizard) buildOp(ids []int) model.PendingOp {
	sorted := append([]int(nil), ids...)
	sort.Ints(sorted)
	pins := make(map[int][]int, len(sorted))
	for vcpu, t := range sorted {
		pins[vcpu] = []int{t}
	}
	memNode := w.resolvedMemNode()
	memText := fmt.Sprintf("node %d (strict)", memNode)
	if memNode == -1 {
		memText = "unchanged"
	}
	summary := fmt.Sprintf("%s: pin %d vcpus -> node %d threads %s; memory -> %s",
		w.vm, len(pins), w.node, formatCPURanges(sorted), memText)
	if w.crossesGPUWarning() != "" {
		summary += " (crosses GPU node)"
	}
	return model.PendingOp{
		Kind:       model.OpPin,
		VM:         w.vm,
		Pins:       pins,
		MemNode:    memNode,
		StagedHash: w.stagedHash,
		StagedXML:  w.stagedXML,
		Summary:    summary,
	}
}

// updateManual handles one key on the manual grid: an alternative
// editor for the form's threads field, scoped to the form's current
// node. enter writes the current selection back into threadsText (as a
// cpulist, via formatCPURanges) and returns to the form -- any count is
// accepted here; the form's own live validation reports a count
// mismatch, same as if the operator had typed it by hand. esc returns
// to the form without touching threadsText.
func (w *wizard) updateManual(msg tea.KeyMsg) (bool, *model.PendingOp, string) {
	cores := nodeCores(w.base, w.node)

	switch {
	case msg.Type == tea.KeyLeft, isRune(msg, 'h'):
		if w.cursor > 0 {
			w.cursor--
		}
		return false, nil, ""
	case msg.Type == tea.KeyRight, isRune(msg, 'l'):
		if w.cursor < len(cores)-1 {
			w.cursor++
		}
		return false, nil, ""
	case msg.Type == tea.KeyUp, isRune(msg, 'k'):
		if w.cursor-coresPerRow >= 0 {
			w.cursor -= coresPerRow
		}
		return false, nil, ""
	case msg.Type == tea.KeyDown, isRune(msg, 'j'):
		if w.cursor+coresPerRow < len(cores) {
			w.cursor += coresPerRow
		}
		return false, nil, ""
	case msg.Type == tea.KeySpace:
		w.toggleCore(cores)
		return false, nil, ""
	case msg.Type == tea.KeyEnter:
		w.threadsText = formatCPURanges(assignedThreads(assignSelected(w.selected)))
		w.threadsCaret = len(w.threadsText)
		w.screen = formScreen
		return false, nil, ""
	case msg.Type == tea.KeyEsc:
		w.screen = formScreen
		return false, nil, ""
	}
	return false, nil, ""
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

// formLabelWidth is the widest field label ("memory node"), so every
// field's value column starts at the same column.
const formLabelWidth = len("memory node")

// fieldLabel names f.
func fieldLabel(f wizardFormField) string {
	switch f {
	case fieldNode:
		return "node"
	case fieldWithin:
		return "within"
	case fieldThreads:
		return "threads"
	case fieldMemNode:
		return "memory node"
	}
	return ""
}

// renderFieldRow renders one "<label>  <value>" row, reverse-video on
// value when f is the focused field.
func (w *wizard) renderFieldRow(f wizardFormField, value string) string {
	if w.field == f {
		value = cursorStyle.Render(value)
	}
	return fmt.Sprintf("%-*s  %s", formLabelWidth, fieldLabel(f), value)
}

// threadsFieldValue renders the threads field's bracketed text box,
// splicing in a reverse-video caret at threadsCaret when the field is
// focused.
func (w *wizard) threadsFieldValue() string {
	text := w.threadsText
	if w.field != fieldThreads {
		return "[" + text + "]"
	}
	if w.threadsCaret >= len(text) {
		return "[" + text + cursorStyle.Render(" ") + "]"
	}
	return "[" + text[:w.threadsCaret] + cursorStyle.Render(text[w.threadsCaret:w.threadsCaret+1]) + text[w.threadsCaret+1:] + "]"
}

// currentWarnings returns the last proposal's own Warnings (share-count/
// memory-shortfall) when ids is a currently-valid thread list -- an
// invalid one already shows its own error under the threads field
// instead. The GPU-cross warning is deliberately NOT repeated here:
// viewForm already prints it loud (gpuWarningStyle) right under the node
// field, and listing it a second time in this tail-of-popup warnings list
// would just be visual noise -- see crossesGPUWarning's own caller in
// viewForm.
func (w *wizard) currentWarnings(ids []int) []string {
	var warnings []string
	if len(ids) > 0 && w.proposal != nil {
		warnings = append(warnings, w.proposal.Warnings...)
	}
	return warnings
}

// summaryLine renders the preview's one-line summary, e.g. "12 vcpus ->
// node 1 threads 0-5,12-17; memory -> node 1 (strict)".
func (w *wizard) summaryLine(ids []int) string {
	threads := w.threadsText
	if len(ids) > 0 {
		threads = formatCPURanges(ids)
	}
	memNode := w.resolvedMemNode()
	memText := fmt.Sprintf("node %d (strict)", memNode)
	if memNode == -1 {
		memText = "unchanged"
	}
	return fmt.Sprintf("%d vcpus -> node %d threads %s; memory -> %s", w.vcpus, w.node, threads, memText)
}

// view renders the active screen against w.base as a single self-
// contained dialog panel dw wide -- one border, not two stacked boxes --
// top-down truncated to fit budget (the same "never exceed the popup"
// rule every dialog in this package follows). Alongside the string it
// returns the active screen's own clickable hits, 0-based relative to
// the wizard panel's own top-left corner.
func (w *wizard) view(dw, budget int) (string, []hit) {
	if w.screen == manualScreen {
		return w.viewManual(dw, budget)
	}
	return w.viewForm(dw, budget)
}

// viewForm renders the form screen: node/within/threads/memory-node
// fields (each a click-to-focus row, "formfield" hit kind, index the
// wizardFormField), their own hint/error/warning lines, a rule, then the
// preview -- a dense glyph strip of w.node (renderNodeMap, no cursor --
// the preview isn't itself clickable, unlike the manual grid) windowed
// to whatever budget is left (so a node with more rows than fit still
// renders, just clipped -- see the width/height invariant tests), the
// one-line summary, and the current warnings.
func (w *wizard) viewForm(dw, budget int) (string, []hit) {
	title := fmt.Sprintf("pin %s (%d vcpus)", w.vm, w.vcpus)
	inner := dw - 2
	if inner < 1 {
		inner = 1
	}

	var head []string
	var hits []hit
	addField := func(f wizardFormField, row string) {
		hits = append(hits, hit{y0: len(head), y1: len(head) + 1, x0: 0, x1: inner, kind: "formfield", index: int(f)})
		head = append(head, row)
	}

	addField(fieldNode, w.renderFieldRow(fieldNode, fmt.Sprintf("< node %d >", w.node)))
	head = append(head, "  "+w.nodeHint(w.node))
	if warn := w.crossesGPUWarning(); warn != "" {
		head = append(head, strings.Split(lipgloss.NewStyle().Width(inner).Render(gpuWarningStyle.Render(warn)), "\n")...)
	}

	addField(fieldWithin, w.renderFieldRow(fieldWithin, fmt.Sprintf("< %s >", withinLabel(w.within))))

	ids, errMsg := w.parseThreads()
	addField(fieldThreads, w.renderFieldRow(fieldThreads, w.threadsFieldValue()))
	if errMsg != "" {
		head = append(head, warningStyle.Render(errMsg))
	}
	head = append(head, w.siblingHints(ids)...)

	addField(fieldMemNode, w.renderFieldRow(fieldMemNode, fmt.Sprintf("< %s >", memSelLabel(w.memSel))))

	head = append(head, strings.Repeat("-", inner))

	var tail []string
	tail = append(tail, w.summaryLine(ids))
	for _, wm := range w.currentWarnings(ids) {
		tail = append(tail, warningStyle.Render(wm))
	}

	contentBudget := budget - 2
	if contentBudget < 1 {
		contentBudget = 1
	}
	previewBudget := contentBudget - len(head) - len(tail)
	if previewBudget < 0 {
		previewBudget = 0
	}
	var highlight map[int]bool
	if len(ids) > 0 {
		highlight = threadSet(ids)
	}
	// truncateLines clamps the grid to inner *before* windowing: a wide
	// node (200+ single-thread cores, 32/row, 95 columns) is wider than
	// inner at most widths, and panelInner's own lipgloss Width(inner)
	// call word-wraps a too-wide body instead of truncating it -- which
	// would silently add extra visual lines beyond what kept/contentBudget
	// below accounts for, overflowing the popup (the exact class of bug
	// the topology package's clampBoxWidth fixes for its own boxes).
	grid, _ := renderNodeMap(w.base, w.node, highlight, -1, "")
	grid = truncateLines(grid, inner)
	gridLines, _, total := windowWithFooter(strings.Split(grid, "\n"), previewBudget, 0)
	preview := strings.Join(gridLines, "\n")
	if footer := scrollFooter(0, len(gridLines), total); footer != "" {
		preview += "\n" + keyBarLabelStyle.Render(footer)
	}

	lines := append([]string{}, head...)
	lines = append(lines, strings.Split(preview, "\n")...)
	lines = append(lines, tail...)

	kept := len(lines)
	if kept > contentBudget {
		lines = lines[:contentBudget]
		kept = contentBudget
	}
	// truncateLines again as a final safety net over the whole body (a
	// long thread list in the summary line, or a node hint, could still
	// exceed inner on its own) -- mirrors buildTopologyTabMode's own
	// final truncateLines pass.
	body := truncateLines(strings.Join(lines, "\n"), inner)
	panel := panelInner(title, body, dw, 0)
	return panel, offsetHits(clipHitsToWindow(hits, kept, inner), 1, 1)
}

// viewManual renders the manual grid screen: the node-map grid
// (truncated, never wrapped), a "-" rule, then a short info line, all
// inside the same titled panel, top-down truncated to fit budget.
func (w *wizard) viewManual(dw, budget int) (string, []hit) {
	title := fmt.Sprintf("pin %s (%d vcpus) -> node %d: manual", w.vm, w.vcpus, w.node)

	grid, gridHits := renderNodeMap(w.base, w.node, w.selected, w.cursor, "wizardcore")
	var info strings.Builder
	fmt.Fprintf(&info, "selected %d/%d\n", len(w.selected), w.vcpus)
	if warn := w.crossesGPUWarning(); warn != "" {
		info.WriteString(warningStyle.Render(warn))
		info.WriteString("\n")
	}

	inner := dw - 2
	if inner < 1 {
		inner = 1
	}
	gridLines := strings.Split(truncateLines(grid, inner), "\n")
	var infoLines []string
	if infoText := strings.TrimRight(info.String(), "\n"); infoText != "" {
		infoLines = strings.Split(lipgloss.NewStyle().Width(inner).Render(infoText), "\n")
	}

	lines := append([]string{}, gridLines...)
	if len(infoLines) > 0 {
		lines = append(lines, strings.Repeat("-", inner))
		lines = append(lines, infoLines...)
	}

	contentBudget := budget - 2
	if contentBudget < 1 {
		contentBudget = 1
	}
	kept := len(lines)
	if kept > contentBudget {
		lines = lines[:contentBudget]
		kept = contentBudget
	}
	panel := panelInner(title, strings.Join(lines, "\n"), dw, 0)

	gridKept := kept
	if gridKept > len(gridLines) {
		gridKept = len(gridLines)
	}
	hits := offsetHits(clipHitsToWindow(gridHits, gridKept, inner), 1, 1)
	return panel, hits
}

// statusBarHint returns the status bar's replacement content while the
// wizard is open: its own keys, since edit/quit/pin/strip are inert while
// a wizard is capturing all key input.
func (w *wizard) statusBarHint() string {
	if w.screen == manualScreen {
		return "[h/l/j/k/up/down] move  [space] toggle  [enter] accept  [esc] back"
	}
	return "[up/down] field  [left/right] change/edit  [a] autofill  [m] manual  [enter] stage  [esc] cancel"
}

// openWizard implements the 'p' key on the VMs tab: after the shared
// edit-mode/supported gates and fetching the domain's current XML (for
// StagedHash), it builds a Propose-ready projection that excludes the VM's
// own current pins/membind -- so re-pinning an already-pinned VM does not
// see its own claim as occupied -- and opens the wizard on the result. The
// wizard keeps that same projection (base) for its own rendering, so the
// preview it draws never disagrees with the proposal it is illustrating.
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

	a.wizard = newWizard(vm.Name, proposal, vm.VCPUs, hash, xml, base)
	a.status = ""
	return a, nil
}

// handleWizardKey routes a key to the open wizard, staging or discarding it
// once the wizard reports done. When the wizard instead hands back a
// confirmPrompt (a valid stage that crosses the VM's GPU node), the
// wizard stays open and untouched -- App opens its own shared y/n confirm
// dialog on top of it instead: "y" stages op verbatim and closes the
// wizard; "n"/esc (handled by App.handleConfirmKey, routed there ahead of
// the wizard -- see App.handleKey) just dismiss the confirm, leaving the
// form/manual screen exactly as the operator left it.
func (a *App) handleWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	done, op, prompt := a.wizard.update(msg)
	if prompt != "" {
		staged := *op
		a.confirm = &confirm{
			prompt: prompt,
			yes: func() tea.Cmd {
				a.queue.Add(staged)
				a.status = "staged: " + staged.Summary
				a.wizard = nil
				return nil
			},
		}
		return a, nil
	}
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

// handleWizardMouse routes a left click: on the form screen, a click on
// a field row focuses it (matching the brief's "navigated with up/down
// (or click)"); on the manual screen, a click on the node map moves the
// cursor to the clicked core and toggles it, same as arrow keys + space.
func (a *App) handleWizardMouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return
	}
	if a.wizard.screen != manualScreen {
		for _, h := range a.hits {
			if h.kind == "formfield" && msg.Y >= h.y0 && msg.Y < h.y1 && msg.X >= h.x0 && msg.X < h.x1 {
				a.wizard.field = wizardFormField(h.index)
				return
			}
		}
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
// hit of the given kind per cell (kind "" records no hits at all -- the
// wizard form's own preview isn't clickable), 0-based relative to the
// grid's own top-left corner (x0 = the cell's column * 3), indexed by its
// position in nodeCores(s, node) -- the CPU Map tab, whose cursor is a
// *global* s.Topo.Cores index, translates that back via
// globalCoreIndices.
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
		if kind != "" {
			hits = append(hits, hit{y0: row, y1: row + 1, x0: x0, x1: x0 + 2, kind: kind, index: i})
		}
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
