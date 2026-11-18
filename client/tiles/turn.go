package tiles

// Turned tiles, for a heading-up map.  The panel can't turn its map fast
// enough itself, so it asks for tiles drawn here already turned.  A turned
// tile at rot (steps of 360/RotSteps degrees clockwise) is the 256 px grid
// square (x, y) of the world, in pixels at zoom z, turned about its origin
// so the heading points up; x and y are offset by 2<<z so they stay
// positive.  Matches map_rotate in ui/widget_map.cpp.

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const RotSteps = 36

// turnGeom is a turned tile's place in the world
type turnGeom struct {
	c, s   float64 // cos and sin of the turn
	ox, oy float64 // its top left in the turned frame
}

func newTurnGeom(z, x, y, rot int) turnGeom {
	a := 2 * math.Pi * float64(rot) / RotSteps
	off := 2 << z
	return turnGeom{math.Cos(a), math.Sin(a), float64((x - off) * tilePx), float64((y - off) * tilePx)}
}

// world is the world px under turned frame point (rx, ry)
func (g turnGeom) world(rx, ry float64) (float64, float64) {
	return g.c*rx - g.s*ry, g.s*rx + g.c*ry
}

// TurnedSources is the range of world tiles under a turned tile, x unwrapped
func TurnedSources(z, x, y, rot int) (tx0, ty0, tx1, ty1 int) {
	return newTurnGeom(z, x, y, rot).sources()
}

func (g turnGeom) sources() (tx0, ty0, tx1, ty1 int) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, d := range [][2]float64{{0, 0}, {tilePx, 0}, {0, tilePx}, {tilePx, tilePx}} {
		wx, wy := g.world(g.ox+d[0], g.oy+d[1])
		minX, minY, maxX, maxY = min(minX, wx), min(minY, wy), max(maxX, wx), max(maxY, wy)
	}
	// a pixel's worth for the bilinear neighbours
	return int(math.Floor((minX - 2) / tilePx)), int(math.Floor((minY - 2) / tilePx)),
		int(math.Floor((maxX + 1) / tilePx)), int(math.Floor((maxY + 1) / tilePx))
}

