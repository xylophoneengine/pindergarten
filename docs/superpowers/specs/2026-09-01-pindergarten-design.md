# pindergarten — Design Spec

Date: 2026-09-01
Status: approved design, pre-implementation

## Problem

A two-socket KVM server runs many libvirt-managed VMs. GPU passthrough VMs
were left unpinned; their memory accumulated on one NUMA node, fragmenting
host RAM until new VMs could not allocate memory. Managing vCPU pinning and
NUMA memory binding by hand (virsh + mental bookkeeping) does not scale.

pindergarten is a TUI that scans reality (host topology + libvirt domain
configs), visualizes which CPU threads are pinned by which VMs, and guides
the operator through pinning/unpinning and memory-node binding — with
staged changes, mandatory backups, and rollback. It never trusts its own
bookkeeping: every view derives from a fresh scan.

## Scope

In scope:
- Read host NUMA/CPU/PCI topology from sysfs.
- Read all libvirt domains (running and shut off): cputune (vcpupin),
  numatune, hostdev PCI passthrough, vCPU count, memory, state.
- Modify ONLY `<cputune>` vcpupin entries and `<numatune>` of domains,
  config-only (takes effect on next VM boot). Never live changes.
- Stage changes in a pending-operations queue; batch apply after review.
- Backup domain XML before every write; browse and revert backups.
- Read-only by default; edit mode explicitly unlocked in-app.

Out of scope (hard non-goals):
- Creating, deleting, starting, stopping, or otherwise editing VMs.
- Live (`--live`) pinning or memory migration.
- Emulator/iothread pinning (possible later; not now).
- Multi-host orchestration (remote URIs work via libvirt, but the tool
  manages one connection at a time).

## Stack

- Go, single binary `pindergarten`.
- TUI: Bubble Tea + lipgloss + bubbles (Charm stack). Mouse + keyboard
  over SSH.
- Libvirt: official cgo bindings `libvirt.org/go/libvirt` (the Red
  Hat-blessed path; used by KubeVirt). Binary dynamically links libvirt.so,
  which every KVM host has. Build needs libvirt-devel (Rocky/RHEL) or
  libvirt-dev (Debian/Ubuntu).
- Host topology read directly from sysfs, not libvirt.

## Architecture

Three layers, strict one-way data flow: scan -> model -> render.
Mutations go through one Actions interface and always trigger rescan.

### 1. Host layer (`internal/hostinfo`)

Reads ground truth from sysfs:
- NUMA nodes: `/sys/devices/system/node/node*/cpulist`, `meminfo`
  (total/free memory per node).
- CPU topology: `/sys/devices/system/cpu/cpu*/topology/` (socket, core,
  thread siblings).
- PCI device locality: `/sys/bus/pci/devices/<addr>/numa_node`.
Takes a root path parameter so tests run against fixture trees.

### 2. Libvirt layer (`internal/libvirtio`)

- Connects to a libvirt URI. Default `qemu:///system`, overridable via
  `-c`/`--connect` (any libvirt URI: `qemu:///session`,
  `qemu+ssh://host/system`, ...). Active URI always visible in TUI header.
- Lists all defined domains; fetches inactive (config) XML.
- Parses only the elements we own: `<vcpu>`, `<memory>`, `<cputune>`
  vcpupin entries, `<numatune>`, `<hostdev>` PCI source addresses, state.
- Writes: modify those elements in the domain XML, persist via
  virDomainDefineXML. Config semantics only.
- After every define: re-read the domain XML and verify our elements
  landed as intended; mismatch is reported loudly (backup already exists).
- Domains with unparsable or exotic configs are listed as
  "unsupported — view only" and never touched.

### 3. TUI layer (`internal/tui`)

Bubble Tea app rendering the allocation model. Tabs described below.

### Allocation model (`internal/model`)

Pure functions joining host + libvirt data, recomputed after every scan:

