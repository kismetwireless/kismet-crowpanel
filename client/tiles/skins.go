package tiles

import "math"

// Skins re-colour OSM tiles to match the panel's themes.  The ids are the
// tile request's skin byte (ui/widget_map.h); 0 and 1 are the old day and
// night, so "day" and "night" are their aliases.
var Skins = []string{"classic", "amber", "ocean", "sunlight", "red", "cyberpunk"}

// SkinID is the skin for a name or alias, or -1
func SkinID(name string) int {
	switch name {
	case "day":
		return 0
	case "night":
		return 1
	}
	for i, n := range Skins {
		if n == name {
			return i
		}
	}
	return -1
}

// OSM Carto's standard colours, by what they draw
var osm = []struct {
	role string
	c    rgb
}{
	{"land", rgb{0xf2, 0xef, 0xe9}},
	{"resid", rgb{0xe0, 0xdf, 0xdf}},
	{"building", rgb{0xd9, 0xd0, 0xc9}},
	{"bedge", rgb{0xc4, 0xb6, 0xab}},
	{"commercial", rgb{0xf2, 0xda, 0xd9}},
	{"retail", rgb{0xff, 0xd6, 0xd1}},
	{"industrial", rgb{0xeb, 0xdb, 0xe8}},
	{"farmland", rgb{0xee, 0xf0, 0xd5}},
	{"sand", rgb{0xff, 0xf1, 0xba}},
	{"parking", rgb{0xee, 0xee, 0xee}},
	{"water", rgb{0xaa, 0xd3, 0xdf}},
	{"waterink", rgb{0x4d, 0x80, 0xb3}},
	{"park", rgb{0xc8, 0xfa, 0xcc}},
	{"forest", rgb{0xad, 0xd1, 0x9e}},
	{"grass", rgb{0xcd, 0xeb, 0xb0}},
	{"scrub", rgb{0xc8, 0xd7, 0xab}},
	{"pitch", rgb{0xaa, 0xe0, 0xcb}},
	{"motorway", rgb{0xe8, 0x92, 0xa2}},
	{"trunk", rgb{0xf9, 0xb2, 0x9c}},
	{"primary", rgb{0xfc, 0xd6, 0xa4}},
	{"secondary", rgb{0xf7, 0xfa, 0xbf}},
	{"minor", rgb{0xff, 0xff, 0xff}},
	{"casing", rgb{0xbb, 0xbb, 0xbb}},
	{"rail", rgb{0x70, 0x70, 0x70}},
	{"footway", rgb{0xfa, 0x80, 0x72}},
	{"ink", rgb{0x00, 0x00, 0x00}},
	{"ink2", rgb{0x55, 0x55, 0x55}},
	{"boundary", rgb{0xac, 0x46, 0xac}},
}

func rgbOf(v uint32) rgb { return rgb{uint8(v >> 16), uint8(v >> 8), uint8(v)} }

