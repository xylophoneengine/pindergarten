package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// testTopo replicates the 2-node/8-thread topology from
// internal/model/snapshot_test.go: test helpers do not cross packages.
func testTopo() *hostinfo.Topology {
	threads := map[int]hostinfo.Thread{
		0: {ID: 0, Core: 0, Socket: 0, Node: 0, Sibling: 4},
		4: {ID: 4, Core: 0, Socket: 0, Node: 0, Sibling: 0},
		1: {ID: 1, Core: 1, Socket: 0, Node: 0, Sibling: 5},
		5: {ID: 5, Core: 1, Socket: 0, Node: 0, Sibling: 1},
		2: {ID: 2, Core: 0, Socket: 1, Node: 1, Sibling: 6},
		6: {ID: 6, Core: 0, Socket: 1, Node: 1, Sibling: 2},
		3: {ID: 3, Core: 1, Socket: 1, Node: 1, Sibling: 7},
		7: {ID: 7, Core: 1, Socket: 1, Node: 1, Sibling: 3},
	}
	cores := []hostinfo.Core{
		{Socket: 0, ID: 0, Node: 0, Threads: []int{0, 4}},
		{Socket: 0, ID: 1, Node: 0, Threads: []int{1, 5}},
		{Socket: 1, ID: 0, Node: 1, Threads: []int{2, 6}},
		{Socket: 1, ID: 1, Node: 1, Threads: []int{3, 7}},
	}
	nodes := []hostinfo.Node{
		{ID: 0, Threads: []int{0, 1, 4, 5}, MemTotalKiB: 1000},
		{ID: 1, Threads: []int{2, 3, 6, 7}, MemTotalKiB: 1000},
	}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads}
}

const plainVMXML = `<domain type='kvm'>
  <name>plain-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b01</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`

// testApp builds an App around a Fake hypervisor with one plain-vm domain.
// It does not run a scan; call runScan when a test needs a's snapshot
// populated.
func testApp(t *testing.T, ro bool) *App {
	t.Helper()
	return testAppDir(t, ro, t.TempDir())
}

func testAppDir(t *testing.T, ro bool, backupDir string) *App {
	t.Helper()
	f := &libvirtio.Fake{
		ConnURI:  "test:///x",
		XML:      map[string]string{"plain-vm": plainVMXML},
		RO:       ro,
		ROReason: "no write perm",
	}
	scan := func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error) {
		doms, err := f.ListDomains()
		if err != nil {
			return nil, nil, err
		}
		domsMap := make(map[string]*libvirtio.DomainConfig, len(doms))
		for _, d := range doms {
			domsMap[d.Config.Name] = d.Config
		}
		return model.Build(testTopo(), doms, func(string) int { return -1 }), domsMap, nil
	}
	return New(f, scan, backupDir, "test")
}

// runScan drives Init's scan Cmd synchronously and feeds its result back
// through Update, so a.snap is populated for tests that need body content.
func runScan(t *testing.T, a *App) {
	t.Helper()
	cmd := a.Init()
	if cmd == nil {
		t.Fatal("Init() returned a nil Cmd")
	}
	if _, cmd2 := a.Update(cmd()); cmd2 != nil {
		t.Fatalf("Update(scanDoneMsg) returned unexpected Cmd: %v", cmd2)
	}
}

func sendKey(a *App, r rune) tea.Cmd {
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return cmd
}

func TestStartsReadOnly(t *testing.T) {
	a := testApp(t, false)
	view := a.View()
	if !strings.Contains(view, "READ ONLY") {
		t.Fatalf("View() = %q, want it to contain READ ONLY", view)
	}
}

func TestEditUnlockConfirm(t *testing.T) {
	a := testApp(t, false)

	sendKey(a, 'e')
	view := a.View()
	if !strings.Contains(view, "Enable edit mode") {
		t.Fatalf("View() after 'e' = %q, want confirm prompt", view)
	}

	sendKey(a, 'y')
	view = a.View()
	if !strings.Contains(view, "EDIT") {
		t.Fatalf("View() after 'y' = %q, want EDIT badge", view)
	}
	if strings.Contains(view, "READ ONLY") {
		t.Fatalf("View() after 'y' = %q, want no READ ONLY badge", view)
	}
}

// TestConfirmFitsHeightBudget covers the fix for the confirm panel not
// counting toward chrome: at a short terminal (height 10), the prompt and
// its "[y]es" key hint must still be visible (not pushed out by
// clampHeight's last-resort truncation), and the whole view must still fit
// height 10.
func TestConfirmFitsHeightBudget(t *testing.T) {
	a := wizardTestApp(t, manyVMXMLs(5), noNode)
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 10})

	sendKey(a, 'e')
	if a.confirm == nil {
		t.Fatal("confirm modal did not open")
	}

	view := a.renderFull()
	lines := strings.Split(view, "\n")
	if len(lines) > 10 {
		t.Fatalf("View() has %d lines, want <= 10: %q", len(lines), view)
	}
	if !strings.Contains(view, "Enable edit mode") {
		t.Fatalf("View() = %q, want the confirm prompt visible", view)
	}
	if !strings.Contains(view, "[y]es") {
		t.Fatalf("View() = %q, want the \"[y]es\" hint visible", view)
	}
}

