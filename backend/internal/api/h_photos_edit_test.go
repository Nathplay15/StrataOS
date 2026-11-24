package api

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"strataos/internal/photos"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(50 + x*40), G: 80, B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEditPhoto(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	p := filepath.Join(root, "pic.jpg")
	if err := os.WriteFile(p, testJPEG(t, 8, 4), 0o640); err != nil {
		t.Fatal(err)
	}
	tg := target{root: root, rel: "pic.jpg", abs: p, acc: access{read: true, write: true}}

	// Rotate 90: 8x4 becomes 4x8, mode kept.
	if err := s.editPhoto(tg, photos.Edit{Rotate: 90, Brightness: 0, AutoLight: false}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 4 || img.Bounds().Dy() != 8 {
		t.Fatalf("after rotate 90: bounds %v, want 4x8", img.Bounds())
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v, want 0640 kept", st.Mode().Perm())
	}

	// Auto light: the red channel (50..255) must be stretched to 0..255.
	if err := s.editPhoto(tg, photos.Edit{Rotate: 0, Brightness: 0, AutoLight: true}); err != nil {
		t.Fatal(err)
	}
	f, _ = os.Open(p)
	img, err = jpeg.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	minR, maxR := uint32(255), uint32(0)
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if r>>8 < minR {
				minR = r >> 8
			}
			if r>>8 > maxR {
				maxR = r >> 8
			}
		}
	}
	if minR > 30 || maxR < 230 {
		t.Fatalf("auto light: red range %d..%d, want ~0..255", minR, maxR)
	}

	// Only JPEG.
	png := filepath.Join(root, "pic.png")
	if err := os.WriteFile(png, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := s.editPhoto(target{root: root, rel: "pic.png", abs: png, acc: access{read: true, write: true}}, photos.Edit{Rotate: 90, Brightness: 0, AutoLight: false}); err == nil {
		t.Fatal("PNG must be refused")
	}
	// Missing file.
	if err := s.editPhoto(target{root: root, rel: "gone.jpg", abs: filepath.Join(root, "gone.jpg"), acc: access{read: true, write: true}}, photos.Edit{Rotate: 90, Brightness: 0, AutoLight: false}); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestEditPhotoRefusesLinksOutOfTheShare(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.jpg")
	orig := testJPEG(t, 8, 4)
	os.WriteFile(outside, orig, 0o600)
	os.Symlink(outside, filepath.Join(root, "pic.jpg"))
	tg := target{root: root, rel: "pic.jpg", abs: filepath.Join(root, "pic.jpg"), acc: access{read: true, write: true}}
	if err := s.editPhoto(tg, photos.Edit{Rotate: 90}); err == nil {
		t.Fatal("picture outside the share edited")
	}
	if b, _ := os.ReadFile(outside); !bytes.Equal(b, orig) {
		t.Fatal("file outside the share changed")
	}
}
