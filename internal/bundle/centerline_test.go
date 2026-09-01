package bundle

import (
	"encoding/json"
	"math"
	"os"
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

// The fold runaway: refinement must hand back a line, not a longer one.
//
// The fixture is a real Refine call captured from a game-authored NYC
// build — the 1's 12 km corridor edge with its two directional strands,
// which breathe 5–22 m apart. The median's vote set never settles on
// that geometry, `moved` never reaches the convergence break, and one
// unguarded iteration folds the polyline past its local turn radius.
// Folds compound across iterations and across Split's six refine calls:
// this exact edge reached 766 km of scribble between 137 St and 145 St,
// and the same instability at lower amplitude drew the F's sine wave
// through 2 Av. Arc length is the fold signature — a lateral centering
// move cannot legitimately grow the line, so refuse any iteration that
// does.
func TestRefineNeverGrowsTheLine(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/refine-fold-nyc.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		CL      [][2]float64   `json:"cl"`
		Members [][][2]float64 `json:"members"`
		Free    [2]bool        `json:"free"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	toLine := func(ps [][2]float64) *geo.Line {
		pts := make([]geo.Pt, len(ps))
		for i, q := range ps {
			pts[i] = geo.Pt{X: q[0], Y: q[1]}
		}
		return geo.NewLine(pts)
	}
	cl := toLine(d.CL)
	var members []*geo.Line
	for _, m := range d.Members {
		members = append(members, toLine(m))
	}
	p := DefaultParams()
	p.FreeStart, p.FreeEnd = d.Free[0], d.Free[1]
	got := Refine(cl, members, p)
	if lim := cl.Len()*1.05 + 60; got.Len() > lim {
		t.Fatalf("refinement grew the line: %.0f m in, %.0f m out (limit %.0f)",
			cl.Len(), got.Len(), lim)
	}
	// and the line that comes back contains no fold knots — the length
	// gate bounds the damage, the unfold pass removes it. An authored
	// Chicago Green showed why both matter: on a 35.8 km megachain a
	// percentage-sized length allowance passed ~700 m of local knots per
	// call while total growth stayed "small".
	if turn := geo.MaxTurnDeg(got.Pts); turn > 90 {
		t.Fatalf("refinement left a fold knot: max turn %.0f°", turn)
	}
}
