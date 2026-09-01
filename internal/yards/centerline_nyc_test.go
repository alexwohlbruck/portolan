package yards

import (
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

func nycIndex(t *testing.T) (*Index, []Track, geo.Frame) {
	t.Helper()
	const path = "../../testdata/nyc-rail.geojson"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	regular, service, err := osm.LoadWithService(path)
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]osm.Way{}, regular...), service...)
	lo, hi := all[0].Coords[0], all[0].Coords[0]
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
		tracks = append(tracks, Track{ID: w.ID, Line: geo.NewLine(pts),
			Service: w.Tags["service"], Level: levelOf(w.Tags)})
	}
	t0 := time.Now()
	ix := Build(tracks, DefaultParams())
	t.Logf("Build: %d tracks in %s", len(tracks), time.Since(t0))
	return ix, tracks, frame
}

// The two rules that need no drawn ground truth:
//   5. every centerline path-matches real track end to end
//   6. smooth, flowing geometry — no jagged edges
func TestCenterlineSelfCheck(t *testing.T) {
	ix, _, frame := nycIndex(t)

	var offs, turns []float64
	worstOff, worstAt := 0.0, geo.LL{}
	worstTurn, turnAt := 0.0, geo.LL{}
	n, totalKm := 0, 0.0
	ended, endsTot := 0, 0
	withCL := 0
	for _, r := range ix.Regions() {
		if len(r.Centerlines) > 0 {
			withCL++
		}
		if len(r.Steel) == 0 {
			continue
		}
		g := geo.NewGrid(r.Steel, 64)
		for _, cl := range r.Centerlines {
			n++
			l := geo.NewLine(cl.Pts)
			totalKm += l.Len() / 1000
			for _, e := range cl.Ends {
				endsTot++
				if e >= 0 {
					ended++
				}
			}
			for _, q := range l.Resample(8) {
				d := g.NearestDist(q, 200)
				if math.IsInf(d, 1) {
					d = 200
				}
				offs = append(offs, d)
				if d > worstOff {
					worstOff, worstAt = d, frame.ToLL(q)
				}
			}
			rs := l.Resample(12)
			for i := 1; i < len(rs)-1; i++ {
				td := geo.TurnDeg(rs[i-1], rs[i], rs[i+1])
				turns = append(turns, td)
				if td > worstTurn {
					worstTurn, turnAt = td, frame.ToLL(rs[i])
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no centerlines produced")
	}
	sort.Float64s(offs)
	sort.Float64s(turns)
	p := func(v []float64, q float64) float64 { return v[int(q*float64(len(v)-1))] }
	t.Logf("%d centerlines, %.1f km, in %d/%d regions; %d/%d ends land on an entrance",
		n, totalKm, withCL, len(ix.Regions()), ended, endsTot)
	t.Logf("rule 5 (dist to nearest member steel): p50 %.1f p90 %.1f p99 %.1f max %.1f @ %.5f,%.5f",
		p(offs, 0.5), p(offs, 0.9), p(offs, 0.99), worstOff, worstAt.Lat, worstAt.Lon)
	t.Logf("rule 6 (turn at 12 m resample):        p50 %.1f p90 %.1f p99 %.1f max %.1f @ %.5f,%.5f",
		p(turns, 0.5), p(turns, 0.9), p(turns, 0.99), worstTurn, turnAt.Lat, turnAt.Lon)

	// rule 4: every entry/exit must carry a centerline. The shortfall is
	// entrances alone in their own track component — nothing to draw a
	// corridor TO — so this is high but not 100%.
	servedTot, entTot := 0, 0
	for _, r := range ix.Regions() {
		hit := map[int]bool{}
		for _, cl := range r.Centerlines {
			for _, e := range cl.Ends {
				if e >= 0 {
					hit[e] = true
				}
			}
		}
		for e := range r.Entrances {
			entTot++
			if hit[e] {
				servedTot++
			}
		}
	}
	frac := float64(servedTot) / math.Max(1, float64(entTot))
	t.Logf("rule 4: %d/%d entrances carry a centerline (%.1f%%)", servedTot, entTot, 100*frac)

	// Gates, with headroom over the measured values (rule 4 98.2%,
	// rule 5 p90 5.6 p99 15.4, rule 6 p99 11.5 max 53.9). These are the
	// two rules that need no drawn ground truth; ctr_cov% in `portolan
	// sound` is what judges whether the corridor took the RIGHT path.
	if frac < 0.95 {
		t.Errorf("rule 4: only %.1f%% of entrances carry a centerline, want >= 95%%", 100*frac)
	}
	if v := p(offs, 0.9); v > 10 {
		t.Errorf("rule 5: p90 stray %.1f m, want <= 10 — a centerline must ride real track", v)
	}
	if v := p(offs, 0.99); v > 25 {
		t.Errorf("rule 5: p99 stray %.1f m, want <= 25", v)
	}
	if v := p(turns, 0.99); v > 20 {
		t.Errorf("rule 6: p99 turn %.1f°, want <= 20 — flowing geometry", v)
	}
	if worstTurn > 60 {
		t.Errorf("rule 6: max turn %.1f°, want <= 60 — a fold escaped the cut", worstTurn)
	}
}