- Per host thread: node, core, sibling, list of VMs pinning it.
  0 VMs = free, 1 = pinned, >1 = shared (legitimate but flagged).
- Per VM, derived flags:
  - Unpinned: no vcpupin entries.
  - Partially pinned: some vCPUs pinned, some not.
  - No memory binding: pinned CPUs but no numatune.
  - Node mismatch: vCPU node != numatune node, or either != GPU node.
  - Cross-node spill: vCPU set spans NUMA nodes.
  - Memory pressure: sum of memory of VMs bound to a node exceeds the
    node's total memory.
- GPU/PCI constraint: hostdev PCI address -> sysfs numa_node. This is a
  SOFT constraint: a VM works on the other node, but DMA and device
  access cross the socket interconnect (bandwidth/latency penalty). The
  tool treats the device's node as the preferred placement and warns on
  mismatch; it never blocks an override. Devices reporting numa_node = -1
  are shown as "unknown locality", warned, never used as a constraint.
- Shut-off VMs are included in all bookkeeping: their configs still claim
  pinnings.

Every flag carries a plain-language explanation (one sentence cause, one
sentence consequence) shown in the detail panel. Example: "RAM is not
bound to a node -> the kernel may allocate it on either node -> can starve
the node where GPU VMs live."

### Pending operations queue

KDE-Partition-Manager-style staging. No action writes immediately.

- Pin/unpin/set-numatune/revert-backup actions append a pending operation.
- The TUI renders PROJECTED state: scanned reality + pending ops overlaid,
  pending visually distinct (hatched/yellow vs solid). The pin wizard for
  the next VM sees threads claimed by earlier pending ops.
- Status bar always shows: "N pending operations — [a]pply [d]iscard".
- Pending panel lists each op human-readably; ops individually removable.
  Per-VM ops are merged (order across VMs does not matter).
- Apply flow: review screen lists every op and its exact effect -> single
  confirm -> sequential execution: per domain BACKUP FIRST, then write.
  Rescan when done. On failure: stop, report which ops applied and which
  did not; applied ones have backups.
- Drift check: each queued op stores a hash of its target domain's XML at
  staging time. On apply, if the domain changed since staging, the apply
  pauses, shows what changed, and the user decides per op: discard it, or
  re-open it in the wizard against the new reality. Never auto-apply over
  drift.
- Queue is in-memory only; quitting with pending ops warns and confirms
  discard. Nothing persists across runs — every start is a fresh scan.

## TUI Layout

Tab bar top, detail/help panel bottom, persistent status bar. Mouse and
keyboard both fully functional.

1. **Overview** — per-NUMA-node summary cards: total/free memory, thread
   count, pinned vs free threads, GPUs on the node, VMs bound to it.
2. **CPU Map** — scales to 400+ threads. Per-node grid, one cell per
   physical CORE, thread siblings shown inside the cell (both-pinned /
   one-pinned / free). Colors: free, pinned, shared, pending. Cursor or
   mouse selects a core; detail panel shows exact thread IDs and the VMs
   pinning them. Rows of ~32 cores keep a 200-core node readable.
3. **VMs** — table: name, state, vCPUs, memory, pinned?, memory node, GPU
   node, conflict badges. Enter/click opens VM detail with actions:
   Pin wizard, Strip pinning, Set memory node.
4. **Backups** — chronological list of every change: timestamp, VM, what
   changed, XML diff view (backup vs current), Revert action. A revert is
   itself a pending op and creates its own backup (rollback of rollback
   works).

### Pin wizard

Pick VM -> tool proposes placement:
- Filters to the GPU's node when a hostdev constraint exists.
- Prefers a node with enough free memory and free full cores (thread
  siblings kept together).
