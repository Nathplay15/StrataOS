package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strataos/internal/jobs"
)

func TestValidXferFile(t *testing.T) {
	for _, ok := range []string{"vda.qcow2", "nvram.fd", "cloud-init/user-data", "ct.tar.zst", "domain.xml"} {
		if !validXferFile(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "../x", "/etc/passwd", "a/../../b", ".hidden", "a/b/c", "a//b", "a/.x", "x\n", strings.Repeat("a", 201)} {
		if validXferFile(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if !validXferID("0123456789abcdef") || validXferID("0123456789ABCDEF") || validXferID("../../etc") {
		t.Error("transfer IDs")
	}
}

func TestCheckXferReq(t *testing.T) {
	sum := strings.Repeat("a", 64)
	q := xferReq{ID: "0123456789abcdef", Kind: "vm", Name: "web", Files: []xferFile{{Name: "vda.qcow2", Size: 10, SHA256: sum}}}
	if err := checkXferReq(&q); err != nil || q.NewName != "web" || q.Mode != "stop" {
		t.Fatalf("defaults: %v %+v", err, q)
	}
	bad := []xferReq{
		{ID: "x", Kind: "vm"},
		{ID: "0123456789abcdef", Kind: "docker"},
		{ID: "0123456789abcdef", Kind: "vm", Mode: "suspend"},
		{ID: "0123456789abcdef", Kind: "vm", Mode: "snapshot", Move: true},
		{ID: "0123456789abcdef", Kind: "vm", Bridges: map[string]string{"br0": "nat"}},
		{ID: "0123456789abcdef", Kind: "lxc", Bridges: map[string]string{"br0": "br0;rm"}},
		{ID: "0123456789abcdef", Kind: "vm", Files: []xferFile{{Name: "../x", SHA256: sum}}},
		{ID: "0123456789abcdef", Kind: "vm", Files: []xferFile{{Name: "x", SHA256: "short"}}},
	}
	for i, b := range bad {
		if err := checkXferReq(&b); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	ok := xferReq{ID: "0123456789abcdef", Kind: "lxc", Bridges: map[string]string{"br0": "nat"}}
	if err := checkXferReq(&ok); err != nil {
		t.Errorf("containers may go to NAT: %v", err)
	}
}

func TestXferPlanIssues(t *testing.T) {
	base := xferPlanInput{Kind: "vm", SrcBridges: []string{"br0"}, Mode: "stop", NewName: "web", Storage: "/srv/strata/v1/vms",
		DstReady: true, DstNames: []string{"db"}, DstBridges: []string{"br0", "br1"}, DstVolumes: []string{"/srv/strata/v1"}}
	if is := xferPlanIssues(base); len(is) != 0 {
		t.Fatalf("clean plan: %v", is)
	}
	// Missing bridge: asked, then mapped.
	in := base
	in.DstBridges = []string{"br1"}
	is := xferPlanIssues(in)
	if len(is) != 1 || is[0].Bridge != "br0" {
		t.Fatalf("missing bridge: %v", is)
	}
	in.Bridges = map[string]string{"br0": "br1"}
	if is := xferPlanIssues(in); len(is) != 0 {
		t.Fatalf("mapped bridge: %v", is)
	}
	in.Bridges = map[string]string{"br0": "br7"}
	if is := xferPlanIssues(in); len(is) != 1 || !strings.Contains(is[0].Text, "br7") || is[0].Bridge != "br0" {
		t.Fatalf("mapped to a missing bridge: %v", is)
	}
	// NAT only for containers.
	in.Bridges = map[string]string{"br0": "nat"}
	if is := xferPlanIssues(in); len(is) != 1 {
		t.Fatalf("VM to NAT: %v", is)
	}
	in.Kind = "lxc"
	if is := xferPlanIssues(in); len(is) != 0 {
		t.Fatalf("container to NAT: %v", is)
	}
	// Name taken, storage outside the volumes, Ceph, not installed, same NAS.
	in = base
	in.NewName, in.Storage, in.SrcRBD, in.DstReady, in.SameNAS = "db", "/srv/strata/v2", true, false, true
	if is := xferPlanIssues(in); len(is) != 5 {
		t.Fatalf("five problems expected: %v", is)
	}
	// A prefix is not a volume.
	in = base
	in.Storage = "/srv/strata/v10/vms"
	if is := xferPlanIssues(in); len(is) != 1 {
		t.Fatalf("volume prefix: %v", is)
	}
}

func TestXferBridgesAndNeed(t *testing.T) {
	m := xferBridges([]string{"br0", "br1"}, map[string]string{"br0": "lan", "br1": "br1", "br9": "x"})
	if len(m) != 1 || m["br0"] != "lan" {
		t.Fatalf("%v", m)
	}
	files := []xferFile{{Size: 1 << 30}, {Size: 1 << 20}}
	if n := xferNeed("lxc", files); n != 4*(1<<30+1<<20) {
		t.Errorf("lxc need %d", n)
	}
	if n := xferNeed("vm", files); n < 1<<30+1<<20 || n > 2<<30 {
		t.Errorf("vm need %d", n)
	}
	if xferProgress("export", 1) != 30 || xferProgress("transfer", 0.5) != 57.5 || xferProgress("restore", 2) != 98 || xferProgress("finish", 1) != 100 {
		t.Error("progress")
	}
	if xferRetryDelay(1) != 2*time.Second || xferRetryDelay(10) != time.Minute {
		t.Error("retry delays")
	}
	total, have := xferTotal([]xferFile{{Size: 10, Have: 4}, {Size: 5, Have: 9}})
	if total != 15 || have != 9 {
		t.Errorf("total %d have %d", total, have)
	}
}

func TestGzipChunk(t *testing.T) {
	b := bytes.Repeat([]byte("strata"), 1000)
	gz := gzipChunk(b)
	if len(gz) >= len(b) {
		t.Error("not compressed")
	}
	out, err := gunzipChunk(gz, len(b))
	if err != nil || !bytes.Equal(out, b) {
		t.Fatal("round trip")
	}
	if _, err := gunzipChunk(gz, len(b)-1); err == nil {
		t.Error("oversized chunk accepted")
	}
	if _, err := gunzipChunk([]byte("nope"), 10); err == nil {
		t.Error("garbage accepted")
	}
}

// A copy goes from a source folder to a destination folder chunk by chunk,
// with a conflict on a wrong offset, and the checksums match.
func TestXferChunks(t *testing.T) {
	s := &Server{jobs: jobs.NewManager()}
	srcDir, dstDir := t.TempDir(), t.TempDir()
	data := make([]byte, xferChunk+12345)
	for i := range data {
		data[i] = byte(i * 7)
	}
	_ = os.MkdirAll(filepath.Join(srcDir, "data", "cloud-init"), 0o700)
	_ = os.WriteFile(filepath.Join(srcDir, "data", "vda.qcow2"), data, 0o600)
	_ = os.WriteFile(filepath.Join(srcDir, "data", "cloud-init", "user-data"), []byte("#cloud-config\n"), 0o600)
	files, err := xferHashDir(context.Background(), filepath.Join(srcDir, "data"), func(float64) {})
	if err != nil || len(files) != 2 {
		t.Fatalf("hash: %v %v", err, files)
	}
	src := &xferEntry{st: xferSide{xferReq: xferReq{ID: "0123456789abcdef", Files: files}, Role: "source", Dir: srcDir, Stage: "exporting"}}
	if _, _, err := s.xferRead(src, "vda.qcow2", 0); err == nil {
		t.Fatal("read before the copy is ready")
	}
	src.st.Stage = "ready"
	_ = os.MkdirAll(filepath.Join(dstDir, "data", "cloud-init"), 0o700)
	dst := &xferEntry{st: xferSide{xferReq: xferReq{ID: "0123456789abcdef", Files: files}, Role: "dest", Dir: dstDir, Stage: "receiving"}, running: true}
	for _, f := range files {
		var off int64
		for off < f.Size {
			gz, n, err := s.xferRead(src, f.Name, off)
			if err != nil || n == 0 {
				t.Fatalf("read %s@%d: %v", f.Name, off, err)
			}
			if off > 0 {
				var e *Error
				if _, err := s.xferWrite(dst, f.Name, off+1, gz); !errors.As(err, &e) || e.Status != http.StatusConflict {
					t.Fatalf("wrong offset accepted: %v", err)
				}
			}
			have, err := s.xferWrite(dst, f.Name, off, gz)
			if err != nil || have != off+int64(n) {
				t.Fatalf("write %s@%d: %v %d", f.Name, off, err, have)
			}
			off = have
		}
	}
	got, _ := xferHashDir(context.Background(), filepath.Join(dstDir, "data"), func(float64) {})
	if !sameFiles(got, files) {
		t.Fatalf("checksums differ: %v %v", got, files)
	}
	v := s.xferViewOf(dst)
	if total, have := xferTotal(v.Files); total != have {
		t.Fatalf("view: %d/%d", have, total)
	}
	if _, err := s.xferWrite(dst, "other", 0, gzipChunk([]byte("x"))); err == nil {
		t.Error("unknown file accepted")
	}
	if _, _, err := s.xferRead(src, "vda.qcow2", int64(len(data))); err == nil {
		t.Error("offset past the end accepted")
	}
	dst.running = false
	if _, err := s.xferWrite(dst, "vda.qcow2", 0, gzipChunk([]byte("x"))); err == nil {
		t.Error("write without a receiving job accepted")
	}
}
