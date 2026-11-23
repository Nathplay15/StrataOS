package photos

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func makeJPEG(t *testing.T, w, h int, px func(x, y int) color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, px(x, y))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestEditJPEGRotate(t *testing.T) {
	// 4x2, left half red, right half blue.
	src := makeJPEG(t, 4, 2, func(x, y int) color.RGBA {
		if x < 2 {
			return color.RGBA{R: 200, A: 255}
		}
		return color.RGBA{B: 200, A: 255}
	})
	for _, deg := range []int{90, 180, 270, -90} {
		out, err := EditJPEG(src, deg, 0, false)
		if err != nil {
			t.Fatalf("rotate %d: %v", deg, err)
		}
		img := decode(t, out)
		wantW, wantH := 4, 2
		if deg == 90 || deg == 270 || deg == -90 {
			wantW, wantH = 2, 4
		}
		if img.Bounds().Dx() != wantW || img.Bounds().Dy() != wantH {
			t.Fatalf("rotate %d: bounds = %v, want %dx%d", deg, img.Bounds(), wantW, wantH)
		}
	}
	// 90° clockwise: top row of the output is the left column of the source (red).
	out, _ := EditJPEG(src, 90, 0, false)
	img := decode(t, out)
	r, _, _, _ := img.At(0, 0).RGBA()
	if r>>8 < 150 {
		t.Fatalf("rotate 90: top-left is not red: %v", img.At(0, 0))
	}
	if _, err := EditJPEG(src, 45, 0, false); err == nil {
		t.Fatal("45° must be refused")
	}
}

func TestEditJPEGBrightness(t *testing.T) {
	src := makeJPEG(t, 2, 2, func(x, y int) color.RGBA { return color.RGBA{R: 100, G: 100, B: 100, A: 255} })
	out, err := EditJPEG(src, 0, 50, false) // +50 % ≈ +127
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := decode(t, out).At(0, 0).RGBA()
	if got := int(r >> 8); got < 200 {
		t.Fatalf("brightness +50: R = %d, want ≥ 200", got)
	}
	out, _ = EditJPEG(src, 0, -100, false)
	r, _, _, _ = decode(t, out).At(0, 0).RGBA()
	if r != 0 {
		t.Fatalf("brightness -100: R = %d, want 0", r>>8)
	}
}

func TestEditJPEGAutoLight(t *testing.T) {
	// Dark grey 40 everywhere: auto light must stretch it to white.
	src := makeJPEG(t, 3, 3, func(x, y int) color.RGBA {
		if x == 2 {
			return color.RGBA{R: 120, G: 120, B: 120, A: 255}
		}
		return color.RGBA{R: 40, G: 40, B: 40, A: 255}
	})
	out, err := EditJPEG(src, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, out)
	r0, _, _, _ := img.At(0, 0).RGBA()
	r2, _, _, _ := img.At(2, 0).RGBA()
	if r0>>8 > 30 {
		t.Fatalf("auto light: darkest = %d, want ~0", r0>>8)
	}
	if r2>>8 < 230 {
		t.Fatalf("auto light: brightest = %d, want ~255", r2>>8)
	}
}

func TestEditJPEGNotAJPEG(t *testing.T) {
	if _, err := EditJPEG([]byte("not an image"), 0, 0, false); err == nil {
		t.Fatal("garbage input must fail")
	}
}
