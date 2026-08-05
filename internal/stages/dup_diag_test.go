package stages

import (
	"fmt"
	"math"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/osm"
	"github.com/alexwohlbruck/portolan/internal/sketch"
)

// prints WHERE dup fires and WHICH features double there, for failing lines
func TestDupDiag(t *testing.T) {
	ways, err := osm.Load("../../testdata/nyc-rail.geojson")
	if err != nil {
		t.Fatal(err)
	}
	frame := frameOf(ways)
	net, err := sketch.LoadNetwork("../../sketches/network-5.json")
	if err != nil {
		t.Skip(err)
	}
	// load build features like pipeline.LoadBuildFeatures but keep labels
	type bf struct {
		color, label, kind string
		line               *geo.Line
	}
	var feats []bf
	rawFC := loadFC(t, "../../build/nyc.geojson")
	for _, f := range rawFC {
		if f.props["band_min"] != 15.0 {
			continue
		}
		pts := make([]geo.Pt, len(f.coords))
		for i, c := range f.coords {
			pts[i] = frame.ToXY(geo.LL{Lon: c[0], Lat: c[1]})
		}
		if len(pts) < 2 {
			continue
		}
		feats = append(feats, bf{fmt.Sprint(f.props["color"]),
			fmt.Sprint(f.props["label"]), fmt.Sprint(f.props["kind"]), geo.NewLine(pts)})
	}
	lines := make([]*geo.Line, len(feats))
	for i := range feats {
		lines[i] = feats[i].line
	}
	grid := geo.NewGrid(lines, 64)

	for li, dl := range net.Lines {
		if len(dl.Coords) < 2 {
			continue
		}
		pts := make([]geo.Pt, len(dl.Coords))
		for i, c := range dl.Coords {
			pts[i] = frame.ToXY(geo.LL{Lon: c[0], Lat: c[1]})
		}
		l := geo.NewLine(pts)
		lbl := ""
		for _, r := range dl.Routes {
			lbl += r.Label + "+"
		}
		printed := 0
		nDup, nTot := 0, 0
		for _, q := range l.Resample(5) {
			nTot++
			type hit struct {
				fi int
				at geo.Pt
			}
			var hits []hit
			grid.Near(q, 15, func(fi int) {
				if d := lines[fi].DistTo(q); d < 12 {
					arc, _ := lines[fi].ProjectArc(q)
					hits = append(hits, hit{fi, lines[fi].AtArc(arc)})
				}
			})
			worst, wa, wb := 0.0, -1, -1
			for a := 0; a < len(hits); a++ {
				for b := a + 1; b < len(hits); b++ {
					if d := hits[a].at.Dist(hits[b].at); d > worst {
						worst, wa, wb = d, hits[a].fi, hits[b].fi
					}
				}
			}
			if worst > 3 {
				nDup++
				if printed < 4 {
					ll := frame.ToLL(q)
					fa, fb := feats[wa], feats[wb]
					fmt.Printf("  line %d (%s): dup @ %.5f,%.5f sep=%.1fm  [%s %s %s] vs [%s %s %s]\n",
						li, lbl, ll.Lat, ll.Lon, worst,
						fa.kind, fa.label, fa.color, fb.kind, fb.label, fb.color)
					printed++
				}
			}
		}
		if nDup > 0 {
			fmt.Printf("line %d (%s): dup %d/%d = %.1f%%\n", li, lbl, nDup, nTot,
				100*float64(nDup)/math.Max(1, float64(nTot)))
		}
	}
}

type fcFeat struct {
	props  map[string]any
	coords [][2]float64
}

func loadFC(t *testing.T, path string) []fcFeat {
	raw, err := readFileBytes(path)
	if err != nil {
		t.Skip(err)
	}
	var fc struct {
		Features []struct {
			Props    map[string]any `json:"properties"`
			Geometry struct {
				Type   string       `json:"type"`
				Coords [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := jsonUnmarshal(raw, &fc); err != nil {
		t.Fatal(err)
	}
	var out []fcFeat
	for _, f := range fc.Features {
		if f.Geometry.Type != "LineString" {
			continue
		}
		out = append(out, fcFeat{f.Props, f.Geometry.Coords})
	}
	return out
}
