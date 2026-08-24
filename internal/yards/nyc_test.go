package yards

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

// levelOf mirrors pipeline.levelClass (bridge/tunnel/layer → +1/0/-1);
// the pipeline package cannot be imported from here without a cycle.
func levelOf(tags map[string]string) int {
	if v := tags["bridge"]; v != "" && v != "no" {
		return 1
	}
	if v := tags["tunnel"]; v != "" && v != "no" {
		return -1
	}
	if v := tags["layer"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n > 0 {
				return 1
			}
			if n < 0 {
				return -1
			}
		}
	}
	return 0
}

// TestNYCYards locks the dial defaults against real data: every known
// yard detected, the Queens Blvd 4-track express corridor untouched.
func TestNYCYards(t *testing.T) {
	const path = "../../testdata/nyc-rail.geojson"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	regular, service, err := osm.LoadWithService(path)
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]osm.Way{}, regular...), service...)
	var lo, hi geo.LL
	lo, hi = all[0].Coords[0], all[0].Coords[0]
	for _, w := range all {
		for _, c := range w.Coords {
			lo.Lon, lo.Lat = min(lo.Lon, c.Lon), min(lo.Lat, c.Lat)
			hi.Lon, hi.Lat = max(hi.Lon, c.Lon), max(hi.Lat, c.Lat)
		}
	}
	frame := geo.NewFrame(geo.LL{Lon: (lo.Lon + hi.Lon) / 2, Lat: (lo.Lat + hi.Lat) / 2})
	tracks := make([]Track, 0, len(all))
	for _, w := range all {
		pts := make([]geo.Pt, len(w.Coords))
		for i, c := range w.Coords {
			pts[i] = frame.ToXY(c)
		}
		tracks = append(tracks, Track{
			ID: w.ID, Line: geo.NewLine(pts),
			Service: w.Tags["service"], Level: levelOf(w.Tags),
		})
	}

	t0 := time.Now()
	ix := Build(tracks, DefaultParams())
	t.Logf("Build: %d tracks in %s, %d regions", len(tracks), time.Since(t0), len(ix.Regions()))

	if n := len(ix.Regions()); n < 60 || n > 140 {
		t.Errorf("regions = %d, want 60..140", n)
	}
	yardWays := 0
	for _, tr := range tracks {
		if ix.IsYardWay(tr.ID) {
			yardWays++
		}
	}
	if yardWays != 4816 {
		t.Errorf("IsYardWay ways = %d, want 4816 (yard+siding+spur)", yardWays)
	}

	// Recall: tagged yard steel should overwhelmingly sit inside regions.
	inM, totM := 0.0, 0.0
	for _, tr := range tracks {
		if !svcYard(tr.Service) {
			continue
		}
		l := tr.Line.Len()
		for s := 25.0; s < l; s += 50 {
			totM += 50
			if ix.InYard(tr.Line.AtArc(s)) {
				inM += 50
			}
		}
	}
	// The hull hugs HOT steel + pad; isolated tagged spurs with no
	// parallel neighbours correctly sit outside it now, so recall runs a
	// little below the old dilated-mask footprint (0.885 → ~0.79).
	if recall := inM / totM; recall < 0.75 {
		t.Errorf("tagged-steel recall = %.3f, want >= 0.75", recall)
	} else {
		t.Logf("tagged-steel recall %.3f", recall)
	}

	// Known yards: a tagged-yard way inside each bbox must sit in a region.
	bboxProbe := func(name string, lonLo, latLo, lonHi, latHi float64) {
		for _, tr := range tracks {
			if tr.Service != "yard" {
				continue
			}
			mid := tr.Line.AtArc(tr.Line.Len() / 2)
			ll := frame.ToLL(mid)
			if ll.Lon < lonLo || ll.Lon > lonHi || ll.Lat < latLo || ll.Lat > latHi {
				continue
			}
			if !ix.InYard(mid) {
				t.Errorf("%s: tagged yard way %s midpoint not InYard", name, tr.ID)
			}
			return
		}
		t.Errorf("%s: no tagged yard way found in bbox", name)
	}
	bboxProbe("Coney Island", -73.99, 40.578, -73.965, 40.592)
	bboxProbe("Concourse", -73.90, 40.870, -73.875, 40.884)

	// The false-positive canary: Queens Blvd express tunnels — 4 ridden
	// tracks for miles — must stay out of every region.
	samples, inYard := 0, 0
	for _, tr := range tracks {
		if tr.Service != "" || tr.Level != -1 {
			continue
		}
		l := tr.Line.Len()
		for s := 25.0; s < l; s += 50 {
			ll := frame.ToLL(tr.Line.AtArc(s))
			if ll.Lon < -73.882 || ll.Lon > -73.86 || ll.Lat < 40.727 || ll.Lat > 40.737 {
				continue
			}
			samples++
			if ix.InYard(tr.Line.AtArc(s)) {
				inYard++
			}
		}
	}
	if samples == 0 {
		t.Error("Queens Blvd canary found no tunnel samples")
	} else if frac := float64(inYard) / float64(samples); frac > 0.01 {
		t.Errorf("Queens Blvd tunnel samples InYard = %.3f (%d/%d), want <= 0.01",
			frac, inYard, samples)
	}

	// Entrances must exist at scale without blowing up.
	ents := 0
	for _, r := range ix.Regions() {
		ents += len(r.Entrances)
		if len(r.Outline) < 4 {
			t.Errorf("region %d outline has %d vertices", r.ID, len(r.Outline))
		}
	}
	t.Logf("%d entrances across %d regions", ents, len(ix.Regions()))
	if ents == 0 {
		t.Error("no entrances detected across all of NYC")
	}
}
