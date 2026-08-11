package pipeline

import (
	"math"
	"math/rand"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// markerGrid replaced two linear scans, and the danger in that trade is
// not speed but SILENCE: a grid that misses a marker does not fail, it
// places a chain over a station label. So both queries are checked
// against the brute-force scan they replaced, on the awkward cases —
// points exactly on a cell boundary, at the reach, and spread across
// negative coordinates, which frame space is full of.

func bruteAny(pts []geo.Pt, p geo.Pt, reach float64) bool {
	for _, q := range pts {
		dx, dy := p.X-q.X, p.Y-q.Y
		if dx*dx+dy*dy < reach*reach {
			return true
		}
	}
	return false
}

func randomMarkers(n int, spread float64) []geo.Pt {
	r := rand.New(rand.NewSource(7))
	pts := make([]geo.Pt, n)
	for i := range pts {
		// centred on zero so half the coordinates are negative: an
		// int-truncating cell key gets those wrong, math.Floor does not
		pts[i] = geo.Pt{
			X: (r.Float64() - 0.5) * spread,
			Y: (r.Float64() - 0.5) * spread,
		}
	}
	return pts
}

func TestMarkerGridAnyMatchesBruteForce(t *testing.T) {
	pts := randomMarkers(400, 6000)
	g := newMarkerGrid(pts)
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 4000; i++ {
		p := geo.Pt{X: (r.Float64() - 0.5) * 6000, Y: (r.Float64() - 0.5) * 6000}
		if got, want := g.any(p, catStationClearM), bruteAny(pts, p, catStationClearM); got != want {
			t.Fatalf("any(%.1f,%.1f) = %v, brute force says %v", p.X, p.Y, got, want)
		}
	}
	// and on the markers themselves, where the answer must always be yes
	for _, p := range pts {
		if !g.any(p, catStationClearM) {
			t.Fatalf("a marker is not within reach of itself at %.1f,%.1f", p.X, p.Y)
		}
	}
}

func TestMarkerGridAnyAcrossCellBoundaries(t *testing.T) {
	// one marker, probes ringed around it at just inside and just
	// outside the reach — the cases a halo off by one cell gets wrong
	const c = catStationClearM
	for _, origin := range []geo.Pt{{X: 0, Y: 0}, {X: c, Y: c}, {X: -c, Y: -c},
		{X: c / 2, Y: -c / 2}} {
		g := newMarkerGrid([]geo.Pt{origin})
		for deg := 0; deg < 360; deg += 15 {
			th := float64(deg) * math.Pi / 180
			for _, d := range []float64{c * 0.99, c * 1.01} {
				p := geo.Pt{X: origin.X + d*math.Cos(th), Y: origin.Y + d*math.Sin(th)}
				want := d < c
				if got := g.any(p, c); got != want {
					t.Errorf("origin %.0f,%.0f dist %.1f bearing %d: got %v want %v",
						origin.X, origin.Y, d, deg, got, want)
				}
			}
		}
	}
}

func TestMarkerGridInBoxOfFindsEveryMarkerNearTheLine(t *testing.T) {
	pts := randomMarkers(400, 6000)
	g := newMarkerGrid(pts)
	r := rand.New(rand.NewSource(13))
	for i := 0; i < 200; i++ {
		// a vertex-dense polyline, which is what an authored corridor
		// looks like — the case the synthetic grids did NOT have
		var line []geo.Pt
		x, y := (r.Float64()-0.5)*6000, (r.Float64()-0.5)*6000
		for j := 0; j < 40; j++ {
			line = append(line, geo.Pt{X: x, Y: y})
			x += (r.Float64() - 0.5) * 200
			y += (r.Float64() - 0.5) * 200
		}
		const pad = 40.0
		seen := map[geo.Pt]bool{}
		g.inBoxOf(line, pad, func(q geo.Pt) { seen[q] = true })

		// the contract is a PREFILTER: everything the exact projection
		// would accept must survive it. Extra candidates are fine.
		l := geo.NewLine(line)
		for _, q := range pts {
			if _, d := l.ProjectArc(q); d <= pad && !seen[q] {
				t.Fatalf("marker %.1f,%.1f is %.1f m from the line but was not offered",
					q.X, q.Y, d)
			}
		}
	}
}

func TestMarkerGridHandlesNoMarkers(t *testing.T) {
	g := newMarkerGrid(nil)
	if g.any(geo.Pt{}, catStationClearM) {
		t.Error("an empty grid claims a marker")
	}
	n := 0
	g.inBoxOf([]geo.Pt{{X: 0, Y: 0}, {X: 100, Y: 0}}, 40, func(geo.Pt) { n++ })
	if n != 0 {
		t.Errorf("an empty grid offered %d markers", n)
	}
	// a caller may also hand it an empty line
	g2 := newMarkerGrid([]geo.Pt{{X: 0, Y: 0}})
	g2.inBoxOf(nil, 40, func(geo.Pt) { t.Error("an empty line matched a marker") })
}
