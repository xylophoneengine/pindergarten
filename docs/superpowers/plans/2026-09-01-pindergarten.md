# pindergarten Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** TUI that scans libvirt domains and host NUMA topology, visualizes vCPU pinning and memory binding, and stages config-only pinning changes with mandatory backups and rollback.

**Architecture:** Three layers with one-way data flow: sysfs/libvirt scan -> pure allocation model -> Bubble Tea TUI. All mutations go through a pending-operations queue, applied batch-wise: backup, define, verify, rescan. Read-only by default; edit mode unlocked in-app.

**Tech Stack:** Go 1.22+, Bubble Tea/lipgloss/bubbles (TUI), libvirt.org/go/libvirt (official cgo bindings), beevik/etree (surgical XML edits), stdlib elsewhere.

**Spec:** `docs/superpowers/specs/2026-09-01-pindergarten-design.md`

## Global Constraints

- Module path: `github.com/xylophoneengine/pindergarten`.
- Binary name: `pindergarten`.
- Source is pure ASCII. Any TUI glyph is written as a `\uXXXX` escape inside a Go string literal. The pre-commit hook enforces this.
- All libvirt writes are config-only via `virDomainDefineXML`. Never call any live-modification API.
- Only `<cputune>` vcpupin entries and `<numatune>` are ever modified in domain XML. Everything else is preserved (etree edits in place).
- Backup before every domain write, no exceptions.
- Dependencies allowed: charmbracelet/bubbletea, charmbracelet/lipgloss, charmbracelet/bubbles, beevik/etree, libvirt.org/go/libvirt. Nothing else without a spec change.
- Building needs libvirt-devel (RHEL-family) or libvirt-dev (Debian-family) because of cgo.
- `go test ./...` must not require a running libvirtd. The real libvirt connection is exercised manually only.
- Commit messages: conventional commits (`feat:`, `fix:`, `test:`, `chore:`).

## File Structure

```
cmd/pindergarten/main.go       CLI flags, startup checks, wiring, version
internal/hostinfo/hostinfo.go  sysfs topology + PCI NUMA reader
internal/libvirtio/xml.go      domain XML parse/modify (pure)
internal/libvirtio/conn.go     Hypervisor interface + real libvirt impl
internal/libvirtio/fake.go     Fake Hypervisor for tests
internal/model/snapshot.go     allocation model + conflict flags
internal/model/queue.go        pending ops, projection, drift hash
internal/model/propose.go      pin wizard placement logic
internal/backup/backup.go      backup save/list/load
internal/apply/apply.go        batch apply executor
internal/tui/app.go            root Bubble Tea model, tabs, edit mode, badge
internal/tui/views.go          Overview + CPU map + VMs rendering
internal/tui/wizard.go         pin wizard flow
internal/tui/pending.go        pending tab, apply flow, drift handling
internal/tui/backups.go        backups tab, revert staging
.githooks/pre-commit           ASCII + fmt + vet + lint gate
Makefile                       build/test/lint/init/release
Containerfile.builder          Rocky-based build container
```

---

### Task 1: Scaffold, Makefile, git hooks

**Files:**
- Create: `go.mod`, `cmd/pindergarten/main.go`, `Makefile`, `.githooks/pre-commit`, `.gitignore`

**Interfaces:**
- Produces: module `github.com/xylophoneengine/pindergarten`; `main.version` string var set via ldflags; `make build|test|lint|init`.

- [ ] **Step 1: go.mod + main stub**

```bash
go mod init github.com/xylophoneengine/pindergarten
```

`cmd/pindergarten/main.go`:

```go
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
```

- [ ] **Step 2: Makefile**

```make
BIN=pindergarten
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/pindergarten

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	golangci-lint run ./...

init:
	git config core.hooksPath .githooks

release:
	podman build -f Containerfile.builder -t pindergarten-builder .
	podman run --rm -v $$PWD:/src:Z pindergarten-builder

.PHONY: build test lint init release
```

`.gitignore`: `/pindergarten`

- [ ] **Step 3: pre-commit hook**

`.githooks/pre-commit` (mode 755):

```sh
#!/bin/sh
set -eu
files=$(git diff --cached --name-only --diff-filter=ACM | grep -E '\.(go|md|mod)$|^Makefile$' || true)
[ -z "$files" ] && exit 0
for f in $files; do
	[ -f "$f" ] || continue
	perl -CSD -i -pe 's/[\x{200B}-\x{200D}\x{FEFF}\x{2060}]//g' "$f"
	iconv -f UTF-8 -t ASCII//TRANSLIT "$f" >"$f.tmp"
	mv "$f.tmp" "$f"
done
if LC_ALL=C grep -nP '[^\x00-\x7F]' -- $files; then
	echo "pre-commit: non-ASCII remains after transliteration, fix manually" >&2
	exit 1
fi
gofiles=$(echo "$files" | grep '\.go$' || true)
if [ -n "$gofiles" ]; then
	gofmt -w $gofiles
	go vet ./... || exit 1
	golangci-lint run ./... || exit 1
fi
git add -- $files
```

Run `make init`.

- [ ] **Step 4: Verify**

Run: `make build && ./pindergarten -h` -> usage with `-c` and `-backup-dir`.
Hook check: `printf 'caf\xc3\xa9\n' > note.md && git add note.md && git commit -m tmp` -> commit succeeds AND `cat note.md` shows `cafe` (transliterated). Then `git reset --hard HEAD~1`.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "chore: scaffold module, Makefile, ascii pre-commit hook"
```

---

### Task 2: hostinfo — sysfs topology reader

**Files:**
- Create: `internal/hostinfo/hostinfo.go`
- Test: `internal/hostinfo/hostinfo_test.go`

**Interfaces:**
- Produces:

```go
package hostinfo

type Thread struct{ ID, Core, Socket, Node, Sibling int } // Sibling: other SMT thread ID, -1 if none
type Core struct {
	Socket, ID, Node int
	Threads          []int // sorted
}
type Node struct {
	ID                       int
	Threads                  []int // sorted
	MemTotalKiB, MemFreeKiB  uint64
}
type Topology struct {
	Nodes   []Node // sorted by ID
	Cores   []Core // sorted by Node, then Socket, then ID
	Threads map[int]Thread
}

