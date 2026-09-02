package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/apply"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// flowScreen is one of the four screens the apply flow can show.
type flowScreen int

const (
	flowReview  flowScreen = iota
	flowRunning            // drift check or apply in flight; no key does anything
	flowDrift
	flowResults
)

// applyFlow is the apply-review/drift/results state machine. App holds one
// (nil when closed); while non-nil, App routes key input to
// handleFlowKey and renders view in place of the tab body, mirroring how
// the wizard replaces the body while open.
//
// Stale-message guarding (a driftCheckedMsg/applyDoneMsg arriving after its
// round no longer applies) is done via App.flowGen, not a counter on
// applyFlow itself -- a per-flow counter would restart at zero for every
// new applyFlow, so a leftover message from one flow could collide with an
// unrelated later flow's gen. See App.flowGen's doc comment.
type applyFlow struct {
	screen        flowScreen
	ops           []model.PendingOp // snapshot of the queue when the review screen opened
	drifted       []model.PendingOp // ops whose VM changed since staging
	diffs         []string          // diffLines(op.StagedXML, current live xml), same order/index as drifted
	sel           int               // selected row in drifted, drift screen only
	results       []apply.Result
	resultsScroll int    // scroll offset into the results screen, up/down/wheel-driven (results has no other "selected row" to derive one from)
	running       string // body text shown on the flowRunning screen
}

// driftCheckedMsg carries the result of running apply.CheckDrift, mapped
// back onto the queued ops for the drifted VMs (in queue order), plus a
// precomputed diff of each drifted op's staged XML against its current
// live XML.
type driftCheckedMsg struct {
	gen     int
	drifted []model.PendingOp
	diffs   []string
	err     error
}

// applyDoneMsg carries the result of running apply.Run (which itself has no
// error return: every per-op failure is carried in its Result instead).
type applyDoneMsg struct {
	gen     int
	results []apply.Result
}

// openApplyFlow implements the 'a' key (any tab): after the edit-mode and
// non-empty-queue gates, opens the apply review screen listing every
// queued op.
func (a *App) openApplyFlow() (tea.Model, tea.Cmd) {
	if !a.editMode {
		a.status = "press e to enter edit mode first"
		return a, nil
	}
	if a.queue.Len() == 0 {
		a.status = "nothing to apply"
		return a, nil
	}
	a.flow = &applyFlow{screen: flowReview, ops: append([]model.PendingOp(nil), a.queue.Ops...)}
	a.status = ""
	return a, nil
}

// handleFlowKey routes a key to whichever screen a.flow is showing.
func (a *App) handleFlowKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.flow.screen {
	case flowReview:
		switch {
		case isRune(msg, 'y'):
			// Move to flowRunning immediately: once confirmed, cancelling is
			// impossible (no case below handles a key on that screen), so
			// there is no window where esc/n can race the in-flight Cmd.
			a.flowGen++
			a.flow.screen = flowRunning
			a.flow.running = "checking for drift..."
			a.status = ""
			return a, a.checkDriftCmd(a.flowGen)
		case isRune(msg, 'n'), msg.Type == tea.KeyEsc:
			a.flow = nil
		}
	case flowRunning:
		// Ignore every key: the drift check or apply run is already in
		// flight and cannot be cancelled.
	case flowDrift:
		switch {
		case msg.Type == tea.KeyUp, isRune(msg, 'k'):
			a.moveFlowSel(-1)
		case msg.Type == tea.KeyDown, isRune(msg, 'j'):
			a.moveFlowSel(1)
		case isRune(msg, 'd'):
			a.discardDrifted()
		case isRune(msg, 'w'):
			return a.reopenDrifted()
		case msg.Type == tea.KeyEsc:
			// Back to browsing: no writes have happened yet, so the
			// remaining (undrifted) ops just stay queued untouched.
			a.flow = nil
			a.status = ""
		}
	case flowResults:
		// up/down/j/k scroll a long results list (one line per applied
		// op); any other key dismisses the screen, then a rescan is
		// issued.
		switch {
		case msg.Type == tea.KeyUp, isRune(msg, 'k'):
			a.flow.resultsScroll--
			a.clampResultsScroll()
			return a, nil
		case msg.Type == tea.KeyDown, isRune(msg, 'j'):
			a.flow.resultsScroll++
			a.clampResultsScroll()
			return a, nil
		}
		a.flow = nil
		a.status = "rescanning..."
		return a, a.scanCmd()
	}
	return a, nil
}

