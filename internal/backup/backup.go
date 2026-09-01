// Package backup saves and lists domain XML snapshots taken before
// pindergarten writes changes to libvirt.
package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Meta describes one backup entry.
type Meta struct {
	Time        time.Time `json:"time"`
	VM          string    `json:"vm"`
	Op          string    `json:"op"`
	ToolVersion string    `json:"tool_version"`
}

// Entry is a saved backup: the domain XML file plus its metadata.
type Entry struct {
	XMLPath string
	Meta    Meta
}

// filenameTimestamp formats t for use in a filename, avoiding ':' for
// SELinux/scp friendliness.
const filenameTimestamp = "20060102T150405.000000000Z"

// sanitize replaces path separators in vm so it is safe to use in a
// filename.
func sanitize(vm string) string {
	return strings.ReplaceAll(vm, "/", "_")
}

// Save writes the domain xml and a JSON metadata sidecar to dir, creating
// dir if it does not exist.
func Save(dir, vm, opDesc, toolVersion, xml string) (Entry, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Entry{}, fmt.Errorf("backup: create dir %q: %w", dir, err)
	}

	now := time.Now().UTC()
	name := now.Format(filenameTimestamp) + "_" + sanitize(vm) + ".xml"
	xmlPath := filepath.Join(dir, name)
	meta := Meta{Time: now, VM: vm, Op: opDesc, ToolVersion: toolVersion}

	if err := os.WriteFile(xmlPath, []byte(xml), 0o600); err != nil {
		return Entry{}, fmt.Errorf("backup: write xml %q: %w", xmlPath, err)
	}

	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return Entry{}, fmt.Errorf("backup: marshal meta: %w", err)
	}
	if err := os.WriteFile(xmlPath+".json", metaBytes, 0o600); err != nil {
		return Entry{}, fmt.Errorf("backup: write meta %q: %w", xmlPath+".json", err)
	}

	return Entry{XMLPath: xmlPath, Meta: meta}, nil
}

// List returns all backups in dir, newest first. A missing dir yields an
// empty slice and a nil error.
func List(dir string) ([]Entry, error) {
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: read dir %q: %w", dir, err)
	}

	entries := []Entry{}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		jsonPath := filepath.Join(dir, name)
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			// A single unreadable sidecar must not hide every other backup.
			continue
		}
		var meta Meta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		xmlPath := strings.TrimSuffix(jsonPath, ".json")
		entries = append(entries, Entry{XMLPath: xmlPath, Meta: meta})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Meta.Time.After(entries[j].Meta.Time)
	})
	return entries, nil
}

// LoadXML reads the domain XML for a backup entry.
func LoadXML(e Entry) (string, error) {
	data, err := os.ReadFile(e.XMLPath)
	if err != nil {
		return "", fmt.Errorf("backup: read xml %q: %w", e.XMLPath, err)
	}
	return string(data), nil
}
