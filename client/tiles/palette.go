package tiles

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"math"
	"sort"
)

const (
	PanelColors = 128
	tilePx      = 256
)

type rgb struct{ r, g, b uint8 }

// Night: invert lightness and keep hue, so land goes near black, labels go
// light and water stays blue.  Plain inversion would make white roads darker
// than land, so near-white is lifted to a mid grey instead, and saturated
// features (major roads, parks) get a floor so they stay visible.
func nightColor(c rgb) rgb {
	h, l, s := rgbToHLS(float64(c.r)/255, float64(c.g)/255, float64(c.b)/255)
	var nl float64
	if l > 0.97 && s < 0.5 {
		nl = 0.26
	} else {
		nl = 0.06 + (1-l)*0.85
		// saturated light colours: major roads, but also parks and heath,
		// so the floor stays low and saturation is pulled well down
		if s > 0.6 && l > 0.6 {
			nl = max(nl, 0.17)
		}
	}
	ns := s * 0.32
	if l < 0.6 {
		ns = s * 0.6
	}
	r, g, b := hlsToRGB(h, min(nl, 0.88), ns)
	return rgb{uint8(r * 255), uint8(g * 255), uint8(b * 255)}
}

// as Python's colorsys
func rgbToHLS(r, g, b float64) (h, l, s float64) {
	maxc, minc := max(r, g, b), min(r, g, b)
	l = (maxc + minc) / 2
	if maxc == minc {
		return 0, l, 0
	}
	rng := maxc - minc
	if l <= 0.5 {
		s = rng / (maxc + minc)
	} else {
		s = rng / (2 - maxc - minc)
	}
	rc, gc, bc := (maxc-r)/rng, (maxc-g)/rng, (maxc-b)/rng
	switch {
	case r == maxc:
		h = bc - gc
	case g == maxc:
		h = 2 + rc - bc
	default:
		h = 4 + gc - rc
	}
	return frac(h / 6), l, s
}

func frac(v float64) float64 { return v - math.Floor(v) }

func hlsToRGB(h, l, s float64) (float64, float64, float64) {
	if s == 0 {
		return l, l, l
	}
	m2 := l + s - l*s
	if l <= 0.5 {
		m2 = l * (1 + s)
	}
	m1 := 2*l - m2
	v := func(hue float64) float64 {
		hue = frac(hue)
		switch {
		case hue < 1.0/6:
			return m1 + (m2-m1)*hue*6
		case hue < 0.5:
			return m2
		case hue < 2.0/3:
			return m1 + (m2-m1)*(2.0/3-hue)*6
		}
		return m1
	}
	return v(h + 1.0/3), v(h), v(h - 1.0/3)
}

// to565 is the nearest colour the panel can show for an 8-bit channel,
// given as 8 bits again the way the panel expands a 5- or 6-bit one.
func to565(v uint8, bits uint) uint8 {
	x := (uint(v)*((1<<bits)-1) + 127) / 255
	return uint8(x<<(8-bits) | x>>(2*bits-8))
}

// indexed is a palette image: pal[idx[i]] is pixel i.
type indexed struct {
	w, h int
	pal  []rgb
	idx  []uint8
}

func fromImage(img image.Image) indexed {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if p, ok := img.(*image.Paletted); ok && len(p.Palette) <= 256 {
		opaque := true
		pal := make([]rgb, len(p.Palette))
		for i, c := range p.Palette {
			n := color.NRGBAModel.Convert(c).(color.NRGBA)
			opaque = opaque && n.A == 0xff
			pal[i] = rgb{n.R, n.G, n.B}
		}
		if opaque {
			idx := make([]uint8, 0, w*h)
			for y := 0; y < h; y++ {
				row := p.Pix[y*p.Stride:]
				idx = append(idx, row[:w]...)
			}
			return indexed{w, h, pal, idx}
		}
	}
	// the panel has no alpha
	px := make([]rgb, 0, w*h)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			n := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			px = append(px, rgb{n.R, n.G, n.B})
		}
	}
	return quantize(w, h, px, 256)
}

func (im indexed) pixels() []rgb {
	px := make([]rgb, len(im.idx))
	for i, v := range im.idx {
		px[i] = im.pal[v]
	}
	return px
}

func (im indexed) usedColors() int {
	seen := map[rgb]struct{}{}
	for _, v := range im.idx {
		seen[im.pal[v]] = struct{}{}
	}
	return len(seen)
}