// checkDriftCmd runs apply.CheckDrift against the queue and maps the
// drifted VM names back onto their queued ops, in queue order, alongside a
// precomputed diff of each drifted op's staged XML against its current
// live XML (a second DomainXML fetch per drifted VM -- CheckDrift itself
// only returns names). gen is stamped onto the resulting message so a
// stale round can be dropped.
func (a *App) checkDriftCmd(gen int) tea.Cmd {
	ops := append([]model.PendingOp(nil), a.queue.Ops...)
	return func() tea.Msg {
		names, err := apply.CheckDrift(a.hv, ops)
		if err != nil {
			return driftCheckedMsg{gen: gen, err: err}
		}
		driftedSet := make(map[string]bool, len(names))
		for _, n := range names {
			driftedSet[n] = true
		}
		var drifted []model.PendingOp
		var diffs []string
		for _, op := range ops {
			if !driftedSet[op.VM] {
				continue
			}
			drifted = append(drifted, op)
			current, err := a.hv.DomainXML(op.VM)
			if err != nil {
				diffs = append(diffs, fmt.Sprintf("error fetching current xml: %v", err))
				continue
			}
			diffs = append(diffs, diffLines(op.StagedXML, current))
		}
		return driftCheckedMsg{gen: gen, drifted: drifted, diffs: diffs}
	}
}

// handleDriftChecked reacts to a driftCheckedMsg: any drift opens the drift
// screen; a clean check proceeds straight into apply.Run. A nil flow (the
// user somehow closed it) or a stale gen (a leftover message from an
// earlier round, or from an entirely different flow -- App.flowGen is
// monotonic across flows) is silently dropped rather than acted on.
func (a *App) handleDriftChecked(msg driftCheckedMsg) (tea.Model, tea.Cmd) {
	if a.flow == nil || msg.gen != a.flowGen {
		return a, nil
	}
	if msg.err != nil {
		a.status = msg.err.Error()
		a.flow = nil
		return a, nil
	}
	if len(msg.drifted) > 0 {
		a.flow.screen = flowDrift
		a.flow.drifted = msg.drifted
		a.flow.diffs = msg.diffs
		a.flow.sel = 0
		a.status = ""
		return a, nil
	}
	a.flowGen++
	a.flow.running = "applying..."
	return a, a.runApplyCmd(a.flowGen)
}

// runApplyCmd runs apply.Run against a snapshot of the current queue. gen
// is stamped onto the resulting message so a stale round can be dropped.
func (a *App) runApplyCmd(gen int) tea.Cmd {
	ops := append([]model.PendingOp(nil), a.queue.Ops...)
	return func() tea.Msg {
		return applyDoneMsg{gen: gen, results: apply.Run(a.hv, a.backupDir, a.version, ops)}
	}
}

// handleApplyDone reacts to an applyDoneMsg: shows the results screen and
// clears only the ops that actually applied, leaving failed/skipped ones
// queued so the user can inspect or discard them. A nil flow or stale gen
// is dropped, same as handleDriftChecked.
func (a *App) handleApplyDone(msg applyDoneMsg) (tea.Model, tea.Cmd) {
	if a.flow == nil || msg.gen != a.flowGen {
		return a, nil
	}
	a.flow.screen = flowResults
	a.flow.results = msg.results
	a.status = ""

	var kept []model.PendingOp
	for _, r := range msg.results {
		if !r.Applied {
			kept = append(kept, r.Op)
		}
	}
	a.queue.Ops = kept
	a.clampPendingSel()
	return a, nil
}

