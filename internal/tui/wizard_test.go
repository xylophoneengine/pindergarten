package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
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
	if !strings.Contains(a.View(), "0 pending ops") {
		t.Fatalf("View() = %q, want the pending-op count prefix in the status bar while the wizard is open", a.View())
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

// TestWizardManualCycleNodeWarnsAndStages covers the 'n' node-override key
// on the manual screen: vm2's hostdev forces the proposal onto node 1, so
// cycling away to node 0 must reset the selection, show the crosses-GPU
// warning, and (once threads are picked and accepted) land the staged op
// on node 0 with the warning folded into Summary -- proving the override
// is soft (never blocked), matching the design spec's locality rule.
func TestWizardManualCycleNodeWarnsAndStages(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	if a.wizard.proposal.Node != 1 {
		t.Fatalf("proposal.Node = %d, want 1 (forced by vm2's hostdev)", a.wizard.proposal.Node)
	}

	sendKey(a, 'm')
	if a.wizard.screen != manualScreen {
		t.Fatal("'m' did not switch to the manual screen")
	}
	if a.wizard.node != 1 {
		t.Fatalf("wizard.node = %d, want 1 before cycling", a.wizard.node)
	}

	sendKey(a, 'n')
	if a.wizard.node != 0 {
		t.Fatalf("wizard.node = %d, want 0 after cycling away from node 1", a.wizard.node)
	}
	if len(a.wizard.selected) != 0 {
		t.Fatalf("wizard.selected = %v, want reset to empty after cycling node", a.wizard.selected)
	}

	view, _ := a.wizard.view(200, 40)
	if !strings.Contains(view, "GPU at 0000:81:00.0 is on node 1") {
		t.Fatalf("view() = %q, want the crosses-GPU-node warning", view)
	}

	sendKeyType(a, tea.KeySpace) // core 0 on node 0: threads 0,4 (exactly vcpus()==2)
	sendKeyType(a, tea.KeyEnter)

	if a.wizard != nil {
		t.Fatal("wizard still open after a correctly-sized manual selection")
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}
	op := a.queue.Ops[0]
	if op.MemNode != 0 {
		t.Fatalf("op.MemNode = %d, want 0 (override must not be blocked by GPU locality)", op.MemNode)
	}
	if !strings.Contains(op.Summary, "crosses GPU node") {
		t.Fatalf("op.Summary = %q, want the crosses-GPU-node suffix", op.Summary)
	}
}

// TestWizardManualEscResetsNode covers the esc-from-manual invariant: the
// proposal screen's Pins are specific thread IDs on the proposal's own
// node, so cycling in manual and then escaping without staging must not
// leave the wizard's node pointing anywhere but back at the proposal's.
func TestWizardManualEscResetsNode(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	sendKey(a, 'm')
	sendKey(a, 'n')
	if a.wizard.node == a.wizard.proposal.Node {
		t.Fatal("test setup: cycling did not change the node")
	}

	sendKeyType(a, tea.KeyEsc)
	if a.wizard.screen != proposalScreen {
		t.Fatal("esc did not return to the proposal screen")
	}
	if a.wizard.node != a.wizard.proposal.Node {
		t.Fatalf("wizard.node = %d, want reset to proposal.Node %d", a.wizard.node, a.wizard.proposal.Node)
	}
}

// TestMouseClickTogglesWizardManualCore covers a click on the wizard's
// manual screen node map: it must move the cursor to the clicked core and
// toggle it, same as moving there with the arrow keys and pressing space.
func TestMouseClickTogglesWizardManualCore(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	sendKey(a, 'm')
	if a.wizard.screen != manualScreen {
		t.Fatal("'m' did not switch to the manual screen")
	}
	if len(a.wizard.selected) != 2 {
		t.Fatalf("test setup: manual screen selected = %v, want the proposal's 2 default threads", a.wizard.selected)
	}

	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "wizardcore", 0)
	a.Update(press(h.x0, h.y0))

	if a.wizard.cursor != 0 {
		t.Fatalf("wizard.cursor = %d, want 0 (moved to the clicked core)", a.wizard.cursor)
	}
	if len(a.wizard.selected) != 0 {
		t.Fatalf("wizard.selected = %v, want empty (click toggled core 0 off, mirroring space)", a.wizard.selected)
	}
}

// manyCoresTopo returns a single-node topology with 40 single-thread
// cores (no SMT), so the manual screen's up/down (by coresPerRow == 32)
// has a real second partial row to move into and clamp against -- a
// 2-core node (testTopo) can't distinguish "moves correctly" from "never
// moves": both look identical there.
func manyCoresTopo() *hostinfo.Topology {
	threads := make(map[int]hostinfo.Thread, 40)
	cores := make([]hostinfo.Core, 40)
	nodeThreads := make([]int, 40)
	for i := 0; i < 40; i++ {
		threads[i] = hostinfo.Thread{ID: i, Core: i, Socket: 0, Node: 0, Sibling: -1}
		cores[i] = hostinfo.Core{Socket: 0, ID: i, Node: 0, Threads: []int{i}}
		nodeThreads[i] = i
	}
	nodes := []hostinfo.Node{{ID: 0, Threads: nodeThreads, MemTotalKiB: 1000}}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads}
}