// Targets by role; a role left out keeps OSM's colour
var skinTargets = map[string]map[string]uint32{
	"ocean": {
		"land": 0xe9eff4, "resid": 0xdde5ec, "building": 0xcfd9e2, "bedge": 0xb5c3cf, "commercial": 0xe3e6f0,
		"retail": 0xe6e3f0, "industrial": 0xe0e2ee, "farmland": 0xe6efec, "sand": 0xeef0e6, "parking": 0xe4eaf0,
		"water": 0x8cc4ea, "waterink": 0x1a64b0, "park": 0xcdebe0, "forest": 0xb3d8cc, "grass": 0xd2ebe2,
		"scrub": 0xc6dcd4, "pitch": 0xb4e0d8, "motorway": 0x4f8fcf, "trunk": 0x74a9dc, "primary": 0xa2c8ea,
		"secondary": 0xd4e6f5, "casing": 0xa9b8c6, "rail": 0x5f6f80, "footway": 0x5aa0c8, "ink": 0x08141f,
		"ink2": 0x475d72, "boundary": 0x7b6fb8,
	},
	"sunlight": {
		"land": 0xf6f6f2, "resid": 0xebebeb, "building": 0xcfcac4, "bedge": 0x8f8880, "commercial": 0xf6e0de,
		"retail": 0xffd4cc, "industrial": 0xecd6e8, "farmland": 0xeef2cc, "sand": 0xfff0a8, "parking": 0xe6e6e6,
		"water": 0x6fb6e6, "waterink": 0x0040a8, "park": 0xa6ec9c, "forest": 0x7cc46a, "grass": 0xb6e690,
		"scrub": 0xa8c888, "pitch": 0x7fd6b4, "motorway": 0xe0405a, "trunk": 0xf07a3a, "primary": 0xf5b000,
		"secondary": 0xfff060, "minor": 0xffffff, "casing": 0x5a5a5a, "rail": 0x303030, "footway": 0xe0402e,
		"ink": 0x000000, "ink2": 0x2e2e2e, "boundary": 0x8b0057,
	},
	"cyberpunk": {
		"land": 0x140b22, "resid": 0x190e2b, "building": 0x2a1745, "bedge": 0x4a2a78, "commercial": 0x220e30,
		"retail": 0x2a0e34, "industrial": 0x1e1236, "farmland": 0x14101f, "sand": 0x1f1428, "parking": 0x1c1030,
		"water": 0x051838, "waterink": 0x00e5ff, "park": 0x0a2420, "forest": 0x0b2a22, "grass": 0x0c241f,
		"scrub": 0x0e221e, "pitch": 0x0a2a2a, "motorway": 0xf8e71c, "trunk": 0xff2bd6, "primary": 0x00e5ff,
		"secondary": 0x39ff14, "minor": 0xc020a8, "casing": 0x3c1468, "rail": 0x6a58a0, "footway": 0x8a2a7a,
		"ink": 0xe6f3ff, "ink2": 0x8c7fb0, "boundary": 0xb388ff,
	},
}

// blended maps a colour by its nearness to each OSM colour, so the
// anti-aliased edge between two features lands between their targets
func blended(c rgb, targets map[string]uint32) rgb {
	const sigma2 = 2 * 14.0 * 14.0
	d2 := make([]float64, len(osm))
	least := math.Inf(1)
	for i, a := range osm {
		dr, dg, db := float64(c.r)-float64(a.c.r), float64(c.g)-float64(a.c.g), float64(c.b)-float64(a.c.b)
		d2[i] = dr*dr + dg*dg + db*db
		least = min(least, d2[i])
	}
	var r, g, b, w float64
	for i, a := range osm {
		k := math.Exp(-(d2[i] - least) / sigma2)
		if k < 1e-4 {
			continue
		}
		t := a.c
		if v, ok := targets[a.role]; ok {
			t = rgbOf(v)
		}
		r += k * float64(t.r)
		g += k * float64(t.g)
		b += k * float64(t.b)
		w += k
	}
	return rgb{uint8(r/w + 0.5), uint8(g/w + 0.5), uint8(b/w + 0.5)}
}

// Red: the night map in reds only, for dark adaptation
func redColor(c rgb) rgb {
	n := nightColor(c)
	y := 0.299*float64(n.r) + 0.587*float64(n.g) + 0.114*float64(n.b)
	return rgb{uint8(min(255, y*1.4)), uint8(y * 0.12), uint8(y * 0.08)}
}

func skinColor(c rgb, skin int) rgb {
	switch Skins[skin] {
	case "amber":
		return nightColor(c)
	case "red":
		return redColor(c)
	case "classic":
		return c
	}
	return blended(c, skinTargets[Skins[skin]])
}

// SkinLand is the skin's land colour, for the panel's empty map
func SkinLand(skin int) rgb { return skinColor(osm[0].c, skin) }
