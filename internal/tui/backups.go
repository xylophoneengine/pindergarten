package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// colorDiff colors a diffLines-formatted diff: "+ " lines green, "- " lines
// red, "  " context lines unstyled.
func colorDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+ "):
			lines[i] = statusOKStyle.Render(l)
		case strings.HasPrefix(l, "- "):
			lines[i] = statusErrStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// backupsTable renders the TIME/VM/OPERATION table for entries (natural
// column widths; panel() truncates as a safety net if it still doesn't
// fit), with the row at sel background-highlighted. Alongside the string
// it returns one "backup" hit per row, 0-based relative to the table's own
// top-left corner (row 0 is the header).
func backupsTable(entries []backup.Entry, sel int) (string, []hit) {
	names := []string{"TIME", "VM", "OPERATION"}
	vals := make([][]string, len(names))
	for _, e := range entries {
		vals[0] = append(vals[0], e.Meta.Time.Format(backupTimeFormat))
		vals[1] = append(vals[1], e.Meta.VM)
		vals[2] = append(vals[2], e.Meta.Op)
	}
	widths := make([]int, len(names))
	for i, name := range names {
		widths[i] = lipgloss.Width(name)
		for _, v := range vals[i] {
			if vw := lipgloss.Width(v); vw > widths[i] {
				widths[i] = vw
			}
		}
	}
	rowCells := func(row int) []string {
		cells := make([]string, len(names))
		for i, name := range names {
			val := name
			if row >= 0 {
				val = vals[i][row]
			}
			cells[i] = padRight(val, widths[i])
		}
		return cells
	}

	lines := []string{tableHeaderStyle.Render(strings.Join(rowCells(-1), "  "))}
	hits := make([]hit, 0, len(entries))
	for i := range entries {
		line := strings.Join(rowCells(i), "  ")
		if i == sel {
			line = selectedRowStyle.Render(line)
		}
		lines = append(lines, line)
		hits = append(hits, hit{y0: i + 1, y1: i + 2, x0: 0, x1: hitWide, kind: "backup", index: i})
	}
	return strings.Join(lines, "\n"), hits
}

// renderBackups renders the Backups tab's table body (no panel/border):
// "no backups in <dir>" for an empty dir, an error line for a List error,
// else backupsTable's TIME/VM/OPERATION table. w is currently unused
// (panel() applies the width safety net).
func (a *App) renderBackups(sel int, w int) (string, []hit) {
	entries, err := backup.List(a.backupDir)
	if err != nil {
		return fmt.Sprintf("error listing backups: %v", err), nil
	}
	if len(entries) == 0 {
		return fmt.Sprintf("no backups in %s", a.backupDir), nil
	}
	return backupsTable(entries, sel)
}

// renderBackupsTab renders the Backups tab: renderBackups's table (or
// message) inside a titled panel, plus a one-line action hint.
func (a *App) renderBackupsTab(sel, w int) (string, []hit) {
	body, hits := a.renderBackups(sel, w)
	body += "\n\nenter: view diff  R: restore (edit mode)"
	return panel("Backups", body, w), offsetHits(hits, 1, 1)
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
		StagedXML:  current,
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
