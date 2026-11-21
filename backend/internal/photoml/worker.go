package photoml

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"strataos/internal/sys"
)

//go:embed worker.py
var workerSource []byte

// Model files, downloaded by packaging/scripts/install-photoml.sh (pinned
// by SHA-256 there).
var ModelFiles = []string{
	"face_detection_yunet_2023mar.onnx",
	"face_recognition_sface_2021dec.onnx",
	"vision_model_quantized.onnx",
	"text_model_quantized.onnx",
	"vocab.json",
	"merges.txt",
}

// Dimensions of the embeddings.
const (
	FaceDim = 128
	ClipDim = 512
)

// Dirs: the models and the worker script (root-owned, readable by the
// sandbox user that runs the worker).
var (
	ModelDir  = "/usr/local/lib/strata/photoml/models"
	WorkerDir = "/usr/local/lib/strata/photoml/worker"
	python    = "/usr/bin/python3"
)

// Installed reports whether the runtime and the models are present.
func Installed() bool {
	for _, f := range ModelFiles {
		if _, err := os.Stat(filepath.Join(ModelDir, f)); err != nil {
			return false
		}
	}
	_, err := os.Stat("/usr/lib/python3/dist-packages/onnxruntime")
	return err == nil || venvPython() != ""
}

// venvPython is the Python of the optional accelerated runtime (an ONNX
// Runtime build with a GPU/NPU provider, installed by the installer).
func venvPython() string {
	p := filepath.Join(filepath.Dir(ModelDir), "runtime", "bin", "python3")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// ModelBytes is the size of the models on disk.
func ModelBytes() int64 {
	var n int64
	for _, f := range ModelFiles {
		if fi, err := os.Stat(filepath.Join(ModelDir, f)); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// Worker runs worker.py on demand and stops it when idle (its models take
// several hundred MB of memory).
type Worker struct {
	Threads func() int // CPU threads given to ONNX Runtime
	Idle    time.Duration

	mu       sync.Mutex // one request at a time
	cmd      *exec.Cmd
	in       io.WriteCloser
	out      *bufio.Reader
	last     time.Time
	timer    *time.Timer
	threads  int
	Provider string // execution provider in use, from the last answer
}

// Answer of the worker for a picture.
type ImageResult struct {
	W, H  int
	Faces []struct {
		Box   [4]float64 `json:"box"`
		Score float64    `json:"score"`
		Emb   string     `json:"emb"`
	} `json:"faces"`
	Clip     string `json:"clip"`
	Provider string `json:"provider"`
	Error    string `json:"error"`
}

func (w *Worker) start() error {
	if w.cmd != nil {
		return nil
	}
	if !Installed() {
		return errors.New("the smart photos component is not installed")
	}
	if err := os.MkdirAll(WorkerDir, 0o755); err != nil {
		return err
	}
	script := filepath.Join(WorkerDir, "worker.py")
	if b, err := os.ReadFile(script); err != nil || string(b) != string(workerSource) {
		if err := sys.WriteFileAtomic(script, workerSource, 0o644); err != nil {
			return err
		}
	}
	threads := 1
	if w.Threads != nil {
		threads = max(1, w.Threads())
	}
	py := python
	if v := venvPython(); v != "" {
		py = v
	}
	// -I: isolated (no user site, no current folder in the path), -B: no .pyc.
	cmd := exec.Command("nice", "-n", "19", py, "-I", "-B", script, ModelDir, strconv.Itoa(threads))
	cmd.Dir = WorkerDir
	t := strconv.Itoa(threads)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C.UTF-8", "OMP_NUM_THREADS=" + t, "OPENBLAS_NUM_THREADS=" + t, "MKL_NUM_THREADS=" + t}
	if p := os.Getenv("STRATA_ML_PROVIDER"); p != "" {
		cmd.Env = append(cmd.Env, "STRATA_ML_PROVIDER="+p)
	}
	cmd.SysProcAttr = sys.Sandboxed()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil // ONNX Runtime's warnings are noise
	if err := cmd.Start(); err != nil {
		return err
	}
	w.cmd, w.in, w.out, w.threads = cmd, in, bufio.NewReaderSize(out, 1<<20), threads
	return nil
}

func (w *Worker) stopLocked() {
	if w.cmd == nil {
		return
	}
	w.in.Close()
	done := make(chan struct{})
	go func() { _ = w.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	w.cmd = nil
}

// Stop stops the worker (settings changed, component removed).
func (w *Worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
}

// call sends a request (and data) and decodes the answer into v.
func (w *Worker) call(req map[string]any, data []byte, v any, timeout time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd != nil && w.Threads != nil && max(1, w.Threads()) != w.threads {
		w.stopLocked() // new CPU limit
	}
	if err := w.start(); err != nil {
		return err
	}
	if len(data) > 0 {
		req["len"] = len(data)
	}
	line, _ := json.Marshal(req)
	type answer struct {
		b   []byte
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		if _, err := w.in.Write(append(line, '\n')); err != nil {
			ch <- answer{nil, err}
			return
		}
		if len(data) > 0 {
			if _, err := w.in.Write(data); err != nil {
				ch <- answer{nil, err}
				return
			}
		}
		b, err := w.out.ReadBytes('\n')
		ch <- answer{b, err}
	}()
	var a answer
	select {
	case a = <-ch:
	case <-time.After(timeout):
		w.stopLocked()
		return errors.New("the smart photos worker did not answer in time")
	}
	if a.err != nil {
		w.stopLocked()
		return fmt.Errorf("the smart photos worker stopped: %w", a.err)
	}
	w.last = time.Now()
	if w.Idle > 0 {
		if w.timer == nil {
			w.timer = time.AfterFunc(w.Idle, w.stopIfIdle)
		} else {
			w.timer.Reset(w.Idle)
		}
	}
	var e struct {
		Error    string `json:"error"`
		Provider string `json:"provider"`
	}
	if json.Unmarshal(a.b, &e) == nil {
		if e.Error != "" {
			return errors.New(strings.TrimSpace(e.Error))
		}
		if e.Provider != "" {
			w.Provider = e.Provider
		}
	}
	return json.Unmarshal(a.b, v)
}

func (w *Worker) stopIfIdle() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if time.Since(w.last) >= w.Idle {
		w.stopLocked()
	}
}

// Hello starts the worker and returns the execution providers it can use.
func (w *Worker) Hello() ([]string, error) {
	var a struct {
		Providers []string `json:"providers"`
	}
	err := w.call(map[string]any{"op": "hello"}, nil, &a, 2*time.Minute)
	return a.Providers, err
}

// Image analyses a JPEG picture.
func (w *Worker) Image(jpeg []byte, faces, clip bool) (*ImageResult, error) {
	var r ImageResult
	if err := w.call(map[string]any{"op": "image", "faces": faces, "clip": clip}, jpeg, &r, 5*time.Minute); err != nil {
		return nil, err
	}
	return &r, nil
}

// Texts returns the CLIP embeddings of texts.
func (w *Worker) Texts(texts []string) ([]Vec, error) {
	var a struct {
		Embs []string `json:"embs"`
	}
	if err := w.call(map[string]any{"op": "text", "texts": texts}, nil, &a, 5*time.Minute); err != nil {
		return nil, err
	}
	if len(a.Embs) != len(texts) {
		return nil, errors.New("the worker gave a wrong number of embeddings")
	}
	out := make([]Vec, len(texts))
	for i, s := range a.Embs {
		v, err := DecodeVec(s, ClipDim)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}
