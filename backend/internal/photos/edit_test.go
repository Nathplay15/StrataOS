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
		out, err := EditJPEG(src, Edit{Rotate: deg, Brightness: 0, AutoLight: false})
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
	out, _ := EditJPEG(src, Edit{Rotate: 90, Brightness: 0, AutoLight: false})
	img := decode(t, out)
	r, _, _, _ := img.At(0, 0).RGBA()
	if r>>8 < 150 {
		t.Fatalf("rotate 90: top-left is not red: %v", img.At(0, 0))
	}
	if _, err := EditJPEG(src, Edit{Rotate: 45, Brightness: 0, AutoLight: false}); err == nil {
		t.Fatal("45° must be refused")
	}
}

func TestEditJPEGBrightness(t *testing.T) {
	src := makeJPEG(t, 2, 2, func(x, y int) color.RGBA { return color.RGBA{R: 100, G: 100, B: 100, A: 255} })
	out, err := EditJPEG(src, Edit{Rotate: 0, Brightness: 50, AutoLight: false}) // +50 % ≈ +127
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := decode(t, out).At(0, 0).RGBA()
	if got := int(r >> 8); got < 200 {
		t.Fatalf("brightness +50: R = %d, want ≥ 200", got)
	}
	out, _ = EditJPEG(src, Edit{Rotate: 0, Brightness: -100, AutoLight: false})
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
	out, err := EditJPEG(src, Edit{Rotate: 0, Brightness: 0, AutoLight: true})
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
	if _, err := EditJPEG([]byte("not an image"), Edit{}); err == nil {
		t.Fatal("garbage input must fail")
	}
}

// withExif inserts an APP1 Exif block (orientation o, DateTimeOriginal) after SOI.
func withExif(jpg []byte, o uint16, date string) []byte {
	le := func(b []byte, v uint32, n int) []byte {
		for i := 0; i < n; i++ {
			b = append(b, byte(v>>(8*i)))
		}
		return b
	}
	t := []byte("II*\x00")
	t = le(t, 8, 4)
	// IFD0 at 8: 2 entries, next 0. Exif IFD at 8+2+24+4 = 38.
	t = le(t, 2, 2)
	t = le(t, 0x0112, 2)
	t = le(t, 3, 2)
	t = le(t, 1, 4)
	t = le(t, uint32(o), 4)
	t = le(t, 0x8769, 2)
	t = le(t, 4, 2)
	t = le(t, 1, 4)
	t = le(t, 38, 4)
	t = le(t, 0, 4)
	// Exif IFD at 38: 1 entry, the date at 38+2+12+4 = 56.
	t = le(t, 1, 2)
	t = le(t, 0x9003, 2)
	t = le(t, 2, 2)
	t = le(t, uint32(len(date)+1), 4)
	t = le(t, 56, 4)
	t = le(t, 0, 4)
	t = append(append(t, date...), 0)
	seg := append([]byte("Exif\x00\x00"), t...)
	app := []byte{0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	out := append([]byte{}, jpg[:2]...)
	out = append(append(out, app...), seg...)
	return append(out, jpg[2:]...)
}

func TestEditJPEGKeepsExifAndNormalizesOrientation(t *testing.T) {
	src := withExif(makeJPEG(t, 40, 20, func(x, y int) color.RGBA {
		if x < 20 {
			return color.RGBA{255, 0, 0, 255}
		}
		return color.RGBA{0, 0, 255, 255}
	}), 6, "2019:07:14 10:20:30")
	out, err := EditJPEG(src, Edit{Brightness: 1})
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, out)
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("orientation 6 not applied: %v", b)
	}
	m, ok := exifOf(out)
	if !ok || m.date.Year() != 2019 || m.date.Month() != 7 {
		t.Fatalf("EXIF date lost: %+v %v", m, ok)
	}
	if _, o := exifSegment(out); o != 1 {
		t.Fatalf("orientation should be reset to 1, got %d", o)
	}
}

func TestEditJPEGCrop(t *testing.T) {
	src := makeJPEG(t, 100, 50, func(x, y int) color.RGBA {
		if x < 50 {
			return color.RGBA{255, 0, 0, 255}
		}
		return color.RGBA{0, 0, 255, 255}
	})
	out, err := EditJPEG(src, Edit{Crop: &Crop{X: 0.5, Y: 0, W: 0.5, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, out)
	if b := img.Bounds(); b.Dx() != 50 || b.Dy() != 50 {
		t.Fatalf("crop size %v", b)
	}
	if r, _, bl, _ := img.At(25, 25).RGBA(); r>>8 > 60 || bl>>8 < 200 {
		t.Fatalf("crop kept the wrong half")
	}
	for _, c := range []Crop{{X: -0.1, W: 0.5, H: 0.5}, {X: 0.8, W: 0.5, H: 0.5}, {W: 0, H: 1}} {
		if _, err := EditJPEG(src, Edit{Crop: &c}); err == nil {
			t.Errorf("crop %+v accepted", c)
		}
	}
}

func TestEditJPEGRefusesHugePictures(t *testing.T) {
	src := makeJPEG(t, 16, 16, func(x, y int) color.RGBA { return color.RGBA{1, 2, 3, 255} })
	for i := 2; i+9 < len(src); i++ {
		if src[i] == 0xFF && src[i+1] == 0xC0 { // SOF0: height, width at +5, +7
			src[i+5], src[i+6], src[i+7], src[i+8] = 0xEA, 0x60, 0xEA, 0x60 // 60000 x 60000
			break
		}
	}
	if _, err := EditJPEG(src, Edit{Rotate: 90}); err == nil {
		t.Fatal("a 3600 MP picture was decoded")
	}
}
