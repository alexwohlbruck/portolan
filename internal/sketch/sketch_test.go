package sketch

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// metres → lon/lat around a NYC-ish origin, so a test can be written in
// the units the gates are written in.
var org = geo.LL{Lon: -74.0, Lat: 40.75}

func at(x, y float64) LL {
	f := geo.NewFrame(org)
	ll := f.ToLL(geo.Pt{X: x, Y: y})
	return LL{ll.Lon, ll.Lat}
}

// a 400 × 200 m box centred on the origin
func box() Curve {
	return Curve{ID: "b", Closed: true, Coords: Path{
		at(-200, -100), at(200, -100), at(200, 100), at(-200, 100), at(-200, -100),
	}}
}

func TestRingDropsClosingVertex(t *testing.T) {
	if got := len(box().Ring()); got != 4 {
		t.Errorf("Ring() = %d vertices, want 4", got)
	}
	open := Curve{Coords: Path{at(0, 0), at(10, 0)}}
	if got := len(open.Ring()); got != 2 {
		t.Errorf("open Ring() = %d, want 2", got)
	}
}

func TestEntrancesCrossings(t *testing.T) {
	y := Yard{
		ID: "y", Boundary: box(),
		Centerlines: []Curve{
			// straight through, west to east: one entrance each side
			{ID: "c1", Coords: Path{at(-300, 0), at(300, 0)}},
		},
	}
	ents := y.Entrances()
	if len(ents) != 2 {
		t.Fatalf("entrances = %d, want 2: %+v", len(ents), ents)
	}
	f := geo.NewFrame(org)
	for _, e := range ents {
		p := f.ToXY(geo.LL{Lon: e.At[0], Lat: e.At[1]})
		if math.Abs(math.Abs(p.X)-200) > 1 || math.Abs(p.Y) > 1 {
			t.Errorf("entrance at (%.1f, %.1f), want (±200, 0)", p.X, p.Y)
		}
		// heading points INTO the yard: east on the west edge, west on the east
		wantDeg := 0.0
		if p.X > 0 {
			wantDeg = 180
		}
		if d := math.Abs(math.Abs(e.Heading) - wantDeg); d > 1 {
			t.Errorf("entrance at x=%.0f heading %.1f°, want %.0f°", p.X, e.Heading, wantDeg)
		}
	}
}

func TestEntrancesClusterOneThroat(t *testing.T) {
	// three tracks leaving the same west throat within the cluster radius
	// collapse to one entrance at their average — the owner's rule.
	var cls []Curve
	for i, dy := range []float64{-20, 0, 20} {
		cls = append(cls, Curve{
			ID:     string(rune('a' + i)),
			Coords: Path{at(0, dy), at(-300, dy)},
		})
	}
	y := Yard{ID: "y", Boundary: box(), Centerlines: cls}
	ents := y.Entrances()
	if len(ents) != 1 {
		t.Fatalf("entrances = %d, want 1 clustered: %+v", len(ents), ents)
	}
	if got := len(ents[0].Lines); got != 3 {
		t.Errorf("clustered entrance carries %d lines, want 3", got)
	}
	f := geo.NewFrame(org)
	p := f.ToXY(geo.LL{Lon: ents[0].At[0], Lat: ents[0].At[1]})
	if math.Abs(p.X+200) > 1 || math.Abs(p.Y) > 1 {
		t.Errorf("clustered at (%.1f, %.1f), want (-200, 0) — the average", p.X, p.Y)
	}
	if math.Abs(ents[0].Heading) > 1 {
		t.Errorf("clustered heading %.1f°, want 0 (into the yard)", ents[0].Heading)
	}
}

func TestEntrancesIgnoreLinesThatStayInside(t *testing.T) {
	// a terminal stub that dies inside the yard is not an entrance
	y := Yard{ID: "y", Boundary: box(), Centerlines: []Curve{
		{ID: "c", Coords: Path{at(-100, 0), at(100, 0)}},
	}}
	if ents := y.Entrances(); len(ents) != 0 {
		t.Errorf("entrances = %d, want 0: %+v", len(ents), ents)
	}
}