// TestConfirmWithWrappedStatusKeepsKeyBarAndYes covers the fix for
// bodyBudget's floor of 3: at width 40 height 16, a long status message
// (word-wrapped to several lines) plus an open confirm dialog could
// exceed chrome's own footprint, and clampHeight's bottom-up truncation
// used to eat the key bar (or worse). Chrome must now shrink itself
// (dropping the status line) rather than ever losing the key bar; the
// confirm dialog (now clamped to the body's own budget rather than
// chrome, and no longer protected by buildConfirmPanel's old tiered
// fallback -- see renderDialog) keeps its "[y]es" hint too, as long as
// the status message hasn't starved the body budget down to nothing (a
// repeat count of 5+ here does -- see TestConfirmDialogWrapsPromptBefore-
// Clipping and renderDialog's own doc comment for that simplification).
func TestConfirmWithWrappedStatusKeepsKeyBarAndYes(t *testing.T) {
	a := testApp(t, false)
	a.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
	a.status = strings.Repeat("a long status message that wraps across several lines ", 4)
	sendKey(a, 'e')
	if a.confirm == nil {
		t.Fatal("confirm modal did not open")
	}

	view := a.renderFull()
	lines := strings.Split(view, "\n")
	if len(lines) > 16 {
		t.Fatalf("View() has %d lines, want <= 16: %q", len(lines), view)
	}
	// Checked as two separate substrings, not one "[q] quit": at width 40
	// the key bar's own hint list is wider than the terminal and word-
	// wraps regardless of this fix, which can land "[q]" and "quit" on
	// different lines.
	if !strings.Contains(view, "[q]") || !strings.Contains(view, "quit") {
		t.Fatalf("View() = %q, want the key bar's quit hint still visible", view)
	}
	if !strings.Contains(view, "[y]es") {
		t.Fatalf("View() = %q, want the \"[y]es\" hint visible", view)
	}
}

// TestConfirmAtHeight8KeepsYesHint covers the same fix at an even tighter
// height (8): the confirm panel's own "[y]es" key line must never be
// sacrificed, even when there isn't room for its prompt text too.
func TestConfirmAtHeight8KeepsYesHint(t *testing.T) {
	a := wizardTestApp(t, manyVMXMLs(5), noNode)
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 8})

	sendKey(a, 'e')
	if a.confirm == nil {
		t.Fatal("confirm modal did not open")
	}

	view := a.renderFull()
	lines := strings.Split(view, "\n")
	if len(lines) > 8 {
		t.Fatalf("View() has %d lines, want <= 8: %q", len(lines), view)
	}
	if !strings.Contains(view, "[y]es") {
		t.Fatalf("View() = %q, want the \"[y]es\" hint visible", view)
	}
}

func TestEditBlockedWhenRO(t *testing.T) {
	a := testApp(t, true)

	sendKey(a, 'e')
	view := a.View()
	if !strings.Contains(view, "READ ONLY") {
		t.Fatalf("View() after 'e' on RO hypervisor = %q, want it to stay READ ONLY", view)
	}
	if !strings.Contains(view, "no write perm") {
		t.Fatalf("View() after 'e' on RO hypervisor = %q, want reason shown", view)
	}
}

// activeTabName reports which tab a considers active. The tab pill's own
// text carries no bracket/marker (both active and inactive tabs render
// "Name" identically padded; only the style -- a filled pill vs. dimmed --
// tells them apart), so tests check this directly rather than grepping
// rendered output for a marker.
func activeTabName(a *App) string {
	return tabNames[a.tab]
}

func TestTabSwitch(t *testing.T) {
	a := testApp(t, false)

	sendKey(a, '3')
	if got := activeTabName(a); got != "VMs" {
		t.Fatalf("active tab after '3' = %q, want VMs", got)
	}
}

func TestQuitConfirmWithPending(t *testing.T) {
	a := testApp(t, false)
	a.queue.Add(model.PendingOp{VM: "plain-vm", Summary: "pin"})

	sendKey(a, 'q')
	view := a.View()
	if !strings.Contains(view, "Discard") {
		t.Fatalf("View() after 'q' with pending ops = %q, want discard confirm", view)
	}

	cmd := sendKey(a, 'n')
	if cmd != nil {
		t.Fatalf("Update('n') returned a Cmd, want nil (declining should not quit)")
	}
	if a.confirm != nil {
		t.Fatal("confirm still set after 'n'")
	}
}

// TestCtrlCQuitsFromConfirmModal covers ctrl+c reaching tea.Quit from
// every modal/screen: even with a confirm prompt open (which would
// otherwise swallow every key but y/n/esc), ctrl+c must still quit --
// unconditionally, unlike 'q' which asks first when ops are pending.
func TestCtrlCQuitsFromConfirmModal(t *testing.T) {
	a := testApp(t, false)
	a.queue.Add(model.PendingOp{VM: "plain-vm", Summary: "pin"})

	sendKey(a, 'q') // opens the "discard pending ops and quit?" confirm
	if a.confirm == nil {
		t.Fatal("confirm modal did not open")
	}

	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c while a confirm modal is open returned a nil Cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c Cmd produced %T, want tea.QuitMsg", cmd())
	}
}

func TestMouseClickSwitchesTab(t *testing.T) {
	a := testApp(t, false)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = a.View() // record tabRanges

	rng := a.tabRanges[2] // VMs
	a.Update(tea.MouseMsg{X: rng[0], Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})

	if got := activeTabName(a); got != "VMs" {
		t.Fatalf("active tab after click on the VMs label = %q, want VMs", got)
	}
}

