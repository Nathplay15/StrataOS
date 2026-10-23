// Package qr encodes QR codes (ISO/IEC 18004, byte mode), small enough to draw
// the address of the NAS on its own screen.
package qr

import "errors"

// Level is the error correction level.
type Level int

const (
	L Level = iota // ~7 % of the symbol can be restored
	M              // ~15 %
	Q              // ~25 %
	H              // ~30 %
)

// formatBits are the two bits of each level in the format information.
var formatBits = [4]int{1, 0, 3, 2}

// Per level and version (index 0 unused), from the standard's tables.
var eccPerBlock = [4][41]int{
	{-1, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28},
	{-1, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{-1, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
}

var numBlocks = [4][41]int{
	{-1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25},
	{-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49},
	{-1, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68},
	{-1, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81},
}

// Code is an encoded symbol, without quiet zone.
type Code struct {
	Size    int
	Version int
	Mask    int
	mod     [][]bool
	isFunc  [][]bool
}

// Black reports whether the module at column x, row y is dark. Outside the
// symbol (the quiet zone) everything is light.
func (c *Code) Black(x, y int) bool {
	return x >= 0 && y >= 0 && x < c.Size && y < c.Size && c.mod[y][x]
}

// ErrTooLong is returned when the data does not fit in a version 40 symbol.
var ErrTooLong = errors.New("qr: data too long")

// Encode makes the smallest symbol holding data at the given level, with the
// mask of lowest penalty.
func Encode(data []byte, level Level) (*Code, error) { return encode(data, level, 1, -1) }

func numRawDataModules(ver int) int {
	n := (16*ver+128)*ver + 64
	if ver >= 2 {
		align := ver/7 + 2
		n -= (25*align-10)*align - 55
		if ver >= 7 {
			n -= 36
		}
	}
	return n
}

func numDataCodewords(ver int, l Level) int {
	return numRawDataModules(ver)/8 - eccPerBlock[l][ver]*numBlocks[l][ver]
}

func encode(data []byte, l Level, minVer, mask int) (*Code, error) {
	ver := minVer
	for ; ver <= 40; ver++ {
		count := 8
		if ver >= 10 {
			count = 16
		}
		if 4+count+8*len(data) <= numDataCodewords(ver, l)*8 && len(data) < 1<<count {
			break
		}
	}
	if ver > 40 {
		return nil, ErrTooLong
	}
	// Data bits: byte mode, character count, bytes, terminator and padding.
	var bb bitBuf
	bb.put(4, 4)
	if ver >= 10 {
		bb.put(len(data), 16)
	} else {
		bb.put(len(data), 8)
	}
	for _, b := range data {
		bb.put(int(b), 8)
	}
	capacity := numDataCodewords(ver, l) * 8
	bb.put(0, min(4, capacity-len(bb)))
	bb.put(0, (8-len(bb)%8)%8)
	for pad := 0xEC; len(bb) < capacity; pad ^= 0xEC ^ 0x11 {
		bb.put(pad, 8)
	}
	words := make([]byte, len(bb)/8)
	for i, bit := range bb {
		if bit {
			words[i>>3] |= 1 << (7 - i&7)
		}
	}

	c := &Code{Version: ver, Size: ver*4 + 17}
	c.mod = grid(c.Size)
	c.isFunc = grid(c.Size)
	c.drawFunctionPatterns()
	c.drawCodewords(addECCAndInterleave(words, ver, l))
	if mask < 0 {
		best := 1 << 30
		for m := 0; m < 8; m++ {
			c.applyMask(m)
			c.drawFormat(l, m)
			if p := c.penalty(); p < best {
				best, mask = p, m
			}
			c.applyMask(m) // XOR again: undone
		}
	}
	c.Mask = mask
	c.applyMask(mask)
	c.drawFormat(l, mask)
	c.isFunc = nil
	return c, nil
}

func grid(n int) [][]bool {
	g := make([][]bool, n)
	for i := range g {
		g[i] = make([]bool, n)
	}
	return g
}

type bitBuf []bool

func (b *bitBuf) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		*b = append(*b, (v>>i)&1 == 1)
	}
}

func (c *Code) set(x, y int, dark bool) {
	c.mod[y][x] = dark
	c.isFunc[y][x] = true
}