func Read(sysfsRoot string) (*Topology, error)
func PCINumaNode(sysfsRoot, addr string) int // -1 if unknown/missing
func ParseCPUList(s string) ([]int, error)   // "0-3,8" -> [0 1 2 3 8]; "" -> []
func FormatCPUList(ids []int) string          // [4 68] -> "4,68"
```

Sysfs files read (all relative to `sysfsRoot`):
- `devices/system/node/node<N>/cpulist`
- `devices/system/node/node<N>/meminfo` (lines `Node 0 MemTotal: 196494356 kB`, `Node 0 MemFree: ...`)
- `devices/system/cpu/cpu<N>/topology/physical_package_id`, `core_id`, `thread_siblings_list`
- `bus/pci/devices/<addr>/numa_node`

- [ ] **Step 1: Failing tests**

Test helper writes a fake sysfs tree into `t.TempDir()` (do NOT commit fixture trees):

```go
func writeSysfs(t *testing.T, root string, nodes map[int]string, mem map[int][2]uint64,
	cpus map[int][3]string, pci map[string]string) {
	t.Helper()
	for n, cpulist := range nodes {
		d := fmt.Sprintf("%s/devices/system/node/node%d", root, n)
		os.MkdirAll(d, 0o755)
		os.WriteFile(d+"/cpulist", []byte(cpulist+"\n"), 0o644)
		m := mem[n]
		mi := fmt.Sprintf("Node %d MemTotal: %d kB\nNode %d MemFree: %d kB\n", n, m[0], n, m[1])
		os.WriteFile(d+"/meminfo", []byte(mi), 0o644)
	}
	for c, v := range cpus { // v = [package, core_id, siblings_list]
		d := fmt.Sprintf("%s/devices/system/cpu/cpu%d/topology", root, c)
		os.MkdirAll(d, 0o755)
		os.WriteFile(d+"/physical_package_id", []byte(v[0]+"\n"), 0o644)
		os.WriteFile(d+"/core_id", []byte(v[1]+"\n"), 0o644)
		os.WriteFile(d+"/thread_siblings_list", []byte(v[2]+"\n"), 0o644)
	}
	for addr, node := range pci {
		d := root + "/bus/pci/devices/" + addr
		os.MkdirAll(d, 0o755)
		os.WriteFile(d+"/numa_node", []byte(node+"\n"), 0o644)
	}
}
```

Tests:

```go
func TestParseCPUList(t *testing.T) {
	got, err := ParseCPUList("0-3,8")
	// assert nil err, got == []int{0,1,2,3,8}
	// also: "" -> empty, "5" -> [5], "1-2,2" -> [1 2] (dedup), "x" -> error
}

func TestReadTwoNodeSMT(t *testing.T) {
	root := t.TempDir()
	writeSysfs(t, root,
		map[int]string{0: "0-1,4-5", 1: "2-3,6-7"},
		map[int][2]uint64{0: {1000, 400}, 1: {1000, 900}},
		map[int][3]string{
			0: {"0", "0", "0,4"}, 4: {"0", "0", "0,4"},
			1: {"0", "1", "1,5"}, 5: {"0", "1", "1,5"},
			2: {"1", "0", "2,6"}, 6: {"1", "0", "2,6"},
			3: {"1", "1", "3,7"}, 7: {"1", "1", "3,7"},
		}, nil)
	topo, err := Read(root)
	// assert: 2 nodes; node0.MemTotalKiB==1000, MemFreeKiB==400
	// topo.Threads[4] == Thread{ID:4, Core:0, Socket:0, Node:0, Sibling:0}
	// 4 cores total; core (Socket:0,ID:0) has Threads [0,4]
}

func TestPCINumaNode(t *testing.T) {
	root := t.TempDir()
	writeSysfs(t, root, map[int]string{0: "0"},
		map[int][2]uint64{0: {1, 1}},
		map[int][3]string{0: {"0", "0", "0"}},
		map[string]string{"0000:81:00.0": "1", "0000:01:00.0": "-1"})
	// PCINumaNode(root, "0000:81:00.0") == 1
	// PCINumaNode(root, "0000:01:00.0") == -1
	// PCINumaNode(root, "0000:ff:00.0") == -1 (missing file)
}
```

- [ ] **Step 2: Run, verify FAIL** — `go test ./internal/hostinfo/ -v` -> compile errors (undefined symbols).

- [ ] **Step 3: Implement**

Notes for implementer:
- Node discovery: glob `devices/system/node/node[0-9]*`.
- Thread's Node: from which node cpulist contains it. Sibling: the other ID in `thread_siblings_list` (after ParseCPUList, remove own ID; take first or -1).
- Cores: group threads by (Socket, core_id).
- meminfo parse: `strings.Fields`, match suffix `MemTotal:` / `MemFree:` tokens.
- ParseCPUList: split on comma, each piece either int or `a-b`; dedup+sort result.
- No caching, read fresh every call.

- [ ] **Step 4: Run, verify PASS** — `go test ./internal/hostinfo/ -v`

- [ ] **Step 5: Commit** — `git add internal/hostinfo && git commit -m "feat: sysfs topology and pci numa reader"`

---

### Task 3: libvirtio — domain XML parsing

**Files:**
- Create: `internal/libvirtio/xml.go`
- Test: `internal/libvirtio/xml_test.go`

**Interfaces:**
- Consumes: `hostinfo.ParseCPUList`, `hostinfo.FormatCPUList`.
- Produces:

```go
package libvirtio

type DomainConfig struct {
	Name      string
	UUID      string
	VCPUs     int
	MemoryKiB uint64
	VCPUPins  map[int][]int // vcpu -> host thread IDs; empty map if no cputune
	MemNodes  []int         // numatune nodeset; nil if no numatune
	MemMode   string        // numatune mode attr, "" if none
	Hostdevs  []string      // PCI addrs "0000:81:00.0" from passthrough hostdevs
	Raw       string        // original XML verbatim
}