// TestStatusClearsOnTabSwitch covers the digit-key, Tab-key, and mouse
// tab-switch paths: each must clear a stale a.status, since it belonged to
// whatever the user was doing on the tab they're leaving.
func TestStatusClearsOnTabSwitch(t *testing.T) {
	a := testApp(t, false)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = a.View() // record tabRanges

	a.status = "leftover"
	sendKey(a, '3') // digit path, tab differs from current (0)
	if a.status != "" {
		t.Fatalf("status = %q after a digit tab switch, want cleared", a.status)
	}

	a.status = "leftover"
	sendKeyType(a, tea.KeyTab)
	if a.status != "" {
		t.Fatalf("status = %q after Tab, want cleared", a.status)
	}

	a.status = "leftover"
	rng := a.tabRanges[0] // Overview: a.tab is currently 3 (Pending) after the switches above
	a.Update(tea.MouseMsg{X: rng[0], Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if a.status != "" {
		t.Fatalf("status = %q after a mouse tab click, want cleared", a.status)
	}
}

// TestMouseIgnoredWhileWizardOpen covers handleMouse's modal guard: a
// tab-bar click while the wizard (or mem-node picker, or apply flow) is
// open must not switch tabs out from under it -- previously only
// a.confirm was checked.
func TestMouseIgnoredWhileWizardOpen(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"plain-vm": plainVMXML}, noNode)
	runScan(t, a)
	enterEdit(a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = a.View() // record tabRanges

	sendKey(a, 'p')
	if a.wizard == nil {
		t.Fatalf("status = %q, wizard did not open", a.status)
	}

	rng := a.tabRanges[0] // Overview
	a.Update(tea.MouseMsg{X: rng[0], Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})

	if a.tab != 2 {
		t.Fatalf("a.tab = %d, want unchanged 2 (mouse must be ignored while the wizard is open)", a.tab)
	}
	if a.wizard == nil {
		t.Fatal("wizard closed by a mouse click, want it to stay open")
	}
}

func TestEditBlockedWhenBackupDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: backup dir writability probe cannot fail")
	}

	tmp := t.TempDir()
	notADir := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := testAppDir(t, false, notADir)
	sendKey(a, 'e')

	view := a.View()
	if strings.Contains(view, "Enable edit mode") {
		t.Fatalf("View() = %q, should not offer confirm when backup dir is unwritable", view)
	}
	if !strings.Contains(view, "READ ONLY") {
		t.Fatalf("View() = %q, want it to stay READ ONLY", view)
	}
	if !strings.Contains(view, "backup dir not writable") {
		t.Fatalf("View() = %q, want the writability error in status", view)
	}
}

func TestEditBackToReadOnlyBlockedWithPending(t *testing.T) {
	a := testApp(t, false)
	sendKey(a, 'e')
	sendKey(a, 'y')
	a.queue.Add(model.PendingOp{VM: "plain-vm", Summary: "pin"})

	sendKey(a, 'e')
	view := a.View()
	if !strings.Contains(view, "EDIT") {
		t.Fatalf("View() = %q, want to stay in EDIT mode with pending ops", view)
	}
	if !strings.Contains(view, "discard or apply pending ops first") {
		t.Fatalf("View() = %q, want the blocking reason in status", view)
	}
}

func TestViewBeforeScanDoesNotPanic(t *testing.T) {
	a := testApp(t, false)
	_ = a.View()
}

func TestOverviewUsesProjectedSnapshot(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)

	// plain-vm scans with no pins and no membind, so node 0 starts with
	// nothing bound to it. Staging a pin+membind op must show up in the
	// Overview body, proving it renders model.Project's output rather than
	// the raw scanned snapshot.
	a.queue.Add(model.PendingOp{
		Kind:    model.OpPin,
		VM:      "plain-vm",
		Pins:    map[int][]int{0: {0}},
		MemNode: 0,
	})

	view := a.View()
	if !strings.Contains(view, "1000.0K/1000.0K") {
		t.Fatalf("View() = %q, want the staged op's membind reflected in node 0's memory bar (bound/total)", view)
	}
}

func TestHeaderFitsWidthAndEndsWithBadge(t *testing.T) {
	a := testApp(t, false)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	lines := strings.Split(a.View(), "\n")
	header := lines[1]
	if w := lipgloss.Width(header); w > 80 {
		t.Fatalf("header width = %d, want <= 80: %q", w, header)
	}
	if !strings.HasSuffix(strings.TrimRight(header, " "), "READ ONLY") {
		t.Fatalf("header = %q, want it to end with the badge", header)
	}
}

func TestStatusBarApplyDiscardVisibility(t *testing.T) {
	a := testApp(t, false)

	if view := a.View(); strings.Contains(view, "[a] apply") || strings.Contains(view, "[d] discard") {
		t.Fatalf("View() in read-only mode = %q, want no apply/discard hints", view)
	}

	sendKey(a, 'e')
	sendKey(a, 'y')
	if view := a.View(); strings.Contains(view, "[a] apply") || strings.Contains(view, "[d] discard") {
		t.Fatalf("View() in edit mode with an empty queue = %q, want no apply/discard hints", view)
	}

	a.queue.Add(model.PendingOp{VM: "plain-vm", Summary: "pin"})
	view := a.View()
	if !strings.Contains(view, "[a] apply") {
		t.Fatalf("View() in edit mode with a pending op = %q, want the apply hint on any tab", view)
	}
	if strings.Contains(view, "[d] discard") {
		t.Fatalf("View() on a non-Pending tab = %q, want no discard hint ('d' is only routed on the Pending tab)", view)
	}

	a.tab = 3
	view = a.View()
	if !strings.Contains(view, "[d] discard") {
		t.Fatalf("View() on the Pending tab with a pending op = %q, want the discard hint", view)
	}
}

// TestViewWrapsLongStatusToWidth covers the fix for text running off the
// right side of the terminal: a long single-line status message (e.g. a
// libvirt error) must be wrapped to a.width rather than left to overflow,
// and the tail of the message must still be reachable (not truncated away).
func TestViewWrapsLongStatusToWidth(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.Update(tea.WindowSizeMsg{Width: 40, Height: 24})

	tail := "ZZZFINALTAILMARKERZZZ"
	a.status = "scan error: " + strings.Repeat("connection refused retrying now ", 6) + tail

	view := a.renderFull()
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Fatalf("line %d width = %d, want <= 40: %q", i, w, line)
		}
	}
	if !strings.Contains(view, tail) {
		t.Fatalf("View() = %q, want the tail of the long status message present (not truncated)", view)
	}
}

