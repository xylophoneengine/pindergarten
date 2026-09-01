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

	f.XML["plain-vm"] = plainVMXML + "\n<!-- edited behind the app's back -->"

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

	sendKey(a, 'R')
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 after 'R'", a.queue.Len())
	}
	if a.queue.Ops[0].Kind != model.OpRestore {
		t.Fatalf("op.Kind = %v, want OpRestore", a.queue.Ops[0].Kind)
	}
}
