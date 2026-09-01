package tui

import (
	"fmt"
	"strings"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/model"
)

const coresPerRow = 32

// fmtKiB formats a KiB quantity as a human size with one decimal place and
// a K/M/G/T suffix, e.g. 196485734 -> "187.4G".
func fmtKiB(kib uint64) string {
	const unit = 1024
	f := float64(kib)
	switch {
	case kib >= unit*unit*unit:
		return fmt.Sprintf("%.1fT", f/(unit*unit*unit))
	case kib >= unit*unit:
		return fmt.Sprintf("%.1fG", f/(unit*unit))
	case kib >= unit:
		return fmt.Sprintf("%.1fM", f/unit)
	default:
		return fmt.Sprintf("%.1fK", f)
	}
}

// renderOverview renders one card per NUMA node: memory totals, thread
// pinning counts, bound-vm memory (marked OVER when it exceeds the node's
// total), attached GPUs, and bound VM names. It ends with a one-line-per-VM
// list of any domains flagged FlagUnsupported. w is unused for now (no
// wrapping beyond the fixed layout).
func renderOverview(s *model.Snapshot, w int) string {
	var b strings.Builder
	for _, node := range s.Topo.Nodes {
		pinned := 0
		for _, t := range node.Threads {
			if len(s.Use[t].VMs) > 0 {
				pinned++
			}
		}
		bound := s.BoundMemKiB[node.ID]

		line := fmt.Sprintf("node %d  mem %s free %s  threads %d (%d pinned)  bound-vm-mem %s",
			node.ID, fmtKiB(node.MemTotalKiB), fmtKiB(node.MemFreeKiB), len(node.Threads), pinned, fmtKiB(bound))
		if bound > node.MemTotalKiB {
			line += " " + overStyle.Render("OVER")
		}
		b.WriteString(line)
		b.WriteString("\n")

		if gpus := gpusOnNode(s, node.ID); gpus != "" {
			b.WriteString("  gpus: " + gpus + "\n")
		}
		if vms := vmsOnNode(s, node.ID); vms != "" {
			b.WriteString("  vms: " + vms + "\n")
		}
		b.WriteString("\n")
	}

	for _, v := range s.VMs {
		if v.Unsupported {
			fmt.Fprintf(&b, "%s: unsupported config, view only\n", v.Name)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// gpusOnNode returns the comma-separated PCI addresses of devices, across
// all VMs, whose Node matches node, or "" when there are none.
func gpusOnNode(s *model.Snapshot, node int) string {
	var addrs []string
	for _, v := range s.VMs {
		for _, d := range v.Devices {
			if d.Node == node {
				addrs = append(addrs, d.Addr)
			}
		}
	}
	return strings.Join(addrs, ", ")
}

// vmsOnNode returns the comma-separated names of VMs whose MemNodes include
// node, or "" when there are none. s.VMs is sorted by name, so the result
// is too.
func vmsOnNode(s *model.Snapshot, node int) string {
	var names []string
	for _, v := range s.VMs {
		for _, n := range v.MemNodes {
			if n == node {
				names = append(names, v.Name)
				break
			}
		}
	}
	return strings.Join(names, ", ")
}

// renderCPUMap renders every node's cores as a grid of two-glyph cells (one
// glyph per sibling thread), 32 cells per row, with a "node N" heading
// before each node's cores. cursor is a position in s.Topo.Cores; that cell
// renders reverse-video. w is unused for now (no wrapping beyond the fixed
// 32-per-row layout).
func renderCPUMap(s *model.Snapshot, cursor, w int) string {
	var b strings.Builder
	curNode := -1
	col := 0
	for i, core := range s.Topo.Cores {
		if i == 0 || core.Node != curNode {
			if i != 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "node %d\n", core.Node)
			curNode = core.Node
			col = 0
		} else if col == coresPerRow {
			b.WriteString("\n")
			col = 0
		}
		if col > 0 {
			b.WriteString(" ")
		}
		b.WriteString(renderCell(s, core, i == cursor))
		col++
	}
	b.WriteString("\n")
	return b.String()
}

// renderCell renders one core's two-glyph cell. A single-thread core (no
// SMT sibling) renders its one glyph followed by a space.
func renderCell(s *model.Snapshot, core hostinfo.Core, isCursor bool) string {
	var glyphs strings.Builder
	for _, t := range core.Threads {
		glyphs.WriteString(threadGlyph(s, t, isCursor))
	}
	if len(core.Threads) == 1 {
		glyphs.WriteString(" ")
	}
	return glyphs.String()
}

// threadGlyph returns the styled glyph for one thread: pinned (solid),
// free, or shared (2+ claimants counting VMs+Pending); a pending-only claim
// keeps the pinned glyph but in a distinct color. The cursor cell instead
// renders its plain glyph reverse-video, ignoring the pending color.
func threadGlyph(s *model.Snapshot, t int, isCursor bool) string {
	use := s.Use[t]
	total := len(use.VMs) + len(use.Pending)

	var glyph string
	switch total {
	case 0:
		glyph = "\u25cb" // white circle: free
	case 1:
		glyph = "\u25cf" // black circle: pinned
	default:
		glyph = "\u25d0" // circle, left half black: shared
	}

	if isCursor {
		return cursorStyle.Render(glyph)
	}
	if total == 1 && len(use.Pending) == 1 {
		return pendingGlyphStyle.Render(glyph)
	}
	return glyph
}

// cpuMapDetail renders the detail panel for the core at coreIdx: its id,
// socket, node and thread list, then one line per thread naming its
// pinning VM(s) and pending claimant(s). Returns "" for an out-of-range
// index.
func cpuMapDetail(s *model.Snapshot, coreIdx int) string {
	if coreIdx < 0 || coreIdx >= len(s.Topo.Cores) {
		return ""
	}
	core := s.Topo.Cores[coreIdx]

	var b strings.Builder
	fmt.Fprintf(&b, "core %d (socket %d, node %d)  threads %s\n",
		core.ID, core.Socket, core.Node, hostinfo.FormatCPUList(core.Threads))
	for _, t := range core.Threads {
		b.WriteString(threadDetailLine(s, t))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// threadDetailLine formats one thread's detail line. It naturally produces
// all four documented shapes: "free" (no claimants), the bare VM name (one
// pinning VM), "pending: name" (one pending-only claimant), or the
// comma-joined list of names/"pending: name" entries suffixed "(shared)"
// (2+ claimants).
func threadDetailLine(s *model.Snapshot, t int) string {
	use := s.Use[t]
	var parts []string
	parts = append(parts, use.VMs...)
	for _, p := range use.Pending {
		parts = append(parts, "pending: "+p)
	}

	switch len(parts) {
	case 0:
		return fmt.Sprintf("thread %d: free", t)
	case 1:
		return fmt.Sprintf("thread %d: %s", t, parts[0])
	default:
		return fmt.Sprintf("thread %d: %s (shared)", t, strings.Join(parts, ", "))
	}
}