// wideCoreTopo builds a single-node topology with n two-thread cores, wide
// enough that renderCPUMap's row of core cells overflows a narrow width.
func wideCoreTopo(n int) *hostinfo.Topology {
	threads := map[int]hostinfo.Thread{}
	cores := make([]hostinfo.Core, n)
	nodeThreads := make([]int, 0, n*2)
	for i := 0; i < n; i++ {
		a, b := i*2, i*2+1
		threads[a] = hostinfo.Thread{ID: a, Core: i, Socket: 0, Node: 0, Sibling: b}
		threads[b] = hostinfo.Thread{ID: b, Core: i, Socket: 0, Node: 0, Sibling: a}
		cores[i] = hostinfo.Core{Socket: 0, ID: i, Node: 0, Threads: []int{a, b}}
		nodeThreads = append(nodeThreads, a, b)
	}
	nodes := []hostinfo.Node{{ID: 0, Threads: nodeThreads, MemTotalKiB: 1000}}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads}
}

// manyNodeManyCoresTopo builds a numNodes-node topology with coresPerNode
// single-thread cores each (node 0's cores get the lower global core
// indices, node 1's the next block, and so on), wide/tall enough per node
// that its CPU Map panel truncates the grid horizontally and/or vertically.
func manyNodeManyCoresTopo(numNodes, coresPerNode int) *hostinfo.Topology {
	threads := make(map[int]hostinfo.Thread, numNodes*coresPerNode)
	var cores []hostinfo.Core
	nodes := make([]hostinfo.Node, numNodes)
	for n := 0; n < numNodes; n++ {
		nodeThreads := make([]int, 0, coresPerNode)
		for i := 0; i < coresPerNode; i++ {
			id := n*coresPerNode + i
			threads[id] = hostinfo.Thread{ID: id, Core: i, Socket: n, Node: n, Sibling: -1}
			cores = append(cores, hostinfo.Core{Socket: n, ID: i, Node: n, Threads: []int{id}})
			nodeThreads = append(nodeThreads, id)
		}
		nodes[n] = hostinfo.Node{ID: n, Threads: nodeThreads, MemTotalKiB: 1000}
	}
	return &hostinfo.Topology{Nodes: nodes, Cores: cores, Threads: threads}
}

// manyNodesTopo builds a topology with n NUMA nodes, one thread each (no
// cores -- the Overview tab doesn't touch Topo.Cores).
func manyNodesTopo(n int) *hostinfo.Topology {
	threads := make(map[int]hostinfo.Thread, n)
	nodes := make([]hostinfo.Node, n)
	for i := 0; i < n; i++ {
		threads[i] = hostinfo.Thread{ID: i, Core: 0, Socket: i, Node: i, Sibling: -1}
		nodes[i] = hostinfo.Node{ID: i, Threads: []int{i}, MemTotalKiB: 1000}
	}
	return &hostinfo.Topology{Nodes: nodes, Threads: threads}
}

// TestOverviewHeightBudgetKeepsKeyBarVisible covers the fix for
// renderOverviewTab never receiving a height budget: 8 NUMA node cards at
// height 24 must still fit, with the key bar surviving (not pushed out by
// clampHeight's last-resort truncation).
func TestOverviewHeightBudgetKeepsKeyBarVisible(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodesTopo(8), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 0}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 24 {
		t.Fatalf("View() has %d lines, want <= 24: %q", len(lines), view)
	}
	if !strings.Contains(view, "[q] quit") {
		t.Fatalf("View() = %q, want the key bar (\"[q] quit\") still visible", view)
	}
}

// TestCPUMapClickDoesNotCrossPanelBoundary covers the fix for
// clipHitsToWindow only clipping by row: panelH truncates an over-wide
// node grid horizontally, but the hits recorded for the cores past that
// truncation point used to survive anyway -- overlapping the *next*
// node's panel once offset by its cumulative x -- so a click on node 1's
// first core could be swallowed by one of node 0's out-of-view hits
// (checked first, since node 0's panel is built first).
func TestCPUMapClickDoesNotCrossPanelBoundary(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodeManyCoresTopo(2, 40), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 1}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "core", 40) // node 1's first core (global index 40)
	a.Update(press(h.x0, h.y0))
	if a.cursor != 40 {
		t.Fatalf("cursor = %d after clicking node 1's first core, want 40 (a node-0 hit swallowed the click)", a.cursor)
	}
}

// TestCPUMapStackedNodesHeightBudgetKeepsKeyBarVisible covers the fix for
// each stacked CPU Map node panel independently claiming the whole
// primary budget (rather than the budget being divided across them): 4
// nodes with enough cores each to need several grid rows, at width 80
// height 24 (narrow enough that both the outer CPU-map-vs-detail split
// and the per-node panels stack), must still fit, with the key bar
// surviving.
func TestCPUMapStackedNodesHeightBudgetKeepsKeyBarVisible(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodeManyCoresTopo(4, 200), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 1}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 24 {
		t.Fatalf("View() has %d lines, want <= 24: %q", len(lines), view)
	}
	if !strings.Contains(view, "[q] quit") {
		t.Fatalf("View() = %q, want the key bar (\"[q] quit\") still visible", view)
	}
}

