package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	uri := flag.String("c", "qemu:///system", "libvirt connection URI")
	backupDir := flag.String("backup-dir", defaultBackupDir(), "backup directory")
	flag.Parse()
	fmt.Println("pindergarten", version, *uri, *backupDir)
	os.Exit(0)
}

func defaultBackupDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/pindergarten/backups"
	}
	home, _ := os.UserHomeDir()
	return home + "/.local/share/pindergarten/backups"
}
