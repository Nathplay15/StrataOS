// Package sys wraps the execution of host commands used by StrataOS.
package sys

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CmdError is returned when a command exits with a non-zero status.
type CmdError struct {
	Cmd    string
	Code   int
	Stderr string
	Stdout string
}

func (e *CmdError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Stdout)
	}
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Cmd, msg)
}

// DefaultTimeout bounds every command that is not run with an explicit context deadline.
const DefaultTimeout = 2 * time.Minute

// Opts customises a command execution.
type Opts struct {
	Stdin   string
	Env     []string
	Dir     string
	Timeout time.Duration
	// AllowCodes lists exit codes that must not be treated as errors (e.g. smartctl bitmasks).
	AllowCodes []int
	// Output, when set, receives combined stdout/stderr as it is produced.
	Output io.Writer
}

// Run executes name with args and returns its stdout.
func Run(name string, args ...string) (string, error) {
	return RunOpts(context.Background(), Opts{}, name, args...)
}

// RunCtx executes a command bound to ctx.
func RunCtx(ctx context.Context, name string, args ...string) (string, error) {
	return RunOpts(ctx, Opts{}, name, args...)
}

// RunInput executes a command feeding stdin.
func RunInput(stdin string, name string, args ...string) (string, error) {
	return RunOpts(context.Background(), Opts{Stdin: stdin}, name, args...)
}

// RunOpts is the general form of Run.
func RunOpts(ctx context.Context, o Opts, name string, args ...string) (string, error) {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if _, has := ctx.Deadline(); !has && timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.Env = append(cmd.Env, o.Env...)
	cmd.Dir = o.Dir
	if o.Stdin != "" {
		cmd.Stdin = strings.NewReader(o.Stdin)
	}
	var stdout, stderr bytes.Buffer
	if o.Output != nil {
		cmd.Stdout = io.MultiWriter(&stdout, o.Output)
		cmd.Stderr = io.MultiWriter(&stderr, o.Output)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	start := time.Now()
	err := cmd.Run()
	slog.Debug("exec", "cmd", name, "args", args, "dur", time.Since(start), "err", err)
	if err != nil {
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			for _, c := range o.AllowCodes {
				if c == code || c == -2 { // -2 = allow any code
					return stdout.String(), nil
				}
			}
		}
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.String(), fmt.Errorf("%s: timed out after %s", name, timeout)
		}
		return stdout.String(), &CmdError{Cmd: name, Code: code, Stderr: stderr.String(), Stdout: stdout.String()}
	}
	return stdout.String(), nil
}

// Has reports whether a binary is present in PATH.
func Has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// WriteFileAtomic writes data to path through a temp file and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".strata-tmp"
	// os.WriteFile keeps the mode of an existing file: start from a fresh one so
	// secrets (state.json, sessions.json) always get perm.
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
