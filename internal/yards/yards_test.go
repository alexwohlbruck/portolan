package yards

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// straight builds a horizontal track at height y spanning [x0,x1], vertexed
// every ~25 m (the detector walks arc, but CrossSection needs real
// segments to intersect).
func straight(y, x0, x1 float64) *geo.Line {
	n := int(math.Max(2, math.Round((x1-x0)/25)+1))
	pts := make([]geo.Pt, n)
	for i := range pts {
		pts[i] = geo.Pt{X: x0 + (x1-x0)*float64(i)/float64(n-1), Y: y}
	}
	return geo.NewLine(pts)
}

// ladder builds n parallel tracks at the given pitch. Real ladder tracks
// sit ~4 m apart — at pitch 4 a 10-track ladder scores ≈8.1 at its heart
// while a 4-track revenue corridor peaks at 3.
func ladder(n int, pitch, y0, x0, x1 float64, service string) []Track {
	out := make([]Track, n)
	for i := range out {
		out[i] = Track{
			ID:      fmt.Sprintf("%s%d", "L", i),
			Line:    straight(y0+float64(i)*pitch, x0, x1),
			Service: service,
		}
	}
	return out
}

func TestLadderIsRegion(t *testing.T) {
	ix := Build(ladder(10, 4, 0, 0, 800, ""), DefaultParams())
	rs := ix.Regions()
	if len(rs) != 1 {
		t.Fatalf("regions = %d, want 1", len(rs))
	}
	if rs[0].Peak < 8 {
		t.Errorf("peak = %.2f, want >= 8", rs[0].Peak)
	}
	if rs[0].TrackLen < 6000 {
		t.Errorf("track len = %.0f, want > 6000", rs[0].TrackLen)
	}
	if !ix.InYard(geo.Pt{X: 400, Y: 20}) {
		t.Error("on-track ladder point not InYard")
	}
	if !ix.InYard(geo.Pt{X: 401, Y: 18}) {
		t.Error("between-tracks point not InYard")
	}
	if ix.InYard(geo.Pt{X: 400, Y: 150}) {
		t.Error("point 100+ m off the ladder InYard")
	}
	if ix.InYard(geo.Pt{X: -100, Y: 20}) {
		t.Error("point past the ladder end InYard")
	}
}

func TestFourTrackCorridorIsNot(t *testing.T) {
	// A 4-track revenue trunk is many close parallel tracks for miles —
	// exactly what the detector must NOT call a yard (max score 3 < hot 5).
	ix := Build(ladder(4, 4, 0, 0, 2000, ""), DefaultParams())
	if n := len(ix.Regions()); n != 0 {
		t.Fatalf("regions = %d, want 0 (express trunk eaten)", n)
	}
}

func TestLoneSidingNoRegionButIsYardWay(t *testing.T) {
	tracks := []Track{
		{ID: "main", Line: straight(0, 0, 2000)},
		{ID: "side", Line: straight(6, 300, 600), Service: "siding"},
	}
	ix := Build(tracks, DefaultParams())
	if n := len(ix.Regions()); n != 0 {
		t.Fatalf("regions = %d, want 0 (a lone siding is not a yard)", n)
	}
	if !ix.IsYardWay("side") {
		t.Error("tagged siding not IsYardWay")
	}
	if ix.IsYardWay("main") {
		t.Error("mainline IsYardWay")
	}
	if ix.InYard(geo.Pt{X: 450, Y: 6}) {
		t.Error("lone siding midpoint InYard")
	}
	if ix.RegionWay("side") {
		t.Error("lone siding RegionWay with no region")
	}
}

func TestTaggedSeedBeatsLowScore(t *testing.T) {
	// A 4-track tagged yard scores only ~3 geometrically; the tag seed plus
	// tagged-mass anchoring must still make it a region.
	ix := Build(ladder(4, 4, 0, 0, 800, "yard"), DefaultParams())
	if n := len(ix.Regions()); n != 1 {
		t.Fatalf("regions = %d, want 1 (tag seeding failed)", n)
	}
}

func TestThroughMainline(t *testing.T) {
	tracks := append(ladder(10, 4, 4, 100, 700, "yard"),
		Track{ID: "main", Line: straight(0, 0, 2000)})
	ix := Build(tracks, DefaultParams())
	if n := len(ix.Regions()); n != 1 {
		t.Fatalf("regions = %d, want 1", n)
	}
	if !ix.InYard(geo.Pt{X: 400, Y: 0}) {
		t.Error("mainline inside the complex not InYard")
	}
	if ix.InYard(geo.Pt{X: 1500, Y: 0}) {
		t.Error("mainline far past the complex InYard")
	}
	// The through mainline is inside the complex but is NOT yard steel:
	// RegionWay (pool suppression for unridden strands) yes, IsYardWay no.
	if ix.IsYardWay("main") {
		t.Error("through mainline IsYardWay")
	}
	if !ix.RegionWay("main") {
		t.Error("through mainline not RegionWay")
	}
}

func TestBuildDeterministic(t *testing.T) {
	tracks := append(ladder(10, 4, 4, 100, 700, "yard"),
		Track{ID: "main", Line: straight(0, 0, 2000)})
	a := Build(tracks, DefaultParams())
	b := Build(tracks, DefaultParams())
	if !reflect.DeepEqual(a.Regions(), b.Regions()) {
		t.Fatal("two builds from the same input differ")
	}
}

// NearestDist must agree with a brute-force scan over member steel: a
// query that silently misses does not fail a test on its own — it lets a
// consumer treat the middle of a yard as open country.
func TestNearestDistMatchesBrute(t *testing.T) {
	// Center the scene near the origin so half the probe coords are
	// negative — int truncation vs math.Floor on negative cells is a real
	// bug class here (markergrid lesson).
	tracks := ladder(10, 4, -20, -400, 400, "yard")
	ix := Build(tracks, DefaultParams())
	if len(ix.Regions()) != 1 {
		t.Fatal("scene did not form one region")
	}
	byID := map[string]*geo.Line{}
	for _, tr := range tracks {
		byID[tr.ID] = tr.Line
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 4000; i++ {
		p := geo.Pt{X: rng.Float64()*2400 - 1200, Y: rng.Float64()*2400 - 1200}
		brute := math.Inf(1)
		for _, id := range ix.Regions()[0].WayIDs {
			if d := byID[id].DistTo(p); d < brute {
				brute = d
			}
		}
		if math.Abs(brute-64) < 1e-6 {
			continue // knife edge
		}
		got := ix.NearestDist(p, 64)
		if brute < 64 {
			if math.Abs(got-brute) > 1e-9 {
				t.Fatalf("probe %v: NearestDist %.9f, brute %.9f", p, got, brute)
			}
		} else if !math.IsInf(got, 1) {
			t.Fatalf("probe %v: NearestDist %.9f, want +Inf (brute %.1f)", p, got, brute)
		}
	}
}
