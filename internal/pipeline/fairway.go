package pipeline

import (
	"math"

	"github.com/alexwohlbruck/portolan/internal/fairway"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
	"github.com/alexwohlbruck/portolan/internal/osm"
)

// withFairway derives river centerlines from the shoreline and drops OSM's
// ferry lanes. Ferry shapes are snapped onto the centerlines (snapFerries)
// and drawn as their own geometry: with no lanes to match, MATCH chords
// each shape verbatim, so every ferry on a river lands on the same line.
func withFairway(water string, bbox []float64, frame geo.Frame, ways []osm.Way,
	logf func(string, ...any)) (*fairway.Net, []osm.Way, error) {
	shore, err := fairway.Load(water, frame)
	if err != nil {
		return nil, nil, err
	}
	lo, hi := windowOf(bbox, frame, ways)
	net := fairway.Build(shore, lo, hi, fairway.DefaultParams())
	out := make([]osm.Way, 0, len(ways))
	for _, w := range ways {
		if w.Tags["railway"] != "seaway" {
			out = append(out, w)
		}
	}
	logf("chart: fairway: %d river reaches; %d ferry lanes set aside",
		len(net.Reaches), len(ways)-len(out))
	return net, out, nil
}

func windowOf(bbox []float64, frame geo.Frame, ways []osm.Way) (lo, hi geo.Pt) {
	if len(bbox) == 4 {
		return frame.ToXY(geo.LL{Lon: bbox[0], Lat: bbox[1]}), frame.ToXY(geo.LL{Lon: bbox[2], Lat: bbox[3]})
	}
	lo = geo.Pt{X: math.Inf(1), Y: math.Inf(1)}
	hi = geo.Pt{X: math.Inf(-1), Y: math.Inf(-1)}
	for _, w := range ways {
		for _, ll := range w.Coords {
			p := frame.ToXY(ll)
			lo = geo.Pt{X: math.Min(lo.X, p.X), Y: math.Min(lo.Y, p.Y)}
			hi = geo.Pt{X: math.Max(hi.X, p.X), Y: math.Max(hi.Y, p.Y)}
		}
	}
	pad := geo.Pt{X: 2000, Y: 2000}
	return lo.Sub(pad), hi.Add(pad)
}

// snapFerries pulls each ferry shape onto the fairway where it runs along
// a river.
func snapFerries(pats []gtfs.Pattern, net *fairway.Net, frame geo.Frame) {
	for i := range pats {
		if mode.Of(pats[i].Route.Type) != mode.Ferry || len(pats[i].Shape) < 2 {
			continue
		}
		pts := make([]geo.Pt, len(pats[i].Shape))
		for j, ll := range pats[i].Shape {
			pts[j] = frame.ToXY(ll)
		}
		snapped := net.Snap(pts)
		shape := make([]geo.LL, len(snapped))
		for j, p := range snapped {
			shape[j] = frame.ToLL(p)
		}
		pats[i].Shape = shape
	}
}
