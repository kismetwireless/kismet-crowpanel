// kw-fixture makes test inputs for the display harnesses with the client's
// own code:
//
//	kw-fixture variant SKIN IN OUT [IN OUT ...]   (a skin name, or day|night)
//	    panel tiles from source PNGs
//	kw-fixture turned SKIN Z ROT LAT LON DIR
//	    turned tiles (heading-up) round lat/lon from the panel tiles in
//	    DIR/SKIN/Z/X/Y.png, written to DIR/SKIN/rROT/Z/X/Y.png
//	kw-fixture markers SPEC OUTDIR
//	    markers.bin, track.bin and detail.json from a JSON spec; prints the
//	    detail device's id
//
// SPEC: {"lat", "lon", "z", "w", "h", "devices": [{"key", "dx", "dy", "phy",
// "mac", "name", "first_ago", "last_ago", "peak", "signal", "packets"}],
// "track": [[dx, dy], ...], "detail": key}.  dx, dy are world px from
// lat/lon at z.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kismet-crowpanel/markers"
	"kismet-crowpanel/tiles"
)

type spec struct {
	Lat, Lon float64
	Z, W, H  int
	Devices  []struct {
		Key, Phy, MAC, Name string
		DX, DY              float64
		FirstAgo            float64 `json:"first_ago"`
		LastAgo             float64 `json:"last_ago"`
		Peak, Signal        int
		Packets             int64
		// a device at its own reported position, e.g. an aircraft
		Type     string
		Reported bool
		Heading  float64
	}
	Track  [][2]float64
	Detail string
}

func unproject(x, y float64, z int) (float64, float64) {
	scale := 256 * math.Pow(2, float64(z))
	lon := x/scale*360 - 180
	lat := math.Atan(math.Sinh(math.Pi*(1-2*y/scale))) * 180 / math.Pi
	return lat, lon
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "kw-fixture: "+f+"\n", a...)
	os.Exit(1)
}

func variant(theme string, pairs []string) {
	if len(pairs)%2 != 0 {
		fail("variant needs IN OUT pairs")
	}
	for i := 0; i < len(pairs); i += 2 {
		in, err := os.ReadFile(pairs[i])
		if err != nil {
			fail("%v", err)
		}
		sk := tiles.SkinID(theme)
		if sk < 0 {
			fail("unknown skin %q", theme)
		}
		out, err := tiles.MakeVariant(in, sk)
		if err != nil {
			fail("%s: %v", pairs[i], err)
		}
		os.MkdirAll(filepath.Dir(pairs[i+1]), 0o755)
		if err := os.WriteFile(pairs[i+1], out, 0o644); err != nil {
			fail("%v", err)
		}
	}
}

func turned(skin string, args []string) {
	var z, rot int
	var lat, lon float64
	if _, err := fmt.Sscan(strings.Join(args[:4], " "), &z, &rot, &lat, &lon); err != nil {
		fail("turned: %v", err)
	}
	dir, sk := args[4], tiles.SkinID(skin)
	if sk < 0 {
		fail("unknown skin %q", skin)
	}
	n := 1 << z
	wx, wy := markers.WorldPx(lat, lon, z)
	a := 2 * math.Pi * float64(rot) / tiles.RotSteps
	rx, ry := math.Cos(a)*wx+math.Sin(a)*wy, math.Cos(a)*wy-math.Sin(a)*wx
	off := 2 << z
	cx, cy := int(math.Floor(rx/256))+off, int(math.Floor(ry/256))+off
	made := 0
	for y := cy - 4; y <= cy+4; y++ {
		for x := cx - 4; x <= cx+4; x++ {
			tx0, ty0, tx1, ty1 := tiles.TurnedSources(z, x, y, rot)
			var pngs [][]byte
			for ty := ty0; ty <= ty1; ty++ {
				for tx := tx0; tx <= tx1; tx++ {
					b, _ := os.ReadFile(filepath.Join(dir, skin, fmt.Sprint(z), fmt.Sprint(((tx%n)+n)%n),
						fmt.Sprintf("%d.png", ty)))
					pngs = append(pngs, b)
				}
			}
			out, err := tiles.MakeTurned(z, x, y, rot, sk, pngs)
			if err != nil {
				fail("%d/%d/%d r%d: %v", z, x, y, rot, err)
			}
			p := filepath.Join(dir, skin, fmt.Sprintf("r%d", rot), fmt.Sprint(z), fmt.Sprint(x), fmt.Sprintf("%d.png", y))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, out, 0o644); err != nil {
				fail("%v", err)
			}
			made++
		}
	}
	fmt.Println(made)
}

func markerFiles(specPath, dir string) {
	b, err := os.ReadFile(specPath)
	if err != nil {
		fail("%v", err)
	}
	var s spec
	if err := json.Unmarshal(b, &s); err != nil {
		fail("%s: %v", specPath, err)
	}
	now := float64(time.Now().UnixNano()) / 1e9
	cx, cy := markers.WorldPx(s.Lat, s.Lon, s.Z)
	dm := markers.NewDeviceMap()
	dm.SetClock(now)
	for _, d := range s.Devices {
		lat, lon := unproject(cx+d.DX, cy+d.DY, s.Z)
		dm.Update(d.Key, lat, lon, markers.Info{Name: d.Name, MAC: d.MAC, Phy: d.Phy, Signal: d.Signal, Peak: d.Peak,
			First: now - d.FirstAgo, Last: now - d.LastAgo, Packets: d.Packets, Type: d.Type,
			Reported: d.Reported, Heading: d.Heading})
	}
	var tl markers.TrackLog
	for _, p := range s.Track {
		lat, lon := unproject(cx+p[0], cy+p[1], s.Z)
		tl.Add(3, lat, lon)
	}
	_, track := tl.Payload()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			fail("%v", err)
		}
	}
	write("markers.bin", dm.Markers(markers.View{Seq: 1, Z: s.Z, Lat: s.Lat, Lon: s.Lon, W: s.W, H: s.H,
		PhyMask: markers.PhyAll}))
	write("track.bin", track)
	if s.Detail != "" {
		id, ok := dm.ID(s.Detail)
		if !ok {
			fail("no device %q", s.Detail)
		}
		d, _ := dm.Detail(id)
		j, _ := json.Marshal(d)
		write("detail.json", j)
		fmt.Println(id)
	}
}

func main() {
	a := os.Args[1:]
	switch {
	case len(a) >= 2 && a[0] == "variant":
		variant(a[1], a[2:])
	case len(a) == 7 && a[0] == "turned":
		turned(a[1], a[2:])
	case len(a) == 3 && a[0] == "markers":
		markerFiles(a[1], a[2])
	default:
		fail("usage: kw-fixture variant day|night IN OUT ... | turned SKIN Z ROT LAT LON DIR | markers SPEC OUTDIR")
	}
}
