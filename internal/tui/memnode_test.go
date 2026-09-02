package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/model"
)

func TestMemNodeRefusedReadOnly(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	a.tab = tabVMs

	sendKey(a, 'n')

	if a.memPicker != nil {
		t.Fatal("mem-node picker opened in read-only mode, want refused")
	}
	if !strings.Contains(a.status, "press e to enter edit mode first") {
		t.Fatalf("status = %q, want the edit-mode hint", a.status)
	}
}

// TestMemNodeStagesOp covers an unpinned VM: Pins must stay empty (not
// fabricate a pin), MemNode must be the picked node, and the Summary must
// match the fixed convention.
func TestMemNodeStagesOp(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatalf("status = %q, mem-node picker did not open", a.status)
	}
	view, _ := a.memPicker.view(80, 24)
	if !strings.Contains(view, "node 1") {
		t.Fatalf("picker view = %q, want it to list node 1", view)
	}

	sendKey(a, '1')
	if a.memPicker != nil {
		t.Fatal("mem-node picker still open after picking a node")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}

	op := a.queue.Ops[0]
	if op.Kind != model.OpPin {
		t.Fatalf("op.Kind = %v, want OpPin", op.Kind)
	}
	if len(op.Pins) != 0 {
		t.Fatalf("op.Pins = %v, want empty (plain-vm has no existing pins)", op.Pins)
	}
	if op.MemNode != 1 {
		t.Fatalf("op.MemNode = %d, want 1", op.MemNode)
	}
	if op.Summary != "plain-vm: memory -> node 1 (strict); vcpu pinning unchanged" {
		t.Fatalf("op.Summary = %q, want the fixed memory-node summary", op.Summary)
	}
	if op.StagedHash != model.HashXML(plainVMXML) {
		t.Fatalf("op.StagedHash = %q, want hash of the fake's current xml", op.StagedHash)
	}
}

// TestMemNodeUsesProjectedPins covers the fix for pressing 'n' right after
// 'p' on the same VM: the picker must see the just-staged pins (via the
// projected snapshot), not the stale on-disk ones, so a memory-only change
// afterward genuinely leaves vcpu pinning unchanged instead of silently
// replacing the staged pin op with an unpinned one.
func TestMemNodeUsesProjectedPins(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	sendKeyType(a, tea.KeyEnter) // accept the proposal
	if a.wizard != nil {
		t.Fatal("wizard still open after accepting the proposal")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 after staging the pin", a.queue.Len())
	}
	stagedOp := a.queue.Ops[0]
	if len(stagedOp.Pins) == 0 {
		t.Fatal("test setup: staged pin op has no Pins")
	}

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatalf("status = %q, mem-node picker did not open", a.status)
	}

	otherNode := 0
	if stagedOp.MemNode == 0 {
		otherNode = 1
	}
	sendKey(a, rune('0'+otherNode))

	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (the mem-node op replaces the same VM's staged op)", a.queue.Len())
	}
	op := a.queue.Ops[0]
	if len(op.Pins) != len(stagedOp.Pins) {
		t.Fatalf("op.Pins = %v, want the previously staged pins %v", op.Pins, stagedOp.Pins)
	}
	for vcpu, threads := range stagedOp.Pins {
		got := op.Pins[vcpu]
		if len(got) != len(threads) || got[0] != threads[0] {
			t.Fatalf("op.Pins[%d] = %v, want %v", vcpu, got, threads)
		}
	}
	if op.MemNode != otherNode {
		t.Fatalf("op.MemNode = %d, want %d", op.MemNode, otherNode)
	}
	if !strings.Contains(op.Summary, "vcpu pinning unchanged") {
		t.Fatalf("op.Summary = %q, want it to say vcpu pinning unchanged", op.Summary)
	}
}

// TestMouseClickPicksMemNode covers a left click on a node-picker line
// staging the memory-node op, same as pressing its digit.
func TestMouseClickPicksMemNode(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatalf("status = %q, mem-node picker did not open", a.status)
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "memnode", 1)
	a.Update(press(h.x0, h.y0))

	if a.memPicker != nil {
		t.Fatal("mem-node picker still open after clicking a node line")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}
	if a.queue.Ops[0].MemNode != 1 {
		t.Fatalf("op.MemNode = %d, want 1", a.queue.Ops[0].MemNode)
	}
}

// TestMemNodeInvalidDigitStagesNothing covers hasNode's guard: a digit
// that names no topology node must leave the picker open and stage
// nothing.
func TestMemNodeInvalidDigitStagesNothing(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatal("mem-node picker did not open")
	}

	sendKey(a, '5') // testTopo only has nodes 0 and 1
	if a.memPicker == nil {
		t.Fatal("mem-node picker closed after an invalid digit, want it to stay open")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 (an invalid digit must not stage anything)", a.queue.Len())
	}
}

