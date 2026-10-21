package vms

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strataos/internal/sys"
)

// Progress reports the advance of a long operation (0-100) and log lines.
type Progress struct {
	Set func(float64)
	Log func(string, ...any)
}

func (p Progress) set(v float64) {
	if p.Set != nil {
		p.Set(v)
	}
}

func (p Progress) log(f string, a ...any) {
	if p.Log != nil {
		p.Log(f, a...)
	}
}

// scaled maps the 0-100 progress of one step into [from, to] of the whole.
func (p Progress) scaled(from, to float64) Progress {
	return Progress{Log: p.Log, Set: func(v float64) { p.set(from + (to-from)*v/100) }}
}

var qemuImgPct = regexp.MustCompile(`\((\d+(?:\.\d+)?)/100%\)`)

// pctWriter reads "(12.34/100%)" progress lines of qemu-img -p (split on \r).
type pctWriter struct {
	buf bytes.Buffer
	p   Progress
}

func (w *pctWriter) Write(b []byte) (int, error) {
	for _, c := range b {
		if c != '\r' && c != '\n' {
			w.buf.WriteByte(c)
			continue
		}
		l := w.buf.String()
		w.buf.Reset()
		if m := qemuImgPct.FindStringSubmatch(l); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			w.p.set(v)
		} else if strings.TrimSpace(l) != "" {
			w.p.log("%s", strings.TrimSpace(l))
		}
	}
	return len(b), nil
}

// imgFormats are the source formats qemu-img is asked to read (never guessed
// for raw files: a raw image could pretend to be a qcow2 with a backing file).
var imgFormats = map[string]bool{"qcow2": true, "raw": true, "vmdk": true, "vpc": true, "vhdx": true, "vdi": true, "qed": true}

// convertImage copies src (format srcFmt, "" = detected) into a new qcow2 dst,
// flattening any backing chain. shared reads an image open in a running VM.
func convertImage(ctx context.Context, src, srcFmt, dst string, shared bool, p Progress) error {
	args := []string{"convert", "-p", "-O", "qcow2"}
	if srcFmt != "" {
		if !imgFormats[srcFmt] {
			return fmt.Errorf("unsupported disk format %s", srcFmt)
		}
		args = append(args, "-f", srcFmt)
	}
	if shared {
		args = append(args, "-U")
	}
	args = append(args, "--", src, dst)
	p.log("qemu-img %s", strings.Join(args, " "))
	_, err := sys.RunOpts(ctx, sys.Opts{Timeout: 24 * time.Hour, Output: &pctWriter{p: p}}, "qemu-img", args...)
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	p.set(100)
	return nil
}

type imgInfo struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
	BackingFile string `json:"backing-filename"`
}

func imageInfo(path string) (imgInfo, error) {
	var i imgInfo
	out, err := sys.Run("qemu-img", "info", "--force-share", "--output=json", "--", path)
	if err != nil {
		return i, err
	}
	err = json.Unmarshal([]byte(out), &i)
	return i, err
}

// resizeImage grows a disk image to gb GiB (never shrinks).
func resizeImage(path string, gb int) error {
	info, err := imageInfo(path)
	if err != nil {
		return err
	}
	want := int64(gb) << 30
	if gb <= 0 || want <= info.VirtualSize {
		return nil
	}
	_, err = sys.Run("qemu-img", "resize", "-f", "qcow2", "--", path, strconv.FormatInt(want, 10))
	return err
}

// randomMAC returns a locally administered address in QEMU's 52:54:00 range.
func randomMAC() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
}

// copyFile copies a regular file (NVRAM, small files).
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// defineXML defines a domain from an XML document.
func defineXML(doc string) error {
	f, err := os.CreateTemp("", "strata-vm-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(doc); err != nil {
		f.Close()
		return err
	}
	f.Close()
	_, err = virsh("define", f.Name())
	return err
}