func (c *Code) drawFunctionPatterns() {
	n := c.Size
	for i := 0; i < n; i++ { // timing patterns
		c.set(6, i, i%2 == 0)
		c.set(i, 6, i%2 == 0)
	}
	c.drawFinder(3, 3)
	c.drawFinder(n-4, 3)
	c.drawFinder(3, n-4)
	pos := alignmentPositions(c.Version)
	for i := range pos {
		for j := range pos {
			// Not over the three finder patterns.
			if i == 0 && j == 0 || i == 0 && j == len(pos)-1 || i == len(pos)-1 && j == 0 {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					c.set(pos[i]+dx, pos[j]+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	c.drawFormat(L, 0) // reserves the areas; the real bits come with the mask
	if c.Version >= 7 {
		rem := c.Version
		for i := 0; i < 12; i++ {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		bits := c.Version<<12 | rem
		for i := 0; i < 18; i++ {
			dark := (bits>>i)&1 == 1
			a, b := n-11+i%3, i/3
			c.set(a, b, dark)
			c.set(b, a, dark)
		}
	}
}

// drawFinder draws a finder pattern and its separator around (x, y).
func (c *Code) drawFinder(x, y int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			xx, yy := x+dx, y+dy
			if xx >= 0 && xx < c.Size && yy >= 0 && yy < c.Size {
				d := max(abs(dx), abs(dy))
				c.set(xx, yy, d != 2 && d != 4)
			}
		}
	}
}

func alignmentPositions(ver int) []int {
	if ver == 1 {
		return nil
	}
	num := ver/7 + 2
	step := (ver*8 + num*3 + 5) / (num*4 - 4) * 2
	pos := make([]int, num)
	pos[0] = 6
	for i, p := num-1, ver*4+17-7; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

func (c *Code) drawFormat(l Level, mask int) {
	data := formatBits[l]<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>i)&1 == 1 }
	n := c.Size
	for i := 0; i <= 5; i++ {
		c.set(8, i, bit(i))
	}
	c.set(8, 7, bit(6))
	c.set(8, 8, bit(7))
	c.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		c.set(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		c.set(n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		c.set(8, n-15+i, bit(i))
	}
	c.set(8, n-8, true) // the dark module
}

func addECCAndInterleave(data []byte, ver int, l Level) []byte {
	nb, ecLen := numBlocks[l][ver], eccPerBlock[l][ver]
	raw := numRawDataModules(ver) / 8
	numShort := nb - raw%nb
	shortLen := raw / nb
	div := rsDivisor(ecLen)
	var blocks [][]byte
	k := 0
	for i := 0; i < nb; i++ {
		n := shortLen - ecLen
		if i >= numShort {
			n++
		}
		d := data[k : k+n]
		k += n
		b := append([]byte{}, d...)
		if i < numShort {
			b = append(b, 0) // placeholder, skipped when interleaving
		}
		blocks = append(blocks, append(b, rsRemainder(d, div)...))
	}
	var out []byte
	for i := range blocks[0] {
		for j, b := range blocks {
			if i != shortLen-ecLen || j >= numShort {
				out = append(out, b[i])
			}
		}
	}
	return out
}

func gfMul(x, y byte) byte {
	var z int
	for i := 7; i >= 0; i-- {
		z = z<<1 ^ (z>>7)*0x11D
		z ^= int(y>>i&1) * int(x)
	}
	return byte(z)
}

func rsDivisor(degree int) []byte {
	res := make([]byte, degree)
	res[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range res {
			res[j] = gfMul(res[j], root)
			if j+1 < len(res) {
				res[j] ^= res[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return res
}

func rsRemainder(data, div []byte) []byte {
	res := make([]byte, len(div))
	for _, b := range data {
		f := b ^ res[0]
		copy(res, res[1:])
		res[len(res)-1] = 0
		for i := range res {
			res[i] ^= gfMul(div[i], f)
		}
	}
	return res
}

func (c *Code) drawCodewords(words []byte) {
	n := c.Size
	i := 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5 // skips the vertical timing pattern
		}
		for v := 0; v < n; v++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := v
				if (right+1)&2 == 0 {
					y = n - 1 - v // upward
				}
				if !c.isFunc[y][x] && i < len(words)*8 {
					c.mod[y][x] = words[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func (c *Code) applyMask(m int) {
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			var inv bool
			switch m {
			case 0:
				inv = (x+y)%2 == 0
			case 1:
				inv = y%2 == 0
			case 2:
				inv = x%3 == 0
			case 3:
				inv = (x+y)%3 == 0
			case 4:
				inv = (x/3+y/2)%2 == 0
			case 5:
				inv = x*y%2+x*y%3 == 0
			case 6:
				inv = (x*y%2+x*y%3)%2 == 0
			case 7:
				inv = ((x+y)%2+x*y%3)%2 == 0
			}
			if inv && !c.isFunc[y][x] {
				c.mod[y][x] = !c.mod[y][x]
			}
		}
	}
}

// penalty scores a masked symbol with the four rules of the standard.
func (c *Code) penalty() int {
	n := c.Size
	p := 0
	at := func(x, y int, vertical bool) bool {
		if vertical {
			return c.mod[x][y]
		}
		return c.mod[y][x]
	}
	finder := []bool{true, false, true, true, true, false, true}
	for _, vertical := range []bool{false, true} {
		for y := 0; y < n; y++ {
			run := 0
			for x := 0; x < n; x++ {
				if x > 0 && at(x, y, vertical) == at(x-1, y, vertical) {
					run++
				} else {
					run = 1
				}
				if run == 5 {
					p += 3
				} else if run > 5 {
					p++
				}
			}
			// 1:1:3:1:1 with four light modules on one side.
			for x := 0; x+7 <= n; x++ {
				match := true
				for k, d := range finder {
					if at(x+k, y, vertical) != d {
						match = false
						break
					}
				}
				if !match {
					continue
				}
				lightBefore, lightAfter := true, true
				for k := 1; k <= 4; k++ {
					if x-k >= 0 && at(x-k, y, vertical) {
						lightBefore = false
					}
					if x+6+k < n && at(x+6+k, y, vertical) {
						lightAfter = false
					}
				}
				if lightBefore || lightAfter {
					p += 40
				}
			}
		}
	}
	dark := 0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if c.mod[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n {
				v := c.mod[y][x]
				if v == c.mod[y][x+1] && v == c.mod[y+1][x] && v == c.mod[y+1][x+1] {
					p += 3
				}
			}
		}
	}
	total := n * n
	k := (abs(dark*20-total*10)+total-1)/total - 1
	return p + k*10
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
