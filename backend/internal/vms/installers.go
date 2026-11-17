package vms

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Installer is an official installation image (ISO) StrataOS can download
// straight from its editor, so no ISO has to be fetched by hand first.
// Microsoft publishes stable fwlink redirects for its evaluation ISOs but no
// checksum file: the downloads go over HTTPS and are used as delivered.
type Installer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"-"`
	File   string `json:"file"`
	SizeGB int    `json:"sizeGb"`
	Cached bool   `json:"cached"`
}

func installers() []Installer {
	return []Installer{
		{ID: "winserver2025", Name: "Windows Server 2025 (evaluation, 180 days)",
			URL: "https://go.microsoft.com/fwlink/?linkid=2293312", File: "Windows-Server-2025-EVAL-x64.iso", SizeGB: 6},
		{ID: "winserver2022", Name: "Windows Server 2022 (evaluation, 180 days)",
			URL: "https://go.microsoft.com/fwlink/p/?LinkID=2195174", File: "Windows-Server-2022-EVAL-x64.iso", SizeGB: 5},
	}
}

func installer(id string) (Installer, error) {
	for _, i := range installers() {
		if i.ID == id {
			return i, nil
		}
	}
	return Installer{}, fmt.Errorf("unknown installer %q", id)
}

// isoDirName is the folder, inside a volume, holding the downloaded ISOs.
const isoDirName = "ISOs"

// Installers lists the catalog; Cached tells whether the ISO is already in a
// volume's ISOs folder.
func Installers() []Installer {
	list := installers()
	for i, c := range list {
		if m, _ := filepath.Glob(filepath.Join("/srv/strata", "*", isoDirName, c.File)); len(m) > 0 {
			list[i].Cached = true
		}
	}
	return list
}

// EnsureInstaller downloads an installer ISO into <storage>/ISOs once, and
// returns its path.
func EnsureInstaller(ctx context.Context, id, storage string, p Progress) (string, error) {
	ins, err := installer(id)
	if err != nil {
		return "", err
	}
	dir, err := insideStrata(storage)
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, isoDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ins.File)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 64<<20 {
		p.log("%s is already there", ins.File)
		p.set(100)
		return path, nil
	}
	p.log("Downloading %s from Microsoft", ins.Name)
	resp, err := httpGet(ctx, ins.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 1<<20)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(part)
				return "", werr
			}
			done += int64(n)
			if total > 0 && time.Since(last) > time.Second {
				p.set(float64(done) * 100 / float64(total))
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			_ = os.Remove(part)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, path); err != nil {
		return "", err
	}
	p.log("%s downloaded (%.1f GiB)", ins.File, float64(done)/(1<<30))
	p.set(100)
	return path, nil
}