func ParseDomainXML(raw string) (*DomainConfig, error)
```

- [ ] **Step 1: Failing tests**

Shared fixture const in test file:

```go
const gpuVMXML = `<domain type='kvm'>
  <name>gpu-vm-01</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b01</uuid>
  <memory unit='KiB'>16777216</memory>
  <vcpu placement='static'>4</vcpu>
  <cputune>
    <vcpupin vcpu='0' cpuset='4'/>
    <vcpupin vcpu='1' cpuset='68'/>
  </cputune>
  <numatune>
    <memory mode='strict' nodeset='1'/>
  </numatune>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <hostdev mode='subsystem' type='pci' managed='yes'>
      <source>
        <address domain='0x0000' bus='0x81' slot='0x00' function='0x0'/>
      </source>
    </hostdev>
    <disk type='file' device='disk'><target dev='vda'/></disk>
  </devices>
</domain>`

const plainVMXML = `<domain type='kvm'>
  <name>plain-vm</name>
  <uuid>2fdd4bd1-6f52-4a3c-9e57-1f6a1d6f3b02</uuid>
  <memory unit='MiB'>4096</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices/>
</domain>`
```

Tests assert:
- gpuVMXML: Name `gpu-vm-01`, VCPUs 4, MemoryKiB 16777216, VCPUPins `{0:[4], 1:[68]}`, MemNodes `[1]`, MemMode `strict`, Hostdevs `["0000:81:00.0"]`.
- plainVMXML: MemoryKiB 4194304 (MiB converted), VCPUPins empty, MemNodes nil, Hostdevs empty.
- `ParseDomainXML("<not xml")` -> error.
- cpuset ranges: a vcpupin with `cpuset='4-5'` parses to `[4,5]`.

- [ ] **Step 2: Run, verify FAIL** — `go test ./internal/libvirtio/ -v`

- [ ] **Step 3: Implement**

Use `github.com/beevik/etree` (`go get github.com/beevik/etree`). Notes:
- memory unit conversion: `b/bytes`->/1024, `KiB` or missing ->1, `MiB`->*1024, `GiB`->*1024*1024, `KB`->*1000/1024 not needed; support b, KiB, MiB, GiB, TiB; unknown unit -> error.
- Hostdev filter: `mode='subsystem' type='pci'` only. Address attrs are hex `0x0000` etc; format as `%04x:%02x:%02x.%x` after `strconv.ParseUint(v, 0, 32)`.
- nodeset parsed with `hostinfo.ParseCPUList` (same syntax).

- [ ] **Step 4: Run, verify PASS**

- [ ] **Step 5: Commit** — `git commit -m "feat: parse domain xml pinning, numatune, hostdevs"`

---

### Task 4: libvirtio — XML modification

**Files:**
- Modify: `internal/libvirtio/xml.go`
- Test: `internal/libvirtio/xml_test.go`

**Interfaces:**
- Produces:

```go
// SetPinning replaces all vcpupin entries with pins and, when memNode >= 0,
// sets <numatune><memory mode='strict' nodeset='<memNode>'/>. memNode < 0 leaves numatune untouched.
func SetPinning(raw string, pins map[int][]int, memNode int) (string, error)

// StripPinning removes every vcpupin element and the numatune memory binding.
// Empty <cputune>/<numatune> elements are removed entirely.
func StripPinning(raw string) (string, error)
```

- [ ] **Step 1: Failing tests**

```go
func TestSetPinningRoundTrip(t *testing.T) {
	out, err := SetPinning(plainVMXML, map[int][]int{0: {2}, 1: {6}}, 1)
	cfg, _ := ParseDomainXML(out)
	// assert cfg.VCPUPins == {0:[2],1:[6]}, cfg.MemNodes == [1], cfg.MemMode == "strict"
	// assert everything else preserved: strings.Contains(out, "<os>") etc,
	// re-parse Name/UUID/MemoryKiB unchanged
}

func TestSetPinningReplacesExisting(t *testing.T) {
	out, _ := SetPinning(gpuVMXML, map[int][]int{0: {10}}, -1)
	cfg, _ := ParseDomainXML(out)
	// assert cfg.VCPUPins == {0:[10]} (old pins gone), cfg.MemNodes still [1] (memNode -1 untouched)
	// assert hostdev still present in out
}

func TestStripPinning(t *testing.T) {
	out, _ := StripPinning(gpuVMXML)
	cfg, _ := ParseDomainXML(out)
	// assert VCPUPins empty, MemNodes nil
	// assert no "<cputune" and no "<numatune" substring in out
	// assert hostdev + disk still present
}
```

- [ ] **Step 2: Run, verify FAIL**

- [ ] **Step 3: Implement** — etree: load doc, find/create `cputune` under root, remove all `vcpupin` children, append new sorted by vcpu id, `cpuset` via `hostinfo.FormatCPUList`. Same pattern for numatune. `doc.WriteToString()`.

- [ ] **Step 4: Run, verify PASS**

- [ ] **Step 5: Commit** — `git commit -m "feat: surgical xml edits for vcpupin and numatune"`

---

### Task 5: libvirtio — Hypervisor interface, real + fake

**Files:**
- Create: `internal/libvirtio/conn.go`, `internal/libvirtio/fake.go`
- Test: `internal/libvirtio/fake_test.go`

**Interfaces:**
- Produces:

```go
type DomState int

const (
	StateRunning DomState = iota
	StateShutoff
	StateOther
)

type Domain struct {
	Config   *DomainConfig
	State    DomState
	ParseErr error // non-nil => unsupported, view-only
}

type Hypervisor interface {
	URI() string
	ReadOnly() (bool, string) // true + reason when writes impossible
	ListDomains() ([]Domain, error)
	DomainXML(name string) (string, error) // inactive/config XML
	Define(xml string) error
	Close()
}

func Connect(uri string) (Hypervisor, error)

