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
// "Z" suffix so it is never mistaken for local time. backupTimeFormatShort
// drops the seconds, used as a fallback when the table is too narrow for
// the full format even after OPERATION is capped.
const (
	backupTimeFormat      = "2006-01-02 15:04:05Z"
	backupTimeFormatShort = "2006-01-02 15:04Z"
)

// backupOpCap is the Backups table's OPERATION column cap (truncated with
// ".." past this many cells).
const backupOpCap = 30

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

// backupsColVals builds the TIME/VM/OPERATION column values for entries
// (formatting TIME with tf; OPERATION always capped at backupOpCap,
// truncated with ".." -- that cap is permanent regardless of width, same
// as the VMs table's NAME/PINS caps).
func backupsColVals(entries []backup.Entry, tf string) [][]string {
	vals := make([][]string, 3)
	for _, e := range entries {
		vals[0] = append(vals[0], e.Meta.Time.Format(tf))
		vals[1] = append(vals[1], e.Meta.VM)
		vals[2] = append(vals[2], ansiTruncate(e.Meta.Op, backupOpCap))
	}
	return vals
}

// backupsColWidths returns the natural (max of header and values) width
// of each of vals' columns, against the fixed TIME/VM/OPERATION headers.
func backupsColWidths(vals [][]string) []int {
	names := []string{"TIME", "VM", "OPERATION"}
	widths := make([]int, len(names))
	for i, name := range names {
		widths[i] = lipgloss.Width(name)
		for _, v := range vals[i] {
			if vw := lipgloss.Width(v); vw > widths[i] {
				widths[i] = vw
			}
		}
	}
	return widths
}

// backupsTableWidth returns how wide a TIME/VM/OPERATION table built from
// widths would be (columns plus 2-cell gaps between them).
func backupsTableWidth(widths []int) int {
	total := 0
	for _, w := range widths {
		total += w
	}
	return total + 2*(len(widths)-1)
}

// backupsTable renders the TIME/VM/OPERATION table for visible (a slice of
// all, already scrolled by the caller to fit its height budget): OPERATION
// is capped (truncated with ".."), and if the table still doesn't fit w
// even with that cap, TIME drops its seconds. Column widths are computed
// from all, not just visible -- otherwise scrolling a long, ragged-width
// list could resize the columns out from under the visible rows on every
// scroll tick. The row at sel is background-highlighted; offset translates
// a visible row back to its real index in the full list for hit-testing
// and selection. Alongside the string it returns one "backup" hit per row,
// bounded to w (the table's inner width, so a click in a neighboring panel
// in a two-column layout can't land on it), 0-based relative to the
// table's own top-left corner (row 0 is the header).
func backupsTable(all, visible []backup.Entry, sel, offset, w int) (string, []hit) {
	names := []string{"TIME", "VM", "OPERATION"}
	tf := backupTimeFormat
	widths := backupsColWidths(backupsColVals(all, tf))
	if backupsTableWidth(widths) > w {
		tf = backupTimeFormatShort
		widths = backupsColWidths(backupsColVals(all, tf))
	}
	vals := backupsColVals(visible, tf)

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
	hits := make([]hit, 0, len(visible))
	for i := range visible {
		realIdx := offset + i
		line := strings.Join(rowCells(i), "  ")
		if realIdx == sel {
			line = selectedRowStyle.Render(line)
		}
		lines = append(lines, line)
		hits = append(hits, hit{y0: i + 1, y1: i + 2, x0: 0, x1: w, kind: "backup", index: realIdx})
	}
	return strings.Join(lines, "\n"), hits
}

// backupsCount returns len(backup.List(a.backupDir)), or 0 on a List error
// -- shared by every caller that just needs the count (the key handler and
// the mouse-wheel handler), so a single input event lists the directory
// once rather than each caller re-listing it.
func (a *App) backupsCount() int {
	entries, err := backup.List(a.backupDir)
	if err != nil {
		return 0
	}
	return len(entries)
}

// renderBackupsTab renders the Backups tab: renderBackups's table (or
// message) inside a titled panel, plus a one-line action hint. Rows scroll
// (keeping sel visible) to fit budget.
func (a *App) renderBackupsTab(sel, w, budget int) (string, []hit) {
	entries, err := backup.List(a.backupDir)
	if err != nil {
		p, _ := panelH("Backups", fmt.Sprintf("error listing backups: %v", err), w, budget, true)
		return p, nil
	}
	if len(entries) == 0 {
		p, _ := panelH("Backups", fmt.Sprintf("no backups in %s", a.backupDir), w, budget, true)
		return p, nil
	}

	rowBudget := budget - 3 // 2 borders + 1 header row
	visible, offset, _ := scrollWindow(entries, rowBudget, sel)

	table, hits := backupsTable(entries, visible, sel, offset, w-2)
	p, _ := panelH("Backups", table, w, budget, true)
	return p, offsetHits(hits, 1, 1)
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
