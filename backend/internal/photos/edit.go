package photos

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
)

// EditJPEG applies a rotation, a brightness shift and/or an automatic light
// fix to a JPEG picture and returns the new JPEG (quality 90). Pure Go, no
// external tool: the gallery edits a photo without leaving the NAS.
// rotate is in degrees (multiple of 90), brightness in -100..100.
func EditJPEG(src []byte, rotate, brightness int, autoLight bool) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("not a JPEG picture: %w", err)
	}
	switch ((rotate % 360) + 360) % 360 {
	case 0:
	case 90, 180, 270:
		img = rotateImage(img, ((rotate%360)+360)%360)
	default:
		return nil, fmt.Errorf("rotation must be a multiple of 90°")
	}
	if autoLight {
		img = autoLightImage(img)
	}
	if brightness != 0 {
		img = brightnessImage(img, brightness)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
