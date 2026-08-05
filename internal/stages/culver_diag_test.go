package stages

import (
	"fmt"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

func TestCulverDoubling(t *testing.T) {
	rail, tracks, frame := loadNYC(t)
	paths, err := Match(rail, tracks, frame)
	if err != nil {
		t.Fatal(err)
	}
	net, err := Split(paths, tracks)
	if err != nil {
		t.Fatal(err)
	}
	strands := bundle.Chain(tracks, 1.0)
	var strandLines []*geo.Line
	for _, s := range strands {
		strandLines = append(strandLines, s.Line)
	}
	sgrid := geo.NewGrid(strandLines, 64)
	sp := defaultSplitParams()

	spot := frame.ToXY(geo.LL{Lon: -73.98849, Lat: 40.66965})
	for ei, e := range net.Edges {
		cl := geo.NewLine(e.Pts)
		if cl.DistTo(spot) > 30 {
			continue
		}
		members := edgeMates(cl, strandLines, sgrid, sp)
		arc, d := cl.ProjectArc(spot)
		q := cl.AtArc(arc)
		tan := cl.TangentAtArc(arc, 15)
		var offs []float64
		for _, m := range members {
			for _, c := range m.CrossSection(q, tan, 25) {
				if c.Parallel >= 0.82 {
					offs = append(offs, c.Offset)
				}
			}
		}
		centers := bundle.Strands(offs, 4.5)
		fmt.Printf("edge %d routes=%v gap=%v len=%.0f distToSpot=%.1f members=%d strandCenters=%.1f\n",
			ei, e.Routes, e.Gap, cl.Len(), d, len(members), centers)
	}
}
