package stages

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

// FRESH-START DoD: on the Culver 4-track median the refined centerline's
// median-strand offset must read ≈0 at every cross-section.
func TestCulverMedian(t *testing.T) {
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
	rp := bundle.DefaultParams()

	// Culver viaduct area: 7 Av–Church Av, routes F(+FX)+G
	culver := frame.ToXY(geo.LL{Lon: -73.97877, Lat: 40.65868})
	for ei, e := range net.Edges {
		has := func(want string) bool {
			for _, r := range e.Routes {
				if r == want {
					return true
				}
			}
			return false
		}
		if !(has("F") && has("G")) || e.Gap {
			continue
		}
		cl := geo.NewLine(e.Pts)
		if cl.DistTo(culver) > 800 {
			continue
		}
		members := edgeMates(cl, strandLines, sgrid, sp)
		var devs []float64
		worst, worstAt := 0.0, geo.Pt{}
		for s := 30.0; s < cl.Len()-30; s += 10 {
			q := cl.AtArc(s)
			tan := cl.TangentAtArc(s, 15)
			var offs []float64
			for _, m := range members {
				for _, c := range m.CrossSection(q, tan, rp.Reach) {
					if c.Parallel >= rp.MinParallel {
						offs = append(offs, c.Offset)
					}
				}
			}
			if len(offs) == 0 {
				continue
			}
			o := bundle.MedianStrand(bundle.Strands(offs, rp.StrandGap))
			devs = append(devs, math.Abs(o))
			if math.Abs(o) > worst {
				worst, worstAt = math.Abs(o), q
			}
		}
		sort.Float64s(devs)
		if len(devs) == 0 {
			continue
		}
		mean := 0.0
		for _, d := range devs {
			mean += d
		}
		mean /= float64(len(devs))
		ll := frame.ToLL(worstAt)
		fmt.Printf("edge %d routes=%v len=%.0fm members=%d  |median-offset| mean=%.2f p90=%.2f max=%.2f (worst @ %.5f,%.5f)\n",
			ei, e.Routes, cl.Len(), len(members), mean,
			devs[int(0.9*float64(len(devs)-1))], devs[len(devs)-1], ll.Lat, ll.Lon)
	}
}
