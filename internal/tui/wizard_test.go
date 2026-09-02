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
	a.tab = tabVMs

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
	a.tab = tabVMs
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
	a.tab = tabVMs

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
	a.tab = tabVMs

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
	a.tab = tabVMs

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
	a.tab = tabVMs

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

// TestWizardManualRoundTrip drives the manual grid as an alternative
// threads-field editor: opening it starts with the form's current
// threads pre-selected, an edit there (deselect core 0, select core 1
// instead) writes back into threadsText as a cpulist on enter, and
// returns to the form screen -- not staged outright, since any count is
// accepted by the manual screen itself; the form's own validation is
// what actually gates staging (see TestWizardFormInvalidThreadsBlocksEnter).
func TestWizardManualRoundTrip(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

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
	if len(a.wizard.selected) != 2 {
		t.Fatalf("manual screen selected = %v, want the form's 2 current threads pre-selected", a.wizard.selected)
	}

	// Cursor starts at core 0 (threads 0,4); toggle it off, move to core 1
	// (threads 1,5) and toggle it on -- still exactly 2 threads overall.
	sendKeyType(a, tea.KeySpace)
	sendKeyType(a, tea.KeyRight)
	sendKeyType(a, tea.KeySpace)
	sendKeyType(a, tea.KeyEnter)

	if a.wizard == nil {
		t.Fatal("wizard closed after accepting the manual selection, want it back on the form")
	}
	if a.wizard.screen != formScreen {
		t.Fatal("enter on the manual screen did not return to the form")
	}
	if a.wizard.threadsText != "1,5" {
		t.Fatalf("threadsText = %q, want %q (the manual selection, written back as a cpulist)", a.wizard.threadsText, "1,5")
	}

	sendKeyType(a, tea.KeyEnter) // now stage from the form
	if a.wizard != nil {
		t.Fatal("wizard still open after staging from the form")
	}
	op := a.queue.Ops[0]
	if got := op.Pins[0]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("op.Pins[0] = %v, want [1]", got)
	}
	if got := op.Pins[1]; len(got) != 1 || got[0] != 5 {
		t.Fatalf("op.Pins[1] = %v, want [5]", got)
	}
}

// TestWizardFormCyclesNodeAndWarnsOnGPUCross covers the form's node field
// (left/right cycles, re-proposing within the new node) and the loud
// GPU-crossing warning: vm2's hostdev forces the proposal onto node 1;
// cycling the node field to 0 must re-propose there (a fresh, valid
// 2-thread selection on node 0) and show the crosses-GPU warning. What
// happens on enter from here is covered separately below (see
// openWizardCrossingGPU and the TestWizardGPUCross* tests), since that's
// now App's shared y/n confirm dialog rather than the wizard's own state.
func TestWizardFormCyclesNodeAndWarnsOnGPUCross(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	if a.wizard.proposal.Node != 1 {
		t.Fatalf("proposal.Node = %d, want 1 (forced by vm2's hostdev)", a.wizard.proposal.Node)
	}
	if a.wizard.field != fieldNode {
		t.Fatalf("field = %d, want fieldNode focused by default", a.wizard.field)
	}

	sendKeyType(a, tea.KeyLeft) // cycle node 1 -> node 0 (wraps)
	if a.wizard.node != 0 {
		t.Fatalf("node = %d, want 0 after cycling", a.wizard.node)
	}
	if len(a.wizard.threadsText) == 0 {
		t.Fatal("threadsText empty after re-propose, want a fresh 2-thread selection on node 0")
	}

	view, _ := a.wizard.view(200, 40)
	if !strings.Contains(view, "GPU at 0000:81:00.0 is on node 1") {
		t.Fatalf("view() = %q, want the crosses-GPU-node warning", view)
	}
}

