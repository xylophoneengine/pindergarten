package tui

import (
	"fmt"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/backup"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

// backupTimeFormat renders a backup.Meta.Time (always UTC) with an explicit
// "Z" suffix so it is never mistaken for local time.
const backupTimeFormat = "2006-01-02 15:04:05Z"

// diffLines returns a line-oriented diff of a against b: lines common to
// both (found via LCS) are prefixed "  ", lines only in a are prefixed
// "- ", and lines only in b are prefixed "+ ".
//
// ponytail: classic O(n*m) LCS table, fine for domain XML (a few hundred
// lines); switch to a linear-space/Myers diff if inputs grow to thousands
// of lines.
func diffLines(a, b string) string {
	as := splitLines(a)
	bs := splitLines(b)
	n, m := len(as), len(bs)

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case as[i] == bs[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var out strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case as[i] == bs[j]:
			out.WriteString("  " + as[i] + "\n")
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out.WriteString("- " + as[i] + "\n")
			i++
		default:
			out.WriteString("+ " + bs[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		out.WriteString("- " + as[i] + "\n")
	}
	for ; j < m; j++ {
		out.WriteString("+ " + bs[j] + "\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// splitLines splits s on newlines, returning nil for an empty string (so
// diffLines("", "") produces no lines at all rather than one empty one). A
// single trailing newline is stripped first, so a "...\n" input does not
// produce a spurious trailing "" element (which would otherwise render as a
// bare "  " common line).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// renderBackups renders the Backups tab as a tabwriter table (time, VM,
// operation), one row per entry from backup.List(a.backupDir) (newest
// first). The row at sel renders reverse-video via cursorStyle, matching
// renderVMs. An empty dir renders "no backups in <dir>"; a List error
// renders as an error line. w is unused for now (no wrapping beyond
// tabwriter's own column alignment).
func (a *App) renderBackups(sel int, w int) string {
	entries, err := backup.List(a.backupDir)
	if err != nil {
		return fmt.Sprintf("error listing backups: %v", err)
	}
	if len(entries) == 0 {
		return fmt.Sprintf("no backups in %s", a.backupDir)
	}

	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tVM\tOPERATION")
	for _, e := range entries {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Meta.Time.Format(backupTimeFormat), e.Meta.VM, e.Meta.Op)
	}
	_ = tw.Flush()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	rowIdx := sel + 1 // row 0 is the header
	if rowIdx >= 1 && rowIdx < len(lines) {
		lines[rowIdx] = cursorStyle.Render(lines[rowIdx])
	}
	return strings.Join(lines, "\n")
}

// backupsEntry returns the sel-th entry from backup.List(a.backupDir),
// shared by backupsDiff and stageRestore.
func (a *App) backupsEntry(sel int) (backup.Entry, error) {
	entries, err := backup.List(a.backupDir)
	if err != nil {
		return backup.Entry{}, err
	}
	if sel < 0 || sel >= len(entries) {
		return backup.Entry{}, fmt.Errorf("no backup at index %d", sel)
	}
	return entries[sel], nil
}

// backupsDiff returns a diff of the VM's current domain XML against the
// selected backup: current is the diff's "a" side (a line only there is
// what restoring would remove) and the backup is "b" (a line only there is
// what restoring would bring back), preceded by a header naming both
// sides.
func (a *App) backupsDiff(sel int) (string, error) {
	e, err := a.backupsEntry(sel)
	if err != nil {
		return "", err
	}
	backupXML, err := backup.LoadXML(e)
	if err != nil {
		return "", err
	}
	current, err := a.hv.DomainXML(e.Meta.VM)
	if err != nil {
		return "", fmt.Errorf("current xml for %s: %w", e.Meta.VM, err)
	}

	header := fmt.Sprintf("current %s vs backup %s (%s)\n",
		e.Meta.VM, e.Meta.Time.Format(backupTimeFormat), e.Meta.Op)
	return header + diffLines(current, backupXML), nil
}

// stageRestore implements the Backups tab's restore action. It applies the
// same gates every other staging path in app.go applies (gateVMAction):
// edit mode must be on, and an unsupported domain is view-only and never
// written -- staging a restore for one would either silently no-op at
// apply time or fail verify after Define already landed, so it is refused
// here instead. It then loads the selected backup's XML and the VM's
// current XML and stages an OpRestore. Returns the resulting status line
// (or a refusal/error line) for the caller to display.
func (a *App) stageRestore(sel int) string {
	if !a.editMode {
		return "press e to enter edit mode first"
	}

	e, err := a.backupsEntry(sel)
	if err != nil {
		return err.Error()
	}
	var vm *model.VM
	if a.snap != nil {
		vm = a.snap.VM(e.Meta.VM)
	}
	if vm != nil && vm.Unsupported {
		return fmt.Sprintf("%s: unsupported config, view only", vm.Name)
	}

	backupXML, err := backup.LoadXML(e)
	if err != nil {
		return err.Error()
	}
	current, err := a.hv.DomainXML(e.Meta.VM)
	if err != nil {
		return fmt.Sprintf("%s: %v", e.Meta.VM, err)
	}

	op := model.PendingOp{
		Kind:       model.OpRestore,
		VM:         e.Meta.VM,
		BackupXML:  backupXML,
		StagedHash: model.HashXML(current),
		MemNode:    -1,
		Summary: fmt.Sprintf("%s: restore config from backup %s (%s)",
			e.Meta.VM, e.Meta.Time.Format(backupTimeFormat), e.Meta.Op),
	}
	a.queue.Add(op)
	return "staged: " + op.Summary
}

// handleBackupsKey handles the Backups tab's list-navigation keys
// (up/down, j/k), moving *sel by one row and clamping to [0, n-1] (0 when
// n is 0). It reports whether it consumed msg. Enter (show diff) and R
// (stage restore) are the orchestrator's to wire directly against
// backupsDiff/stageRestore, since displaying a multi-line diff needs an App
// field this package does not own.
func (a *App) handleBackupsKey(msg tea.KeyMsg, sel *int, n int) bool {
	delta := 0
	switch {
	case msg.Type == tea.KeyUp, isRune(msg, 'k'):
		delta = -1
	case msg.Type == tea.KeyDown, isRune(msg, 'j'):
		delta = 1
	default:
		return false
	}

	*sel += delta
	if *sel < 0 {
		*sel = 0
	}
	if n <= 0 {
		*sel = 0
	} else if *sel > n-1 {
		*sel = n - 1
	}
	return true
}
