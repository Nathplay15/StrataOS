package photos

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
)

// MaxEditPixels bounds the pictures the light editor decodes (100 MP).
const MaxEditPixels = 100_000_000

// Crop is a rectangle in fractions (0..1) of the upright, rotated picture.
type Crop struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Edit is what the light editor applies, in this order: rotation, crop,
// automatic light, brightness.
type Edit struct {
	Rotate     int // degrees, multiple of 90 (clockwise)
	Brightness int // -100..100
	AutoLight  bool
	Crop       *Crop
}

// EditJPEG applies e to a JPEG picture and returns the new JPEG (quality
// 90). Pure Go, no external tool: the gallery edits a photo without leaving
// the NAS. The EXIF block (date taken, place, camera) is kept; the picture
// is turned upright from its EXIF orientation, which is then reset to 1.
func EditJPEG(src []byte, e Edit) ([]byte, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("not a JPEG picture: %w", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxEditPixels {
		return nil, fmt.Errorf("picture too large to edit here (over 100 megapixels)")
	}
	img, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("not a JPEG picture: %w", err)
	}
	exif, orient := exifSegment(src)
	img = orientImage(img, orient)
	switch ((e.Rotate % 360) + 360) % 360 {
	case 0:
	case 90, 180, 270:
		img = rotateImage(img, ((e.Rotate%360)+360)%360)
	default:
		return nil, fmt.Errorf("rotation must be a multiple of 90°")
	}
	if e.Crop != nil {
		if img, err = cropImage(img, *e.Crop); err != nil {
			return nil, err
		}
	}
	if e.AutoLight {
		img = autoLightImage(img)
	}
	if e.Brightness != 0 {
		img = brightnessImage(img, e.Brightness)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if exif != nil {
		// Right after SOI: Go's encoder writes no APP segment of its own.
		out = append(append(append([]byte{}, out[:2]...), exif...), out[2:]...)
	}
	return out, nil
}

// cropImage keeps the rectangle c (fractions of the picture).
func cropImage(src image.Image, c Crop) (image.Image, error) {
	if c.X < 0 || c.Y < 0 || c.W <= 0 || c.H <= 0 || c.X+c.W > 1.0001 || c.Y+c.H > 1.0001 {
		return nil, fmt.Errorf("invalid crop")
	}
	b := src.Bounds()
	x0 := b.Min.X + int(c.X*float64(b.Dx()))
	y0 := b.Min.Y + int(c.Y*float64(b.Dy()))
	x1 := min(b.Max.X, x0+int(c.W*float64(b.Dx())+0.5))
	y1 := min(b.Max.Y, y0+int(c.H*float64(b.Dy())+0.5))
	if x1-x0 < 8 || y1-y0 < 8 {
		return nil, fmt.Errorf("crop too small")
	}
	dst := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			dst.Set(x-x0, y-y0, src.At(x, y))
		}
	}
	return dst, nil
}

// exifSegment returns the whole APP1 Exif segment of a JPEG (marker
// included) with its orientation tag reset to 1, and the orientation it had
// (1 when absent).
func exifSegment(b []byte) ([]byte, int) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return nil, 1
		}
		marker := b[i+1]
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA || size < 2 || i+2+size > len(b) {
			return nil, 1
		}
		if marker == 0xE1 && size >= 16 && string(b[i+4:i+10]) == "Exif\x00\x00" {
			seg := append([]byte{}, b[i:i+2+size]...)
			return seg, resetOrientation(seg[10:])
		}
		i += 2 + size
	}
	return nil, 1
}

// resetOrientation sets the orientation tag (0x0112) of IFD0 of the TIFF
// block t to 1 in place and returns its previous value.
func resetOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch {
	case len(t) < 8:
		return 1
	case string(t[:2]) == "II":
		bo = binary.LittleEndian
	case string(t[:2]) == "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n && k < 512; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			break
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 { // SHORT
			v := int(bo.Uint16(t[e+8:]))
			bo.PutUint16(t[e+8:], 1)
			if v < 1 || v > 8 {
				return 1
			}
			return v
		}
	}
	return 1
}

// orientImage turns a picture stored with EXIF orientation o upright.
func orientImage(src image.Image, o int) image.Image {
	switch o {
	case 2:
		return flipImage(src)
	case 3:
		return rotateImage(src, 180)
	case 4:
		return flipImage(rotateImage(src, 180))
	case 5:
		return flipImage(rotateImage(src, 90))
	case 6:
		return rotateImage(src, 90)
	case 7:
		return flipImage(rotateImage(src, 270))
	case 8:
		return rotateImage(src, 270)
	}
	return src
}

// flipImage mirrors a picture left to right.
func flipImage(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(b.Dx()-1-x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// rotateImage turns a picture by 90, 180 or 270 degrees.
func rotateImage(src image.Image, deg int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if deg == 180 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(x, y, src.At(b.Min.X+x, b.Max.Y-1-y))
			}
		}
		return dst
	}
	// 90 or 270: the output is h x w.
	dst = image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			if deg == 90 { // clockwise
				nx, ny = h-1-y, x
			} else { // 270
				nx, ny = y, w-1-x
			}
			dst.Set(nx, ny, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// brightnessImage adds delta (-100..100) to every channel, clamped.
func brightnessImage(src image.Image, delta int) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// delta is a percentage of 255.
	add := int64(delta * 255 / 100)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			dst.Set(x, y, color.RGBA{
				R: clamp8(int64(r>>8) + add),
				G: clamp8(int64(g>>8) + add),
				B: clamp8(int64(bl>>8) + add),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

// autoLightImage stretches each channel's histogram so the darkest pixel
// becomes black and the brightest white (simple "fix the light").
func autoLightImage(src image.Image) image.Image {
	b := src.Bounds()
	var minR, minG, minB, maxR, maxG, maxB uint32 = 255, 255, 255, 0, 0, 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := src.At(x, y).RGBA()
			r, g, bl = r>>8, g>>8, bl>>8
			if r < minR {
				minR = r
			}
			if g < minG {
				minG = g
			}
			if bl < minB {
				minB = bl
			}
			if r > maxR {
				maxR = r
			}
			if g > maxG {
				maxG = g
			}
			if bl > maxB {
				maxB = bl
			}
		}
	}
	stretch := func(v, min, max uint32) uint8 {
		if max <= min {
			return uint8(v)
		}
		return uint8((v - min) * 255 / (max - min))
	}
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			dst.Set(x, y, color.RGBA{
				R: stretch(r>>8, minR, maxR),
				G: stretch(g>>8, minG, maxG),
				B: stretch(bl>>8, minB, maxB),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

func clamp8(v int64) uint8 {
	if v > 255 {
		return 255
	}
	if v < 0 {
		return 0
	}
	return uint8(v)
}
