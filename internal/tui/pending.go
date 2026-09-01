package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/apply"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// flowScreen is one of the three screens the apply flow can show.
type flowScreen int

const (
	flowReview flowScreen = iota
	flowDrift
	flowResults
)

// applyFlow is the apply-review/drift/results state machine. App holds one
// (nil when closed); while non-nil, App routes key input to
// handleFlowKey and renders view in place of the tab body, mirroring how
// the wizard replaces the body while open.
type applyFlow struct {
	screen  flowScreen
	ops     []model.PendingOp // snapshot of the queue when the review screen opened
	drifted []model.PendingOp // ops whose VM changed since staging
	sel     int               // selected row in drifted, drift screen only
	results []apply.Result
}

// driftCheckedMsg carries the result of running apply.CheckDrift, mapped
// back onto the queued ops for the drifted VMs (in queue order).
type driftCheckedMsg struct {
	drifted []model.PendingOp
	err     error
}

// applyDoneMsg carries the result of running apply.Run (which itself has no
// error return: every per-op failure is carried in its Result instead).
type applyDoneMsg struct {
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
			return a, a.checkDriftCmd()
		case isRune(msg, 'n'), msg.Type == tea.KeyEsc:
			a.flow = nil
		}
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
		}
	case flowResults:
		// Any key dismisses the results screen, then a rescan is issued.
		a.flow = nil
		a.status = "rescanning..."
		return a, a.scanCmd()
	}
	return a, nil
}

// checkDriftCmd runs apply.CheckDrift against the queue and maps the
// drifted VM names back onto their queued ops, in queue order.
func (a *App) checkDriftCmd() tea.Cmd {
	ops := append([]model.PendingOp(nil), a.queue.Ops...)
	return func() tea.Msg {
		names, err := apply.CheckDrift(a.hv, ops)
		if err != nil {
			return driftCheckedMsg{err: err}
		}
		driftedSet := make(map[string]bool, len(names))
		for _, n := range names {
			driftedSet[n] = true
		}
		var drifted []model.PendingOp
		for _, op := range ops {
			if driftedSet[op.VM] {
				drifted = append(drifted, op)
			}
		}
		return driftCheckedMsg{drifted: drifted}
	}
}

// handleDriftChecked reacts to a driftCheckedMsg: any drift opens the drift
// screen; a clean check proceeds straight into apply.Run.
func (a *App) handleDriftChecked(msg driftCheckedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.status = msg.err.Error()
		a.flow = nil
		return a, nil
	}
	if len(msg.drifted) > 0 {
		a.flow.screen = flowDrift
		a.flow.drifted = msg.drifted
		a.flow.sel = 0
		return a, nil
	}
	return a, a.runApplyCmd()
}

// runApplyCmd runs apply.Run against a snapshot of the current queue.
func (a *App) runApplyCmd() tea.Cmd {
	ops := append([]model.PendingOp(nil), a.queue.Ops...)
	return func() tea.Msg {
		return applyDoneMsg{results: apply.Run(a.hv, a.backupDir, a.version, ops)}
	}
}

