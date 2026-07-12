package bundle

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

func straight(y float64, x0, x1, step float64) *geo.Line {
	var pts []geo.Pt
	for x := x0; x <= x1; x += step {
		pts = append(pts, geo.Pt{X: x, Y: y})
	}
	return geo.NewLine(pts)
}

func TestMedianStrandRules(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{[]float64{3.0}, 3.0},                   // 1 → follow
		{[]float64{-2, 2}, 0.0},                 // 2 → midpoint
		{[]float64{-4, 0.5, 4}, 0.5},            // 3 → center track
		{[]float64{-6, -2, 2, 6}, 0.0},          // 4 → middle-two midpoint
		{[]float64{-20, -6, -2, 2, 6}, -2},      // 5 → drop outermost, center of 3
		{[]float64{-20, -6, -2, 2, 6, 30}, 0.0}, // 6 → drop outer pair → 4-rule
	}
	for _, c := range cases {
		got := MedianStrand(c.in)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("MedianStrand(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestStrandClustering(t *testing.T) {
	// two tracks: tight point clouds around -5 and +5
	offs := []float64{-5.5, -5.0, -4.6, 4.7, 5.0, 5.3}
	got := Strands(offs, 4.5)
	if len(got) != 2 {
		t.Fatalf("want 2 strands, got %v", got)
	}
	if math.Abs(got[0]+5.03) > 0.1 || math.Abs(got[1]-5.0) > 0.1 {
		t.Errorf("strand centers %v", got)
	}
}

// A 2-track bundle: the refined centerline must ride the exact midpoint,
// even when the initial spine is one of the tracks (4m off-center).
func TestRefineTwoTrackMidline(t *testing.T) {
	a := straight(0, 0, 1000, 5)
	b := straight(8, 0, 1000, 5)
	got := Refine(a, []*geo.Line{a, b}, DefaultParams())
	pts := got.Resample(20)
	for _, p := range pts[3 : len(pts)-3] {
		if math.Abs(p.Y-4.0) > 0.6 {
			t.Fatalf("midline off at x=%.0f: y=%.2f (want 4.0)", p.X, p.Y)
		}
	}
}

// Curve-following probes: a 4-track 90° bend must not be corner-cut and must
// stay smooth. (The straight-tangent-probe bug froze refinement on every
// curve — a 10m corner-cut at Church/Franklin.)
func TestRefineCurveNoCornerCut(t *testing.T) {
	// concentric quarter arcs around the origin with tangent straight tails
	arc := func(r float64) *geo.Line {
		var pts []geo.Pt
		for x := -400.0; x < 0; x += 5 {
			pts = append(pts, geo.Pt{X: x, Y: -r})
		}
		for a := -math.Pi / 2; a <= 0; a += 0.02 {
			pts = append(pts, geo.Pt{X: r * math.Cos(a), Y: r * math.Sin(a)})
		}
		for y := 5.0; y <= 400; y += 5 {
			pts = append(pts, geo.Pt{X: r, Y: y})
		}
		return geo.NewLine(pts)
	}
	tracks := []*geo.Line{arc(96), arc(100), arc(104), arc(108)}
	spine := arc(96) // worst-case spine: innermost track
	got := Refine(spine, tracks, DefaultParams())

	for _, p := range got.Resample(10) {
		// in the bend quadrant the middle-two midpoint is radius 102
		if p.X > 15 && p.Y < -15 {
			r := p.Norm()
			if math.Abs(r-102.0) > 2.5 {
				t.Fatalf("corner cut: radius %.1f at (%.0f,%.0f), want 102", r, p.X, p.Y)
			}
		}
	}
	// turn gate on the interior only: endpoints stay pinned to the spine
	// (6m off the converged interior) — in the pipeline they tie into nodes
	rs := got.Resample(12)
	if len(rs) > 10 {
		if turn := geo.MaxTurnDeg(rs[5 : len(rs)-5]); turn > 15 {
			t.Fatalf("centerline jagged through bend: max turn %.0f°", turn)
		}
	}
}

// Kiss guard: a track that brushes past for ~40m must not drag the centerline.
func TestRefineKissImmunity(t *testing.T) {
	a := straight(0, 0, 1000, 5)
	b := straight(8, 0, 1000, 5)
	var kiss []geo.Pt
	for x := 400.0; x <= 600; x += 5 {
		kiss = append(kiss, geo.Pt{X: x, Y: 14 + 40*math.Abs(x-500)/100})
	}
	k := geo.NewLine(kiss)
	got := Refine(a, []*geo.Line{a, b, k}, DefaultParams())
	for _, p := range got.Resample(20) {
		if p.X > 420 && p.X < 580 && math.Abs(p.Y-4.0) > 1.0 {
			t.Fatalf("kiss dragged centerline: y=%.2f at x=%.0f", p.Y, p.X)
		}
	}
}

func TestSoundMateshipKissRule(t *testing.T) {
	a := Track{ID: "a", Line: straight(0, 0, 500, 5)}
	b := Track{ID: "b", Line: straight(8, 0, 500, 5)}
	var cross []geo.Pt
	for y := -50.0; y <= 50; y += 5 {
		cross = append(cross, geo.Pt{X: 250, Y: y})
	}
	c := Track{ID: "c", Line: geo.NewLine(cross)}
	mates := Sound([]Track{a, b, c}, DefaultSoundParams())
	for _, m := range mates {
		if m.A == 2 || m.B == 2 {
			t.Fatalf("perpendicular crossing became a mate: %+v", m)
		}
	}
	found := false
	for _, m := range mates {
		if m.A == 0 && m.B == 1 && (m.ToA-m.FromA) > 400 {
			found = true
		}
	}
	if !found {
		t.Fatalf("parallel pair not mated: %+v", mates)
	}
}