// TestCPUMapNodePanelsWindowAroundCursor covers the fix for stacked CPU
// Map node panels always showing nodes 0..N-1: with 8 single-core nodes
// at width 80 height 24 (narrow enough that only ~5 node panels fit), the
// cursor can still be moved onto a core belonging to node 7 -- which must
// then render (windowing the panel list around the cursor's node, not
// just the first few), with a hit recorded for it.
func TestCPUMapNodePanelsWindowAroundCursor(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodeManyCoresTopo(8, 1), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 1, cursor: 7}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := a.View()
	if !strings.Contains(view, "node 7") {
		t.Fatalf("View() = %q, want node 7's panel rendered (cursor is on one of its cores)", view)
	}
	findHit(t, a, "core", 7) // fails the test itself if no such hit was recorded
}

// TestCPUMapFirstNodePanelClampedToBudget covers the fix for the stacked
// branch handing every panel its own natural height regardless of how
// much budget is actually left: 4 nodes x 200 cores at width 80 height 10
// -- so tight that even the *first* node panel's natural height alone
// exceeds the whole budget -- must still leave the key bar visible.
func TestCPUMapFirstNodePanelClampedToBudget(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodeManyCoresTopo(4, 200), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 1}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 10})

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 10 {
		t.Fatalf("View() has %d lines, want <= 10: %q", len(lines), view)
	}
	if !strings.Contains(view, "[q] quit") {
		t.Fatalf("View() = %q, want the key bar (\"[q] quit\") still visible", view)
	}
}

// TestOverviewFirstCardClampedToBudget is TestCPUMapFirstNodePanelClamped-
// ToBudget's Overview-tab counterpart.
func TestOverviewFirstCardClampedToBudget(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodesTopo(4), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 0}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 6})

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 6 {
		t.Fatalf("View() has %d lines, want <= 6: %q", len(lines), view)
	}
	if !strings.Contains(view, "[q] quit") {
		t.Fatalf("View() = %q, want the key bar (\"[q] quit\") still visible", view)
	}
}

// TestOverviewScrollRevealsHiddenNodes covers the Overview scroll minor:
// with more NUMA node cards than fit stacked, a "+N more nodes (scroll)"
// line is shown, and up/down on the Overview tab moves a.overviewScroll so
// every node becomes reachable.
func TestOverviewScrollRevealsHiddenNodes(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodesTopo(8), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 0}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := a.View()
	if strings.Contains(view, "node 7") {
		t.Fatalf("View() = %q, want node 7 NOT yet visible (only the first few cards fit)", view)
	}
	if !strings.Contains(view, "more nodes (scroll)") {
		t.Fatalf("View() = %q, want a \"+N more nodes (scroll)\" line", view)
	}

	for i := 0; i < 7; i++ {
		sendKeyType(a, tea.KeyDown)
	}
	view = a.View()
	if !strings.Contains(view, "node 7") {
		t.Fatalf("View() after scrolling down = %q, want node 7 visible", view)
	}
}

// TestOverviewScrollStaysAtZeroWhenEverythingFits covers the fix for
// clampOverviewScroll clamping to len(Nodes)-1 regardless of how many
// cards actually fit: with only 2 small node cards at 80x40 (both comfortably
// fit already, side by side isn't triggered at this width), pressing down
// must not scroll node 0 out of view and manufacture a "+1 more nodes"
// footer out of an already-complete view.
func TestOverviewScrollStaysAtZeroWhenEverythingFits(t *testing.T) {
	s := &model.Snapshot{Topo: manyNodesTopo(2), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 0}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	sendKeyType(a, tea.KeyDown)
	if a.overviewScroll != 0 {
		t.Fatalf("overviewScroll = %d after pressing down with everything already visible, want 0", a.overviewScroll)
	}
	if view := a.View(); strings.Contains(view, "more nodes") {
		t.Fatalf("View() = %q, want no \"more nodes\" footer -- both cards already fit", view)
	}
}

// TestConfirmDialogWrapsPromptBeforeClipping covers the fix for the
// confirm dialog clipping the prompt's raw (unwrapped) lines before
// wrapping it: at a narrow width, a long single-line prompt wraps to
// several visual lines only *after* word-wrap, so clipping by raw line
// count could blow right past the dialog's own budget. The confirm dialog
// is built via panelWrapH (wrap, then clip), which must honor budget
// exactly.
func TestConfirmDialogWrapsPromptBeforeClipping(t *testing.T) {
	a := testApp(t, false)
	a.confirm = &confirm{prompt: "Discard 3 pending ops and quit? [y/n]"}
	panel, _, ok := a.renderDialog(40, 4)
	if !ok {
		t.Fatal("renderDialog() ok = false, want true with a.confirm set")
	}
	if n := strings.Count(panel, "\n") + 1; n > 4 {
		t.Fatalf("renderDialog() panel has %d lines, want <= 4: %q", n, panel)
	}
}

// TestViewWrapsWideCPUMapToWidth covers the same fix on the CPU Map tab: a
// row of many two-glyph core cells is far wider than a narrow terminal, and
// must not be left to overflow it.
func TestViewWrapsWideCPUMapToWidth(t *testing.T) {
	s := &model.Snapshot{Topo: wideCoreTopo(20), Use: map[int]model.ThreadUse{}, BoundMemKiB: map[int]uint64{}}
	a := &App{hv: &libvirtio.Fake{ConnURI: "test:///x"}, snap: s, doms: map[string]*libvirtio.DomainConfig{}, tab: 1}
	a.Update(tea.WindowSizeMsg{Width: 40, Height: 24})

	for i, line := range strings.Split(a.renderFull(), "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Fatalf("line %d width = %d, want <= 40: %q", i, w, line)
		}
	}
}

