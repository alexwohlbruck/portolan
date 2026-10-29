package stages

import (
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

func TestLvlOKKeepsStackedServicesApart(t *testing.T) {
	line := func(y float64) *geo.Line { return geo.NewLine([]geo.Pt{{X: 0, Y: y}, {X: 400, Y: y}}) }
	setLevelIndex([]bundle.Track{
		{ID: "el", Line: line(0), Level: 1},
		{ID: "tunnel", Line: line(4), Level: -1},
	})
	setWayRouteIndex(map[string]map[string]bool{
		"el":     {"N": true, "W": true},
		"tunnel": {"R": true, "W": true},
	})
	t.Cleanup(func() { lvlGrid, lvlWayRoutes = nil, nil })

	la, lb := line(0), line(4)
	samples := la.Resample(10)
	check := func(ra, rb []string, want bool) {
		t.Helper()
		net := &Network{Edges: []Edge{{Pts: la.Pts, Routes: ra}, {Pts: lb.Pts, Routes: rb}}}
		if got := lvlOK(net, 0, 1, la, lb, samples, 0, len(samples)-1); got != want {
			t.Errorf("%v over %v: lvlOK %v, want %v", ra, rb, got, want)
		}
	}
	check([]string{"N", "W"}, []string{"R"}, false)
	check([]string{"N", "W"}, []string{"R", "W"}, false)
	check([]string{"W"}, []string{"W"}, true)
}