// fake.go
type Fake struct {
	ConnURI    string
	XML        map[string]string   // name -> domain xml
	States     map[string]DomState // default StateShutoff
	RO         bool
	ROReason   string
	DefineErr  error
	Defined    []string // xml passed to Define, in order
}
// Fake implements Hypervisor. Define parses <name> from xml and updates XML[name].
```

- [ ] **Step 1: Failing tests (fake only)**

```go
func TestFakeDefineUpdates(t *testing.T) {
	f := &Fake{XML: map[string]string{"plain-vm": plainVMXML}}
	doms, _ := f.ListDomains()
	// assert 1 domain, Config.Name == "plain-vm", State == StateShutoff
	modified, _ := SetPinning(plainVMXML, map[int][]int{0: {2}}, 0)
	f.Define(modified)
	xml, _ := f.DomainXML("plain-vm")
	cfg, _ := ParseDomainXML(xml)
	// assert cfg.VCPUPins == {0:[2]}
}

func TestFakeUnparsableDomainListed(t *testing.T) {
	f := &Fake{XML: map[string]string{"weird": "<domain><name>weird</name><vcpu>bogus</vcpu></domain>"}}
	doms, _ := f.ListDomains()
	// assert 1 domain with ParseErr != nil (name recovered best-effort or "weird")
}
```

- [ ] **Step 2: Run, verify FAIL**

- [ ] **Step 3: Implement**

`fake.go`: straightforward; extract name via ParseDomainXML, fall back to regexp `<name>(.*?)</name>` when parse fails.

`conn.go` real impl (`go get libvirt.org/go/libvirt`), thin and NOT unit-tested:
- `Connect`: try `libvirt.NewConnect(uri)`; on failure try `libvirt.NewConnectReadOnly(uri)` -> if that works, mark RO with the original error string as reason; if both fail return combined error.
- `ListDomains`: `conn.ListAllDomains(0)` (flag 0 = all: active + inactive). For each: name, `dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)`, state via `dom.GetState()` mapped: `DOMAIN_RUNNING`->StateRunning, `DOMAIN_SHUTOFF`->StateShutoff, else StateOther. ParseDomainXML errors land in `ParseErr`, never abort the listing.
- `Define`: `conn.DomainDefineXML(xml)`.
- Wrap in `//go:build !nolibvirt` if needed later — for now plain file; CI-less project, builds always have libvirt-devel.

- [ ] **Step 4: Run, verify PASS** — `go test ./internal/libvirtio/ -v` (fake tests; conn.go only compiles)

- [ ] **Step 5: Commit** — `git commit -m "feat: hypervisor interface with libvirt and fake impls"`

---

### Task 6: backup package

**Files:**
- Create: `internal/backup/backup.go`
- Test: `internal/backup/backup_test.go`

**Interfaces:**
- Produces:

```go
package backup

type Meta struct {
	Time        time.Time `json:"time"`
	VM          string    `json:"vm"`
	Op          string    `json:"op"`
	ToolVersion string    `json:"tool_version"`
}

type Entry struct {
	XMLPath string
	Meta    Meta
}

func Save(dir, vm, opDesc, toolVersion, xml string) (Entry, error)
func List(dir string) ([]Entry, error) // newest first; missing dir -> empty, no error
func LoadXML(e Entry) (string, error)
```

Layout: `<dir>/<RFC3339-with-nanos>_<vm>.xml` + same path with `.json` sidecar holding Meta. Save creates dir with 0o755 if missing. Filename timestamp uses `time.Now().UTC().Format("20060102T150405.000000000Z")` with `:` avoided (SELinux/scp friendliness); Meta.Time is authoritative for display.

- [ ] **Step 1: Failing tests**

```go
func TestSaveListLoad(t *testing.T) {
	dir := t.TempDir() + "/bk"
	e1, err := Save(dir, "vm-a", "pin 2 vcpus to node 1", "test", "<domain>a</domain>")
	time.Sleep(10 * time.Millisecond)
	e2, _ := Save(dir, "vm-b", "strip pinning", "test", "<domain>b</domain>")
	list, _ := List(dir)
	// assert len 2, list[0].Meta.VM == "vm-b" (newest first)
	// assert list[1].Meta.Op == "pin 2 vcpus to node 1"
	xml, _ := LoadXML(e1)
	// assert xml == "<domain>a</domain>"
	_ = e2
}

func TestListMissingDir(t *testing.T) {
	list, err := List(t.TempDir() + "/nope")
	// assert err == nil, len(list) == 0
}
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement** — stdlib only (os, encoding/json, sort).
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: xml backup save list load"`

---

### Task 7: model — snapshot and conflict flags

**Files:**
- Create: `internal/model/snapshot.go`
- Test: `internal/model/snapshot_test.go`

**Interfaces:**
- Consumes: `hostinfo.Topology`, `libvirtio.Domain`.
- Produces:

```go
package model

type FlagKind int

const (
	FlagUnpinned FlagKind = iota
	FlagPartialPin
	FlagNoMemBind
	FlagNodeMismatch
	FlagCrossNode
	FlagMemPressure
	FlagGPUUnknownNode
	FlagUnsupported
)

type Flag struct {
	Kind        FlagKind
	Cause       string // one sentence
	Consequence string // one sentence
}

type Device struct {
	Addr string
	Node int // -1 unknown
}

type VM struct {
	Name        string
	State       libvirtio.DomState
	VCPUs       int
	MemoryKiB   uint64
	Pins        map[int][]int
	MemNodes    []int
	Devices     []Device
	Flags       []Flag
	Unsupported bool
}

type ThreadUse struct {
	VMs     []string // sorted
	Pending []string // VMs claiming this thread via pending ops (filled by Project)
}

type Snapshot struct {
	Topo        *hostinfo.Topology
	VMs         []VM // sorted by name
	Use         map[int]ThreadUse
	BoundMemKiB map[int]uint64 // node -> sum of memory of VMs bound to it
}

func Build(topo *hostinfo.Topology, doms []libvirtio.Domain, pciNode func(addr string) int) *Snapshot
func (s *Snapshot) VM(name string) *VM // nil if absent
func (v *VM) HasFlag(k FlagKind) bool
func (v *VM) GPUNode() int // node of first device with known node, else -1
```