func TestRingIoU(t *testing.T) {
	f := geo.NewFrame(org)
	metric := func(c Curve) []geo.Pt { return pts(c.Ring(), f) }
	same := metric(box())
	if got := ringIoU(same, same); math.Abs(got-1) > 0.01 {
		t.Errorf("IoU(self) = %.3f, want 1", got)
	}
	// shifted 200 m east: halves overlap → 200×200 ∩ over 600×200 ∪ = 1/3
	shifted := metric(Curve{Coords: Path{
		at(0, -100), at(400, -100), at(400, 100), at(0, 100),
	}})
	if got := ringIoU(same, shifted); math.Abs(got-1.0/3) > 0.02 {
		t.Errorf("IoU(half-overlap) = %.3f, want 0.333", got)
	}
	away := metric(Curve{Coords: Path{
		at(5000, -100), at(5400, -100), at(5400, 100), at(5000, 100),
	}})
	if got := ringIoU(same, away); got != 0 {
		t.Errorf("IoU(disjoint) = %.3f, want 0", got)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	hin := at(-210, -100)
	n := &Network{
		Feed:  "test",
		Lines: []Curve{{ID: "l1", Label: "A · B", Coords: Path{at(0, 0), at(50, 10)}}},
		Yards: []Yard{{
			ID: "y1", Label: "Test Yard",
			Boundary: Curve{ID: "b1", Anchors: []Anchor{
				{P: at(-200, -100), HIn: &hin, HOut: nil},
			}, Coords: box().Coords},
			Centerlines: []Curve{{ID: "c1", Coords: Path{at(-300, 0), at(300, 0)}}},
		}},
	}
	p := filepath.Join(t.TempDir(), "n.json")
	if err := Save(p, n); err != nil {
		t.Fatal(err)
	}
	got, err := LoadNetwork(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Label != "A · B" {
		t.Fatalf("lines round-tripped as %+v", got.Lines)
	}
	if len(got.Yards) != 1 || len(got.Yards[0].Centerlines) != 1 {
		t.Fatalf("yards round-tripped as %+v", got.Yards)
	}
	if !got.Yards[0].Boundary.Closed {
		t.Error("a saved boundary must come back closed")
	}
	a := got.Yards[0].Boundary.Anchors[0]
	if a.HIn == nil || a.HOut != nil {
		t.Errorf("anchor handles = %v/%v, want set/null (three-valued)", a.HIn, a.HOut)
	}
	if len(got.Yards[0].Entrances()) != 2 {
		t.Error("entrances must survive the round trip")
	}
	// the file is reviewable: one anchor per line, coords on one line
	raw, _ := os.ReadFile(p)
	if n := countLines(string(raw)); n > 40 {
		t.Errorf("saved document is %d lines — coords must not be exploded", n)
	}
}

func countLines(s string) int {
	n := 0
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}

func TestSaveIsDeterministic(t *testing.T) {
	n := &Network{Feed: "test", Lines: []Curve{{ID: "l1", Coords: Path{at(0, 0), at(9, 9)}}}}
	d := t.TempDir()
	a, b := filepath.Join(d, "a.json"), filepath.Join(d, "b.json")
	if err := Save(a, n); err != nil {
		t.Fatal(err)
	}
	if err := Save(b, n); err != nil {
		t.Fatal(err)
	}
	ra, _ := os.ReadFile(a)
	rb, _ := os.ReadFile(b)
	if string(ra) != string(rb) {
		t.Error("two saves of one document differ — the file would churn in git")
	}
}

func TestScoreYardsMatchesBestOverlap(t *testing.T) {
	f := geo.NewFrame(org)
	drawn := Yard{
		ID: "y", Label: "Test", Boundary: box(),
		Centerlines: []Curve{{ID: "c", Coords: Path{at(-300, 0), at(300, 0)}}},
	}
	net := &Network{Yards: []Yard{drawn}}

	// a decoy region far away, and the real one 20 m north of the drawing
	shifted := make([]geo.Pt, 0, 4)
	for _, p := range pts(box().Ring(), f) {
		shifted = append(shifted, geo.Pt{X: p.X, Y: p.Y + 20})
	}
	det := []DetectedYard{
		{ID: "decoy", Outline: pts(Path{at(9000, 0), at(9400, 0), at(9400, 200), at(9000, 200)}, f)},
		{ID: "real", Outline: shifted,
			Entrances: []geo.Pt{{X: -200, Y: 20}, {X: 200, Y: 20}},
			// 10 m off the drawn centerline: inside YardCoverM, so covered
			Centerlines: []*geo.Line{geo.NewLine(pts(Path{at(-300, 10), at(300, 10)}, f))}},
	}
	res := ScoreYards(net, det, f)
	if res == nil || len(res.Yards) != 1 {
		t.Fatalf("result = %+v", res)
	}
	y := res.Yards[0]
	// 400×200 boxes offset 20 m: intersection 400×180, union 400×220
	if math.Abs(y.IoU-180.0/220) > 0.02 {
		t.Errorf("IoU = %.3f, want %.3f", y.IoU, 180.0/220)
	}
	if y.BndP90 < 1 || y.BndP90 > 25 {
		t.Errorf("boundary p90 = %.1f m, want a 20 m-ish offset", y.BndP90)
	}
	if y.EntDrawn != 2 || y.EntFound != 2 || y.EntExtra != 0 {
		t.Errorf("entrances %d/%d +%d, want 2/2 +0", y.EntFound, y.EntDrawn, y.EntExtra)
	}
	if y.CtrCoverPct < 99 {
		t.Errorf("centerline cover = %.1f%%, want ~100 (10 m apart is inside %g m)",
			y.CtrCoverPct, YardCoverM)
	}
	if math.Abs(y.CtrMean-10) > 1 {
		t.Errorf("centerline mean = %.1f m, want 10", y.CtrMean)
	}
	if y.AreaHa < 7.9 || y.AreaHa > 8.1 {
		t.Errorf("area = %.2f ha, want 8 (400×200 m)", y.AreaHa)
	}
}

func TestScoreYardsUndetectedIsZero(t *testing.T) {
	f := geo.NewFrame(org)
	net := &Network{Yards: []Yard{{ID: "y", Label: "Missed", Boundary: box()}}}
	res := ScoreYards(net, nil, f)
	if res == nil || len(res.Yards) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Yards[0].IoU != 0 || !res.Yards[0].Fail {
		t.Errorf("an undetected yard must score 0 and fail, got %+v", res.Yards[0])
	}
	if res.Failures != 1 {
		t.Errorf("failures = %d, want 1", res.Failures)
	}
}

func TestScoreYardsNilWithoutDrawing(t *testing.T) {
	f := geo.NewFrame(org)
	if got := ScoreYards(&Network{}, nil, f); got != nil {
		t.Errorf("a drawing with no yards must score nothing, got %+v", got)
	}
}
