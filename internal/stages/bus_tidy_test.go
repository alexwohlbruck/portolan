package stages

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

func line(pts ...geo.Pt) *geo.Line { return geo.NewLine(pts) }

func maxDistTo(l *geo.Line, pts []geo.Pt) float64 {
	m := 0.0
	for _, q := range geo.NewLine(pts).Resample(5) {
		if d := l.DistTo(q); d > m {
			m = d
		}
	}
	return m
}

// A street runs east; the bus drives it straight. The match went round
// the block to the north instead.
func TestMendReplacesARoundTheBlockDetour(t *testing.T) {
	shape := line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 0})
	matched := []geo.Pt{{X: 0, Y: 0}, {X: 400, Y: 0}, {X: 400, Y: 150}, {X: 600, Y: 150}, {X: 600, Y: 0}, {X: 1000, Y: 0}}
	out := mendExcursions(matched, shape)
	if d := maxDistTo(shape, out); d > busOnShape+2 {
		t.Errorf("detour survives: %.0f m off the street", d)
	}
}

// The match rode the other carriageway of a divided road, 30 m over: the
// same road, and it stays.
func TestMendKeepsTheOtherCarriageway(t *testing.T) {
	shape := line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 2000, Y: 0})
	matched := []geo.Pt{{X: 0, Y: 30}, {X: 2000, Y: 30}}
	out := mendExcursions(matched, shape)
	if d := maxDistTo(line(matched...), out); d > 1 {
		t.Errorf("matched carriageway replaced: moved %.0f m", d)
	}
}

// A loop route out to an airport and back along the same road: its
// return leg is no excursion.
func TestMendKeepsALoopRoutesReturnLeg(t *testing.T) {
	shape := line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 3000, Y: 0}, geo.Pt{X: 3000, Y: 12}, geo.Pt{X: 0, Y: 12})
	matched := []geo.Pt{{X: 0, Y: 0}, {X: 3000, Y: 0}, {X: 3200, Y: 200}, {X: 3000, Y: 12}, {X: 0, Y: 12}}
	out := geo.NewLine(mendExcursions(matched, shape))
	if out.Len() < 5900 {
		t.Errorf("return leg lost: %.0f m drawn of a 6 km loop", out.Len())
	}
}

func TestSpurOffTheShapeIsRemoved(t *testing.T) {
	shape := line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 0})
	matched := []geo.Pt{{X: 0, Y: 0}, {X: 500, Y: 0}, {X: 500, Y: 300}, {X: 500, Y: 0}, {X: 1000, Y: 0}}
	out := dropSpurs(matched, shape)
	if d := maxDistTo(shape, out); d > busOnShape+2 {
		t.Errorf("spur survives, %.0f m off the route", d)
	}
}

func TestOutAndBackTheShapeMakesStays(t *testing.T) {
	shape := line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 500, Y: 0}, geo.Pt{X: 500, Y: 300}, geo.Pt{X: 500, Y: 0}, geo.Pt{X: 1000, Y: 0})
	matched := append([]geo.Pt(nil), shape.Pts...)
	if out := dropSpurs(matched, shape); geo.NewLine(out).Len() < 1500 {
		t.Errorf("a street the bus really runs up and back was cut: %.0f m left", geo.NewLine(out).Len())
	}
}

func TestCutTrimsOvershootPastTheTerminal(t *testing.T) {
	shape := line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 0})
	matched := []geo.Pt{{X: 0, Y: 0}, {X: 1000, Y: 0}, {X: 1000, Y: 250}}
	out := cutToTerminals(matched, shape, geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 0})
	if last := out[len(out)-1]; last.Dist(geo.Pt{X: 1000, Y: 0}) > 1 {
		t.Errorf("line still runs on to %v past the last stop", last)
	}
}

func TestCutKeepsATailOnTheShape(t *testing.T) {
	// the terminal stop sits partway round a turnaround the shape draws
	shape := line(geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 0}, geo.Pt{X: 1000, Y: 200})
	matched := append([]geo.Pt(nil), shape.Pts...)
	out := cutToTerminals(matched, shape, geo.Pt{X: 0, Y: 0}, geo.Pt{X: 1000, Y: 60})
	if geo.NewLine(out).Len() < 1190 {
		t.Errorf("cut into the shape's own turnaround: %.0f m left", geo.NewLine(out).Len())
	}
}