// TestViewNoWrapBeforeWindowSize covers width 0 (before the first
// WindowSizeMsg): wrapping must be a no-op rather than collapsing content to
// zero width.
func TestViewNoWrapBeforeWindowSize(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.status = "some status"

	view := a.View()
	if !strings.Contains(view, "some status") {
		t.Fatalf("View() with width 0 = %q, want status text present, unwrapped", view)
	}
}

// findHit returns the recorded hit of the given kind/index, or fails the
// test if none was recorded (a.View() must have been called first, since
// hits are only refreshed on render).
func findHit(t *testing.T, a *App, kind string, index int) hit {
	t.Helper()
	for _, h := range a.hits {
		if h.kind == kind && h.index == index {
			return h
		}
	}
	t.Fatalf("no %s hit recorded for index %d (hits = %+v)", kind, index, a.hits)
	return hit{}
}

func press(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

// TestMouseClickSelectsVMRow covers the fix for mouse clicks doing nothing
// beyond the tab bar: a left click on the second VM row must select it.
func TestMouseClickSelectsVMRow(t *testing.T) {
	a, _ := pendingFakeAppMulti(t, map[string]string{"vm1": vm1XML, "vm2": vm2XML})
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "vm", 1)
	a.Update(press(h.x0, h.y0))
	if a.vmSel != 1 {
		t.Fatalf("vmSel = %d, want 1", a.vmSel)
	}
}

// TestMouseClickSelectsCPUMapCore covers a click on a CPU Map core cell
// moving the cursor to that core.
func TestMouseClickSelectsCPUMapCore(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.tab = 1
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "core", 1)
	a.Update(press(h.x0, h.y0))
	if a.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", a.cursor)
	}
}

// TestMouseWheelMovesVMSel covers wheel up/down acting as the up/down key
// on the VMs tab's active selection.
func TestMouseWheelMovesVMSel(t *testing.T) {
	a, _ := pendingFakeAppMulti(t, map[string]string{"vm1": vm1XML, "vm2": vm2XML})
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.View()

	a.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if a.vmSel != 1 {
		t.Fatalf("vmSel = %d after wheel down, want 1", a.vmSel)
	}
	a.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if a.vmSel != 0 {
		t.Fatalf("vmSel = %d after wheel up, want 0", a.vmSel)
	}
}

// TestMouseIgnoredDuringApplyFlow mirrors TestMouseIgnoredWhileWizardOpen
// for the apply flow: a tab-bar click while a flow screen is open must not
// switch tabs out from under it.
func TestMouseIgnoredDuringApplyFlow(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	enterEdit(a)
	a.queue.Add(model.PendingOp{VM: "plain-vm", Summary: "pin"})
	a.tab = 3
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = a.View()

	sendKey(a, 'a')
	if a.flow == nil {
		t.Fatal("apply flow did not open")
	}

	rng := a.tabRanges[0]
	a.Update(tea.MouseMsg{X: rng[0], Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if a.tab != 3 {
		t.Fatalf("a.tab = %d, want unchanged 3 (mouse must be ignored while the apply flow is open)", a.tab)
	}
	if a.flow == nil {
		t.Fatal("flow closed by a mouse click, want it to stay open")
	}
}

// TestVMsTableFitsNarrowWidth covers the reported regression: at a narrow
// width, VMs table rows must stay single lines (never fold, e.g. FLAGS
// header alone on one line and "[!]" alone on the next) and every rendered
// line must still fit the terminal.
func TestVMsTableFitsNarrowWidth(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 60, Height: 24})

	view := a.renderFull()
	for i, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Fatalf("line %d width = %d, want <= 60: %q", i, w, line)
		}
	}
	if !strings.Contains(view, "plain-vm") {
		t.Fatalf("View() = %q, want the VM name to still appear intact", view)
	}
}

// TestVMsTabTwoColumnAtWideWidth covers the two-column gating: at width
// 140 (>= twoColThreshold, and the table's low-priority columns dropped
// would comfortably fit that primary panel even though the full table
// wouldn't), the VMs tab must render the table and detail panels side by
// side (both panel titles land on the same top-border line), not stacked.
func TestVMsTabTwoColumnAtWideWidth(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 24})

	lines := strings.Split(a.View(), "\n")
	if len(lines) <= bodyY0 {
		t.Fatalf("View() has only %d lines, want more than bodyY0=%d", len(lines), bodyY0)
	}
	top := lines[bodyY0]
	if !strings.Contains(top, "VMs") || !strings.Contains(top, "plain-vm") {
		t.Fatalf("top border line = %q, want both the table panel's \"VMs\" title and the detail panel's \"plain-vm\" title on the same line (side by side)", top)
	}
}

// TestVMsTabStacksJustBelowThreshold covers the other side of that gate:
// at width 119 (just below twoColThreshold), the VMs tab must stay
// stacked at full width, showing every column (there's no primary/
// secondary split at all to make dropping columns necessary).
func TestVMsTabStacksJustBelowThreshold(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 119, Height: 24})

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) <= bodyY0 {
		t.Fatalf("View() has only %d lines, want more than bodyY0=%d", len(lines), bodyY0)
	}
	top := lines[bodyY0]
	if strings.Contains(top, "plain-vm") {
		t.Fatalf("top border line = %q, want the table stacked (not the detail panel's title on the same line)", top)
	}
	for _, col := range []string{"NAME", "STATE", "VCPUS", "MEM", "PINS", "MEMNODE", "GPUNODE", "FLAGS"} {
		if !strings.Contains(view, col) {
			t.Fatalf("View() at width 119 = %q, want column %q still shown", view, col)
		}
	}
}

