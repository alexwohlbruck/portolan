package yards

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// A 6-track ladder with a through track past both throats: the corridor
// through it must run down the MIDDLE of the bundle, not along whichever
// rail the graph happened to offer, and it must reach the entrances.
func TestCenterlineRidesTheBundle(t *testing.T) {
	tracks := append(ladder(6, 4, 4, 100, 500, "yard"),
		Track{ID: "main", Line: straight(0, -300, 800)})
	ix := Build(tracks, DefaultParams())
	if len(ix.Regions()) != 1 {
		t.Fatalf("regions = %d, want 1", len(ix.Regions()))
	}
	r := ix.Regions()[0]
	if len(r.Centerlines) == 0 {
		t.Fatal("no centerlines")
	}

	// rule 3: every centerline connects to at least one entrance
	for i, cl := range r.Centerlines {
		if cl.Ends[0] < 0 && cl.Ends[1] < 0 {
			t.Errorf("centerline %d joins no entrance", i)
		}
		for _, e := range cl.Ends {
			if e >= len(r.Entrances) {
				t.Errorf("centerline %d references entrance %d of %d", i, e, len(r.Entrances))
			}
		}
	}

	// rule 4: every entrance carries a centerline
	served := map[int]bool{}
	for _, cl := range r.Centerlines {
		for _, e := range cl.Ends {
			if e >= 0 {
				served[e] = true
			}
		}
	}
	for i := range r.Entrances {
		if !served[i] {
			t.Errorf("entrance %d (terminal=%v) carries no centerline",
				i, r.Entrances[i].Terminal)
		}
	}

	// rule 5: every sample sits on real steel
	g := geo.NewGrid(r.Steel, 64)
	for i, cl := range r.Centerlines {
		for _, q := range geo.NewLine(cl.Pts).Resample(8) {
			if d := g.NearestDist(q, 100); d > 12 {
				t.Errorf("centerline %d strays %.1f m from any track at %v", i, d, q)
				break
			}
		}
	}

	// rule 6: flowing geometry
	for i, cl := range r.Centerlines {
		rs := geo.NewLine(cl.Pts).Resample(12)
		for j := 1; j < len(rs)-1; j++ {
			if turn := geo.TurnDeg(rs[j-1], rs[j], rs[j+1]); turn > 40 {
				t.Errorf("centerline %d turns %.0f° at vertex %d", i, turn, j)
				break
			}
		}
	}

	// the point of the whole exercise: the corridor crossing the ladder
	// rides the middle of it (tracks at y = 4..24, so the middle is 14),
	// rather than the rail at either edge
	mid, n := 0.0, 0
	for _, cl := range r.Centerlines {
		for _, q := range geo.NewLine(cl.Pts).Resample(10) {
			if q.X < 150 || q.X > 450 {
				continue
			}
			mid += q.Y
			n++
		}
	}
	if n == 0 {
		t.Fatal("no centerline samples inside the ladder")
	}
	if avg := mid / float64(n); math.Abs(avg-14) > 6 {
		t.Errorf("centerlines average y=%.1f across the ladder, want ~14 (its middle)", avg)
	}
}

// A fold is never emitted, whatever the graph did to produce one.
func TestFoldCutsSplitAHairpin(t *testing.T) {
	// out 60 m and straight back — the shape every failed attempt made
	var pts []geo.Pt
	for x := 0.0; x <= 60; x += 12 {
		pts = append(pts, geo.Pt{X: x, Y: 0})
	}
	for x := 48.0; x >= 0; x -= 12 {
		pts = append(pts, geo.Pt{X: x, Y: 1})
	}
	cuts := foldCuts(pts)
	if len(cuts) != 1 {
		t.Fatalf("foldCuts = %v, want exactly one cut at the turnaround", cuts)
	}
	got := splitFolds([]chain{{pts: pts, a: 7, b: 9}})
	if len(got) != 2 {
		t.Fatalf("splitFolds produced %d chains, want 2", len(got))
	}
	if got[0].a != 7 || got[0].b != -1 {
		t.Errorf("first arm ends = %d/%d, want 7/-1 (a cut end is not a node)", got[0].a, got[0].b)
	}
	if got[1].a != -1 || got[1].b != 9 {
		t.Errorf("second arm ends = %d/%d, want -1/9", got[1].a, got[1].b)
	}
	for i, c := range got {
		for j := 1; j < len(c.pts)-1; j++ {
			if turn := geo.TurnDeg(c.pts[j-1], c.pts[j], c.pts[j+1]); turn > 40 {
				t.Errorf("arm %d still turns %.0f°", i, turn)
			}
		}
	}
}

// smoothScalar must not move the ends, which the pinned taper relies on.
func TestSmoothScalarHoldsShape(t *testing.T) {
	arc := []float64{0, 12, 24, 36, 48}
	// a single spike between neighbours: the fold case
	got := smoothScalar([]float64{0, 0, 30, 0, 0}, arc, 36)
	if got[2] >= 30 {
		t.Errorf("spike survived smoothing: %v", got)
	}
	if got[2] <= 0 {
		t.Errorf("smoothing erased the signal entirely: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if math.Abs(got[i]-got[i-1]) > 12 {
			t.Errorf("smoothed profile still jumps %.1f between neighbours: %v",
				got[i]-got[i-1], got)
		}
	}
}
