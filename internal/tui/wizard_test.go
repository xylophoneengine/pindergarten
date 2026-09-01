package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// vm1XML and vm2XML are two distinct unpinned domains, used where a test
// needs more than one VM in the same scan.
const vm1XML = `<domain type='kvm'>
  <name>vm1</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b21</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// vm2XML carries a passthrough hostdev at 0000:81:00.0, resolved to node 1
// by the test's pciNode function, so Propose forces it onto node 1
// regardless of memory ranking.
const vm2XML = `<domain type='kvm'>
  <name>vm2</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b22</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci'>
      <source>
        <address domain='0x0000' bus='0x81' slot='0x00' function='0x0'/>
      </source>
    </hostdev>
  </devices>
</domain>`

// brokenXML fails etree parsing (unclosed elements) but still contains a
// <name> a regex fallback can pick up, so the resulting VM is Unsupported
// but named "broken-vm".
const brokenXML = `<domain type='kvm'><name>broken-vm</name><vcpu>1</vcpu>`

// vm2PCINode resolves the hostdev address in vm2XML to node 1, everything
// else to unknown.
func vm2PCINode(addr string) int {
	if addr == "0000:81:00.0" {
		return 1
	}
	return -1
}

func noNode(string) int { return -1 }

// wizardTestApp builds an App around a Fake with the given domains, using
// pciNode to resolve hostdev addresses to NUMA nodes.
func wizardTestApp(t *testing.T, xmls map[string]string, pciNode func(string) int) *App {
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
		return model.Build(testTopo(), doms, pciNode), domsMap, nil
	}
	return New(f, scan, t.TempDir(), "test")
}

// enterEdit drives the App into edit mode via the same 'e'/'y' sequence a
// real session would use.
func enterEdit(a *App) {
	sendKey(a, 'e')
	sendKey(a, 'y')
}

// sendKeyType sends a non-rune key (enter, esc, space, arrows).
func sendKeyType(a *App, kt tea.KeyType) tea.Cmd {
	_, cmd := a.Update(tea.KeyMsg{Type: kt})
	return cmd
}

// vmIndex returns the index of name within a.snap.VMs (sorted by Build).
func vmIndex(t *testing.T, a *App, name string) int {
	t.Helper()
	for i, v := range a.snap.VMs {
		if v.Name == name {
			return i
		}
	}
	t.Fatalf("vm %q not found in snapshot", name)
	return -1
}

func TestVMsTabShowsFlags(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	a.tab = 2

	view := a.View()
	if !strings.Contains(view, "[!]") {
		t.Fatalf("View() = %q, want a [!] flag badge for the unpinned VM", view)
	}
	if !strings.Contains(view, "This VM's vCPUs are not pinned to any host thread.") {
		t.Fatalf("View() = %q, want the FlagUnpinned Cause sentence in the detail panel", view)
	}
}

func TestStripStagesOp(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"pinned-vm": pinnedNode0XML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2
	a.vmSel = vmIndex(t, a, "pinned-vm")

	sendKey(a, 's')

	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}
	op := a.queue.Ops[0]
	if op.Kind != model.OpStrip {
		t.Fatalf("op.Kind = %v, want OpStrip", op.Kind)
	}
	if op.VM != "pinned-vm" {
		t.Fatalf("op.VM = %q, want pinned-vm", op.VM)
	}
	want := model.HashXML(pinnedNode0XML)
	if op.StagedHash != want {
		t.Fatalf("op.StagedHash = %q, want %q", op.StagedHash, want)
	}
	if !strings.Contains(a.status, "staged:") {
		t.Fatalf("status = %q, want it to report the staged summary", a.status)
	}
}

func TestStripRefusedReadOnly(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	a.tab = 2

	sendKey(a, 's')

	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 (read-only)", a.queue.Len())
	}
	if !strings.Contains(a.status, "press e to enter edit mode first") {
		t.Fatalf("status = %q, want the edit-mode hint", a.status)
	}
}

func TestUnsupportedVMRefusesPinAndStrip(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"broken-vm": brokenXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 's')
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after 's' on unsupported VM, want 0", a.queue.Len())
	}
	if !strings.Contains(a.status, "unsupported config, view only") {
		t.Fatalf("status = %q after 's', want unsupported-config reason", a.status)
	}

	sendKey(a, 'p')
	if a.wizard != nil {
		t.Fatal("wizard opened for an unsupported VM, want refused")
	}
	if !strings.Contains(a.status, "unsupported config, view only") {
		t.Fatalf("status = %q after 'p', want unsupported-config reason", a.status)
	}
}

func TestWizardAcceptStagesPin(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	wantPins := a.wizard.proposal.Pins
	wantNode := a.wizard.proposal.Node

	sendKeyType(a, tea.KeyEnter)

	if a.wizard != nil {
		t.Fatal("wizard still open after accepting the proposal")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}
	op := a.queue.Ops[0]
	if op.Kind != model.OpPin {
		t.Fatalf("op.Kind = %v, want OpPin", op.Kind)
	}
	if op.MemNode != wantNode {
		t.Fatalf("op.MemNode = %d, want %d", op.MemNode, wantNode)
	}
	if len(op.Pins) != len(wantPins) {
		t.Fatalf("op.Pins = %v, want %v", op.Pins, wantPins)
	}
	for vcpu, threads := range wantPins {
		if got := op.Pins[vcpu]; len(got) != 1 || got[0] != threads[0] {
			t.Fatalf("op.Pins[%d] = %v, want %v", vcpu, got, threads)
		}
	}
	if !strings.Contains(a.status, "staged:") {
		t.Fatalf("status = %q, want the staged summary", a.status)
	}
}

func TestWizardEscCancels(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatal("wizard did not open")
	}

	sendKeyType(a, tea.KeyEsc)

	if a.wizard != nil {
		t.Fatal("wizard still open after esc")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after esc, want 0", a.queue.Len())
	}
	if !strings.Contains(a.status, "cancelled") {
		t.Fatalf("status = %q, want cancelled", a.status)
	}
}

// TestWizardManualCount drives the manual-adjust screen: an initial toggle
// that empties the selection must be refused with a running-count warning;
// selecting the node's other core (exactly 2 threads, matching plain-vm's
// VCPUs) must then stage successfully.
func TestWizardManualCount(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatal("wizard did not open")
	}
	if a.wizard.proposal.Node != 0 {
		t.Fatalf("proposal.Node = %d, want 0 (no other VMs, lower ID wins the tie)", a.wizard.proposal.Node)
	}

	sendKey(a, 'm')
	if a.wizard.screen != manualScreen {
		t.Fatal("'m' did not switch to the manual screen")
	}

	// Cursor starts at core 0 (node 0's first core), which is exactly where
	// the proposal's 2 threads (0 and 4, both siblings on that core) live.
	// Toggling it empties the selection.
	sendKeyType(a, tea.KeySpace)
	sendKeyType(a, tea.KeyEnter)

	if a.wizard == nil {
		t.Fatal("wizard closed after an incomplete manual selection, want it to stay open")
	}
	if !strings.Contains(a.wizard.status, "select exactly 2 threads (0 selected)") {
		t.Fatalf("wizard.status = %q, want the running-count warning", a.wizard.status)
	}

	sendKeyType(a, tea.KeyRight) // move to core 1 (threads 1,5)
	sendKeyType(a, tea.KeySpace)
	sendKeyType(a, tea.KeyEnter)

	if a.wizard != nil {
		t.Fatal("wizard still open after a correctly-sized manual selection")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}
	op := a.queue.Ops[0]
	if op.Kind != model.OpPin {
		t.Fatalf("op.Kind = %v, want OpPin", op.Kind)
	}
	if got := op.Pins[0]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("op.Pins[0] = %v, want [1]", got)
	}
	if got := op.Pins[1]; len(got) != 1 || got[0] != 5 {
		t.Fatalf("op.Pins[1] = %v, want [5]", got)
	}
	if op.MemNode != 0 {
		t.Fatalf("op.MemNode = %d, want 0", op.MemNode)
	}
}

// TestSecondWizardSeesPending stages a pin for vm1 taking threads 2 and 6
// directly (bypassing the wizard, as the brief allows), then opens the
// wizard for vm2 -- forced onto node 1 by its hostdev -- and checks the
// proposal avoids the threads vm1 already claims.
func TestSecondWizardSeesPending(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"vm1": vm1XML, "vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)

	a.queue.Add(model.PendingOp{
		Kind:    model.OpPin,
		VM:      "vm1",
		Pins:    map[int][]int{0: {2}, 1: {6}},
		MemNode: 1,
		Summary: "vm1: pin",
	})

	a.tab = 2
	a.vmSel = vmIndex(t, a, "vm2")
	sendKey(a, 'p')

	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open for vm2", a.status)
	}
	if a.wizard.proposal.Node != 1 {
		t.Fatalf("proposal.Node = %d, want 1 (forced by vm2's hostdev)", a.wizard.proposal.Node)
	}
	for vcpu, threads := range a.wizard.proposal.Pins {
		for _, th := range threads {
			if th == 2 || th == 6 {
				t.Fatalf("proposal.Pins[%d] = %v, want it to avoid threads 2 and 6 (claimed by vm1's pending op)", vcpu, threads)
			}
		}
	}
}

// TestRepinIncludesOwnPins re-opens the wizard for an already-pinned VM and
// checks the proposal treats node 0 (where it's already pinned) as free --
// proving the wizard projects away the VM's own current pins before
// proposing. Without that projection, node 0 would show 0 fully-free cores
// (both of its cores have one thread already claimed by pinned-vm itself)
// and Propose would prefer node 1's 2 fully-free cores instead.
func TestRepinIncludesOwnPins(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"pinned-vm": pinnedNode0XML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	if a.wizard.proposal.Node != 0 {
		t.Fatalf("proposal.Node = %d, want 0 (self-exclusion frees up its own current node)", a.wizard.proposal.Node)
	}
}