// moveFlowSel shifts the drift screen's selection by delta rows, clamped to
// [0, len(drifted)-1].
func (a *App) moveFlowSel(delta int) {
	if a.flow == nil || len(a.flow.drifted) == 0 {
		return
	}
	a.flow.sel += delta
	if a.flow.sel < 0 {
		a.flow.sel = 0
	}
	if max := len(a.flow.drifted) - 1; a.flow.sel > max {
		a.flow.sel = max
	}
}

// discardDrifted implements 'd' on the drift screen: drops the selected
// drifted op from the queue and from the drift list. Once the drift list
// empties, the flow closes -- the user presses 'a' again to re-review and
// re-check the (now smaller) queue; nothing auto-applies.
func (a *App) discardDrifted() {
	if a.flow == nil || len(a.flow.drifted) == 0 {
		return
	}
	op := a.flow.drifted[a.flow.sel]
	a.removeQueueOp(op.VM)
	a.dropFlowDrifted()
	if len(a.flow.drifted) == 0 {
		a.flow = nil
		a.status = "drift resolved; press a to apply"
	}
}

// reopenDrifted implements 'w' on the drift screen: drops the selected
// drifted op from the queue and closes the flow outright, regardless of
// how many other drifted ops remain -- nothing auto-applies, so the user
// presses 'a' again to re-review and re-check whatever is still queued.
// app.go's scanDoneMsg handling re-opens the wizard for that VM once the
// rescan lands (see openWizardFor).
func (a *App) reopenDrifted() (tea.Model, tea.Cmd) {
	if a.flow == nil || len(a.flow.drifted) == 0 {
		return a, nil
	}
	op := a.flow.drifted[a.flow.sel]
	a.removeQueueOp(op.VM)
	a.flow = nil
	a.reopenVM = op.VM
	a.status = "rescanning..."
	return a, a.scanCmd()
}

// dropFlowDrifted removes the currently-selected row from a.flow.drifted
// (and the matching a.flow.diffs entry, if present) and clamps the
// selection into the shrunk list.
func (a *App) dropFlowDrifted() {
	d := a.flow.drifted
	a.flow.drifted = append(d[:a.flow.sel], d[a.flow.sel+1:]...)
	if diffs := a.flow.diffs; a.flow.sel < len(diffs) {
		a.flow.diffs = append(diffs[:a.flow.sel], diffs[a.flow.sel+1:]...)
	}
	if a.flow.sel >= len(a.flow.drifted) {
		a.flow.sel = len(a.flow.drifted) - 1
	}
	if a.flow.sel < 0 {
		a.flow.sel = 0
	}
}

// removeQueueOp drops the queued op for vm, if any, and clamps pendingSel
// back into range (removal can only shrink the queue).
func (a *App) removeQueueOp(vm string) {
	for i, op := range a.queue.Ops {
		if op.VM == vm {
			a.queue.Remove(i)
			a.clampPendingSel()
			return
		}
	}
}

// openWizardFor opens the pin wizard for vm by first pointing a.vmSel at
// its index in the freshly-scanned snapshot, then delegating to openWizard.
// A no-op if vm is no longer present (e.g. removed from libvirt between
// staging and the rescan).
func (a *App) openWizardFor(vm string) {
	if a.snap == nil {
		return
	}
	for i := range a.snap.VMs {
		if a.snap.VMs[i].Name == vm {
			a.vmSel = i
			a.openWizard()
			return
		}
	}
}

