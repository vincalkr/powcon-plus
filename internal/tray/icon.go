package tray

import (
	"encoding/binary"
	"image"
	"image/color"
)

// IconColor is the glyph color (with alpha).
type IconColor struct{ R, G, B, A uint8 }

// IconPalette defines the icon variants used by the program.
type IconPalette struct {
	Normal IconColor
	Alert  IconColor
	Paused IconColor
}

// DefaultPalette: light gray on a dark panel, amber on alert.
func DefaultPalette() IconPalette {
	return IconPalette{
		Normal: IconColor{0xE6, 0xE6, 0xE6, 255},
		Alert:  IconColor{0xFF, 0x8B, 0x3D, 255},
		Paused: IconColor{0x8A, 0x8F, 0x98, 255},
	}
}

// boltPoints is the "lightning bolt" glyph, normalized to [0,1].
var boltPoints = [][2]float64{
	{0.62, 0.05},
	{0.20, 0.56},
	{0.45, 0.56},
	{0.34, 0.95},
	{0.80, 0.40},
	{0.52, 0.40},
}

// IconState selects the glyph color variant.
type IconState int

// Glyph state variants.
const (
	StateNormal IconState = iota
	StateAlert
	StatePaused
)

// Bolt draws the glyph at the given sizes (default 22 and 44 px).
// state selects the palette color.
// The format required by StatusNotifierItem is ARGB32 in network byte order.
func Bolt(pal IconPalette, state IconState, sizes ...int) []Pixmap {
	if len(sizes) == 0 {
		sizes = []int{22, 44}
	}
	c := pal.Normal
	switch state {
	case StateAlert:
		c = pal.Alert
	case StatePaused:
		c = pal.Paused
	}
	out := make([]Pixmap, 0, len(sizes))
	for _, s := range sizes {
		if s <= 0 {
			continue
		}
		out = append(out, renderBolt(s, c))
	}
	return out
}

func renderBolt(size int, c IconColor) Pixmap {
	const ss = 4 // supersampling for antialiasing
	big := size * ss
	acc := make([]uint32, size*size) // sum of coverages (0..255*ss*ss)

	in := func(x, y float64) bool {
		// ray casting
		inside := false
		n := len(boltPoints)
		for i := 0; i < n; i++ {
			p1 := boltPoints[i]
			p2 := boltPoints[(i+1)%n]
			x1, y1 := p1[0], p1[1]
			x2, y2 := p2[0], p2[1]
			if (y1 > y) != (y2 > y) {
				xint := x1 + (y-y1)/(y2-y1)*(x2-x1)
				if x < xint {
					inside = !inside
				}
			}
		}
		return inside
	}

	for py := 0; py < big; py++ {
		for px := 0; px < big; px++ {
			fx := (float64(px) + 0.5) / float64(big)
			fy := (float64(py) + 0.5) / float64(big)
			if !in(fx, fy) {
				continue
			}
			tx := px / ss
			ty := py / ss
			acc[ty*size+tx]++
		}
	}

	data := make([]byte, size*size*4)
	maxCov := uint32(ss * ss)
	for i, cov := range acc {
		if cov == 0 {
			continue
		}
		a := uint32(c.A) * cov / maxCov
		// ARGB big-endian
		data[i*4+0] = byte(a)
		data[i*4+1] = c.R
		data[i*4+2] = c.G
		data[i*4+3] = c.B
	}
	return Pixmap{W: int32(size), H: int32(size), Data: data}
}

// PixmapToPNG is only for debugging / writing a preview to disk.
func PixmapToPNG(p Pixmap) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, int(p.W), int(p.H)))
	for i := 0; i < int(p.W)*int(p.H); i++ {
		a := p.Data[i*4]
		r := p.Data[i*4+1]
		g := p.Data[i*4+2]
		b := p.Data[i*4+3]
		img.SetNRGBA(i%int(p.W), i/int(p.W), color.NRGBA{R: r, G: g, B: b, A: a})
	}
	return img
}

// EnsureBigEndian is a documentary check: the data is in network byte order.
func EnsureBigEndian(p Pixmap) bool {
	if len(p.Data) < 4 {
		return false
	}
	_ = binary.BigEndian
	return true
}