// handleApplyDone reacts to an applyDoneMsg: shows the results screen and
// clears only the ops that actually applied, leaving failed/skipped ones
// queued so the user can inspect or discard them.
func (a *App) handleApplyDone(msg applyDoneMsg) (tea.Model, tea.Cmd) {
	a.flow.screen = flowResults
	a.flow.results = msg.results

	var kept []model.PendingOp
	for _, r := range msg.results {
		if !r.Applied {
			kept = append(kept, r.Op)
		}
	}
	a.queue.Ops = kept
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
// drifted op from the queue and the drift list, then issues a rescan;
// app.go's scanDoneMsg handling re-opens the wizard for that VM once the
// rescan lands (see openWizardFor).
func (a *App) reopenDrifted() (tea.Model, tea.Cmd) {
	if a.flow == nil || len(a.flow.drifted) == 0 {
		return a, nil
	}
	op := a.flow.drifted[a.flow.sel]
	a.removeQueueOp(op.VM)
	a.dropFlowDrifted()
	if len(a.flow.drifted) == 0 {
		a.flow = nil
	}
	a.reopenVM = op.VM
	a.status = "rescanning..."
	return a, a.scanCmd()
}

// dropFlowDrifted removes the currently-selected row from a.flow.drifted
// and clamps the selection into the shrunk list.
func (a *App) dropFlowDrifted() {
	d := a.flow.drifted
	a.flow.drifted = append(d[:a.flow.sel], d[a.flow.sel+1:]...)
	if a.flow.sel >= len(a.flow.drifted) {
		a.flow.sel = len(a.flow.drifted) - 1
	}
	if a.flow.sel < 0 {
		a.flow.sel = 0
	}
}

// removeQueueOp drops the queued op for vm, if any.
func (a *App) removeQueueOp(vm string) {
	for i, op := range a.queue.Ops {
		if op.VM == vm {
			a.queue.Remove(i)
			return
		}
	}
}

// openWizardFor opens the pin wizard for vm by first pointing a.vmSel at
// it in the freshly-scanned snapshot, then delegating to openWizard. A no-op
// if vm is no longer present (e.g. removed from libvirt between staging and
// the rescan).
func (a *App) openWizardFor(vm string) {
	if a.snap == nil {
		return
	}
	for i, v := range a.snap.VMs {
		if v.Name == vm {
			a.vmSel = i
			a.openWizard()
			return
		}
	}
}

// view renders whichever screen f.screen selects.
func (f *applyFlow) view(w int) string {
	var b strings.Builder
	switch f.screen {
	case flowReview:
		b.WriteString("Apply pending operations?\n\n")
		for i, op := range f.ops {
			fmt.Fprintf(&b, "%d. %s\n   backup will be written first; takes effect on next VM boot\n", i+1, op.Summary)
		}
		b.WriteString("\n[y] confirm  [n]/esc cancel")
	case flowDrift:
		b.WriteString("Drift detected -- these VMs changed since staging:\n\n")
		for i, op := range f.drifted {
			line := fmt.Sprintf("%d. %s", i+1, op.Summary)
			if i == f.sel {
				line = cursorStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n[d]iscard  [w] reopen wizard  [up/down] select")
	case flowResults:
		for _, r := range f.results {
			switch {
			case r.Applied:
				fmt.Fprintf(&b, "OK %s (backup: %s)\n", r.Op.Summary, r.BackupPath)
			case r.Err != nil:
				fmt.Fprintf(&b, "FAILED %s: %v\n", r.Op.Summary, r.Err)
			case r.Drifted:
				fmt.Fprintf(&b, "SKIPPED %s: drifted\n", r.Op.Summary)
			default:
				fmt.Fprintf(&b, "SKIPPED %s: not attempted\n", r.Op.Summary)
			}
		}
		b.WriteString("\nany key to dismiss")
	}
	return b.String()
}

// statusBarHint returns the status bar's replacement content while the
// apply flow is open: its own keys, since edit/quit/apply/discard/pin/strip
// are inert while a flow screen is capturing all key input.
func (f *applyFlow) statusBarHint() string {
	switch f.screen {
	case flowDrift:
		return "[d]iscard  [w] reopen wizard  [up/down] select"
	case flowResults:
		return "any key to dismiss"
	default:
		return "[y]es  [n]/esc cancel"
	}
}

// renderPending renders the Pending tab: a numbered list of q's op
// Summaries, with the row at sel reverse-video. w is unused for now (no
// wrapping beyond the fixed layout), mirroring renderBackups.
func renderPending(q model.Queue, sel int, w int) string {
	if q.Len() == 0 {
		return "no pending operations"
	}
	var b strings.Builder
	for i, op := range q.Ops {
		line := fmt.Sprintf("%d. %s", i+1, op.Summary)
		if i == sel {
			line = cursorStyle.Render(line)
		}
		b.WriteString(line)
		if i < len(q.Ops)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
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
	n := a.queue.Len()
	if n == 0 {
		return a, nil
	}
	a.confirm = &confirm{
		prompt: fmt.Sprintf("Discard all %d pending ops? [y/n]", n),
		yes: func() tea.Cmd {
			a.queue.Clear()
			a.pendingSel = 0
			a.status = ""
			return nil
		},
	}
	return a, nil
}
