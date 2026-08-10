package stages

import (
	"fmt"
	"os"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

func frameOf(ways []osm.Way) geo.Frame {
	minLon, minLat := 180.0, 90.0
	maxLon, maxLat := -180.0, -90.0
	for _, w := range ways {
		for _, c := range w.Coords {
			minLon, maxLon = min(minLon, c.Lon), max(maxLon, c.Lon)
			minLat, maxLat = min(minLat, c.Lat), max(maxLat, c.Lat)
		}
	}
	return geo.NewFrame(geo.LL{Lon: (minLon + maxLon) / 2, Lat: (minLat + maxLat) / 2})
}

func TestConvergenceNYC(t *testing.T) {
	gtfsPath := localGTFS("5")
	if _, err := os.Stat(gtfsPath); err != nil {
		t.Skip("NYC GTFS not available")
	}
	ways, err := osm.Load("../../testdata/nyc-rail.geojson")
	if err != nil {
		t.Fatal(err)
	}
	frame := frameOf(ways)
	tracks := make([]bundle.Track, len(ways))
	for i, w := range ways {
		pts := make([]geo.Pt, len(w.Coords))
		for j, ll := range w.Coords {
			pts[j] = frame.ToXY(ll)
		}
		tracks[i] = bundle.Track{ID: w.ID, Line: geo.NewLine(pts)}
	}
	feed, err := gtfs.Load(gtfsPath, 0.99)
	if err != nil {
		t.Fatal(err)
	}
	var rail []gtfs.Pattern
	for _, pat := range feed.Patterns {
		tt := pat.Route.Type
		if tt == 0 || tt == 1 || tt == 2 || (tt >= 100 && tt < 200) {
			rail = append(rail, pat)
		}
	}
	paths, err := Match(rail, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}

	// per route: piece sets of all its patterns; how much do they overlap?
	byRoute := map[string][]map[string]bool{}
	for _, p := range paths {
		set := map[string]bool{}
		for _, w := range p.WayIDs {
			if w != "gap" {
				set[w] = true
			}
		}
		byRoute[p.Pattern.Route.ID] = append(byRoute[p.Pattern.Route.ID], set)
	}
	for rid, sets := range byRoute {
		if len(sets) < 2 {
			continue
		}
		// jaccard of every set vs the union of the others' largest
		union := map[string]bool{}
		for _, s := range sets {
			for k := range s {
				union[k] = true
			}
		}
		shared := 0
		for k := range union {
			n := 0
			for _, s := range sets {
				if s[k] {
					n++
				}
			}
			if n >= 2 {
				shared++
			}
		}
		fmt.Printf("route %-5s patterns=%2d union_pieces=%4d shared_by_2+=%4d (%.0f%%)\n",
			rid, len(sets), len(union), shared, 100*float64(shared)/float64(len(union)))
	}
}
