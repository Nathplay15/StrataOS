package backup

// Time Machine destinations: the Macs backing up to a share are the
// sparsebundles at its root (or one level below).

import (
	"encoding/xml"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TMBackup is one Mac's Time Machine backup on a share.
type TMBackup struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	LastBackup time.Time `json:"lastBackup"`
	MachineID  string    `json:"machineId,omitempty"`
	Model      string    `json:"model,omitempty"`
}

func isBundle(name string) bool {
	return strings.HasSuffix(name, ".sparsebundle") || strings.HasSuffix(name, ".backupbundle")
}

// TimeMachineBackups lists the backup bundles found in dir, newest first.
func TimeMachineBackups(dir string) []TMBackup {
	out := []TMBackup{}
	var bundles []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") && !isBundle(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if isBundle(e.Name()) {
			bundles = append(bundles, p)
			continue
		}
		// One level below: shares with a folder per user.
		sub, _ := os.ReadDir(p)
		for _, s := range sub {
			if s.IsDir() && isBundle(s.Name()) {
				bundles = append(bundles, filepath.Join(p, s.Name()))
			}
		}
	}
	for _, b := range bundles {
		out = append(out, readBundle(b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastBackup.After(out[j].LastBackup) })
	return out
}

func readBundle(p string) TMBackup {
	name := filepath.Base(p)
	b := TMBackup{Name: strings.TrimSuffix(strings.TrimSuffix(name, ".sparsebundle"), ".backupbundle"), Path: p}
	// WalkDir does not follow symbolic links.
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil && d.Type().IsRegular() {
			b.Size += fi.Size()
			if fi.ModTime().After(b.LastBackup) {
				b.LastBackup = fi.ModTime()
			}
		}
		return nil
	})
	if v := plistValues(filepath.Join(p, "com.apple.TimeMachine.MachineID.plist")); v != nil {
		b.MachineID = v["com.apple.backupd.HostUUID"]
		b.Model = v["com.apple.backupd.ModelID"]
	}
	// The completion dates of the backups, when the Mac records them.
	if v := plistValues(filepath.Join(p, "com.apple.TimeMachine.SnapshotHistory.plist")); v != nil {
		if t, err := time.Parse(time.RFC3339, v["_lastDate"]); err == nil {
			b.LastBackup = t
		}
	}
	return b
}

// plistValues reads the string values of an XML property list by key, plus
// the newest <date> as "_lastDate". Nil when the file is missing or invalid.
func plistValues(file string) map[string]string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parsePlist(io.LimitReader(f, 1<<20))
}

func parsePlist(r io.Reader) map[string]string {
	dec := xml.NewDecoder(r)
	out := map[string]string{}
	var key, elem string
	var last time.Time
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			elem = t.Name.Local
			if elem != "key" && elem != "string" {
				key = "" // a value of another type
			}
		case xml.EndElement:
			elem = ""
		case xml.CharData:
			v := strings.TrimSpace(string(t))
			switch elem {
			case "key":
				key = v
			case "string":
				if key != "" {
					out[key] = v
					key = ""
				}
			case "date":
				if d, err := time.Parse(time.RFC3339, v); err == nil && d.After(last) {
					last = d
				}
			}
		}
	}
	if !last.IsZero() {
		out["_lastDate"] = last.Format(time.RFC3339)
	}
	return out
}