// manyVMXMLs returns n distinct plain (unpinned) VM domain XMLs, named
// vm0..vm(n-1), for tests that need a VM list long enough to overflow a
// short terminal.
func manyVMXMLs(n int) map[string]string {
	xmls := make(map[string]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("vm%02d", i)
		xmls[name] = fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f%04d</uuid>
  <memory unit='KiB'>1000</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`, name, i)
	}
	return xmls
}

// TestVMsBodyFillsHeightBudget covers the fix for "TUI squished to the
// top": before, a body panel shorter than its budget (e.g. 5 VMs' worth of
// table + detail rows, well under a 40-row terminal) left a large blank
// gap between it and the key bar, wherever a terminal's actual height
// happened to exceed the content's own natural size. At 120x40, View()
// must be exactly 40 lines, its last line the key bar, the line above it
// either the status line or (blank status, as here) directly the VMs
// panel's own bottom border -- i.e. the panel stretches (via panelH's
// fill option) all the way down to meet the status/key-bar row, with no
// gap in between.
func TestVMsBodyFillsHeightBudget(t *testing.T) {
	a := wizardTestApp(t, manyVMXMLs(5), noNode)
	runScan(t, a)
	a.tab = 2
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 40 {
		t.Fatalf("View() has %d lines, want exactly 40: %q", len(lines), view)
	}
	if !strings.Contains(lines[39], "[q] quit") {
		t.Fatalf("last line = %q, want the key bar", lines[39])
	}
	if a.status != "" {
		t.Fatalf("a.status = %q, want empty for this scenario", a.status)
	}
	if !strings.Contains(lines[38], "?") { // '?', the panel's bottom-left corner
		t.Fatalf("line above the key bar = %q, want the VMs panel's own bottom border directly above it (status is blank here)", lines[38])
	}
}

// rowOf returns the index of the first line of view containing marker, or
// fails the test if there is none.
func rowOf(t *testing.T, view, marker string) int {
	t.Helper()
	for i, l := range strings.Split(view, "\n") {
		if strings.Contains(l, marker) {
			return i
		}
	}
	t.Fatalf("view has no line containing %q: %q", marker, view)
	return -1
}

// TestConfirmOverlayCentersWithoutShiftingBody covers the dialogs-as-
// overlay fix: a modal used to replace the tab body outright, which (with
// many rows) shifted its own scroll position around depending on how
// much room the modal itself needed. With 40 VMs (vmSel 0) at 120x40,
// opening the confirm must now leave the VMs table's own layout/scroll
// completely alone -- vm00's row stays at the same screen y -- while the
// confirm dialog itself is composited on top, horizontally centered
// within the body (its content starts about (width - dialogWidth)/2
// columns in, via overlay/centerXY), and the key bar remains the
// terminal's last line throughout.
func TestConfirmOverlayCentersWithoutShiftingBody(t *testing.T) {
	a := wizardTestApp(t, manyVMXMLs(40), noNode)
	runScan(t, a)
	a.tab = 2
	a.vmSel = 0
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	before := rowOf(t, a.View(), "vm00")

	sendKey(a, 'e')
	if a.confirm == nil {
		t.Fatal("confirm modal did not open")
	}
	view := a.View()
	lines := strings.Split(view, "\n")

	if after := rowOf(t, view, "vm00"); after != before {
		t.Fatalf("vm00's row = %d after opening the confirm, want unchanged from %d (the body's own layout/scroll must not shift while a dialog is open)", after, before)
	}
	if !strings.Contains(lines[len(lines)-1], "[q] quit") {
		t.Fatalf("last line = %q, want the key bar", lines[len(lines)-1])
	}

	row := rowOf(t, view, "? Confirm") // '? Confirm', the dialog's own titled top border
	idx := strings.Index(lines[row], "? Confirm")
	leading := lipgloss.Width(lines[row][:idx])
	dw := dialogWidth(120)
	wantX := (120 - dw) / 2
	if leading < wantX-1 || leading > wantX+1 {
		t.Fatalf("dialog row's leading width = %d, want about %d (= (120-dialogWidth(120))/2 = (120-%d)/2)", leading, wantX, dw)
	}
}

// TestVMsHeightBudgetKeepsSelectionVisibleAndClickable covers the fix for
// bubbletea silently dropping lines off the *top* of a taller-than-
// terminal view (which shifted the tab row off row 0 and threw every
// recorded mouse-hit y off by the overflow amount): with 40 VMs at height
// 24, the whole View() must fit in 24 lines, row 0 must still be the tab
// row, and selecting the last VM must scroll it into view -- with a click
// on its actual on-screen row selecting it.
func TestVMsHeightBudgetKeepsSelectionVisibleAndClickable(t *testing.T) {
	a := wizardTestApp(t, manyVMXMLs(40), noNode)
	runScan(t, a)
	a.tab = 2
	a.vmSel = 39
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 24 {
		t.Fatalf("View() has %d lines, want <= 24 (height budget): %q", len(lines), view)
	}
	if !strings.Contains(lines[0], "Overview") {
		t.Fatalf("lines[0] = %q, want the tab row (bubbletea drops overflow from the top, so this must stay row 0)", lines[0])
	}

	h := findHit(t, a, "vm", 39)
	a.Update(press(h.x0, h.y0))
	if a.vmSel != 39 {
		t.Fatalf("vmSel = %d after clicking vm 39's on-screen row, want 39 (hit y must match the actual scrolled position)", a.vmSel)
	}
}

// TestBackupsDiffHeightBudgetAndWheelScroll covers the same fix for the
// Backups tab's diff view: a large diff must fit the height budget, and
// the mouse wheel must scroll it.
func TestBackupsDiffHeightBudgetAndWheelScroll(t *testing.T) {
	a := testApp(t, false)
	bigXML := plainVMXML + "\n" + strings.Repeat("  <!-- padding line -->\n", 300)
	if _, err := backup.Save(a.backupDir, "plain-vm", "pin", "test", bigXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	a.tab = 4
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	sendKeyType(a, tea.KeyEnter)
	if a.diffView == "" {
		t.Fatal("diff view did not open")
	}

	view := a.View()
	lines := strings.Split(view, "\n")
	if len(lines) > 24 {
		t.Fatalf("View() has %d lines, want <= 24 (height budget): %q", len(lines), view)
	}

	before := a.diffScroll
	a.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if a.diffScroll <= before {
		t.Fatalf("diffScroll = %d after wheel down, want > %d", a.diffScroll, before)
	}
}

// TestBackupsDiffScrollClampsAtInputTime covers the fix for diffScroll
// only being clamped at render time: over-scrolling far past the end (many
// wheel-downs) used to leave a.diffScroll at a huge, unclamped value, so a
// single wheel-up afterward changed nothing visible (render always clamped
// it to the same true max) until enough presses "caught up". diffScroll
// must now be clamped (written back) immediately, so it never overshoots
// in the first place and every wheel-up moves it.
func TestBackupsDiffScrollClampsAtInputTime(t *testing.T) {
	a := testApp(t, false)
	bigXML := plainVMXML + "\n" + strings.Repeat("  <!-- padding line -->\n", 300)
	if _, err := backup.Save(a.backupDir, "plain-vm", "pin", "test", bigXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	a.tab = 4
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	sendKeyType(a, tea.KeyEnter)
	if a.diffView == "" {
		t.Fatal("diff view did not open")
	}

	for i := 0; i < 500; i++ {
		a.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	}
	overshot := a.diffScroll
	if overshot > 400 {
		t.Fatalf("diffScroll = %d after 500 wheel-downs on a ~300-line diff, want it clamped near the true end, not left to grow unbounded", overshot)
	}

	a.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if a.diffScroll >= overshot {
		t.Fatalf("diffScroll = %d after one wheel-up, want it to have decreased from %d", a.diffScroll, overshot)
	}
}

// TestMouseClickInDetailPanelDoesNotChangeSelection covers the fix for row
// hits that used to span the whole line width regardless of panel bounds:
// in the VMs tab's side-by-side layout, a click that lands inside the
// *detail* panel (to the right of the table) must not be mistaken for a
// click on whatever table row happens to share that y. (A previous version
// of this test clicked the header row at a width where the layout was
// actually stacked -- vacuously true regardless of the fix, since the
// header has no hit and x didn't matter. This one targets a real VM row's
// y, at a width that genuinely goes side by side, and clicks well past
// that row's own hit into the detail panel beside it.)
func TestMouseClickInDetailPanelDoesNotChangeSelection(t *testing.T) {
	a, _ := pendingFakeAppMulti(t, map[string]string{"plain-vm": plainVMXML, "vm1": vm1XML})
	runScan(t, a)
	a.tab = 2
	a.vmSel = 0
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "vm", 1) // the second VM row
	a.Update(press(h.x1+20, h.y0))
	if a.vmSel != 0 {
		t.Fatalf("vmSel = %d after a click well past the row's own hit (into the detail panel), want unchanged 0", a.vmSel)
	}
}

// TestSmokeRenderAllTabs is not a correctness check: it renders every tab
// at width 120 and logs each via t.Log (run with -v) so the rendered
// output can be inspected directly, e.g. for a visual review.
func TestSmokeRenderAllTabs(t *testing.T) {
	a := wizardTestApp(t, map[string]string{
		"pinned-vm":     pinnedNode0XML,
		"overcommit-vm": overcommitNode0XML,
		"plain-vm":      plainVMXML,
	}, noNode)
	runScan(t, a)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	enterEdit(a)
	a.vmSel = vmIndex(t, a, "plain-vm")
	a.queue.Add(model.PendingOp{
		VM:         "plain-vm",
		StagedHash: "abcdef1234567890",
		Summary:    "plain-vm: pin 2 vcpus -> node 0 threads 0,1; memory -> node 0 (strict)",
	})

	for i, name := range tabNames {
		a.tab = i
		t.Logf("=== tab %d (%s) ===\n%s", i, name, a.View())
	}
}

// TestSmokeRenderFixRound1 is not a correctness check: it renders Overview,
// the VMs tab (with 40 VMs, to show the height-budget scrolling fix), and
// the Backups tab's diff view (a large diff, same reason) at width 120,
// height 24, logging each via t.Log (run with -v) for the fix-round-1
// report.
func TestSmokeRenderFixRound1(t *testing.T) {
	a := wizardTestApp(t, manyVMXMLs(40), noNode)
	runScan(t, a)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	enterEdit(a)
	a.vmSel = 39

	a.tab = 0
	t.Logf("=== Overview (width 120, height 24) ===\n%s", a.View())

	a.tab = 2
	t.Logf("=== VMs, 40 VMs, vmSel=39 (width 120, height 24) ===\n%s", a.View())

	bigXML := plainVMXML + "\n" + strings.Repeat("  <!-- padding line -->\n", 300)
	if _, err := backup.Save(a.backupDir, "vm00", "pin", "test", bigXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	a.tab = 4
	sendKeyType(a, tea.KeyEnter)
	if a.diffView == "" {
		t.Fatal("diff view did not open")
	}
	t.Logf("=== Backups diff view, 300-line diff (width 120, height 24) ===\n%s", a.View())
}

func TestRescanSetsStatus(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)

	cmd := sendKey(a, 'r')
	if cmd == nil {
		t.Fatal("Update('r') returned nil Cmd, want a rescan Cmd")
	}
	if !strings.Contains(a.View(), "rescanning...") {
		t.Fatalf("View() after 'r' = %q, want rescanning status", a.View())
	}
}
