package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// distinctBackupXML differs from plainVMXML by exactly one line (memory
// size), so tests can tell the two apart instead of asserting a tautology
// against identical inputs.
var distinctBackupXML = strings.Replace(plainVMXML,
	"<memory unit='KiB'>1000</memory>", "<memory unit='KiB'>2000</memory>", 1)

// TestBackupsTableCapsOperationThenDropsSeconds covers the Backups table's
// own width-awareness (mirroring the VMs table): OPERATION truncates with
// ".." past backupOpCap, and once that alone still doesn't fit, TIME drops
// its seconds.
func TestBackupsTableCapsOperationThenDropsSeconds(t *testing.T) {
	longOp := strings.Repeat("pin many vcpus across every numa node ", 3)
	entries := []backup.Entry{{Meta: backup.Meta{VM: "vm", Op: longOp}}}

	out, _ := backupsTable(entries, 0, 0, 54)
	if !strings.Contains(out, "..") {
		t.Fatalf("backupsTable() = %q, want the long OPERATION truncated with \"..\"", out)
	}
	// A zero-value time formats as "0001-01-01 00:00:00Z" with the full
	// format (seconds included); once dropped it reads "...00:00Z".
	if !strings.Contains(out, "0001-01-01 00:00Z") {
		t.Fatalf("backupsTable() = %q, want TIME's seconds dropped once capping OPERATION alone still doesn't fit width 60", out)
	}
}

func TestDiffLines(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want string
	}{
		{"identical", "a\nb\nc", "a\nb\nc", "  a\n  b\n  c"},
		{"mixed removal and addition", "one\ntwo\nthree", "one\nthree\nfour", "  one\n- two\n  three\n+ four"},
		{"empty/empty", "", "", ""},
		{"empty/nonempty", "", "x\ny", "+ x\n+ y"},
		{"nonempty/empty", "x\ny", "", "- x\n- y"},
		{"fully disjoint", "a\nb", "c\nd", "- a\n- b\n+ c\n+ d"},
		{"trailing newline on both", "a\nb\n", "a\nb\n", "  a\n  b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffLines(tc.a, tc.b); got != tc.want {
				t.Fatalf("diffLines(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestRenderBackupsLists(t *testing.T) {
	a := testApp(t, false)

	empty, _ := a.renderBackupsTab(0, 80, 24)
	if !strings.Contains(empty, "no backups") {
		t.Fatalf("renderBackupsTab on empty dir = %q, want it to mention no backups", empty)
	}

	if _, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", plainVMXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}

	got, _ := a.renderBackupsTab(0, 80, 24)
	if !strings.Contains(got, "plain-vm") {
		t.Fatalf("renderBackupsTab = %q, want it to contain the VM name", got)
	}
	if !strings.Contains(got, "pin 2 vcpus -> node 0") {
		t.Fatalf("renderBackupsTab = %q, want it to contain the op description", got)
	}
}

func TestRestoreStagesOp(t *testing.T) {
	a := testApp(t, false)
	a.editMode = true

	entry, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", distinctBackupXML)
	if err != nil {
		t.Fatalf("backup.Save: %v", err)
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
	if op.BackupXML != distinctBackupXML {
		t.Fatalf("op.BackupXML = %q, want the saved (distinct) backup xml, not the fake's current xml", op.BackupXML)
	}
	if op.StagedHash != model.HashXML(plainVMXML) {
		t.Fatalf("op.StagedHash = %q, want hash of the fake's current xml (plainVMXML), not the backup's", op.StagedHash)
	}
	if op.MemNode != -1 {
		t.Fatalf("op.MemNode = %d, want -1", op.MemNode)
	}
	wantSummary := fmt.Sprintf("plain-vm: restore config from backup %s (pin 2 vcpus -> node 0)",
		entry.Meta.Time.Format(backupTimeFormat))
	if op.Summary != wantSummary {
		t.Fatalf("op.Summary = %q, want %q", op.Summary, wantSummary)
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

// TestRestoreRefusedUnsupportedVM covers the same view-only gate every
// other staging path in app.go applies (see wizard_test.go's
// TestUnsupportedVMRefusesPinAndStrip): a domain that fails to parse is
// Unsupported and must never be written to, even via restore.
func TestRestoreRefusedUnsupportedVM(t *testing.T) {
	a := wizardTestApp(t, map[string]string{"broken-vm": brokenXML}, noNode)
	runScan(t, a)
	a.editMode = true

	if _, err := backup.Save(a.backupDir, "broken-vm", "pin", "test", brokenXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}

	status := a.stageRestore(0)
	if !strings.Contains(status, "unsupported config, view only") {
		t.Fatalf("stageRestore = %q, want the unsupported-config refusal", status)
	}
	if a.queue.Len() != 0 {
		t.Fatalf("queue.Len() = %d after restore on unsupported VM, want 0", a.queue.Len())
	}
}

func TestBackupsDiff(t *testing.T) {
	a := testApp(t, false)

	if _, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", distinctBackupXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}

	diff, err := a.backupsDiff(0)
	if err != nil {
		t.Fatalf("backupsDiff: %v", err)
	}
	if !strings.Contains(diff, "plain-vm") {
		t.Fatalf("backupsDiff = %q, want a header naming the VM", diff)
	}
	// Only the memory line differs between the fake's current xml (a-side,
	// plainVMXML) and the backup (b-side, distinctBackupXML): current's
	// 1000 line must be removed and the backup's 2000 line added -- on the
	// correct sides. If a and b were swapped, "+ " would prefix the 1000
	// line instead, failing this.
	if !strings.Contains(diff, "- "+"  <memory unit='KiB'>1000</memory>") {
		t.Fatalf("backupsDiff = %q, want the current (1000) memory line marked removed", diff)
	}
	if !strings.Contains(diff, "+ "+"  <memory unit='KiB'>2000</memory>") {
		t.Fatalf("backupsDiff = %q, want the backup (2000) memory line marked added", diff)
	}
}

// TestMouseClickSelectsBackupRow covers a left click on a Backups row
// selecting it (backupsSel).
func TestMouseClickSelectsBackupRow(t *testing.T) {
	a := testApp(t, false)
	runScan(t, a)
	if _, err := backup.Save(a.backupDir, "plain-vm", "pin 2 vcpus -> node 0", "test", plainVMXML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	if _, err := backup.Save(a.backupDir, "vm1", "strip pins", "test", vm1XML); err != nil {
		t.Fatalf("backup.Save: %v", err)
	}
	a.tab = 4
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	_ = a.View() // record hits

	h := findHit(t, a, "backup", 1)
	a.Update(press(h.x0, h.y0))
	if a.backupsSel != 1 {
		t.Fatalf("backupsSel = %d, want 1", a.backupsSel)
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