- Avoids threads pinned by other VMs and threads claimed by pending ops.
- Shows the proposal on a mini CPU map with a plain-language rationale
  ("GPU 0000:81:00.0 sits on node 1 -> keeping vCPUs and RAM on node 1
  avoids cross-socket traffic").
- User accepts or adjusts the selection manually, then the op is staged.
- Staged op = one vcpupin entry per vCPU + numatune binding.

## Read-only by default, explicit edit mode

- The tool ALWAYS starts read-only. No flag needed; browsing is the
  default, zero-risk state ("view without thinking").
- Edit mode is unlocked in-app: a keybinding (e.g. `e`, advertised in the
  status bar as "[e] enable edit mode") plus a confirm. Only in edit mode
  do actions, the pin wizard, and apply exist.
- If writes are impossible (permissions, polkit, SELinux), the edit
  toggle is disabled and pressing it shows the reason in the status line.
  This is NOT an error and does not fail loudly.
- A persistent, prominent badge in the top-right corner shows the state
  on every screen: red READ ONLY by default, a visually distinct EDIT
  badge (different color, still prominent) while edit mode is active.

## Backups & Rollback

- Location: `/var/lib/pindergarten/backups/`; fallback
  `~/.local/share/pindergarten/backups/` (session mode); overridable
  via flag. SELinux note for Rocky: directory under /var/lib with correct
  context; documented in README.
- Before each domain write: full inactive-config dumpxml ->
  `<timestamp>_<vm-name>.xml` plus a one-line JSON sidecar (when, VM, op
  description, tool version).
- Retention: keep everything (XML is tiny). Manual deletion only.

## Errors & Permissions

- Startup: libvirt socket unreachable -> fail loud with a fix hint ("run
  as root or add user to the libvirt group"). Sysfs unreadable -> fail
  loud likewise. Backup dir writability is checked when edit mode is
  enabled, not at startup; failure blocks edit mode with the reason
  shown, read-only browsing stays available.
- Write-incapable but readable -> edit toggle disabled with reason (see
  above), not an error.
- Post-write verify (see libvirt layer).
- Unsupported domains: view-only, never written.

## CLI

```
pindergarten [-c URI] [--backup-dir PATH]
```

Defaults: `-c qemu:///system`, backup dir as above.

## Code Hygiene

- Source is pure ASCII. TUI glyphs are written as \uXXXX escapes in Go
  string literals so rendering stays rich while source stays ASCII.
- Git hooks committed in-repo: `.githooks/` activated via
  `git config core.hooksPath .githooks` (done by `make init`).
  - pre-commit: strip zero-width/invisible characters; transliterate
    non-ASCII to ASCII in source files; re-stage fixes; then gofmt,
    goimports, go vet, golangci-lint. Unfixable issues block the commit
    with a file:line list.
- `make lint` runs the same checks manually. No CI initially.

## Testing

- hostinfo: fixture sysfs trees (2-socket/400-thread, single-node, GPU
  with numa_node=-1).
- libvirtio XML parse/modify: table-driven tests on real-world domain XML
  fixtures (GPU passthrough, partial pinning, exotic configs left
  untouched).
- model: pure-function table-driven tests for all conflict flags and the
  wizard's placement proposals.
- tui: Bubble Tea message-driven model/update tests; no terminal
  automation.
- Integration against real libvirtd: manual on the target server, not in
  tests.

## Build & Distribution

- `go build` with cgo against system libvirt headers.
- Makefile targets: build, lint, test, init (hook setup).
- Optional Rocky-based builder container so the binary links against the
  oldest targeted libvirt ABI and runs on newer.
- Output: one binary. Runtime dependency: libvirt runtime library
  (libvirt-libs on RHEL-family, libvirt0 on Debian-family) — present on
  any KVM host by definition.

## Repo Layout

```
cmd/pindergarten/    main, CLI flags
internal/hostinfo/      sysfs topology reader
internal/libvirtio/     libvirt connection, domain XML read/write
internal/model/         allocation model, conflict flags, wizard logic
internal/backup/        backup write/list/revert
internal/tui/           Bubble Tea app, tabs, widgets
.githooks/              pre-commit hygiene hooks
docs/superpowers/specs/ this spec
```
