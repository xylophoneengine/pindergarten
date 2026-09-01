# pindergarten

pindergarten is a terminal UI for managing vCPU pinning and NUMA memory
binding on a two-socket KVM host. It scans the host's CPU/NUMA topology from
sysfs and every libvirt domain's configuration, shows which CPU threads and
memory nodes each VM is bound to, and lets you stage pin, unpin, and
numatune changes as a reviewable queue before applying them in one batch. It
runs as a single binary over SSH, works with mouse and keyboard, and never
trusts its own bookkeeping: every view is built from a fresh scan of
reality.

## Install / build

Build dependencies (the libvirt cgo bindings need the development headers):

On Rocky Linux / RHEL-family systems:

    dnf install libvirt-devel golang golangci-lint

On Debian / Ubuntu:

    apt install libvirt-dev golang-go

Then, from the repository root:

    make init && make build

`make init` points git at the repo's hooks directory so the ASCII/format/vet
lint gate runs on commit. `make build` produces the `pindergarten` binary in
the repository root.

To build inside a clean Rocky-based container instead (useful for producing
a binary without installing build dependencies on your own machine):

    make release

This builds and runs `Containerfile.builder` with the repository mounted at
`/src`, so the resulting binary is dropped back into your working tree.

At runtime, only the libvirt client library is needed, not the -devel
headers: `libvirt-libs` on Rocky/RHEL, `libvirt0` on Debian/Ubuntu.

## Usage

    pindergarten [-c URI] [-backup-dir PATH]

- `-c URI`: libvirt connection URI. Defaults to `qemu:///system` (the local
  system libvirtd).
- `-backup-dir PATH`: where domain XML backups are written before each
  change. Defaults to a per-user location; see Backups below.

pindergarten starts read-only: it can scan and display, but it will not
write anything to libvirt or to disk. Press `e` in the running app to
unlock edit mode, after a confirmation prompt. Edit mode requires a
read-write libvirt connection, so you need to run as root or be a member of
the host's `libvirt` group (whichever your libvirtd's socket permissions
require). The top-right corner of the screen always shows a badge: red
`READ ONLY` or orange `EDIT`, so you always know which mode you're in.

## How changes apply

Every change you stage (pinning a vCPU to a thread, stripping a pin,
removing a staged operation, or binding a VM's memory to a NUMA node) is
config-only: it edits the domain's `<cputune>` and/or `<numatune>` XML via
`virDomainDefineXML` and nothing else. It takes effect the next time the VM
boots; it never touches a running VM's live scheduling or memory placement.

Staged changes sit in the Pending tab as a queue, not applied immediately.
Before applying, pindergarten re-checks each affected domain's current XML
against what it read when you staged the change (a drift check), so a
change made outside pindergarten in the meantime does not get silently
clobbered. A backup of a domain's XML is written before every write to that
domain, with no exceptions. If anything goes wrong, or you simply change
your mind after applying, you can restore any domain from its backup from
the Backups tab.

## Backups

Backups are written to `-backup-dir`, which defaults to
`/var/lib/pindergarten/backups` when running as root, or
`$HOME/.local/share/pindergarten/backups` otherwise.

If you run under SELinux with root's default backup directory, leave it
under `/var/lib` rather than redirecting it elsewhere: `/var/lib` already
carries a context SELinux expects a system service's state to live in, and
relocating it can require a custom policy or `restorecon` work you don't
otherwise need.

## Keybindings

| Key | Where | Action |
|-----|-------|--------|
| `1`-`5` | anywhere | jump to tab: Overview, CPU Map, VMs, Pending, Backups |
| `Tab` / `Shift+Tab` | anywhere | next / previous tab |
| mouse click | tab bar | jump to that tab |
| arrows / `h j k l` | CPU Map | move the selected core |
| `e` | anywhere | toggle edit mode (confirmed) |
| `p` | CPU Map / VMs | pin the selected vCPU or core |
| `s` | CPU Map / VMs | strip an existing pin |
| `x` | Pending | remove a staged operation from the queue |
| `a` | Pending | apply all staged operations (backup, define, verify, rescan) |
| `d` | Pending | discard all staged operations |
| `enter` | Pending | show a diff of a staged operation |
| `R` | Backups | restore a domain from a backup |
| `r` | anywhere | rescan host topology and libvirt domains |
| `q` | anywhere | quit (confirms first if operations are pending) |

## Why NUMA pinning matters

A VM whose vCPUs run on one NUMA node while its guest memory lives on
another pays a cross-node memory latency penalty on every access, which
shows up as inconsistent, hard-to-diagnose performance, especially under
GPU passthrough workloads that are latency sensitive. Left unmanaged across
many VMs on a shared host, memory allocations also drift onto whichever
node has free pages at boot time, fragmenting host RAM until new VMs can no
longer find a contiguous NUMA-local allocation. Pinning vCPUs to threads on
the same socket as a VM's bound memory node keeps both the VM's performance
and the host's memory layout predictable.
