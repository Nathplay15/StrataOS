package archive

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strataos/internal/sys"
)

var errStopped = errors.New("stopped")

// sandboxed builds a command run as the sandbox user, killed with ctx.
func sandboxed(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C.UTF-8"}
	cmd.SysProcAttr = sys.Sandboxed()
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// decompress streams f through xz or zstd (sandboxed, reading the open file
// on stdin). wait reaps the process.
func decompress(ctx context.Context, f *os.File, format string) (io.Reader, func() error, error) {
	tool := "xz"
	if strings.HasSuffix(format, "zst") {
		tool = "zstd"
	}
	if !sys.Has(tool) {
		return nil, nil, fmt.Errorf("%s is not installed", tool)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := sandboxed(ctx, tool, "-dc")
	cmd.Stdin = f
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, err
	}
	wait := func() error {
		cancel() // stops the tool when the reader is done early
		_, _ = io.Copy(io.Discard, out)
		err := cmd.Wait()
		if err != nil && ctx.Err() != nil {
			return errStopped
		}
		if err != nil {
			return fmt.Errorf("%s: %s", tool, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	return bufio.NewReaderSize(out, 1<<16), wait, nil
}

// SevenZipInstalled reports whether 7-Zip (7z/RAR support) is present.
func SevenZipInstalled() bool { return sevenZip() != "" }

func sevenZip() string {
	for _, b := range []string{"7z", "7zz"} {
		if sys.Has(b) {
			return b
		}
	}
	return ""
}

// SevenZipCopy is set by the caller: 7-Zip needs a path it can open as the
// sandbox user, so 7z/RAR archives are first copied to a private folder.
// It returns that path and a cleanup function.
var SevenZipCopy func(f *os.File) (string, func(), error)

// SevenZipMax bounds the size of the 7z and RAR archives handled (copied first).
const SevenZipMax = 4 << 30

// parseSevenZipList reads the output of 7z l -slt.
func parseSevenZipList(out string) []Entry {
	var list []Entry
	var cur *Entry
	inBody := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "----------") {
			inBody = true
			continue
		}
		if !inBody {
			continue
		}
		if line == "" {
			cur = nil
			continue
		}
		k, v, ok := strings.Cut(line, " = ")
		if !ok {
			k, v, ok = strings.Cut(line, " =")
			if !ok {
				continue
			}
		}
		if k == "Path" {
			list = append(list, Entry{Name: v})
			cur = &list[len(list)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch k {
		case "Size":
			cur.Size, _ = strconv.ParseInt(v, 10, 64)
		case "Packed Size":
			cur.Packed, _ = strconv.ParseInt(v, 10, 64)
		case "Folder":
			cur.Dir = v == "+"
		case "Modified":
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.SplitN(v, ".", 2)[0], time.Local); err == nil {
				cur.ModTime = t
			}
		case "Encrypted":
			cur.Encrypted = v == "+"
		case "Symbolic Link", "Link":
			if v != "" {
				cur.Skipped = "link"
			}
		case "Attributes":
			// "D drwxr-xr-x", "A lrwxrwxrwx": Windows flags, then the Unix mode.
			f := strings.Fields(v)
			if len(f) > 0 && strings.HasPrefix(f[0], "D") {
				cur.Dir = true
			}
			if len(f) > 1 && strings.HasPrefix(f[len(f)-1], "l") {
				cur.Skipped = "link"
			}
		}
	}
	for i := range list {
		if list[i].Encrypted && list[i].Skipped == "" && !list[i].Dir {
			list[i].Skipped = "encrypted"
		}
	}
	return list
}

func walkSevenZip(ctx context.Context, f *os.File, fn func(m member) error) error {
	bin := sevenZip()
	if bin == "" {
		return errors.New("7z and RAR archives need 7-Zip: install the 7zip package")
	}
	if SevenZipCopy == nil {
		return errors.New("7-Zip is not available")
	}
	p, cleanup, err := SevenZipCopy(f)
	if err != nil {
		return err
	}
	defer cleanup()
	lctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := sandboxed(lctx, bin, "l", "-slt", "-sccUTF-8", "-p", "--", p)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("7-Zip cannot read this archive: %s", strings.TrimSpace(stderr.String()))
	}
	for _, e := range parseSevenZipList(out.String()) {
		name := e.Name
		m := member{Entry: e, open: func() (io.ReadCloser, error) {
			// -spd: the name is literal (no wildcards).
			xctx, xcancel := context.WithTimeout(ctx, time.Hour)
			cmd := sandboxed(xctx, bin, "x", "-so", "-spd", "-sccUTF-8", "-p", "--", p, name)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			rc, err := cmd.StdoutPipe()
			if err != nil {
				xcancel()
				return nil, err
			}
			if err := cmd.Start(); err != nil {
				xcancel()
				return nil, err
			}
			return readCloser{rc, func() error {
				xcancel()
				_, _ = io.Copy(io.Discard, rc)
				_ = cmd.Wait()
				return nil
			}}, nil
		}}
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}

// TempCopy copies f into dir (created by the caller for the sandbox user).
func TempCopy(f *os.File, dir string, uid, gid int) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.Size() > SevenZipMax {
		return "", fmt.Errorf("archives of this type larger than %d GB are not supported", SevenZipMax>>30)
	}
	p := filepath.Join(dir, "archive")
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && uid >= 0 {
		err = os.Chown(p, uid, gid)
	}
	return p, err
}
