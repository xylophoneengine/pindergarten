package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// pendingFakeApp builds an App around a Fake hypervisor with one plain-vm
// domain (seeded from xml), returning the Fake too so drift tests can
// mutate its XML behind the App's back.
func pendingFakeApp(t *testing.T, xml string) (*App, *libvirtio.Fake) {
	t.Helper()
	f := &libvirtio.Fake{ConnURI: "test:///x", XML: map[string]string{"plain-vm": xml}}
	scan := func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error) {
		doms, err := f.ListDomains()
		if err != nil {
			return nil, nil, err
		}
		domsMap := make(map[string]*libvirtio.DomainConfig, len(doms))
		for _, d := range doms {
			domsMap[d.Config.Name] = d.Config
		}
		return model.Build(testTopo(), doms, noNode), domsMap, nil
	}
	return New(f, scan, t.TempDir(), "test"), f
}

// changedVCPUCountXML differs from plainVMXML by vcpu count (4 instead of
// 2): used to prove a post-rescan wizard is built from the fresh snapshot,
// not a stale one, since it forces a different-sized proposal.
const changedVCPUCountXML = `<domain type='kvm'>
  <name>plain-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b01</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>4</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// stagePlainVMPin adds a valid OpPin for plain-vm straight to a's queue,
// with StagedHash matching plainVMXML so drift checks pass until a test
// deliberately mutates the fake's XML.
func stagePlainVMPin(a *App) {
	a.queue.Add(model.PendingOp{
		Kind:       model.OpPin,
		VM:         "plain-vm",
		Pins:       map[int][]int{0: {0}, 1: {1}},
		MemNode:    0,
		StagedHash: model.HashXML(plainVMXML),
		StagedXML:  plainVMXML,
		Summary:    "plain-vm: pin 2 vcpus -> node 0 threads 0,1; memory -> node 0",
	})
}

// drain runs cmd and every Cmd chained from its resulting Update call, so a
// test can fire one key and let a whole cmd->msg->Update->cmd chain settle
// synchronously.
func drain(a *App, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		_, cmd = a.Update(msg)
	}
}

func TestApplyHappyPath(t *testing.T) {
	a, f := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)

	sendKey(a, 'a')
	if a.flow == nil {
		t.Fatalf("status = %q, apply review flow did not open", a.status)
	}
	if view := a.View(); !strings.Contains(view, "backup will be written first; takes effect on next VM boot") {
		t.Fatalf("View() = %q, want the review screen's per-op effect line", view)
	}
	if view := a.View(); !strings.Contains(view, "1 pending ops") {
		t.Fatalf("View() = %q, want the status bar's pending-op count while the review screen is open", view)
	}

	cmd := sendKey(a, 'y')
	if cmd == nil {
		t.Fatal("'y' on the apply review screen returned a nil Cmd, want the drift-check Cmd")
	}
	drain(a, cmd)

	if len(f.Defined) != 1 {
		t.Fatalf("len(f.Defined) = %d, want 1", len(f.Defined))
	}
	entries, err := backup.List(a.backupDir)
	if err != nil {
		t.Fatalf("backup.List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 backup file under %s", len(entries), a.backupDir)
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 (applied op cleared)", a.queue.Len())
	}
	if !strings.Contains(a.View(), "OK") {
		t.Fatalf("View() = %q, want the results screen to contain OK", a.View())
	}
}

func TestApplyDriftBlocks(t *testing.T) {
	a, f := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)

	f.XML["plain-vm"] = plainVMXML + "\n<!-- edited behind the app's back -->"

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	drain(a, cmd)

	if a.flow == nil || a.flow.screen != flowDrift {
		t.Fatalf("status = %q, want the drift screen open", a.status)
	}
	if !strings.Contains(a.View(), "plain-vm") {
		t.Fatalf("View() = %q, want the drift screen to name plain-vm", a.View())
	}
	if len(f.Defined) != 0 {
		t.Fatalf("len(f.Defined) = %d, want 0 (no writes before drift is resolved)", len(f.Defined))
	}

	sendKey(a, 'd')
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 after discarding the only drifted op", a.queue.Len())
	}
	if strings.Contains(a.View(), "OK") || strings.Contains(a.View(), "FAILED") {
		t.Fatalf("View() = %q, want no results screen after discarding", a.View())
	}
}

func TestApplyDriftReopenWizard(t *testing.T) {
	a, f := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)

	// Not just drifted, but a real change (2 vcpus -> 4) that the
	// post-rescan wizard must reflect -- pinning the scanDoneMsg-before-
	// openWizardFor ordering in app.go (the wizard must be built from the
	// freshly-rescanned snapshot, not the stale one from before 'w').
	f.XML["plain-vm"] = changedVCPUCountXML

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	drain(a, cmd)
	if a.flow == nil || a.flow.screen != flowDrift {
		t.Fatalf("status = %q, want the drift screen open", a.status)
	}

	cmd = sendKey(a, 'w')
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 (op removed to re-open the wizard)", a.queue.Len())
	}
	if cmd == nil {
		t.Fatal("'w' on the drift screen returned a nil Cmd, want a rescan Cmd")
	}
	if a.wizard != nil {
		t.Fatal("wizard opened before the rescan message was processed")
	}

	_, cmd2 := a.Update(cmd())
	if cmd2 != nil {
		t.Fatalf("Update(scanDoneMsg) returned unexpected Cmd: %v", cmd2)
	}

	if a.wizard == nil {
		t.Fatal("wizard did not open for plain-vm after the rescan message was processed")
	}
	if a.wizard.vm != "plain-vm" {
		t.Fatalf("wizard.vm = %q, want plain-vm", a.wizard.vm)
	}
	if got := len(a.wizard.proposal.Pins); got != 4 {
		t.Fatalf("len(proposal.Pins) = %d, want 4 (wizard must be built from the freshly-rescanned "+
			"4-vcpu XML, not a stale 2-vcpu snapshot)", got)
	}
}

// TestApplyCancelDuringDriftCheckIsIgnored covers the fixed race: pressing
// 'y' must move the flow to flowRunning synchronously, so a following
// esc/n while the drift-check Cmd is still in flight is ignored rather
// than setting a.flow to nil out from under the eventual driftCheckedMsg
// (which used to panic on the nil dereference).
func TestApplyCancelDuringDriftCheckIsIgnored(t *testing.T) {
	a, f := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	f.XML["plain-vm"] = plainVMXML + "\n<!-- edited behind the app's back -->"

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	if cmd == nil {
		t.Fatal("'y' did not return the drift-check Cmd")
	}
	if a.flow == nil || a.flow.screen != flowRunning {
		t.Fatal("flow did not move to flowRunning immediately after 'y'")
	}

	sendKeyType(a, tea.KeyEsc)
	if a.flow == nil || a.flow.screen != flowRunning {
		t.Fatal("esc during the in-flight drift check must not close the flow")
	}

	// Deliver the drift result: must not panic. The fake's XML drifted, so
	// it must land on the drift screen rather than silently applying.
	drain(a, cmd)

	if a.flow == nil || a.flow.screen != flowDrift {
		t.Fatalf("status = %q, want the drift screen open once the check resolves", a.status)
	}
	if len(f.Defined) != 0 {
		t.Fatalf("len(f.Defined) = %d, want 0 (drift must still block, esc must not have let anything through)", len(f.Defined))
	}
}

// TestDriftScreenEscKeepsQueued covers esc on the drift screen: it closes
// the flow back to browsing without discarding anything and without
// writing.
func TestDriftScreenEscKeepsQueued(t *testing.T) {
	a, f := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	f.XML["plain-vm"] = plainVMXML + "\n<!-- edited behind the app's back -->"

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	drain(a, cmd)
	if a.flow == nil || a.flow.screen != flowDrift {
		t.Fatalf("status = %q, want the drift screen open", a.status)
	}

	sendKeyType(a, tea.KeyEsc)
	if a.flow != nil {
		t.Fatal("flow still open after esc on the drift screen")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (esc must leave the drifted op queued, untouched)", a.queue.Len())
	}
	if len(f.Defined) != 0 {
		t.Fatalf("len(f.Defined) = %d, want 0 (no writes happened)", len(f.Defined))
	}
}

// TestDriftScreenShowsDiff covers the drift screen's per-op diff: it must
// show what actually changed (StagedXML vs. the current live XML), not
// just name the drifted VM.
func TestDriftScreenShowsDiff(t *testing.T) {
	a, f := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	f.XML["plain-vm"] = plainVMXML + "\n<!-- edited behind the app's back -->"

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	drain(a, cmd)
	if a.flow == nil || a.flow.screen != flowDrift {
		t.Fatalf("status = %q, want the drift screen open", a.status)
	}

	view := a.flow.view(a.width, a.height)
	if !strings.Contains(view, "+ <!-- edited behind the app's back -->") {
		t.Fatalf("view = %q, want a diff line with the changed content", view)
	}
}

// TestCapDiff is capDiff's self-check: under budget, unchanged; over
// budget, truncated with a trailing count of the omitted lines.
func TestCapDiff(t *testing.T) {
	if got := capDiff("a\nb", 3); got != "a\nb" {
		t.Fatalf("capDiff under budget = %q, want unchanged", got)
	}
	want := "a\nb\nc\n... 2 more lines"
	if got := capDiff("a\nb\nc\nd\ne", 3); got != want {
		t.Fatalf("capDiff over budget = %q, want %q", got, want)
	}
}

// pendingFakeAppMulti is pendingFakeApp generalized to more than one VM,
// for tests that need to drift/stage several domains at once.
func pendingFakeAppMulti(t *testing.T, xmls map[string]string) (*App, *libvirtio.Fake) {
	t.Helper()
	f := &libvirtio.Fake{ConnURI: "test:///x", XML: xmls}
	scan := func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error) {
		doms, err := f.ListDomains()
		if err != nil {
			return nil, nil, err
		}
		domsMap := make(map[string]*libvirtio.DomainConfig, len(doms))
		for _, d := range doms {
			domsMap[d.Config.Name] = d.Config
		}
		return model.Build(testTopo(), doms, noNode), domsMap, nil
	}
	return New(f, scan, t.TempDir(), "test"), f
}

// TestDriftScreenReopenClosesFlowRegardlessOfRemaining covers 'w' with more
// than one drifted op: it must close the whole flow immediately, even
// though the other op is still drifted and unresolved (nothing
// auto-applies -- the user presses 'a' again to re-review).
func TestDriftScreenReopenClosesFlowRegardlessOfRemaining(t *testing.T) {
	a, f := pendingFakeAppMulti(t, map[string]string{"plain-vm": plainVMXML, "vm1": vm1XML})
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	a.queue.Add(model.PendingOp{
		Kind: model.OpPin, VM: "vm1", Pins: map[int][]int{0: {2}, 1: {3}}, MemNode: 1,
		StagedHash: model.HashXML(vm1XML), StagedXML: vm1XML, Summary: "vm1: pin",
	})

	f.XML["plain-vm"] = plainVMXML + "\n<!-- edited -->"
	f.XML["vm1"] = vm1XML + "\n<!-- edited -->"

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	drain(a, cmd)
	if a.flow == nil || a.flow.screen != flowDrift {
		t.Fatalf("status = %q, want the drift screen open with both ops drifted", a.status)
	}
	if len(a.flow.drifted) != 2 {
		t.Fatalf("len(flow.drifted) = %d, want 2", len(a.flow.drifted))
	}

	sendKey(a, 'w')
	if a.flow != nil {
		t.Fatal("flow still open after 'w', want it closed regardless of the other still-drifted op")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (only the reopened op removed, the other stays queued)", a.queue.Len())
	}
}

// TestFlowGenMonotonicAcrossFlows covers the fix moving the stale-message
// guard's counter onto App: two separate applyFlow rounds (one cancelled,
// one completed) must never see App.flowGen go backwards or repeat.
func TestFlowGenMonotonicAcrossFlows(t *testing.T) {
	a, _ := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)

	sendKey(a, 'a')
	cmd := sendKey(a, 'y') // starts a drift check round; bumps flowGen
	firstGen := a.flowGen
	if firstGen == 0 {
		t.Fatal("flowGen did not advance on the first round")
	}
	drain(a, cmd)
	if a.flow == nil || a.flow.screen != flowResults {
		t.Fatalf("status = %q, want the first round to reach the results screen (clean drift check)", a.status)
	}

	dismissCmd := sendKey(a, ' ') // any key dismisses the results screen -> rescan
	if a.flow != nil {
		t.Fatal("flow still open after dismissing the results screen")
	}
	drain(a, dismissCmd)

	stagePlainVMPin(a)
	sendKey(a, 'a')
	sendKey(a, 'y')
	if a.flowGen <= firstGen {
		t.Fatalf("flowGen = %d after a second round, want strictly greater than the first round's %d", a.flowGen, firstGen)
	}
}

func TestDiscardAllPending(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	a.tab = 3

	sendKey(a, 'd')
	if a.confirm == nil {
		t.Fatal("'d' on the Pending tab did not open a confirm modal")
	}
	if !strings.Contains(a.View(), "Discard all") {
		t.Fatalf("View() = %q, want the discard-all confirm prompt", a.View())
	}

	sendKey(a, 'n')
	if a.confirm != nil {
		t.Fatal("confirm still set after 'n'")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (declining must not discard)", a.queue.Len())
	}

	sendKey(a, 'd')
	sendKey(a, 'y')
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 after confirming discard-all", a.queue.Len())
	}
}

func TestResultsScreenDismissRescans(t *testing.T) {
	a, _ := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	drain(a, cmd)
	if a.flow == nil || a.flow.screen != flowResults {
		t.Fatalf("status = %q, want the results screen open", a.status)
	}

	dismissCmd := sendKey(a, ' ') // any key dismisses
	if a.flow != nil {
		t.Fatal("flow still open after dismissing the results screen")
	}
	if dismissCmd == nil {
		t.Fatal("dismissing the results screen did not return a Cmd, want a rescan Cmd")
	}
	if _, ok := dismissCmd().(scanDoneMsg); !ok {
		t.Fatal("dismiss Cmd did not produce a scanDoneMsg")
	}
}

// TestMouseClickSelectsPendingRow covers a left click on a Pending row
// selecting it (pendingSel).
func TestMouseClickSelectsPendingRow(t *testing.T) {
	a, _ := pendingFakeAppMulti(t, map[string]string{"plain-vm": plainVMXML, "vm1": vm1XML})
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	a.queue.Add(model.PendingOp{
		Kind: model.OpPin, VM: "vm1", Pins: map[int][]int{0: {2}, 1: {3}}, MemNode: 1,
		StagedHash: model.HashXML(vm1XML), StagedXML: vm1XML, Summary: "vm1: pin",
	})
	a.tab = 3
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "pending", 1)
	a.Update(press(h.x0, h.y0))
	if a.pendingSel != 1 {
		t.Fatalf("pendingSel = %d, want 1", a.pendingSel)
	}
}

func TestPendingTabEmptyState(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.tab = 3
	if !strings.Contains(a.View(), "no pending operations") {
		t.Fatalf("View() = %q, want the empty-state text", a.View())
	}
}

func TestRemovePendingOp(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	a.tab = 3

	sendKey(a, 'x')
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 after 'x' in edit mode", a.queue.Len())
	}

	b := testApp(t, false)
	runScan(t, b)
	stagePlainVMPin(b) // added directly, bypassing the staging gate, to test 'x's own refusal
	b.tab = 3

	sendKey(b, 'x')
	if b.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (read-only 'x' must not remove)", b.queue.Len())
	}
	if !strings.Contains(b.status, "press e to enter edit mode first") {
		t.Fatalf("status = %q, want the edit-mode hint", b.status)
	}
}

func TestApplyRefusedReadOnly(t *testing.T) {
	a := testApp(t, true)
	runScan(t, a)
	stagePlainVMPin(a)

	sendKey(a, 'a')
	if a.flow != nil {
		t.Fatal("apply flow opened in read-only mode, want refused")
	}
	if !strings.Contains(a.status, "press e to enter edit mode first") {
		t.Fatalf("status = %q, want the edit-mode hint", a.status)
	}
}

func TestApplyKeepsFailedOps(t *testing.T) {
	a, f := pendingFakeApp(t, plainVMXML)
	runScan(t, a)
	enterEdit(a)
	stagePlainVMPin(a)
	f.DefineErr = errors.New("boom")

	sendKey(a, 'a')
	cmd := sendKey(a, 'y')
	drain(a, cmd)

	if !strings.Contains(a.View(), "FAILED") {
		t.Fatalf("View() = %q, want the results screen to contain FAILED", a.View())
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (failed op stays queued)", a.queue.Len())
	}
}

func TestBackupsTabRoutes(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	enterEdit(a)

	if _, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0",
		"test", "<domain><name>plain-vm</name><old/></domain>"); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	a.tab = 4

	sendKeyType(a, tea.KeyEnter)
	if a.diffView == "" {
		t.Fatalf("status = %q, diffView empty after enter on the Backups tab", a.status)
	}
	if !strings.Contains(a.diffView, "plain-vm") {
		t.Fatalf("diffView = %q, want it to name plain-vm", a.diffView)
	}

	sendKey(a, 'z') // any key dismisses the diff view
	if a.diffView != "" {
		t.Fatal("diffView not cleared after a dismiss key")
	}

	// Re-open the diff, then dismiss it with a key that would otherwise
	// switch tabs: the dismiss must win (app.go used to check this after
	// the tab-digit case, so '1' switched tabs and left diffView stale for
	// whenever the user returned to tab 4).
	sendKeyType(a, tea.KeyEnter)
	if a.diffView == "" {
		t.Fatal("diffView empty after re-opening it")
	}
	sendKey(a, '1')
	if a.diffView != "" {
		t.Fatal("diffView not cleared after '1' while the diff was open")
	}

	sendKey(a, 'R')
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 after 'R'", a.queue.Len())
	}
	if a.queue.Ops[0].Kind != model.OpRestore {
		t.Fatalf("op.Kind = %v, want OpRestore", a.queue.Ops[0].Kind)
	}
}
