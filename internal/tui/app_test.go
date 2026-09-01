package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

func TestTabSwitch(t *testing.T) {
	a := testApp(t, false)

	sendKey(a, '3')
	view := a.View()
	if !strings.Contains(view, "[VMs]") {
		t.Fatalf("View() after '3' = %q, want [VMs] active marker", view)
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

func TestMouseClickSwitchesTab(t *testing.T) {
	a := testApp(t, false)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = a.View() // record tabRanges

	rng := a.tabRanges[2] // VMs
	a.Update(tea.MouseMsg{X: rng[0], Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})

	view := a.View()
	if !strings.Contains(view, "[VMs]") {
		t.Fatalf("View() after click on VMs label = %q, want [VMs] active marker", view)
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

	view := a.View()
	if !strings.Contains(view, "Overview: 1 VMs, 2 nodes") {
		t.Fatalf("View() = %q, want overview line from projected snapshot", view)
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

	if view := a.View(); strings.Contains(view, "[a]pply") || strings.Contains(view, "[d]iscard") {
		t.Fatalf("View() in read-only mode = %q, want no apply/discard hints", view)
	}

	sendKey(a, 'e')
	sendKey(a, 'y')
	if view := a.View(); strings.Contains(view, "[a]pply") || strings.Contains(view, "[d]iscard") {
		t.Fatalf("View() in edit mode with an empty queue = %q, want no apply/discard hints", view)
	}

	a.queue.Add(model.PendingOp{VM: "plain-vm", Summary: "pin"})
	view := a.View()
	if !strings.Contains(view, "[a]pply") || !strings.Contains(view, "[d]iscard") {
		t.Fatalf("View() in edit mode with a pending op = %q, want apply/discard hints", view)
	}
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
