package vms

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildChunklist makes a CNKL chunklist (signature method 1: a zeroed RSA
// signature, method 2: plain digest) for the given chunk sizes, plus the
// matching image content.
func buildChunklist(t *testing.T, dir string, sizes []int, sigMethod byte) (dmg, cnk string) {
	t.Helper()
	var entries []byte
	var content []byte
	for _, s := range sizes {
		chunk := make([]byte, s)
		for i := range chunk {
			chunk[i] = byte(i + len(content))
		}
		content = append(content, chunk...)
		e := make([]byte, 36)
		binary.LittleEndian.PutUint32(e[0:4], uint32(s))
		sum := sha256.Sum256(chunk)
		copy(e[4:36], sum[:])
		entries = append(entries, e...)
	}
	h := sha256.New()
	hdr := make([]byte, 0x24)
	copy(hdr[0:4], recoveryMagic)
	binary.LittleEndian.PutUint32(hdr[4:8], 0x24)
	hdr[8], hdr[9], hdr[10] = 1, 1, sigMethod
	binary.LittleEndian.PutUint64(hdr[12:20], uint64(len(sizes)))
	binary.LittleEndian.PutUint64(hdr[20:28], 0x24)
	sigOffset := uint64(0x24) + uint64(len(entries))
	binary.LittleEndian.PutUint64(hdr[28:36], sigOffset)
	h.Write(hdr)
	h.Write(entries)
	cnk = filepath.Join(dir, "BaseSystem-test.chunklist")
	body := append(append([]byte{}, hdr...), entries...)
	if sigMethod == 1 {
		body = append(body, make([]byte, 256)...)
	} else {
		body = append(body, h.Sum(nil)...)
	}
	if err := os.WriteFile(cnk, body, 0o644); err != nil {
		t.Fatal(err)
	}
	dmg = filepath.Join(dir, "BaseSystem-test.dmg")
	if err := os.WriteFile(dmg, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return dmg, cnk
}

func TestVerifyChunklist(t *testing.T) {
	dir := t.TempDir()
	signed := false
	checkChunklistSignature = func(cnk []byte, off int, digest []byte) error {
		signed = true
		return nil
	}
	defer func() { checkChunklistSignature = verifyChunklistSignature }()

	dmg, cnk := buildChunklist(t, dir, []int{1024, 512, 4096}, 1)
	if err := verifyChunklist(dmg, cnk); err != nil || !signed {
		t.Fatalf("valid image rejected: %v (signature checked: %v)", err, signed)
	}

	if err := os.WriteFile(dmg, []byte("too short"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("truncated image accepted")
	}

	dmg, cnk = buildChunklist(t, dir, []int{2048, 2048}, 1)
	content, _ := os.ReadFile(dmg)
	content[10] ^= 0xff // one flipped bit
	if err := os.WriteFile(dmg, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("corrupted image accepted")
	}

	// A bare digest (method 2) proves nothing: anyone can compute it.
	dmg, cnk = buildChunklist(t, dir, []int{1024}, 2)
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("unsigned chunklist accepted")
	}

	// A count whose 36*count wraps around to a plausible signature offset
	// must be refused, not index past the file.
	dmg, cnk = buildChunklist(t, dir, []int{1024}, 1)
	b, _ := os.ReadFile(cnk)
	huge := uint64(1)<<62 + 1 // 36*huge wraps to 36 (mod 2^64)
	binary.LittleEndian.PutUint64(b[12:20], huge)
	binary.LittleEndian.PutUint64(b[28:36], 0x24+36*huge)
	if err := os.WriteFile(cnk, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("wrapping chunk count accepted")
	}

	checkChunklistSignature = verifyChunklistSignature
	dmg, cnk = buildChunklist(t, dir, []int{1024}, 1) // garbage RSA signature
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("invalid RSA signature accepted")
	}
}

func TestStandaloneInfo(t *testing.T) {
	if err := standaloneInfo("oc.qcow2", `{"format":"qcow2","format-specific":{"type":"qcow2","data":{"compat":"1.1"}}}`); err != nil {
		t.Errorf("plain image refused: %v", err)
	}
	if err := standaloneInfo("oc.qcow2", `{"format":"qcow2","backing-filename":"/dev/sda"}`); err == nil {
		t.Error("backing file accepted")
	}
	if err := standaloneInfo("oc.qcow2", `{"format":"qcow2","format-specific":{"type":"qcow2","data":{"data-file":"/etc/shadow"}}}`); err == nil {
		t.Error("external data file accepted")
	}
}
