package main

import (
	"fmt"
	"math"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

func main() {
	ways, _ := osm.Load("testdata/nyc-rail.geojson")
	minLon, minLat, maxLon, maxLat := 180.0, 90.0, -180.0, -90.0
	for _, w := range ways {
		for _, c := range w.Coords {
			minLon, maxLon = math.Min(minLon, c.Lon), math.Max(maxLon, c.Lon)
			minLat, maxLat = math.Min(minLat, c.Lat), math.Max(maxLat, c.Lat)
		}
	}
	frame := geo.NewFrame(geo.LL{Lon: (minLon + maxLon) / 2, Lat: (minLat + maxLat) / 2})
	tracks := make([]bundle.Track, len(ways))
	for i, w := range ways {
		pts := make([]geo.Pt, len(w.Coords))
		for j, ll := range w.Coords {
			pts[j] = frame.ToXY(ll)
		}
		tracks[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts)}
	}
	strands := bundle.Chain(tracks, 1.0)
	lines := make([]*geo.Line, len(strands))
	for i, s := range strands {
		lines[i] = s.Line
	}
	grid := geo.NewGrid(lines, 64)
	sp := bundle.DefaultSoundParams()
	si := 275
	l := lines[si]
	for arc := 9300.0; arc <= 10900; arc += 100 {
		pt := l.AtArc(arc)
		tan := l.TangentAtArc(arc, sp.SampleStep)
		var ids []int
		grid.Near(pt, sp.MaxGap+2, func(oi int) {
			if oi == si {
				return
			}
			for _, c := range lines[oi].CrossSection(pt, tan, sp.MaxGap+2) {
				off := math.Abs(c.Offset)
				if off >= sp.MinGap && off <= sp.MaxGap && c.Parallel >= sp.MinParallel {
					ids = append(ids, oi)
					return
				}
			}
		})
		ll := frame.ToLL(pt)
		fmt.Printf("arc %5.0f  @%.5f,%.5f  alongside %v\n", arc, ll.Lat, ll.Lon, ids)
	}
}
