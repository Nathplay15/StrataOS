package vms

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildChunklist makes a CNKL chunklist (signature method 2: plain digest) for
// the given chunk sizes, plus the matching image content.
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
	body := append(append(append([]byte{}, hdr...), entries...), h.Sum(nil)...)
	if sigMethod == 1 {
		body = append(body, make([]byte, 256)...)
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

	dmg, cnk := buildChunklist(t, dir, []int{1024, 512, 4096}, 2)
	if err := verifyChunklist(dmg, cnk); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}

	if err := os.WriteFile(dmg, []byte("too short"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("truncated image accepted")
	}

	dmg, cnk = buildChunklist(t, dir, []int{2048, 2048}, 2)
	content, _ := os.ReadFile(dmg)
	content[10] ^= 0xff // one flipped bit
	if err := os.WriteFile(dmg, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("corrupted image accepted")
	}

	dmg, cnk = buildChunklist(t, dir, []int{1024}, 1) // RSA method: garbage signature
	if err := verifyChunklist(dmg, cnk); err == nil {
		t.Fatal("invalid RSA signature accepted")
	}
}
