package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

func pngOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{200, 10, 10, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func TestNormalizeWallpaper(t *testing.T) {
	out, err := normalizeWallpaper(pngOf(64, 32))
	if err != nil {
		t.Fatal(err)
	}
	if http.DetectContentType(out) != "image/jpeg" {
		t.Fatalf("not re-encoded as JPEG: %s", http.DetectContentType(out))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != 64 || cfg.Height != 32 {
		t.Fatalf("%v %+v", err, cfg)
	}

	html := []byte("<!doctype html><script>alert(1)</script>")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	big := append(pngOf(8, 8), make([]byte, maxWallpaperBytes)...)
	// A PNG header announcing a huge image: refused before decoding.
	huge := pngOf(1, 1)
	copy(huge[16:24], []byte{0, 0, 0x7f, 0xff, 0, 0, 0x7f, 0xff})
	truncated := pngOf(64, 64)[:60]
	for name, data := range map[string][]byte{"empty": nil, "html": html, "svg": svg, "big": big, "huge": huge, "truncated": truncated, "text": []byte(strings.Repeat("a", 100))} {
		if _, err := normalizeWallpaper(data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