// TestWizardManualUpDownMovesAndClamps covers the manual screen's up/down
// movement (added alongside h/l, moving by coresPerRow like the CPU Map):
// on manyCoresTopo's 40 single-thread cores, down must actually advance
// the cursor a full row, a second down (no full row left) must clamp
// instead of overflowing, and up must retrace back to 0 and then clamp
// there too.
func TestWizardManualUpDownMovesAndClamps(t *testing.T) {
	topo := manyCoresTopo()
	f := &libvirtio.Fake{ConnURI: "test:///x", XML: map[string]string{"plain-vm": plainVMXML}}
	scan := func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error) {
		doms, err := f.ListDomains()
		if err != nil {
			return nil, nil, err
		}
		domsMap := make(map[string]*libvirtio.DomainConfig, len(doms))
		for _, d := range doms {
			domsMap[d.Config.Name] = d.Config
		}
		return model.Build(topo, doms, noNode), domsMap, nil
	}
	a := New(f, scan, t.TempDir(), "test")
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	sendKey(a, 'm')
	if a.wizard == nil || a.wizard.screen != manualScreen {
		t.Fatal("manual screen did not open")
	}

	sendKeyType(a, tea.KeyDown)
	if a.wizard.cursor != coresPerRow {
		t.Fatalf("cursor = %d, want %d after down (one full row)", a.wizard.cursor, coresPerRow)
	}

	sendKeyType(a, tea.KeyDown) // no full second row left (40 cores total): must clamp
	if a.wizard.cursor != coresPerRow {
		t.Fatalf("cursor = %d, want unchanged %d (clamped: no full row remains)", a.wizard.cursor, coresPerRow)
	}

	sendKeyType(a, tea.KeyUp)
	if a.wizard.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after up", a.wizard.cursor)
	}
	sendKeyType(a, tea.KeyUp)
	if a.wizard.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (clamped at the top)", a.wizard.cursor)
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

// TestWizardProposalShowsOwnPinsAsFree renders the proposal screen for an
// already-pinned VM and checks its own current threads draw the free glyph,
// not the pinned one. Before the fix, view() rendered against the App's
// plain projection (which still shows pinned-vm's own pins), disagreeing
// with a proposal/rationale that was computed against the self-stripped
// base -- an operator would see "these cores are free" text next to a
// pinned-looking map.
func TestWizardProposalShowsOwnPinsAsFree(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"pinned-vm": pinnedNode0XML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}

	view, _ := a.wizard.view(200, 40)
	if strings.Contains(view, "\u25cf") {
		t.Fatalf("wizard.view() = %q, want no pinned glyph (pinned-vm is the only VM, and its own pins must project away)", view)
	}
}

// TestWizardViewRendersRationale covers wizard.view's proposal-screen
// rationale rendering (0% covered before this fix round).
func TestWizardViewRendersRationale(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	if len(a.wizard.proposal.Rationale) == 0 {
		t.Fatal("proposal.Rationale is empty, test fixture needs at least one sentence")
	}

	view, _ := a.wizard.view(200, 40)
	if !strings.Contains(view, a.wizard.proposal.Rationale[0]) {
		t.Fatalf("wizard.view() = %q, want the first Rationale sentence", view)
	}
}

// TestWizardViewRendersWarning saturates every thread of node 1 with vm1's
// pending pin, then opens the wizard for vm2 (forced onto node 1 by its
// hostdev) so Propose has to share threads and returns a Warning. Covers
// wizard.view's warning rendering.
func TestWizardViewRendersWarning(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"vm1": vm1XML, "vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)

	a.queue.Add(model.PendingOp{
		Kind:    model.OpPin,
		VM:      "vm1",
		Pins:    map[int][]int{0: {2}, 1: {3}, 2: {6}, 3: {7}},
		MemNode: 1,
		Summary: "vm1: pin",
	})

	a.tab = 2
	a.vmSel = vmIndex(t, a, "vm2")
	sendKey(a, 'p')

	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open for vm2", a.status)
	}
	if len(a.wizard.proposal.Warnings) == 0 {
		t.Fatal("proposal.Warnings is empty, want a contended-node warning (node 1 is fully claimed by vm1)")
	}

	view, _ := a.wizard.view(200, 40)
	if !strings.Contains(view, a.wizard.proposal.Warnings[0]) {
		t.Fatalf("wizard.view() = %q, want the Warning sentence", view)
	}
}

// TestWizardManualViewShowsSelectedCount covers the manual screen's
// "selected N/M" line.
func TestWizardManualViewShowsSelectedCount(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	sendKey(a, 'm')
	if a.wizard == nil || a.wizard.screen != manualScreen {
		t.Fatal("manual screen did not open")
	}

	view, _ := a.wizard.view(200, 40)
	if !strings.Contains(view, "selected 2/2") {
		t.Fatalf("wizard.view() = %q, want the running selected-count line", view)
	}
	if !strings.Contains(a.View(), "[h/l/j/k/up/down] move") {
		t.Fatalf("View() = %q, want the manual-screen key hint in the status bar", a.View())
	}
}

// TestWizardProposalHighlightsProposedThreads forces a color-emitting
// profile (the test harness otherwise renders styles as plain text) and
// checks the proposal screen's node map actually emits ANSI codes for its
// proposed threads, covering renderNodeMap/nodeMapCell's highlight branch.
func TestWizardProposalHighlightsProposedThreads(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}

	view, _ := a.wizard.view(200, 40)
	if !strings.Contains(view, "\x1b[") {
		t.Fatalf("wizard.view() = %q, want ANSI escapes from the highlight style on proposed threads", view)
	}
}

// TestPinRefusedReadOnly mirrors TestStripRefusedReadOnly for the 'p' key:
// outside edit mode it must refuse without opening a wizard.
func TestPinRefusedReadOnly(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	a.tab = 2

	sendKey(a, 'p')

	if a.wizard != nil {
		t.Fatal("wizard opened in read-only mode, want refused")
	}
	if !strings.Contains(a.status, "press e to enter edit mode first") {
		t.Fatalf("status = %q, want the edit-mode hint", a.status)
	}
}
