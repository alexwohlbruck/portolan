package fairway

import (
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// A straight river 600 m wide running east, banks at y = ±300. OSM coastline
// keeps land on the left: the south bank runs west, the north bank east.
func river() *Shore {
	return &Shore{Coast: [][]geo.Pt{
		{{X: 10000, Y: -300}, {X: -10000, Y: -300}},
		{{X: -10000, Y: 300}, {X: 10000, Y: 300}},
	}}
}

func build(t *testing.T) *Net {
	t.Helper()
	n := Build(river(), geo.Pt{X: -6000, Y: -1500}, geo.Pt{X: 6000, Y: 1500}, DefaultParams())
	if len(n.Reaches) == 0 {
		t.Fatal("no fairway down an open river")
	}
	return n
}

func TestCenterlineRunsDownTheMiddle(t *testing.T) {
	n := build(t)
	length := 0.0
	for _, r := range n.Reaches {
		length += r.Line.Len()
		for _, p := range r.Line.Pts {
			if math.Abs(p.Y) > 30 {
				t.Fatalf("centerline strays %.0f m off the middle at x=%.0f", p.Y, p.X)
			}
		}
	}
	if length < 9000 {
		t.Errorf("centerline covers %.0f m of a 12 km window", length)
	}
}

func TestSnapPullsALaneOntoTheCenterline(t *testing.T) {
	n := build(t)
	lane := []geo.Pt{{X: -3000, Y: 150}, {X: 3000, Y: 150}}
	snapped := n.Snap(lane)
	for _, p := range snapped[10 : len(snapped)-10] {
		if math.Abs(p.Y) > 30 {
			t.Fatalf("lane left at y=%.0f, want it on the centerline", p.Y)
		}
	}
}

func TestSnapLeavesACrossingAlone(t *testing.T) {
	n := build(t)
	crossing := []geo.Pt{{X: 0, Y: -290}, {X: 0, Y: 290}}
	for _, p := range n.Snap(crossing) {
		if math.Abs(p.X) > 5 {
			t.Fatalf("crossing bent to x=%.0f", p.X)
		}
	}
}

func TestNoFairwayInOpenWater(t *testing.T) {
	sea := &Shore{Coast: [][]geo.Pt{{{X: 10000, Y: -3000}, {X: -10000, Y: -3000}}}}
	n := Build(sea, geo.Pt{X: -6000, Y: -4000}, geo.Pt{X: 6000, Y: 4000}, DefaultParams())
	if len(n.Reaches) != 0 {
		t.Errorf("open water grew %d reaches", len(n.Reaches))
	}
}
