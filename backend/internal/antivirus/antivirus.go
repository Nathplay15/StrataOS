// Package antivirus scans the NAS storage with ClamAV and keeps infected files
// in a quarantine they can be restored from.
package antivirus

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"strataos/internal/sys"
)

var (
	QuarantineDir = "/var/lib/strata/quarantine"
	StorageRoot   = "/srv/strata"
)

// ErrInfected matches (errors.Is) the error of a scan that found infected files.
var ErrInfected = errors.New("infected files found")

// InfectedError carries the readable summary of an infected scan.
type InfectedError struct{ Msg string }

func (e *InfectedError) Error() string        { return e.Msg }
func (e *InfectedError) Is(target error) bool { return target == ErrInfected }

func Installed() bool { return sys.Has("clamscan") }

// Status describes the signature database.
type Status struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"` // "ClamAV 1.4.3/27800/Fri Oct 16 …"
}

func GetStatus() Status {
	st := Status{Installed: Installed()}
	if st.Installed {
		out, _ := sys.Run("clamscan", "--version")
		st.Version = strings.TrimSpace(out)
	}
	return st
}

// ValidTarget accepts "all" or a folder inside the storage.
func ValidTarget(t string) (string, error) {
	if t == "" || t == "all" {
		return StorageRoot, nil
	}
	c := filepath.Clean(t)
	if !strings.HasPrefix(c, StorageRoot+"/") || strings.ContainsAny(c, "\n\r") {
		return "", fmt.Errorf("choose a folder inside the NAS storage")
	}
	return c, nil
}

// Item is a quarantined file.
type Item struct {
	ID    string    `json:"id"`
	Path  string    `json:"path"` // original location
	Virus string    `json:"virus"`
	Size  int64     `json:"size"`
	Date  time.Time `json:"date"`
	UID   int       `json:"uid"`
	GID   int       `json:"gid"`
	Mode  uint32    `json:"mode"`
}

// Scan scans target recursively. With quarantine, infected files are moved
// away. It returns a summary; err wraps ErrInfected when something was found.
func Scan(ctx context.Context, target string, quarantine bool, logf func(string, ...any)) (string, error) {
	if !Installed() {
		return "", fmt.Errorf("ClamAV is not installed")
	}
	root, err := ValidTarget(target)
	if err != nil {
		return "", err
	}
	logf("Scanning %s…", root)
	args := []string{"--recursive", "--infected", "--no-summary", "--stdout", "--cross-fs=yes",
		"--max-filesize=500M", "--max-scansize=1000M", "--exclude-dir=^" + QuarantineDir, root}
	pr, pw := io.Pipe()
	type hit struct{ path, virus string }
	var hits []hit
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			l := sc.Text()
			if p, v, ok := strings.Cut(l, ": "); ok && strings.HasSuffix(v, " FOUND") {
				hits = append(hits, hit{p, strings.TrimSuffix(v, " FOUND")})
				logf("Infected: %s (%s)", p, strings.TrimSuffix(v, " FOUND"))
			}
		}
		close(done)
	}()
	// clamscan exits 1 when it finds something: not an error for us.
	_, runErr := sys.RunOpts(ctx, sys.Opts{Timeout: 48 * time.Hour, Output: pw, AllowCodes: []int{1}}, "nice", append([]string{"-n", "19", "ionice", "-c3", "clamscan"}, args...)...)
	pw.Close()
	<-done
	if runErr != nil {
		return "", fmt.Errorf("scan failed: %w", runErr)
	}
	if len(hits) == 0 {
		return "No virus found in " + root, nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, fmt.Sprintf("%s (%s)", h.path, h.virus))
		if quarantine {
			if err := moveToQuarantine(h.path, h.virus); err != nil {
				logf("Cannot quarantine %s: %v", h.path, err)
			}
		}
	}
	what := "left in place"
	if quarantine {
		what = "quarantined"
	}
	return "", &InfectedError{fmt.Sprintf("%d infected file(s) %s: %s", len(hits), what, strings.Join(names, ", "))}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func moveToQuarantine(path, virus string) error {
	// Only regular files inside the storage; never follow a symlink.
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || !strings.HasPrefix(filepath.Clean(path), StorageRoot+"/") {
		return fmt.Errorf("not a regular file in the storage")
	}
	if err := os.MkdirAll(QuarantineDir, 0o700); err != nil {
		return err
	}
	it := Item{ID: newID(), Path: path, Virus: virus, Size: fi.Size(), Date: time.Now(), Mode: uint32(fi.Mode().Perm())}
	if st, ok := sysStat(fi); ok {
		it.UID, it.GID = st[0], st[1]
	}
	dst := filepath.Join(QuarantineDir, it.ID)
	if err := os.Rename(path, dst); err != nil {
		// Another file system: copy then remove.
		if err := copyFile(path, dst); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	_ = os.Chmod(dst, 0o000)
	meta, _ := json.Marshal(it)
	return os.WriteFile(dst+".json", meta, 0o600)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// List returns the quarantined files, newest first.
func List() []Item {
	entries, _ := os.ReadDir(QuarantineDir)
	out := []Item{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var it Item
		if b, err := os.ReadFile(filepath.Join(QuarantineDir, e.Name())); err == nil && json.Unmarshal(b, &it) == nil {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out
}

func get(id string) (Item, error) {
	for _, it := range List() {
		if it.ID == id {
			return it, nil
		}
	}
	return Item{}, fmt.Errorf("not in quarantine")
}

// Delete removes a quarantined file for good.
func Delete(id string) error {
	it, err := get(id)
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(QuarantineDir, it.ID))
	return os.Remove(filepath.Join(QuarantineDir, it.ID+".json"))
}

// Restore puts a file back where it was (it must not exist there any more).
func Restore(id string) (string, error) {
	it, err := get(id)
	if err != nil {
		return "", err
	}
	dst := filepath.Clean(it.Path)
	if !strings.HasPrefix(dst, StorageRoot+"/") {
		return "", fmt.Errorf("invalid original path")
	}
	// The parent must still be a real folder (not a symlink planted meanwhile).
	if real, err := filepath.EvalSymlinks(filepath.Dir(dst)); err != nil || real != filepath.Dir(dst) {
		return "", fmt.Errorf("the original folder no longer exists")
	}
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("a file already exists at %s", dst)
	}
	src := filepath.Join(QuarantineDir, it.ID)
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	_ = os.Chown(dst, it.UID, it.GID)
	_ = os.Chmod(dst, os.FileMode(it.Mode))
	_ = os.Remove(src)
	_ = os.Remove(src + ".json")
	return dst, nil
}