type box struct {
	cols []rgb
	n    int
}

func channel(c rgb, ch int) uint8 {
	switch ch {
	case 0:
		return c.r
	case 1:
		return c.g
	}
	return c.b
}

// widest channel and its range
func (bx box) span() (int, int) {
	best, bestR := 0, -1
	for ch := 0; ch < 3; ch++ {
		lo, hi := uint8(255), uint8(0)
		for _, c := range bx.cols {
			v := channel(c, ch)
			lo, hi = min(lo, v), max(hi, v)
		}
		if int(hi)-int(lo) > bestR {
			best, bestR = ch, int(hi)-int(lo)
		}
	}
	return best, bestR
}

// quantize to at most n colours by median cut, without dithering; exact
// when there are no more than n colours to start with.
func quantize(w, h int, px []rgb, n int) indexed {
	hist := map[rgb]int{}
	for _, c := range px {
		hist[c]++
	}
	cols := make([]rgb, 0, len(hist))
	for c := range hist {
		cols = append(cols, c)
	}
	sort.Slice(cols, func(i, j int) bool {
		a, b := cols[i], cols[j]
		return a.r < b.r || a.r == b.r && (a.g < b.g || a.g == b.g && a.b < b.b)
	})

	boxes := []box{{cols, len(px)}}
	for len(boxes) < n && len(boxes) < len(cols) {
		pick, pickR := -1, 0
		for i, bx := range boxes {
			if len(bx.cols) < 2 {
				continue
			}
			if _, r := bx.span(); r > pickR || r == pickR && pick >= 0 && bx.n > boxes[pick].n {
				pick, pickR = i, r
			}
		}
		if pick < 0 {
			break
		}
		bx := boxes[pick]
		ch, _ := bx.span()
		sort.SliceStable(bx.cols, func(i, j int) bool { return channel(bx.cols[i], ch) < channel(bx.cols[j], ch) })
		// split at the pixel-weighted median, leaving both halves non-empty
		cut, acc := 1, 0
		for i, c := range bx.cols[:len(bx.cols)-1] {
			acc += hist[c]
			cut = i + 1
			if acc*2 >= bx.n {
				break
			}
		}
		a, b := box{bx.cols[:cut], 0}, box{bx.cols[cut:], 0}
		for _, c := range a.cols {
			a.n += hist[c]
		}
		b.n = bx.n - a.n
		boxes[pick] = a
		boxes = append(boxes, b)
	}

	out := indexed{w: w, h: h, pal: make([]rgb, len(boxes)), idx: make([]uint8, len(px))}
	where := make(map[rgb]uint8, len(cols))
	for i, bx := range boxes {
		var r, g, b, cnt int
		for _, c := range bx.cols {
			k := hist[c]
			r += int(c.r) * k
			g += int(c.g) * k
			b += int(c.b) * k
			cnt += k
			where[c] = uint8(i)
		}
		out.pal[i] = rgb{uint8((r + cnt/2) / cnt), uint8((g + cnt/2) / cnt), uint8((b + cnt/2) / cnt)}
	}
	for i, c := range px {
		out.idx[i] = where[c]
	}
	return out
}

// MakeVariant makes a panel tile: a 256x256 palette PNG, colours snapped to
// RGB565 so what the panel shows is what was chosen here.
func MakeVariant(data []byte, skin int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	im := fromImage(img)
	if im.w == 0 || im.h == 0 {
		return nil, fmt.Errorf("empty image")
	}
	if skin > 0 {
		for i, c := range im.pal {
			im.pal[i] = skinColor(c, skin)
		}
	}
	// also merges colours a skin mapped together, so the palette fits
	if len(im.pal) > PanelColors {
		im = quantize(im.w, im.h, im.pixels(), PanelColors)
	}

	pal := make(color.Palette, len(im.pal))
	for i, c := range im.pal {
		pal[i] = color.RGBA{to565(c.r, 5), to565(c.g, 6), to565(c.b, 5), 0xff}
	}
	out := image.NewPaletted(image.Rect(0, 0, tilePx, tilePx), pal)
	for y := 0; y < tilePx; y++ {
		sy := y * im.h / tilePx
		for x := 0; x < tilePx; x++ {
			out.Pix[y*out.Stride+x] = im.idx[sy*im.w+x*im.w/tilePx]
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
