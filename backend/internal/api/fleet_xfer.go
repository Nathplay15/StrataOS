package api

// Copying or moving a VM or a container to another NAS of the console,
// outside a cluster: the pure part (names, plan checks, chunks, progress),
// kept apart from the commands and the HTTP calls to test it.

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	xferChunk = 8 << 20 // bytes of a file per call (before compression)
	// xferIdle: a destination waiting for data that long gives up (its
	// files stay for a resume).
	xferIdle = 30 * time.Minute
	// xferKeep: files of an abandoned transfer are removed after that.
	xferKeep          = 3 * 24 * time.Hour
	xferMaxTransfers  = 30
	xferChunksPerMin  = 3000
	xferRetryAttempts = 8
)

var xferIDRe = regexp.MustCompile(`^[a-f0-9]{16}$`)

func validXferID(id string) bool { return xferIDRe.MatchString(id) }

var xferPartRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validXferFile checks the relative name of a transferred file: one or two
// plain components (cloud-init/user-data), nothing hidden, no "..".
func validXferFile(name string) bool {
	if name == "" || len(name) > 200 || path.Clean(name) != name {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !xferPartRe.MatchString(p) {
			return false
		}
	}
	return true
}

type xferFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Have   int64  `json:"have,omitempty"` // destination: bytes received
}

// xferReq is what the console asks of a transfer: the same on both sides.
type xferReq struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"` // vm | lxc
	Name    string            `json:"name"`
	NewName string            `json:"newName"`
	Mode    string            `json:"mode"` // stop | snapshot
	Move    bool              `json:"move"`
	Storage string            `json:"storage"`
	Bridges map[string]string `json:"bridges,omitempty"`
	Start   bool              `json:"start"`
	Peer    string            `json:"peer"` // name of the other NAS (titles, logs)
	Files   []xferFile        `json:"files,omitempty"`
}

// checkXferReq validates the fields every side reads.
func checkXferReq(q *xferReq) error {
	if !validXferID(q.ID) {
		return badRequest("invalid transfer ID")
	}
	if q.Kind != "vm" && q.Kind != "lxc" {
		return badRequest("invalid kind")
	}
	if q.NewName == "" {
		q.NewName = q.Name
	}
	switch q.Mode {
	case "":
		q.Mode = "stop"
	case "stop", "snapshot":
	default:
		return badRequest("invalid mode")
	}
	if q.Move && q.Mode != "stop" {
		return badRequest("a move stops the source first")
	}
	if len(q.Peer) > 64 || strings.ContainsAny(q.Peer, "\n\r") {
		return badRequest("invalid NAS name")
	}
	if len(q.Bridges) > 16 || len(q.Files) > 64 {
		return badRequest("invalid request")
	}
	for k, v := range q.Bridges {
		if !validIfName(k) || !validIfName(v) || (v == "nat" && q.Kind != "lxc") {
			return badRequest("invalid bridge name")
		}
	}
	for _, f := range q.Files {
		if !validXferFile(f.Name) || f.Size < 0 || len(f.SHA256) != 64 {
			return badRequest("invalid file list")
		}
	}
	return nil
}

var ifNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,14}$`)

func validIfName(n string) bool { return ifNameRe.MatchString(n) }

// xferPlanIssue is a problem found before a transfer; Bridge marks a missing
// bridge the user maps to one of the destination (or to NAT, containers).
type xferPlanIssue struct {
	Text   string `json:"text"`
	Bridge string `json:"bridge,omitempty"`
}

// xferPlan is what the console checks before starting: the source object
// and the destination.
type xferPlanInput struct {
	Kind       string
	SrcBridges []string // bridges the VM or container uses
	SrcState   string
	SrcRBD     bool // VM disks on Ceph (stay in their cluster)
	Mode       string
	Move       bool
	NewName    string
	Storage    string
	Bridges    map[string]string
	DstReady   bool // libvirt / LXC installed on the destination
	DstNames   []string
	DstBridges []string
	DstVolumes []string // mount points
	SameNAS    bool
}

func xferPlanIssues(in xferPlanInput) []xferPlanIssue {
	var out []xferPlanIssue
	add := func(f string, a ...any) { out = append(out, xferPlanIssue{Text: fmt.Sprintf(f, a...)}) }
	if in.SameNAS {
		add("Choose another NAS as the destination")
	}
	if !in.DstReady {
		if in.Kind == "vm" {
			add("Virtual machines are not installed on the destination NAS")
		} else {
			add("Containers (LXC) are not installed on the destination NAS")
		}
	}
	if in.SrcRBD {
		add("This VM has disks on Ceph: move it within its cluster instead")
	}
	if in.Move && in.Mode != "stop" {
		add("A move stops the source first")
	}
	if slices.Contains(in.DstNames, in.NewName) {
		add("%s already exists on the destination NAS: choose another name", in.NewName)
	}
	inVolume := false
	for _, v := range in.DstVolumes {
		if in.Storage == v || strings.HasPrefix(in.Storage, v+"/") {
			inVolume = true
		}
	}
	if !inVolume {
		add("Choose where to put it on the destination NAS")
	}
	for _, b := range in.SrcBridges {
		to := b
		if m := in.Bridges[b]; m != "" {
			to = m
		}
		if to == "nat" && in.Kind == "lxc" {
			continue
		}
		if !slices.Contains(in.DstBridges, to) {
			if to == b {
				out = append(out, xferPlanIssue{Text: fmt.Sprintf("The bridge %s does not exist on the destination NAS: choose another one", b), Bridge: b})
			} else {
				out = append(out, xferPlanIssue{Text: fmt.Sprintf("The bridge %s does not exist on the destination NAS", to), Bridge: b})
			}
		}
	}
	return out
}

// xferBridges keeps the mappings of the bridges in use (identity dropped).
func xferBridges(used []string, m map[string]string) map[string]string {
	out := map[string]string{}
	for _, b := range used {
		if to := m[b]; to != "" && to != b {
			out[b] = to
		}
	}
	return out
}

// xferNeed is the free space a destination needs: VM disks are moved out
// of the staging folder (same volume), a container archive is unpacked.
func xferNeed(kind string, files []xferFile) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	if kind == "lxc" {
		return n * 4
	}
	return n + n/20 + 64<<20
}

// xferProgress maps the stages to the console's progress bar: export
// 0-30, transfer 30-85, verify and restore 85-98.
func xferProgress(stage string, frac float64) float64 {
	frac = min(max(frac, 0), 1)
	switch stage {
	case "export":
		return 30 * frac
	case "transfer":
		return 30 + 55*frac
	case "restore":
		return 85 + 13*frac
	case "finish":
		return 98 + 2*frac
	}
	return 0
}

func xferTotal(files []xferFile) (total, have int64) {
	for _, f := range files {
		total += f.Size
		have += min(f.Have, f.Size)
	}
	return total, have
}

// xferRetryDelay is the wait before retry n (1-based) of a chunk.
func xferRetryDelay(n int) time.Duration {
	d := time.Second << min(n, 6)
	return min(d, time.Minute)
}

// gzipChunk compresses a chunk (fast: the network is the slow part, and
// disk images are mostly zeros or already compressed data).
func gzipChunk(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// gunzipChunk decompresses a chunk of at most max bytes.
func gunzipChunk(b []byte, max int) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(out) > max {
		return nil, fmt.Errorf("chunk too large")
	}
	return out, nil
}