// view renders whichever screen f.screen selects. h is the terminal height
// (0 in tests that never sent a WindowSizeMsg), used only by flowDrift to
// budget how many diff lines it can show. Key hints aren't repeated here
// -- the status bar (statusBarHint) is their one place -- except the
// per-op "backup will be written first" effect line, which isn't a key
// hint.
func (f *applyFlow) view(w, h int) string {
	var b strings.Builder
	switch f.screen {
	case flowReview:
		b.WriteString("Apply pending operations?\n\n")
		for i, op := range f.ops {
			fmt.Fprintf(&b, "%d. %s\n   backup will be written first; takes effect on next VM boot\n", i+1, op.Summary)
		}
	case flowRunning:
		b.WriteString(f.running)
	case flowDrift:
		var head strings.Builder
		head.WriteString("Drift detected -- these VMs changed since staging:\n\n")
		for i, op := range f.drifted {
			line := fmt.Sprintf("%d. %s", i+1, op.Summary)
			if i == f.sel {
				line = cursorStyle.Render(line)
			}
			head.WriteString(line)
			head.WriteString("\n")
		}
		b.WriteString(head.String())
		if f.sel >= 0 && f.sel < len(f.diffs) {
			b.WriteString("\n")
			diff := truncateLines(colorDiff(f.diffs[f.sel]), w-2)
			// used counts the lines actually rendered above the diff (the
			// heading/op-list block plus the blank line just written), not
			// a guess derived from len(f.drifted) -- a long op Summary
			// wrapping to more than one line, or many drifted ops, is
			// reflected exactly rather than approximated.
			used := lineCount(head.String()) + 1
			b.WriteString(capDiff(diff, diffBudget(h, used)))
			b.WriteString("\n")
		}
	case flowResults:
		for _, r := range f.results {
			switch {
			case r.Applied:
				b.WriteString(statusOKStyle.Render(fmt.Sprintf("OK %s (backup: %s)", r.Op.Summary, r.BackupPath)))
			case r.Err != nil:
				b.WriteString(statusErrStyle.Render(fmt.Sprintf("FAILED %s: %v", r.Op.Summary, r.Err)))
			case r.Drifted:
				fmt.Fprintf(&b, "SKIPPED %s: drifted", r.Op.Summary)
			default:
				fmt.Fprintf(&b, "SKIPPED %s: not attempted", r.Op.Summary)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// capDiff truncates diff to at most max lines, appending a count of how
// many more there were when it does.
func capDiff(diff string, max int) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= max {
		return diff
	}
	return strings.Join(lines[:max], "\n") + fmt.Sprintf("\n... %d more lines", len(lines)-max)
}

// diffBudget returns how many diff lines the drift screen can show given
// the terminal height h and used other lines already spent on that
// screen's chrome (heading, op list, blank line, key hint). An unmeasured
// h (0, e.g. before the first WindowSizeMsg -- notably in tests that never
// send one) falls back to a generous fixed budget instead of showing
// nothing; a genuinely short terminal still gets a floor of 3 lines.
func diffBudget(h, used int) int {
	if h <= 0 {
		return 20
	}
	if budget := h - used; budget >= 3 {
		return budget
	}
	return 3
}

// statusBarHint returns the status bar's replacement content while the
// apply flow is open: its own keys, since edit/quit/apply/discard/pin/strip
// are inert while a flow screen is capturing all key input.
func (f *applyFlow) statusBarHint() string {
	switch f.screen {
	case flowRunning:
		return "please wait..."
	case flowDrift:
		return "[d]iscard  [w] reopen wizard  [up/down] select  esc back"
	case flowResults:
		return "[up/down] scroll  any other key dismisses"
	default:
		return "[y]es  [n]/esc cancel"
	}
}

// pendingOpDetail renders the detail panel body for the selected op: its
// full summary, the standard effect line, and a staged-hash prefix (so the
// operator can spot-check it against the VM's current config elsewhere).
// Returns "" for an out-of-range index.
func pendingOpDetail(q model.Queue, sel int) string {
	if sel < 0 || sel >= q.Len() {
		return ""
	}
	op := q.Ops[sel]
	hash := op.StagedHash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return fmt.Sprintf("%s\n\nbackup will be written first; takes effect on next VM boot\n\nstaged hash: %s",
		op.Summary, hash)
}

// pendingCrossesGPU reports whether op's Summary carries the pin
// wizard's/mem-node picker's " (crosses GPU node)" suffix -- the marker
// string is the one place that condition is recorded (PendingOp has no
// dedicated field for it), so this is the one place every caller that
// needs to know checks it.
func pendingCrossesGPU(op model.PendingOp) bool {
	return strings.Contains(op.Summary, "(crosses GPU node)")
}

// renderPendingTab renders the Pending tab: a numbered list panel of q's op
// summaries (the row at sel background-highlighted; one that crosses the
// VM's GPU node -- pendingCrossesGPU -- rendered in gpuWarningStyle
// instead, unless it's also sel, in which case the selection highlight
// wins; rows scrolled to keep sel visible within budget), and a detail
// panel for the selected op below it, or beside it when wide. Alongside
// the string it returns one "pending" hit per visible row, bounded to
// the list panel's inner width (so a click in a neighboring detail panel
// can't land on it), 0-based relative to the list's own top-left corner.
func renderPendingTab(q model.Queue, sel, w, budget int) (string, []hit) {
	primaryW, secondaryW, sideBySide := splitBodyWidth(w)

	primaryBudget, secondaryBudget := budget, budget
	if !sideBySide {
		primaryBudget, secondaryBudget = splitStackedBudget(budget, q.Len())
	}

	list := "no pending operations"
	var hits []hit
	if q.Len() > 0 {
		lines := make([]string, len(q.Ops))
		for i, op := range q.Ops {
			line := fmt.Sprintf("%d. %s", i+1, op.Summary)
			switch {
			case i == sel:
				line = selectedRowStyle.Render(line)
			case pendingCrossesGPU(op):
				line = gpuWarningStyle.Render(line)
			}
			lines[i] = line
		}
		visible, offset, _ := scrollWindow(lines, primaryBudget-2, sel)
		list = strings.Join(visible, "\n")
		for i := range visible {
			hits = append(hits, hit{y0: i, y1: i + 1, x0: 0, x1: primaryW - 2, kind: "pending", index: offset + i})
		}
	}
	listPanel, _ := panelH("Pending", list, primaryW, primaryBudget, true)
	hits = offsetHits(hits, 1, 1)

	var detailPanel string
	if secondaryBudget > 0 {
		detailPanel, _ = panelWrapH("detail", pendingOpDetail(q, sel), secondaryW, secondaryBudget, true)
	}

	if sideBySide {
		if detailPanel == "" {
			return listPanel, hits
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, listPanel, " ", detailPanel), hits
	}
	if detailPanel == "" {
		return listPanel, hits
	}
	return listPanel + "\n" + detailPanel, hits
}

// movePendingSel shifts the Pending tab's selection by delta rows, clamped
// to [0, queue.Len()-1].
func (a *App) movePendingSel(delta int) {
	if a.queue.Len() == 0 {
		return
	}
	a.pendingSel += delta
	a.clampPendingSel()
}

// clampPendingSel clamps a.pendingSel into [0, queue.Len()-1] (0 when the
// queue is empty).
func (a *App) clampPendingSel() {
	if a.queue.Len() == 0 {
		a.pendingSel = 0
		return
	}
	if a.pendingSel < 0 {
		a.pendingSel = 0
	}
	if max := a.queue.Len() - 1; a.pendingSel > max {
		a.pendingSel = max
	}
}

// removeSelectedPendingOp implements 'x' on the Pending tab: after the
// shared edit-mode gate, drops the selected op.
func (a *App) removeSelectedPendingOp() (tea.Model, tea.Cmd) {
	if !a.editMode {
		a.status = "press e to enter edit mode first"
		return a, nil
	}
	if a.pendingSel < 0 || a.pendingSel >= a.queue.Len() {
		return a, nil
	}
	a.queue.Remove(a.pendingSel)
	a.clampPendingSel()
	a.status = ""
	return a, nil
}

// discardAllPending implements 'd' on the Pending tab: confirms, then
// clears the whole queue.
func (a *App) discardAllPending() (tea.Model, tea.Cmd) {
	if !a.editMode {
		a.status = "press e to enter edit mode first"
		return a, nil
	}
	n := a.queue.Len()
	if n == 0 {
		return a, nil
	}
	a.confirm = &confirm{
		prompt: fmt.Sprintf("Discard all %s? [y/n]", pluralize(n, "pending op")),
		yes: func() tea.Cmd {
			a.queue.Clear()
			a.pendingSel = 0
			a.status = ""
			return nil
		},
	}
	return a, nil
}
