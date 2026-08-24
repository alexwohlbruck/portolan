package pipeline

import (
	"encoding/json"
	"math"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/yards"
)

// polyFeature marshals one outer ring (closed on the way out — GeoJSON
// repeats the first vertex) into a Polygon feature.
func polyFeature(props map[string]any, ring []geo.Pt, frame geo.Frame) (feature, bool) {
	if len(ring) < 3 {
		return feature{}, false
	}
	cs := make([][2]float64, 0, len(ring)+1)
	for _, p := range ring {
		ll := frame.ToLL(p)
		cs = append(cs, [2]float64{ll.Lon, ll.Lat})
	}
	cs = append(cs, cs[0])
	raw, _ := json.Marshal([][][2]float64{cs})
	return feature{Type: "Feature", Props: props,
		Geom: geomJSON{Type: "Polygon", Coords: raw}}, true
}

func pointFeature(props map[string]any, p geo.Pt, frame geo.Frame) feature {
	ll := frame.ToLL(p)
	raw, _ := json.Marshal([2]float64{ll.Lon, ll.Lat})
	return feature{Type: "Feature", Props: props,
		Geom: geomJSON{Type: "Point", Coords: raw}}
}

// writeYards streams the yard overlay: per region its outline polygon,
// entrance points and spine lines, all carrying the region id so the
// console can color per region. A nil index writes the usual empty
// collection — the dump always exists, like every other stage artifact.
func writeYards(path string, ix *yards.Index, frame geo.Frame) error {
	rs := ix.Regions()
	ri, kind, k := 0, 0, 0 // kind: 0 outline, 1 entrances, 2 spines
	return writeFCSeq(path, func() (feature, bool) {
		for ri < len(rs) {
			r := rs[ri]
			switch kind {
			case 0:
				kind = 1
				if f, ok := polyFeature(map[string]any{
					"kind": "yard", "region": r.ID, "level": r.Level,
					"track_m": int(r.TrackLen), "peak": math.Round(r.Peak*10) / 10,
					"ways": len(r.WayIDs), "entrances": len(r.Entrances),
				}, r.Outline, frame); ok {
					return f, true
				}
			case 1:
				if k < len(r.Entrances) {
					e := r.Entrances[k]
					deg := int(math.Round(math.Atan2(e.Heading.Y, e.Heading.X) * 180 / math.Pi))
					f := pointFeature(map[string]any{
						"kind": "yard_entrance", "region": r.ID, "entrance": k,
						"heading_deg": deg, "ways": len(e.WayIDs),
					}, e.Pt, frame)
					k++
					return f, true
				}
				kind, k = 2, 0
			case 2:
				for k < len(r.Spines) {
					s := r.Spines[k]
					k++
					if f, ok := lineFeature(map[string]any{
						"kind": "yard_spine", "region": r.ID,
						"from": s.From, "to": s.To,
					}, s.Line, 10, frame); ok {
						return f, true
					}
				}
				ri, kind, k = ri+1, 0, 0
			}
		}
		return feature{}, false
	})
}
