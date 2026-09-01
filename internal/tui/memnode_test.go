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
	a.tab = 2

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
	a.tab = 2

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatalf("status = %q, mem-node picker did not open", a.status)
	}
	if !strings.Contains(a.memPicker.view(), "node 1") {
		t.Fatalf("picker view = %q, want it to list node 1", a.memPicker.view())
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

// TestMemNodeKeepsExistingPins covers an already-pinned VM: Pins in the
// staged op must equal its current pins verbatim (unchanged), only
// MemNode moves.
func TestMemNodeKeepsExistingPins(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"pinned-vm": pinnedNode0XML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

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
	a.tab = 2

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

// TestMemNodeWarnsOnGPUMismatch covers the non-blocking GPU-locality
// warning: vm2XML's hostdev resolves to node 1 (vm2PCINode), so picking
// node 0 must stage successfully but surface a warning.
func TestMemNodeWarnsOnGPUMismatch(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'n')
	if a.memPicker == nil {
		t.Fatal("mem-node picker did not open")
	}
	sendKey(a, '0')

	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 (warning must not block staging)", a.queue.Len())
	}
	if a.queue.Ops[0].MemNode != 0 {
		t.Fatalf("op.MemNode = %d, want 0", a.queue.Ops[0].MemNode)
	}
	if !strings.Contains(a.status, "GPU is on node 1") {
		t.Fatalf("status = %q, want the GPU-locality warning", a.status)
	}
}