Flag rules (each Flag carries fixed Cause/Consequence text, exact wording implementer's choice but must be plain-language full sentences):
- FlagUnpinned: len(Pins)==0 && !Unsupported.
- FlagPartialPin: 0 < len(Pins) < VCPUs.
- FlagNoMemBind: len(Pins)>0 && MemNodes==nil.
- FlagCrossNode: pinned threads span >1 node.
- FlagNodeMismatch: (pinned threads' node-set != MemNodes when both exist) OR (GPUNode()>=0 and pinned node-set or MemNodes exclude it).
- FlagGPUUnknownNode: any Device.Node == -1.
- FlagMemPressure: VM bound (MemNodes non-nil) to node(s) where BoundMemKiB[node] > node MemTotalKiB. Flag every VM bound to the overcommitted node.
- FlagUnsupported: ParseErr != nil; VM gets Unsupported=true, name best-effort, no other flags.

- [ ] **Step 1: Failing tests**

Build a topo via a test helper `testTopo()` returning the 2-node/8-thread topology from Task 2 (constructed literally, no sysfs). Domains via `libvirtio.Fake` XML constants + `ParseDomainXML`. Table-driven:

```go
func TestFlags(t *testing.T) {
	cases := []struct {
		name  string
		xml   string
		pci   map[string]int
		want  []FlagKind
	}{
		{"unpinned", plainXML, nil, []FlagKind{FlagUnpinned}},
		{"pinned no membind", pinnedNoNumaXML, nil, []FlagKind{FlagNoMemBind}},
		{"gpu mismatch", gpuOnNode1PinnedNode0XML, map[string]int{"0000:81:00.0": 1}, []FlagKind{FlagNodeMismatch}},
		{"cross node spill", pinnedAcrossNodesXML, nil, []FlagKind{FlagCrossNode, FlagNoMemBind}},
		{"gpu unknown", gpuXML, map[string]int{"0000:81:00.0": -1}, []FlagKind{FlagGPUUnknownNode, FlagUnpinned}},
	}
	// build XML consts inline; assert exact flag-kind sets and that every Flag has non-empty Cause and Consequence
}

func TestMemPressure(t *testing.T) {
	// two VMs each 800 MemoryKiB bound to node 0 whose MemTotalKiB is 1000
	// assert both VMs have FlagMemPressure and snapshot.BoundMemKiB[0] == 1600
}

func TestThreadUse(t *testing.T) {
	// two VMs pin thread 4 -> Use[4].VMs == both names sorted
}
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement** — pure functions, no I/O.
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: allocation snapshot with conflict flags"`

---

### Task 8: model — pending queue, projection, drift hash

**Files:**
- Create: `internal/model/queue.go`
- Test: `internal/model/queue_test.go`

**Interfaces:**
- Produces:

```go
type OpKind int

const (
	OpPin OpKind = iota // set Pins + MemNode
	OpStrip
	OpRestore // define BackupXML verbatim
)

type PendingOp struct {
	Kind       OpKind
	VM         string
	Pins       map[int][]int
	MemNode    int    // -1 = leave numatune untouched (OpPin only)
	BackupXML  string // OpRestore only
	StagedHash string // sha256 hex of domain config XML at staging time
	Summary    string // human line, e.g. "vm-x: pin 4 vcpus -> node 1 threads 2,3,6,7; memory -> node 1"
}

func HashXML(xml string) string // sha256 hex

type Queue struct{ Ops []PendingOp }

func (q *Queue) Add(op PendingOp)   // replaces any existing op for the same VM
func (q *Queue) Remove(i int)
func (q *Queue) Clear()
func (q *Queue) Len() int

// Project returns a copy of s with ops applied to VM pin/membind state,
// Use recomputed (pending claims land in ThreadUse.Pending), flags recomputed.
func Project(s *Snapshot, doms map[string]*libvirtio.DomainConfig, ops []PendingOp) *Snapshot
```

Projection semantics: OpPin -> VM.Pins = op.Pins, MemNodes = [MemNode] (or unchanged when -1); OpStrip -> Pins empty, MemNodes nil; OpRestore -> parse BackupXML for pins/membind (ignore parse error: leave VM as-is but still mark pending). Threads claimed only via ops land in `Use[t].Pending`, not `Use[t].VMs`.

- [ ] **Step 1: Failing tests**

```go
func TestAddReplacesSameVM(t *testing.T) { /* two Adds same VM -> Len 1, second wins */ }

func TestProjectPin(t *testing.T) {
	// snapshot with unpinned plain-vm; op pins vcpu0->thread 2, MemNode 1
	// projected: VM("plain-vm").Pins == {0:[2]}, MemNodes == [1]
	// projected.Use[2].Pending == ["plain-vm"], original snapshot unchanged
	// projected VM no longer has FlagUnpinned
}

func TestProjectStrip(t *testing.T) { /* pinned gpu-vm + OpStrip -> projected unpinned, FlagUnpinned present */ }

func TestHashXMLStable(t *testing.T) { /* same input same hash, differs on change */ }
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement** — deep-copy snapshot (VMs slice, maps), apply ops, re-run the flag computation from Task 7 (factor `computeFlags` so Build and Project share it).
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: pending op queue with projection and drift hash"`

---

### Task 9: model — pin wizard proposal

**Files:**
- Create: `internal/model/propose.go`
- Test: `internal/model/propose_test.go`

**Interfaces:**
- Produces:

```go
type Proposal struct {
	Node      int
	Pins      map[int][]int // 1:1 vcpu -> single thread
	MemNode   int
	Rationale []string // plain-language sentences, why this node/threads
	Warnings  []string // e.g. not enough free full cores, sharing threads
}

// Propose picks placement for vm against the PROJECTED state (s already projected by caller).
func Propose(s *Snapshot, vmName string) (*Proposal, error)
```

Algorithm (deterministic, documented in code):
1. Candidate node: VM's GPUNode() if >= 0 (rationale sentence names the device addr and node). Else rank nodes by: enough free memory (MemTotalKiB - BoundMemKiB >= VM.MemoryKiB) first, then most fully-free cores (both siblings unused in Use, counting Pending as used). Ties: lower node ID.
2. Thread selection on chosen node: prefer fully-free cores, take sibling pairs in core order, assign vcpu 0..N-1 one thread each (fill both siblings of a core before next core).
3. Not enough free threads on node: fill remainder with least-used threads of same node (len(VMs)+len(Pending) ascending), add warning "sharing threads with: <vms>". Never propose spilling to another node; if node lacks that many threads total, error.
4. Free memory insufficient on forced GPU node: proceed (soft constraint) with warning naming the shortfall.
5. MemNode = chosen node always. Rationale always explains memory binding ("memory bound to node N so the kernel cannot allocate it on the other node").

- [ ] **Step 1: Failing tests**

```go
func TestProposeFollowsGPU(t *testing.T) {
	// gpu vm (device node 1), both nodes free -> Proposal.Node == 1, MemNode == 1
	// Rationale mentions "0000:81:00.0" and "node 1"
}

func TestProposePrefersFreeMemoryAndCores(t *testing.T) {
	// no gpu; node0 BoundMemKiB nearly full, node1 free -> Node == 1
}

func TestProposeSiblingPairs(t *testing.T) {
	// 4 vcpus on empty node with cores {2,6},{3,7} -> Pins {0:[2],1:[6],2:[3],3:[7]}
}

func TestProposeSharingWarning(t *testing.T) {
	// node has 4 threads, 2 already pinned by other vm, vm needs 4
	// -> 2 pins land on used threads, Warnings mentions sharing
}

func TestProposeTooBig(t *testing.T) {
	// vm vcpus > node thread count -> error
}
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement**
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: wizard placement proposal with numa and gpu awareness"`

---

### Task 10: apply — batch executor

**Files:**
- Create: `internal/apply/apply.go`
- Test: `internal/apply/apply_test.go`

**Interfaces:**
- Consumes: `libvirtio.Hypervisor`, `libvirtio.SetPinning/StripPinning/ParseDomainXML`, `backup.Save`, `model.PendingOp`, `model.HashXML`.
- Produces:

```go
package apply

type Result struct {
	Op         model.PendingOp
	BackupPath string
	Applied    bool
	Drifted    bool
	Err        error
}

// CheckDrift returns names of VMs whose current config XML hash differs from the op's StagedHash.
func CheckDrift(h libvirtio.Hypervisor, ops []model.PendingOp) ([]string, error)

// Run executes ops sequentially. Per op: fetch XML, re-check drift (Drifted+skip, continue others),
// backup.Save, build new XML (SetPinning / StripPinning / BackupXML), h.Define, verify by re-fetching
// and comparing parsed pins/membind. First hard error stops remaining ops (they return Applied=false, Err=nil).
func Run(h libvirtio.Hypervisor, backupDir, version string, ops []model.PendingOp) []Result
```

Verify step: after Define, `h.DomainXML`, ParseDomainXML, compare `VCPUPins` and `MemNodes` against intent (OpStrip: both empty/nil; OpPin: equal to op; OpRestore: equal to parsed BackupXML values). Mismatch -> Err on that result, stop.

- [ ] **Step 1: Failing tests** (all against `libvirtio.Fake`)

```go
func TestRunPinHappyPath(t *testing.T) {
	// Fake with plain-vm; op OpPin StagedHash=HashXML(current)
	// -> Applied true, backup file exists in tempdir containing original XML,
	//    fake's stored XML now has pins, Result.Err nil
}

func TestRunDriftSkips(t *testing.T) {
	// StagedHash "stale" -> Drifted true, Applied false, fake XML unchanged, no backup written
}

func TestRunStopsOnDefineError(t *testing.T) {
	// two ops, Fake.DefineErr set -> first Result.Err != nil, second Applied false Err nil
	// backup for first op EXISTS (backup before write)
}

func TestRunRestore(t *testing.T) {
	// OpRestore with BackupXML = original unpinned xml over a pinned domain -> fake XML unpinned after
}
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement**
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: batch apply with backup drift check and verify"`

---

### Task 11: TUI skeleton — tabs, badge, edit mode, status bar

**Files:**
- Create: `internal/tui/app.go`, `internal/tui/styles.go`
- Test: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `model.Snapshot/Queue/Project`, `libvirtio.Hypervisor`.
- Produces:

```go
package tui

type ScanFn func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error)

type App struct {
	// exported for tests where noted; construct via New
}

func New(hv libvirtio.Hypervisor, scan ScanFn, backupDir, version string) *App
// App implements tea.Model.
```

Behavior contract (all tested):
- Tabs: Overview, CPU Map, VMs, Pending, Backups. Keys `1`-`5` and `tab`/`shift+tab` switch; mouse clicks on tab labels switch (tea.MouseMsg zones may be simplified: clickable row Y=0, X ranges recorded at render).
- Header line: `pindergarten <version>  <uri>` + right-aligned badge: `READ ONLY` (red bg) or `EDIT` (orange/yellow bg). Badge glyphless pure ASCII text.
- `e` toggles edit mode: if `hv.ReadOnly()` returns true, stays read-only, status line shows the reason. If backup dir unwritable (probe: create+delete `.probe` file), stays read-only with reason. Otherwise shows confirm modal ("Enable edit mode? [y/n]"); `y` enables.
- `e` while in edit mode returns to read-only (only when queue empty; else status "discard or apply pending ops first").
- `q`/`ctrl+c`: quit; if queue non-empty, confirm modal warns pending ops are discarded.
- Status bar bottom: `N pending ops  [a]pply  [d]iscard  [e]dit  [q]uit` (contextual).
- Startup issues `scan` via tea.Cmd; scanDoneMsg{snap, doms, err} stores raw snapshot; every render uses `model.Project(snap, doms, queue.Ops)`.
- `r` triggers rescan.

- [ ] **Step 1: Failing tests**

Helper:

```go
func testApp(t *testing.T, ro bool) *App {
	f := &libvirtio.Fake{ConnURI: "test:///x", XML: map[string]string{"plain-vm": plainVMXML}, RO: ro, ROReason: "no write perm"}
	scan := func() (*model.Snapshot, map[string]*libvirtio.DomainConfig, error) { /* build from fake + testTopo() */ }
	return New(f, scan, t.TempDir(), "test")
}
```

Tests drive `Update` directly with `tea.KeyMsg` and assert on `View()` strings:

```go
func TestStartsReadOnly(t *testing.T)      // View contains "READ ONLY"
func TestEditUnlockConfirm(t *testing.T)   // 'e' -> View contains "Enable edit mode"; 'y' -> View contains "EDIT", not "READ ONLY"
func TestEditBlockedWhenRO(t *testing.T)   // ro fake: 'e' -> still READ ONLY, View contains "no write perm"
func TestTabSwitch(t *testing.T)           // '3' -> VMs tab marker active
func TestQuitConfirmWithPending(t *testing.T) // queue has op, 'q' -> confirm text visible, 'n' -> still running
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement** — `go get github.com/charmbracelet/bubbletea github.com/charmbracelet/lipgloss github.com/charmbracelet/bubbles`. Tab views render placeholder text for tabs not yet built ("coming in later task" NOT allowed to survive the full plan — Tasks 12-15 replace them; placeholder here is scaffold within this plan, acceptable only between tasks). Keep confirm modal as a tiny reusable struct `confirm{prompt string, yes func() tea.Cmd}` in app.go.
- [ ] **Step 4: Run, verify PASS** — `go test ./internal/tui/ -v`
- [ ] **Step 5: Commit** — `git commit -m "feat: tui skeleton with tabs edit mode and badges"`

---

### Task 12: TUI — Overview and CPU Map tabs

**Files:**
- Create: `internal/tui/views.go`
- Modify: `internal/tui/app.go` (route render + cursor keys)
- Test: `internal/tui/views_test.go`

**Interfaces:**
- Consumes: projected `model.Snapshot`.
- Produces: `renderOverview(s *model.Snapshot, w int) string`, `renderCPUMap(s *model.Snapshot, cursor int, w int) string`, `cpuMapDetail(s *model.Snapshot, coreIdx int) string` (pure functions, unit-testable without tea).

Rendering contract:
- Overview: one card per node: `node 0  mem 187.4G free 12.1G  threads 96 (80 pinned)  bound-vm-mem 190.0G OVER`, GPUs on node (addr list), VM names bound there. Overcommit marked `OVER` in red.
- CPU Map: per node section, one cell per core, 32 cores per row. Cell = 2 glyphs (one per sibling thread): `●` pinned solid, `○` free, `◐` shared, pending claims rendered in distinct color (lipgloss style, not glyph). Cursor = reverse video cell. Left/right/up/down move cursor across cores; detail panel below shows selected core: thread IDs + pinning VMs + pending claimants.
- Colors via lipgloss adaptive styles; glyphs ONLY as \u escapes (ASCII source rule).

- [ ] **Step 1: Failing tests**

```go
func TestOverviewShowsPressure(t *testing.T)  // snapshot with overcommitted node -> output contains "OVER"
func TestCPUMapMarksPinned(t *testing.T)      // pinned thread's core cell contains "●"
func TestCPUMapDetail(t *testing.T)           // detail for core with pinned thread names the VM
func TestCursorMoves(t *testing.T)            // app on tab 2, right arrow -> detail shows next core
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement**
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: overview and cpu map tabs"`

---

### Task 13: TUI — VMs tab, strip action, pin wizard

**Files:**
- Create: `internal/tui/wizard.go`
- Modify: `internal/tui/views.go` (VMs table + detail), `internal/tui/app.go` (routing)
- Test: `internal/tui/wizard_test.go`

**Interfaces:**
- Consumes: `model.Propose`, `model.Queue.Add`, `model.HashXML`, `libvirtio.Hypervisor.DomainXML`.
- Produces: VMs tab + wizard flow inside App.

Behavior contract:
- VMs tab: table (bubbles/table or hand-rolled): name, state (`running`/`shut off`/`other`), vcpus, mem, pins summary (`8 pinned -> node 1` / `unpinned` / `partial`), mem node, gpu node, flag badges (`[!]` per flag, ASCII). Up/down selects; detail panel shows full flag sentences (Cause + Consequence per flag) — this is the tooltip requirement.
- Edit mode only (keys inert + status hint otherwise): `p` opens wizard for selected VM, `s` stages strip: fetch current XML via hv.DomainXML for StagedHash, `queue.Add(PendingOp{Kind: OpStrip, ...})`, status shows staged summary. Unsupported VMs: both keys refused with reason.
- Wizard screens (state machine in wizard.go):
  1. Proposal screen: mini CPU map of target node with proposed threads highlighted, Rationale sentences, Warnings. Keys: `enter` accept, `m` manual adjust, `esc` cancel.
  2. Manual adjust: cursor over node's cores, `space` toggles thread pair selection, running count `selected 6/8`; `enter` accepts when count == VCPUs (else status warning), `esc` back.
  3. Accept -> stage `PendingOp{Kind: OpPin, Pins, MemNode, StagedHash, Summary}`.
- Pending claims from earlier queued ops appear as used in the wizard (caller passes projected snapshot to Propose — already guaranteed since App always projects).

- [ ] **Step 1: Failing tests**

```go
func TestVMsTabShowsFlags(t *testing.T)      // unpinned vm row shows "[!]", detail shows Cause sentence
func TestStripStagesOp(t *testing.T)         // edit mode, select pinned vm, 's' -> queue.Len()==1, Kind OpStrip, StagedHash == HashXML(fake xml)
func TestStripRefusedReadOnly(t *testing.T)  // no edit mode: 's' -> queue empty, status hint
func TestWizardAcceptStagesPin(t *testing.T) // 'p','enter' -> OpPin staged, Pins == Propose result, MemNode == proposal node
func TestWizardManualCount(t *testing.T)     // 'p','m', toggle too few, 'enter' -> not staged, warning; toggle right count -> staged
func TestSecondWizardSeesPending(t *testing.T) // stage pin for vm1 taking threads 2,6; wizard vm2 proposal avoids 2,6
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement**
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: vms tab with strip action and pin wizard"`

---

### Task 14: TUI — pending tab, apply flow, drift handling; backups tab

**Files:**
- Create: `internal/tui/pending.go`, `internal/tui/backups.go`
- Modify: `internal/tui/app.go`
- Test: `internal/tui/pending_test.go`

**Interfaces:**
- Consumes: `apply.CheckDrift`, `apply.Run`, `backup.List/LoadXML`, `model.Queue`.
- Produces: complete mutation UX.

Behavior contract:
- Pending tab: numbered list of op Summaries; `x` removes selected op; `a` (edit mode, queue non-empty, any tab) opens apply review: every op with exact effect lines ("backup will be written first; takes effect on next VM boot"). `y` confirms.
- Apply sequence as tea.Cmd: first `apply.CheckDrift`; drifted VMs -> drift screen listing them, per-op choice: `d` discard op, `w` re-open wizard for it (op removed, wizard started against fresh rescan). No writes happen until drift list is empty.
- Clean check -> `apply.Run`; results screen: per op OK/FAILED(+error)/SKIPPED, backup paths shown; then rescan. Queue cleared of applied ops only.
- Backups tab: `backup.List` entries: time, VM, op description. `enter` -> diff view backup XML vs current `hv.DomainXML` (simple unified-ish line diff, own tiny func `diffLines(a, b string) string` — stdlib only). `R` (edit mode) stages `OpRestore{BackupXML: LoadXML(e), StagedHash: HashXML(current)}`.
- Read-only mode: apply/restore keys inert with status hint; browsing works.

- [ ] **Step 1: Failing tests**

```go
func TestApplyHappyPath(t *testing.T)   // staged pin, 'a','y' -> fake Defined has 1 entry, backup file exists, queue empty, results show "OK"
func TestApplyDriftBlocks(t *testing.T) // stage op, mutate fake XML behind app's back, 'a' -> drift screen names vm, fake.Defined empty; 'd' discards -> queue empty
func TestRemovePendingOp(t *testing.T)  // pending tab 'x' -> queue empty
func TestRestoreStagesOp(t *testing.T)  // seed backup dir via backup.Save, backups tab, 'R' -> OpRestore staged with BackupXML content
func TestDiffLines(t *testing.T)        // pure diff func marks added/removed lines
```

- [ ] **Step 2: Run, verify FAIL**
- [ ] **Step 3: Implement**
- [ ] **Step 4: Run, verify PASS**
- [ ] **Step 5: Commit** — `git commit -m "feat: pending apply flow with drift gate and backups tab"`

---

### Task 15: main wiring, README, builder container

**Files:**
- Modify: `cmd/pindergarten/main.go`
- Create: `README.md`, `Containerfile.builder`

**Interfaces:**
- Consumes: everything.

- [ ] **Step 1: Wire main**

```go
func main() {
	uri := flag.String("c", "qemu:///system", "libvirt connection URI")
	backupDir := flag.String("backup-dir", defaultBackupDir(), "backup directory")
	flag.Parse()

	hv, err := libvirtio.Connect(*uri)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to %s: %v\nhint: run as root or add your user to the libvirt group\n", *uri, err)
		os.Exit(1)
	}
	defer hv.Close()

	sysfs := "/sys"
	if _, err := os.Stat(sysfs + "/devices/system/node"); err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", sysfs, err)
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
		snap := model.Build(topo, doms, func(addr string) int { return hostinfo.PCINumaNode(sysfs, addr) })
		return snap, cfgs, nil
	}

	p := tea.NewProgram(tui.New(hv, scan, *backupDir, version), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Containerfile.builder**

```dockerfile
FROM rockylinux:9
RUN dnf -y install golang libvirt-devel git make && dnf clean all
WORKDIR /src
CMD ["make", "build"]
```

- [ ] **Step 3: README.md**

Sections (write full prose, normal English): what it is (one paragraph), install/build (`dnf install libvirt-devel golang golangci-lint`, `make init && make build`, `make release` for container build), usage (`pindergarten [-c URI] [--backup-dir PATH]`, read-only by default, `e` unlocks edit mode), how changes apply (config-only, next VM boot), backups location + SELinux note (`/var/lib/pindergarten/backups`, keep default context of /var/lib), keybindings table, why NUMA pinning matters (three sentences).

- [ ] **Step 4: Verify** — `make lint && make test && make build && ./pindergarten -h`. Manual smoke on a libvirt host: `./pindergarten -c test:///default` (libvirt's built-in mock driver) — tabs render, read-only badge shown, no crash. Optional: `make release` if podman present.

- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat: wire cli, readme, builder container"`

---

## Self-Review (done at plan time)

- Spec coverage: every spec section maps to a task — topology (2), parse (3), modify (4), connection+RO fallback (5), backups (6), flags/tooltip text (7), queue/projection/drift (8), wizard (9), apply+verify (10), tabs/badge/edit-mode (11), overview+cpumap scaling (12), vms+wizard UX (13), pending/apply/drift UI + backups tab + revert-as-op (14), CLI flags/startup checks/README/container (15). Hooks + ASCII rule (1).
- Type consistency: `ScanFn` returns cfgs map consumed by `model.Project(s, doms, ops)`; `Propose(s, vmName)` takes projected snapshot (App projects before calling). `PCINumaNode` returns int (no error) — Build consumes `func(string) int`.
- No placeholders: TUI placeholder views exist only between Tasks 11-14 and are replaced within the plan.
