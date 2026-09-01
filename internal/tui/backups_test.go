package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

func TestDiffLines(t *testing.T) {
	a := "one\ntwo\nthree"
	b := "one\nthree\nfour"
	want := "  one\n- two\n  three\n+ four"
	if got := diffLines(a, b); got != want {
		t.Fatalf("diffLines(a, b) = %q, want %q", got, want)
	}
}

func TestDiffLinesIdentical(t *testing.T) {
	same := "a\nb\nc"
	got := diffLines(same, same)
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("diffLines(same, same) = %q, want every line marked common", got)
		}
	}
}

func TestRenderBackupsLists(t *testing.T) {
	a := testApp(t, false)

	empty := a.renderBackups(0, 80)
	if !strings.Contains(empty, "no backups") {
		t.Fatalf("renderBackups on empty dir = %q, want it to mention no backups", empty)
	}

	if _, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", plainVMXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}

	got := a.renderBackups(0, 80)
	if !strings.Contains(got, "plain-vm") {
		t.Fatalf("renderBackups = %q, want it to contain the VM name", got)
	}
	if !strings.Contains(got, "pin 2 vcpus -> node 0") {
		t.Fatalf("renderBackups = %q, want it to contain the op description", got)
	}
}

func TestRestoreStagesOp(t *testing.T) {
	a := testApp(t, false)
	a.editMode = true

	entry, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", plainVMXML)
	if err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	savedXML, err := backup.LoadXML(entry)
	if err != nil {
		t.Fatalf("backup.LoadXML: %v", err)
	}

	status := a.stageRestore(0)
	if !strings.Contains(status, "staged:") {
		t.Fatalf("stageRestore = %q, want a staged status", status)
	}
	if a.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", a.queue.Len())
	}

	op := a.queue.Ops[0]
	if op.Kind != model.OpRestore {
		t.Fatalf("op.Kind = %v, want OpRestore", op.Kind)
	}
	if op.BackupXML != savedXML {
		t.Fatalf("op.BackupXML = %q, want the saved backup xml", op.BackupXML)
	}
	if op.StagedHash != model.HashXML(plainVMXML) {
		t.Fatalf("op.StagedHash = %q, want hash of the fake's current xml", op.StagedHash)
	}
}

func TestBackupsDiff(t *testing.T) {
	a := testApp(t, false)

	if _, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", "<domain><name>plain-vm</name><old/></domain>"); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}

	diff, err := a.backupsDiff(0)
	if err != nil {
		t.Fatalf("backupsDiff: %v", err)
	}
	if !strings.Contains(diff, "plain-vm") {
		t.Fatalf("backupsDiff = %q, want a header naming the VM", diff)
	}
	if !strings.Contains(diff, "- ") || !strings.Contains(diff, "+ ") {
		t.Fatalf("backupsDiff = %q, want both removed and added lines", diff)
	}
}

func TestHandleBackupsKey(t *testing.T) {
	a := testApp(t, false)
	sel := 0

	if !a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyDown}, &sel, 3) {
		t.Fatalf("handleBackupsKey(down) = false, want true")
	}
	if sel != 1 {
		t.Fatalf("sel after down = %d, want 1", sel)
	}

	if !a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyDown}, &sel, 3) {
		t.Fatalf("handleBackupsKey(down) = false, want true")
	}
	if !a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyDown}, &sel, 3) {
		t.Fatalf("handleBackupsKey(down) = false, want true")
	}
	if sel != 2 {
		t.Fatalf("sel clamped at end = %d, want 2 (n-1)", sel)
	}

	if !a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyUp}, &sel, 3) {
		t.Fatalf("handleBackupsKey(up) = false, want true")
	}
	if sel != 1 {
		t.Fatalf("sel after up = %d, want 1", sel)
	}

	if !a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, &sel, 3) {
		t.Fatalf("handleBackupsKey('k') = false, want true")
	}
	if sel != 0 {
		t.Fatalf("sel after 'k' = %d, want 0", sel)
	}
	if !a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}, &sel, 3) {
		t.Fatalf("handleBackupsKey('k') at 0 = false, want true (still handled)")
	}
	if sel != 0 {
		t.Fatalf("sel clamped at start = %d, want 0", sel)
	}

	if a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}, &sel, 3) {
		t.Fatalf("handleBackupsKey('x') = true, want false (unhandled)")
	}

	sel = 5
	if !a.handleBackupsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, &sel, 0) {
		t.Fatalf("handleBackupsKey('j') with n=0 = false, want true")
	}
	if sel != 0 {
		t.Fatalf("sel with n=0 = %d, want 0", sel)
	}
}

func TestRestoreRefusedReadOnly(t *testing.T) {
	a := testApp(t, false) // editMode stays false

	if _, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", plainVMXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}

	status := a.stageRestore(0)
	if !strings.Contains(status, "press e to enter edit mode first") {
		t.Fatalf("stageRestore = %q, want the edit-mode hint", status)
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d, want 0", a.queue.Len())
	}
}