// TestMemNodeKeepsExistingPins covers an already-pinned VM: Pins in the
// staged op must equal its current pins verbatim (unchanged), only
// MemNode moves.
func TestMemNodeKeepsExistingPins(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"pinned-vm": pinnedNode0XML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	wantPins := a.snap.VM("pinned-vm").Pins

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatal("mem-node picker did not open")
	}
	sendKey(a, '1')

	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}
	op := a.queue.Ops[0]
	if len(op.Pins) != len(wantPins) {
		t.Fatalf("op.Pins = %v, want unchanged %v", op.Pins, wantPins)
	}
	for vcpu, threads := range wantPins {
		if got := op.Pins[vcpu]; len(got) != len(threads) || got[0] != threads[0] {
			t.Fatalf("op.Pins[%d] = %v, want %v", vcpu, got, threads)
		}
	}
	if op.MemNode != 1 {
		t.Fatalf("op.MemNode = %d, want 1", op.MemNode)
	}
}

// TestMemNodeEscCancels covers esc: the picker closes without staging
// anything.
func TestMemNodeEscCancels(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatal("mem-node picker did not open")
	}
	sendKeyType(a, tea.KeyEsc)

	if a.memPicker != nil {
		t.Fatal("mem-node picker still open after esc")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 after cancelling", a.queue.Len())
	}
}

// openMemPickerCrossingGPU opens the mem-node picker for vm2 (whose
// hostdev resolves to node 1 via vm2PCINode) -- shared setup for the
// TestMemNodeGPUCross* confirm tests below, mirroring
// openWizardCrossingGPU.
func openMemPickerCrossingGPU(t *testing.T) *App {
	t.Helper()
	a := wizardTestApp(t, map[string]string{"vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatal("mem-node picker did not open")
	}
	return a
}

// TestMemNodeGPUCrossOpensConfirm covers pickMemNode's confirm path (the
// fix for the old pendingConfirm "pick again" softening): picking node 0
// while vm2's GPU sits on node 1 opens App's shared y/n confirm instead
// of staging outright -- nothing is queued yet, and the picker stays open
// underneath, untouched.
func TestMemNodeGPUCrossOpensConfirm(t *testing.T) {
	a := openMemPickerCrossingGPU(t)

	sendKey(a, '0')

	if a.confirm == nil {
		t.Fatal("no confirm opened when picking a node crossing the GPU node")
	}
	if a.confirm.prompt != "Bind memory across the GPU's node anyway? [y/n]" {
		t.Fatalf("confirm.prompt = %q, want the GPU-cross confirm text", a.confirm.prompt)
	}
	if a.memPicker == nil {
		t.Fatal("picker closed while the confirm is open, want it to stay open underneath")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d before confirming, want 0 (not staged yet)", a.queue.Len())
	}
}

// TestMemNodeGPUCrossYStages covers the confirm's "y": it stages exactly
// the picked node (0, not the GPU's node 1) with the Summary's "crosses
// GPU node" suffix, closing both the confirm and the picker.
func TestMemNodeGPUCrossYStages(t *testing.T) {
	a := openMemPickerCrossingGPU(t)
	sendKey(a, '0')
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	sendKey(a, 'y')

	if a.confirm != nil {
		t.Fatal("confirm still open after y")
	}
	if a.memPicker != nil {
		t.Fatal("picker still open after y")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (warning must not block staging)", a.queue.Len())
	}
	op := a.queue.Ops[0]
	if op.MemNode != 0 {
		t.Fatalf("op.MemNode = %d, want 0", op.MemNode)
	}
	if !strings.Contains(op.Summary, "crosses GPU node") {
		t.Fatalf("op.Summary = %q, want the crosses-GPU-node suffix", op.Summary)
	}
}

// TestMemNodeGPUCrossNKeepsPickerOpen covers the confirm's "n": it must
// only dismiss the confirm, not the picker, staging nothing.
func TestMemNodeGPUCrossNKeepsPickerOpen(t *testing.T) {
	a := openMemPickerCrossingGPU(t)
	sendKey(a, '0')
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	sendKey(a, 'n')

	if a.confirm != nil {
		t.Fatal("confirm still open after n")
	}
	if a.memPicker == nil {
		t.Fatal("picker closed after declining the confirm with n, want it to stay open")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after n, want 0", a.queue.Len())
	}
}

// TestMemNodeGPUCrossEscKeepsPickerOpen mirrors
// TestMemNodeGPUCrossNKeepsPickerOpen for esc.
func TestMemNodeGPUCrossEscKeepsPickerOpen(t *testing.T) {
	a := openMemPickerCrossingGPU(t)
	sendKey(a, '0')
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	sendKeyType(a, tea.KeyEsc)

	if a.confirm != nil {
		t.Fatal("confirm still open after esc")
	}
	if a.memPicker == nil {
		t.Fatal("picker closed by esc, want only the confirm to have closed")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after esc, want 0", a.queue.Len())
	}
}