// openWizardCrossingGPU opens the pin wizard for vm2 (whose hostdev forces
// its proposal onto node 1) and cycles the node field to node 0, leaving a
// valid, GPU-crossing form with enter not yet pressed -- shared setup for
// the TestWizardGPUCross* confirm tests below.
func openWizardCrossingGPU(t *testing.T) *App {
	t.Helper()
	a := wizardTestApp(t, map[string]string{"vm2": vm2XML}, vm2PCINode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	sendKeyType(a, tea.KeyLeft) // cycle node 1 -> node 0 (wraps), a GPU-crossing pick
	if a.wizard.node != 0 {
		t.Fatalf("node = %d, want 0 after cycling", a.wizard.node)
	}
	return a
}

// TestWizardGPUCrossEnterOpensConfirm covers tryStage's confirm path (the
// fix for the old crossConfirmed "press enter again" softening): a valid
// form that crosses the GPU node opens App's shared y/n confirm on the
// first enter instead of staging outright -- nothing is queued yet, and
// the wizard stays open underneath, untouched.
func TestWizardGPUCrossEnterOpensConfirm(t *testing.T) {
	a := openWizardCrossingGPU(t)
	threadsBefore := a.wizard.threadsText

	sendKeyType(a, tea.KeyEnter)

	if a.confirm == nil {
		t.Fatal("no confirm opened on enter while crossing the GPU node")
	}
	if a.confirm.prompt != "Pin across the GPU's node anyway? [y/n]" {
		t.Fatalf("confirm.prompt = %q, want the GPU-cross confirm text", a.confirm.prompt)
	}
	if a.wizard == nil {
		t.Fatal("wizard closed while the confirm is open, want it to stay open underneath")
	}
	if a.wizard.threadsText != threadsBefore {
		t.Fatalf("threadsText changed to %q while the confirm is open, want %q unchanged", a.wizard.threadsText, threadsBefore)
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after the first enter, want 0 (not staged yet)", a.queue.Len())
	}
}

// TestWizardGPUCrossYStages covers the confirm's "y": it stages exactly
// what the preview showed (op.MemNode 0, not the GPU's node 1) with the
// Summary's "crosses GPU node" suffix, closing both the confirm and the
// wizard.
func TestWizardGPUCrossYStages(t *testing.T) {
	a := openWizardCrossingGPU(t)
	sendKeyType(a, tea.KeyEnter)
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	sendKey(a, 'y')

	if a.confirm != nil {
		t.Fatal("confirm still open after y")
	}
	if a.wizard != nil {
		t.Fatal("wizard still open after y")
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

// TestWizardGPUCrossNKeepsFormOpen covers the confirm's "n": it must only
// dismiss the confirm, not the wizard -- the form stays open with every
// field exactly as the operator left it, so declining the cross doesn't
// cost them the filled-in form.
func TestWizardGPUCrossNKeepsFormOpen(t *testing.T) {
	a := openWizardCrossingGPU(t)
	wantNode, wantThreads := a.wizard.node, a.wizard.threadsText
	sendKeyType(a, tea.KeyEnter)
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	sendKey(a, 'n')

	if a.confirm != nil {
		t.Fatal("confirm still open after n")
	}
	if a.wizard == nil {
		t.Fatal("wizard closed after declining the confirm with n, want it to stay open")
	}
	if a.wizard.node != wantNode || a.wizard.threadsText != wantThreads {
		t.Fatalf("form fields changed after n: node=%d threads=%q, want node=%d threads=%q",
			a.wizard.node, a.wizard.threadsText, wantNode, wantThreads)
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after n, want 0", a.queue.Len())
	}
}

// TestWizardGPUCrossEscKeepsFormOpen mirrors TestWizardGPUCrossNKeepsFormOpen
// for esc: the bug this fixes had esc, while the old crossConfirmed flag
// was armed, cancel the WHOLE wizard instead of just the confirm --
// losing the operator's filled-in form for declining one cross-node pick.
func TestWizardGPUCrossEscKeepsFormOpen(t *testing.T) {
	a := openWizardCrossingGPU(t)
	wantNode, wantThreads := a.wizard.node, a.wizard.threadsText
	sendKeyType(a, tea.KeyEnter)
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	sendKeyType(a, tea.KeyEsc)

	if a.confirm != nil {
		t.Fatal("confirm still open after esc")
	}
	if a.wizard == nil {
		t.Fatal("wizard closed by esc, want only the confirm to have closed")
	}
	if a.wizard.node != wantNode || a.wizard.threadsText != wantThreads {
		t.Fatalf("form fields changed after esc: node=%d threads=%q, want node=%d threads=%q",
			a.wizard.node, a.wizard.threadsText, wantNode, wantThreads)
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after esc, want 0", a.queue.Len())
	}
}

// TestWizardGPUCrossWarningShownOnce covers the fix for the GPU-cross
// warning printing twice in the same popup: it must appear loud (under
// the node field) exactly once, not again in the tail warnings list --
// see currentWarnings, which deliberately omits it now that viewForm
// prints it itself.
func TestWizardGPUCrossWarningShownOnce(t *testing.T) {
	a := openWizardCrossingGPU(t)

	view, _ := a.wizard.view(200, 40)
	const warningPrefix = "GPU at 0000:81:00.0 is on node 1"
	if n := strings.Count(view, warningPrefix); n != 1 {
		t.Fatalf("view() contains the GPU-cross warning %d times, want exactly 1:\n%s", n, view)
	}
}

// TestConfirmStacksOverWizardAt80x24 covers the fix for renderDialog's
// switch putting a.confirm ahead of a.wizard/a.memPicker with no case that
// keeps the wizard visible underneath: opening the GPU-cross confirm used
// to make the whole wizard popup vanish rather than stay stacked beneath
// it. At 80x24 -- comfortably enough for both, the confirm is 5 lines and
// the wizard 12 against a 21-line body budget (see renderConfirmUnder) --
// the rendered view must show both the wizard's own form (its title) and
// the confirm's own prompt.
func TestConfirmStacksOverWizardAt80x24(t *testing.T) {
	a := openWizardCrossingGPU(t)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	sendKeyType(a, tea.KeyEnter)
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	view := a.View()
	if !strings.Contains(view, "pin vm2") {
		t.Fatalf("View() = %q, want the wizard's own title still visible stacked under the confirm", view)
	}
	if !strings.Contains(view, "Pin across the GPU's node anyway? [y/n]") {
		t.Fatalf("View() = %q, want the confirm's own prompt visible", view)
	}
}

// TestConfirmOverWizardFitsBudgetAt80x16 covers the same stacking fix at
// the smallest terminal View ever renders at without going into the
// too-small screen (80x16): whichever of renderConfirmUnder's two outcomes
// applies (the wizard fits stacked under the confirm, or -- the fallback --
// only the confirm shows), the whole view must still fit exactly within
// budget: 16 lines total, no line wider than 80.
func TestConfirmOverWizardFitsBudgetAt80x16(t *testing.T) {
	a := openWizardCrossingGPU(t)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 16})

	sendKeyType(a, tea.KeyEnter)
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 16 {
		t.Fatalf("View() has %d lines, want exactly 16: %q", len(lines), view)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw > 80 {
			t.Fatalf("line %d width = %d, want <= 80: %q", i, lw, l)
		}
	}
}

// TestConfirmOverWizardKeyBarHintsYN covers the fix for renderStatusBar
// having no a.confirm case of its own: while the wizard's GPU-cross confirm
// is open on top of it, the key bar used to keep advertising the wizard's
// own keys (e.g. "[a] autofill"), none of which do anything while the
// confirm is capturing all key input, and never mentioned y/n at all. It
// must show the confirm's own y/n hint instead.
func TestConfirmOverWizardKeyBarHintsYN(t *testing.T) {
	a := openWizardCrossingGPU(t)
	sendKeyType(a, tea.KeyEnter)
	if a.confirm == nil {
		t.Fatal("confirm did not open")
	}

	hint := a.renderStatusBar()
	if !strings.Contains(hint, "[y]") {
		t.Fatalf("renderStatusBar() = %q, want a \"[y]\" hint while the confirm is open", hint)
	}
	if strings.Contains(hint, "[a] autofill") {
		t.Fatalf("renderStatusBar() = %q, want the wizard's own (inert) hints gone while the confirm is open", hint)
	}
}

// fourVCPUWizardXML is a plain 4-vcpu VM with no pins/devices, sized to
// fit inside realHostTopo's L3 #0 domain (12 threads) for
// TestWizardFormWithinFilterRestrictsThreads.
const fourVCPUWizardXML = `<domain type='kvm'>
  <name>four-vcpu</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b30</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>4</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// TestWizardFormWithinFilterRestrictsThreads covers the "within" field's
// L3-domain filter (cycling it re-proposes restricted to just that
// domain's threads) on realHostTopo (2 L3 domains: #0 threads
// 0-5,12-17, #1 threads 6-11,18-23).
func TestWizardFormWithinFilterRestrictsThreads(t *testing.T) {
	topo := realHostTopo()
	f := &libvirtio.Fake{ConnURI: "test:///x", XML: map[string]string{"four-vcpu": fourVCPUWizardXML}}
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
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	a.wizard.field = fieldWithin
	sendKeyType(a, tea.KeyRight) // any -> L3 #0 (threads 0-5,12-17)

	if a.wizard.within != 0 {
		t.Fatalf("within = %d, want 0 (L3 #0)", a.wizard.within)
	}
	ids, errMsg := a.wizard.parseThreads()
	if errMsg != "" {
		t.Fatalf("parseThreads() error = %q, want a valid 4-thread selection within L3 #0", errMsg)
	}
	l3Zero := threadSet([]int{0, 1, 2, 3, 4, 5, 12, 13, 14, 15, 16, 17})
	for _, id := range ids {
		if !l3Zero[id] {
			t.Fatalf("threads = %v, want all within L3 #0 (0-5,12-17)", ids)
		}
	}
}

// TestWizardFormWithinFilterRejectsHandTypedOutsideL3 covers parseThreads'
// L3 check: with within = L3 #0 (threads 0-5,12-17), a hand-typed 4-thread
// list drawn from L3 #1 instead (6,7,8,9 -- same node 0, so the node check
// alone would pass it) must still be rejected, naming the first offending
// thread and the filter it's not in, even though the field still displays
// "L3 #0".
func TestWizardFormWithinFilterRejectsHandTypedOutsideL3(t *testing.T) {
	topo := realHostTopo()
	f := &libvirtio.Fake{ConnURI: "test:///x", XML: map[string]string{"four-vcpu": fourVCPUWizardXML}}
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
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	a.wizard.field = fieldWithin
	sendKeyType(a, tea.KeyRight) // any -> L3 #0 (threads 0-5,12-17)
	if a.wizard.within != 0 {
		t.Fatalf("within = %d, want 0 (L3 #0)", a.wizard.within)
	}

	a.wizard.field = fieldThreads
	a.wizard.threadsText = "6-9" // valid node (0), wrong count is not the point here: 4 threads, all in L3 #1
	a.wizard.threadsCaret = len(a.wizard.threadsText)

	ids, errMsg := a.wizard.parseThreads()
	if errMsg == "" {
		t.Fatalf("parseThreads() = %v, nil error, want a rejection naming the L3 #0 filter", ids)
	}
	if !strings.Contains(errMsg, "not in L3 #0") {
		t.Fatalf("parseThreads() error = %q, want it to name the L3 #0 filter", errMsg)
	}

	sendKeyType(a, tea.KeyEnter)
	if a.wizard == nil {
		t.Fatal("wizard closed on enter with an out-of-filter thread list, want it to stay open")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 (an out-of-filter list must not stage)", a.queue.Len())
	}
}

// TestWizardFormOpensWithProposalDefaults covers the form's own defaults:
// node = Propose's node, threadsText = Propose's own threads (formatted
// as a cpulist), field starts on fieldNode, within on "any", memory node
// on "same as node".
func TestWizardFormOpensWithProposalDefaults(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	w := a.wizard
	if w.node != w.proposal.Node {
		t.Fatalf("node = %d, want proposal.Node %d", w.node, w.proposal.Node)
	}
	if w.field != fieldNode {
		t.Fatalf("field = %d, want fieldNode", w.field)
	}
	if w.within != -1 {
		t.Fatalf("within = %d, want -1 (any)", w.within)
	}
	if w.memSel != -2 {
		t.Fatalf("memSel = %d, want -2 (same as node)", w.memSel)
	}
	want := formatCPURanges(assignedThreads(w.proposal.Pins))
	if w.threadsText != want {
		t.Fatalf("threadsText = %q, want %q (the proposal's own threads)", w.threadsText, want)
	}
}

// TestWizardFormInvalidThreadsBlocksEnter covers live validation: a
// threads field with the wrong count never stages, and the resulting
// error message ("N threads given, need M") shows in the view.
func TestWizardFormInvalidThreadsBlocksEnter(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatal("wizard did not open")
	}
	a.wizard.field = fieldThreads
	a.wizard.threadsText = "0"
	a.wizard.threadsCaret = len(a.wizard.threadsText)

	sendKeyType(a, tea.KeyEnter)
	if a.wizard == nil {
		t.Fatal("wizard closed after enter with an invalid thread count, want it to stay open")
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0 (an invalid list must not stage)", a.queue.Len())
	}
	view, _ := a.wizard.view(90, 30)
	if !strings.Contains(view, "1 threads given, need 2") {
		t.Fatalf("view() = %q, want the count-mismatch error message", view)
	}
}

// TestWizardFormLeaveMemoryLeavesMemNode covers the "memory node" field's
// "leave" option (cycled to, wrapping past every node id): the staged op
// gets MemNode -1 (SetPinning's own "leave numatune untouched" sentinel)
// and the summary reads "memory -> unchanged".
func TestWizardFormLeaveMemoryLeavesMemNode(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatal("wizard did not open")
	}
	a.wizard.field = fieldMemNode
	sendKeyType(a, tea.KeyLeft) // "same as node" is first; left wraps to "leave", the last option

	if a.wizard.memSel != -1 {
		t.Fatalf("memSel = %d, want -1 (leave)", a.wizard.memSel)
	}

	sendKeyType(a, tea.KeyEnter)
	if a.wizard != nil {
		t.Fatal("wizard still open after staging")
	}
	op := a.queue.Ops[0]
	if op.MemNode != -1 {
		t.Fatalf("op.MemNode = %d, want -1 (leave)", op.MemNode)
	}
	if !strings.Contains(op.Summary, "memory -> unchanged") {
		t.Fatalf("op.Summary = %q, want \"memory -> unchanged\"", op.Summary)
	}
}

// TestWizardFormFitsPopupAtExtremeSizes covers the width/height
// invariant: at 80x16 (cramped) and 250x95 (huge) with a 2-node/200-
// core-per-node fixture (so the preview grid has far more rows than any
// budget could show), the form panel must never exceed the dialog width
// (dw) or the body budget's line count -- the preview windows/scrolls
// itself to fit rather than overflow the popup.
func TestWizardFormFitsPopupAtExtremeSizes(t *testing.T) {
	topo := manyNodeManyCoresTopo(2, 200)
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
	a.tab = tabVMs

	for _, sz := range []struct{ w, h int }{{80, 16}, {250, 95}} {
		a.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		sendKey(a, 'p')
		if a.wizard == nil {
			t.Fatalf("width %d height %d: wizard did not open (status %q)", sz.w, sz.h, a.status)
		}

		_, _, _, _, chrome := a.renderChrome()
		budget := a.bodyBudget(chrome)
		dw := dialogWidth(effectiveWidth(a.width), dialogMaxWidth)
		panel, _ := a.wizard.view(dw, budget)
		lines := strings.Split(panel, "\n")
		if len(lines) > budget {
			t.Fatalf("width %d height %d: panel has %d lines, want <= %d (budget)", sz.w, sz.h, len(lines), budget)
		}
		for i, l := range lines {
			if lw := lipgloss.Width(l); lw > dw {
				t.Fatalf("width %d height %d: line %d width = %d, want <= %d (dw)", sz.w, sz.h, i, lw, dw)
			}
		}

		sendKeyType(a, tea.KeyEsc)
		if a.wizard != nil {
			t.Fatalf("width %d height %d: wizard still open after esc", sz.w, sz.h)
		}
	}
}

// TestWizardDialogIsCenteredAndWidthCapped covers the wizard's dialog
// treatment: wizard.view itself now returns a tight, uncentered panel --
// App.renderFull composites it onto the (normally, fully rendered) body
// via overlay, using centerXY for the placement math, so it's no longer
// meaningful to look for a literal run of blank characters in the
// composited output (the body underneath a dialog is real content, not
// blank space, wherever the dialog doesn't cover it). At a terminal much
// wider than dialogMaxWidth, the raw dialog panel must still be capped
// well under the terminal width, and centerXY's own placement math must
// still put it well clear of the left edge.
func TestWizardDialogIsCenteredAndWidthCapped(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs
	a.Update(tea.WindowSizeMsg{Width: 300, Height: 40})
	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatal("wizard did not open")
	}

	dialog, _, ok := a.renderDialog(300, 40)
	if !ok {
		t.Fatal("renderDialog() ok = false, want true with a.wizard open")
	}
	dw := lipgloss.Width(strings.SplitN(dialog, "\n", 2)[0])
	if dw >= 300 {
		t.Fatalf("wizard dialog panel width = %d, want it capped well under the 300-column terminal", dw)
	}

	x, _ := centerXY(dw, lineCount(dialog), 300, 40)
	if x < 50 {
		t.Fatalf("centerXY x = %d, want a large left margin centering a %d-wide dialog in a 300-column terminal", x, dw)
	}

	if !strings.Contains(a.View(), "pin plain-vm") {
		t.Fatalf("View() = %q, want the wizard's dialog composited over the body", a.View())
	}
}

// TestWizardViewIsOnePanel covers the fix for wizard.view rendering two
// stacked bordered boxes (a grid panel, then a separate "info" panel):
// it must now be a single popup panel -- exactly one top-left border
// corner -- with the grid, a rule line, and the info text all inside it.
func TestWizardViewIsOnePanel(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}
	if len(a.wizard.proposal.Rationale) == 0 {
		t.Fatal("proposal.Rationale is empty, test fixture needs at least one sentence")
	}

	view, _ := a.wizard.view(60, 40)
	if n := strings.Count(view, "\u256d"); n != 1 { // top-left border corner
		t.Fatalf("wizard.view() has %d top-left border corners, want exactly 1 (one panel, not two): %q", n, view)
	}
	if !strings.Contains(view, strings.Repeat("-", 10)) {
		t.Fatalf("wizard.view() = %q, want a \"-\" rule line between the grid and info sections", view)
	}
}

// TestMouseClickTogglesWizardManualCore covers a click on the wizard's
// manual screen node map: it must move the cursor to the clicked core and
// toggle it, same as moving there with the arrow keys and pressing space.
func TestMouseClickTogglesWizardManualCore(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

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
	a.tab = tabVMs

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

	a.tab = tabVMs
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
	a.tab = tabVMs

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
	a.tab = tabVMs

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}

	view, _ := a.wizard.view(200, 40)
	if strings.Contains(view, "\u25cf") {
		t.Fatalf("wizard.view() = %q, want no pinned glyph (pinned-vm is the only VM, and its own pins must project away)", view)
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

	a.tab = tabVMs
	a.vmSel = vmIndex(t, a, "vm2")
	sendKey(a, 'p')

	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open for vm2", a.status)
	}
	if len(a.wizard.proposal.Warnings) == 0 {
		t.Fatal("proposal.Warnings is empty, want a contended-node warning (node 1 is fully claimed by vm1)")
	}

	view, _ := a.wizard.view(200, 40)
	// The dialog is capped at dialogMaxWidth regardless of the terminal
	// width, so a long sentence word-wraps across lines even at width 200
	// -- check a short, guaranteed-single-line prefix rather than the
	// whole (possibly-wrapped) sentence.
	prefix := a.wizard.proposal.Warnings[0][:30]
	if !strings.Contains(view, prefix) {
		t.Fatalf("wizard.view() = %q, want the Warning sentence (prefix %q)", view, prefix)
	}
}

// TestWizardManualViewShowsSelectedCount covers the manual screen's
// "selected N/M" line.
func TestWizardManualViewShowsSelectedCount(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = tabVMs

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
	a.tab = tabVMs

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
	a.tab = tabVMs

	sendKey(a, 'p')

	if a.wizard != nil {
		t.Fatal("wizard opened in read-only mode, want refused")
	}
	if !strings.Contains(a.status, "press e to enter edit mode first") {
		t.Fatalf("status = %q, want the edit-mode hint", a.status)
	}
}
