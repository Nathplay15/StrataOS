// Package icon draws the app icon (the layers of StrataOS) in the colors
// of the sync states, as PNG and ICO, so that no image files are needed.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// State selects the color of the icon.
type State int

const (
	Idle State = iota
	Syncing
	Paused
	Error
)

var colors = map[State]color.RGBA{
	Idle:    {79, 107, 255, 255},
	Syncing: {34, 184, 240, 255},
	Paused:  {128, 136, 156, 255},
	Error:   {220, 38, 38, 255},
}

// coverage of the pixel (x, y) of a size×size icon, 4×4 supersampled.
func coverage(x, y, size int, inside func(fx, fy float64) bool) float64 {
	n := 0
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			fx := (float64(x) + (float64(i)+0.5)/4) / float64(size)
			fy := (float64(y) + (float64(j)+0.5)/4) / float64(size)
			if inside(fx, fy) {
				n++
			}
		}
	}
	return float64(n) / 16
}

// roundRect: a rounded square of the whole icon.
func roundRect(fx, fy float64) bool {
	const r = 0.22
	cx := math.Max(r, math.Min(1-r, fx))
	cy := math.Max(r, math.Min(1-r, fy))
	return (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= r*r
}

// layer: one of three slanted bars.
func layer(fx, fy float64) bool {
	for i, y := range []float64{0.30, 0.50, 0.70} {
		w := 0.44 + float64(i)*0.12
		x0 := 0.5 - w/2
		if fx >= x0 && fx <= x0+w && math.Abs(fy-y) <= 0.065 {
			return true
		}
	}
	return false
}

// Image draws the icon.
func Image(s State, size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := colors[s]
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			a := coverage(x, y, size, roundRect)
			if a == 0 {
				continue
			}
			w := coverage(x, y, size, layer)
			mix := func(c uint8) uint8 { return uint8(float64(c)*(1-w) + 255*w) }
			img.SetRGBA(x, y, color.RGBA{uint8(float64(mix(bg.R)) * a), uint8(float64(mix(bg.G)) * a), uint8(float64(mix(bg.B)) * a), uint8(255 * a)})
		}
	}
	return img
}

// PNG returns the icon as PNG.
func PNG(s State, size int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, Image(s, size))
	return b.Bytes()
}

// ICO returns the icon as a Windows icon (PNG images inside).
func ICO(s State, sizes ...int) []byte {
	if len(sizes) == 0 {
		sizes = []int{16, 32, 48, 256}
	}
	var imgs [][]byte
	for _, n := range sizes {
		imgs = append(imgs, PNG(s, n))
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, n := range sizes {
		d := uint8(n)
		if n >= 256 {
			d = 0
		}
		b.Write([]byte{d, d, 0, 0})
		_ = binary.Write(&b, binary.LittleEndian, []uint16{1, 32})
		_ = binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, im := range imgs {
		b.Write(im)
	}
	return b.Bytes()
}

// ICNS returns the icon for a macOS app bundle (PNG images inside).
func ICNS(s State) []byte {
	types := []struct {
		t    string
		size int
	}{{"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024}}
	var body bytes.Buffer
	for _, x := range types {
		data := PNG(s, x.size)
		body.WriteString(x.t)
		_ = binary.Write(&body, binary.BigEndian, uint32(8+len(data)))
		body.Write(data)
	}
	var b bytes.Buffer
	b.WriteString("icns")
	_ = binary.Write(&b, binary.BigEndian, uint32(8+body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}