// GetTurned returns a turned panel tile, made from the panel tiles under
// it; cachedOnly fails with 404 unless all of those are cached.
func (s *Service) GetTurned(skin string, z, x, y, rot int, cachedOnly bool) ([]byte, error) {
	sk := SkinID(skin)
	if sk < 0 {
		return nil, &Error{400, "bad skin"}
	}
	if z < 0 || z > MaxZoom || rot <= 0 || rot >= RotSteps || x < 0 || y < 0 || x >= 4<<z || y >= 4<<z {
		return nil, &Error{400, "bad tile coordinates"}
	}
	if d := s.Doc(); !d.Available {
		return nil, &Error{403, d.Reason}
	}
	cur, _ := s.current()
	base := s.sourceDir(cur.URL)
	path := filepath.Join(base, "panel", "turned", Skins[sk], strconv.Itoa(rot), strconv.Itoa(z),
		strconv.Itoa(x), strconv.Itoa(y)+".png")

	tx0, ty0, tx1, ty1 := TurnedSources(z, x, y, rot)
	cols, rows := tx1-tx0+1, ty1-ty0+1
	n := 1 << z
	pngs := make([][]byte, cols*rows)
	errs := make([]error, cols*rows)
	var wg sync.WaitGroup
	for ty := ty0; ty <= ty1; ty++ {
		if ty < 0 || ty >= n {
			continue // beyond the poles: land
		}
		for tx := tx0; tx <= tx1; tx++ {
			i := (ty-ty0)*cols + tx - tx0
			wg.Add(1)
			go func() {
				defer wg.Done()
				pngs[i], errs[i] = s.Get(skin, z, ((tx%n)+n)%n, ty, cachedOnly)
			}()
		}
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	// still good if made after every tile under it was fetched
	if ts, err := os.Stat(path); err == nil {
		fresh := true
		for ty := max(ty0, 0); ty <= min(ty1, n-1) && fresh; ty++ {
			for tx := tx0; tx <= tx1 && fresh; tx++ {
				zxy := filepath.Join(strconv.Itoa(z), strconv.Itoa(((tx%n)+n)%n), strconv.Itoa(ty)+".png")
				os_, err := os.Stat(filepath.Join(base, zxy))
				fresh = err == nil && !ts.ModTime().Before(os_.ModTime())
			}
		}
		if fresh {
			if b, err := os.ReadFile(path); err == nil {
				now := time.Now()
				os.Chtimes(path, now, now)
				return b, nil
			}
		}
	}

	out, err := MakeTurned(z, x, y, rot, sk, pngs)
	if err != nil {
		return nil, &Error{502, err.Error()}
	}
	s.write(path, out)
	return out, nil
}

// MakeTurned draws a turned tile from the panel tiles under it, the
// TurnedSources range row by row; nil ones are land
func MakeTurned(z, x, y, rot, skin int, pngs [][]byte) ([]byte, error) {
	g := newTurnGeom(z, x, y, rot)
	tx0, ty0, tx1, ty1 := g.sources()
	cols, rows := tx1-tx0+1, ty1-ty0+1
	if len(pngs) != cols*rows {
		return nil, fmt.Errorf("%d tiles under a turned tile, not %d", len(pngs), cols*rows)
	}
	srcs := make([]indexed, cols*rows)
	for i, p := range pngs {
		if p == nil {
			continue
		}
		img, _, err := image.Decode(bytes.NewReader(p))
		if err != nil {
			return nil, fmt.Errorf("bad tile image: %v", err)
		}
		if srcs[i] = fromImage(img); srcs[i].w != tilePx || srcs[i].h != tilePx {
			return nil, fmt.Errorf("bad tile image size")
		}
	}
	return panelPNG(turn(g, srcs, tx0, ty0, cols, rows, SkinLand(skin)))
}

// turn draws the turned tile from the world tiles under it (srcs, cols x
// rows from tile tx0, ty0; missing ones are land), sampled bilinearly
func turn(g turnGeom, srcs []indexed, tx0, ty0, cols, rows int, land rgb) indexed {
	at := func(x, y int) rgb {
		tx, ty := floorDiv(x, tilePx)-tx0, floorDiv(y, tilePx)-ty0
		if tx < 0 || ty < 0 || tx >= cols || ty >= rows {
			return land
		}
		im := srcs[ty*cols+tx]
		if im.idx == nil {
			return land
		}
		return im.pal[im.idx[(y-(ty+ty0)*tilePx)*tilePx+x-(tx+tx0)*tilePx]]
	}
	px := make([]rgb, tilePx*tilePx)
	for j := 0; j < tilePx; j++ {
		for i := 0; i < tilePx; i++ {
			// pixel centres both sides
			wx, wy := g.world(g.ox+float64(i)+0.5, g.oy+float64(j)+0.5)
			wx, wy = wx-0.5, wy-0.5
			x0, y0 := math.Floor(wx), math.Floor(wy)
			fx, fy := wx-x0, wy-y0
			xi, yi := int(x0), int(y0)
			a, b, c, d := at(xi, yi), at(xi+1, yi), at(xi, yi+1), at(xi+1, yi+1)
			// snapped to RGB565 here, which leaves fewer colours to quantize
			mix := func(p, q, r, s uint8, bits uint) uint8 {
				top := float64(p)*(1-fx) + float64(q)*fx
				bot := float64(r)*(1-fx) + float64(s)*fx
				return to565(uint8(math.Round(top*(1-fy)+bot*fy)), bits)
			}
			px[j*tilePx+i] = rgb{mix(a.r, b.r, c.r, d.r, 5), mix(a.g, b.g, c.g, d.g, 6), mix(a.b, b.b, c.b, d.b, 5)}
		}
	}
	return quantize(tilePx, tilePx, px, PanelColors)
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
