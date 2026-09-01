package geo

import (
	"math"
	"testing"
)

func TestUnfoldCutsKnotsKeepsCurves(t *testing.T) {
	// A genuine tight curve at drawing resolution: 90° over six vertices
	// (15°/vertex — sharper than any real steel reads at a 6 m step).
	var curve []Pt
	for i := 0; i <= 6; i++ {
		th := float64(i) * math.Pi / 2 / 6
		curve = append(curve, Pt{X: 20 * math.Sin(th), Y: 20 - 20*math.Cos(th)})
	}
	if got := Unfold(curve, 90); len(got) != len(curve) {
		t.Fatalf("unfold ate a legitimate curve: %d -> %d vertices", len(curve), len(got))
	}

	// A fold curl: the line doubles back on itself mid-run — three
	// vertices of ~150° reversal, the exact knot a refinement offset
	// that outran its base curvature leaves behind.
	fold := []Pt{{0, 0}, {10, 0}, {20, 0}, {24, 1}, {16, 3}, {25, 5}, {30, 0}, {40, 0}, {50, 0}}
	got := Unfold(fold, 90)
	if MaxTurnDeg(got) > 90 {
		t.Fatalf("knot survived: max turn %.0f°", MaxTurnDeg(got))
	}
	// endpoints are never touched
	if got[0] != fold[0] || got[len(got)-1] != fold[len(fold)-1] {
		t.Fatalf("unfold moved an endpoint")
	}
}
