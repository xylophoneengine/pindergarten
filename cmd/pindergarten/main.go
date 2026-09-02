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
	flag.Parse()

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
		snap := model.Build(topo, doms, func(addr string) int { return hostinfo.PCINumaNodeIn(topo, sysfs, addr) })
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
