package stages

import (
	"fmt"
	"testing"
)

func TestWalkContinuity(t *testing.T) {
	rail, tracks, frame := loadNYC(t)
	g := buildTrackGraph(tracks)
	paths, err := Match(rail, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}
	breaks := 0
	for _, pa := range paths {
		for i := 1; i < len(pa.Steps); i++ {
			a, b := pa.Steps[i-1], pa.Steps[i]
			if a.Piece < 0 || b.Piece < 0 {
				continue // gap bridges are anchored by construction
			}
			ea, eb := g.edges[2*a.Piece], g.edges[2*b.Piece]
			arr := ea.To
			if a.Rev {
				arr = ea.From
			}
			dep := eb.From
			if b.Rev {
				dep = eb.To
			}
			if arr != dep {
				breaks++
				if breaks <= 12 {
					ll := frame.ToLL(g.nodes[arr].At)
					ll2 := frame.ToLL(g.nodes[dep].At)
					fmt.Printf("break %s %s: step %d arrives n%d (%.5f,%.5f) departs n%d (%.5f,%.5f) dist=%.1fm\n",
						pa.Pattern.Route.ID, pa.Pattern.ShapeID, i, arr, ll.Lat, ll.Lon,
						dep, ll2.Lat, ll2.Lon, g.nodes[arr].At.Dist(g.nodes[dep].At))
				}
			}
		}
	}
	fmt.Printf("total step discontinuities: %d\n", breaks)
}
