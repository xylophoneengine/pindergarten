package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xylophoneengine/pindergarten/internal/hostinfo"
	"github.com/xylophoneengine/pindergarten/internal/libvirtio"
	"github.com/xylophoneengine/pindergarten/internal/model"
	"github.com/xylophoneengine/pindergarten/internal/tui"
)

var version = "dev"

func main() {
	uri := flag.String("c", "qemu:///system", "libvirt connection URI")
	backupDir := flag.String("backup-dir", defaultBackupDir(), "backup directory")
	reserve := flag.Int("reserve", 0, "reserve the first N physical cores of every NUMA node\n"+
		"(all SMT threads) for the host and for VMs' emulator threads;\n"+
		"never proposed for vCPUs (default 0: off)")
	flag.Parse()

	if *reserve < 0 {
		fmt.Fprintf(os.Stderr, "-reserve must be >= 0, got %d\n", *reserve)
		os.Exit(1)
	}

	if err := os.MkdirAll(*backupDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot create backup dir %s: %v (edit mode will be unavailable)\n", *backupDir, err)
	}

	hv, err := libvirtio.Connect(*uri)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to %s: %v\nhint: run as root or add your user to the libvirt group\n", *uri, err)
		os.Exit(1)
	}
	defer hv.Close()

	sysfs := "/sys"
	nodePath := sysfs + "/devices/system/node"
	if _, err := os.Stat(nodePath); err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", nodePath, err)
		os.Exit(1)
	}

	if *reserve > 0 {
		topo, err := hostinfo.Read(sysfs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot read host topology: %v\n", err)
			os.Exit(1)
		}
		if min := minCoresPerNode(topo); *reserve >= min {
			fmt.Fprintf(os.Stderr, "-reserve %d must be less than the smallest NUMA node's core count (%d)\n", *reserve, min)
			os.Exit(1)
		}
	}

	scan := func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error) {
		topo, err := hostinfo.Read(sysfs)
		if err != nil {
			return nil, nil, err
		}
		doms, err := hv.ListDomains()
		if err != nil {
			return nil, nil, err
		}
		cfgs := map[string]*libvirtio.DomainConfig{}
		for _, d := range doms {
			if d.Config != nil {
				cfgs[d.Config.Name] = d.Config
			}
		}
		snap := model.Build(topo, doms, func(addr string) int { return hostinfo.PCINumaNodeIn(topo, sysfs, addr) }).
			WithReserved(reservedThreads(topo, *reserve))
		return snap, cfgs, nil
	}

	p := tea.NewProgram(tui.New(hv, scan, *backupDir, version), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func defaultBackupDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/pindergarten/backups"
	}
	home, _ := os.UserHomeDir()
	return home + "/.local/share/pindergarten/backups"
}

// minCoresPerNode returns the smallest per-node core count in topo (0 if
// topo has no nodes), used to validate -reserve at startup.
func minCoresPerNode(topo *hostinfo.Topology) int {
	counts := map[int]int{}
	for _, c := range topo.Cores {
		counts[c.Node]++
	}
	min := 0
	first := true
	for _, n := range counts {
		if first || n < min {
			min, first = n, false
		}
	}
	return min
}

// reservedThreads returns the thread ids of the first n cores (topology
// order -- topo.Cores is already sorted by node, then socket, then core
// id) of every NUMA node, all SMT siblings included. n <= 0 (reserve off)
// returns nil.
func reservedThreads(topo *hostinfo.Topology, n int) map[int]bool {
	if n <= 0 {
		return nil
	}
	reserved := map[int]bool{}
	seen := map[int]int{} // node -> cores already counted
	for _, c := range topo.Cores {
		if seen[c.Node] >= n {
			continue
		}
		seen[c.Node]++
		for _, t := range c.Threads {
			reserved[t] = true
		}
	}
	return reserved
}
