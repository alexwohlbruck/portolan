package stages

import (
	"math"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

// BusSegments: buses are PATH MATCHING AND NOTHING MORE (owner call,
// 2026-08-08). A matched bus path is already a street centerline walk —
// the drawn geometry — so it goes straight to segments: no junction
// graph, no strand refinement, no corridor merging, no slots, no
// smoothing. One thin neutral line per street, visible from the bus band
// floor up.
//
// The only processing is overlap dedup: forty routes down Fifth Avenue
// must not stack forty ribbons. Paths share graph-piece geometry
// verbatim, so identical drawn segments dedupe on their quantized
// endpoint pair (first path wins, later paths keep only their unseen
// stretches). The emit stays linear in ridden street km, not route count.
func BusSegments(paths []Path) []Segment {
	const q = 1.0 // metres — piece geometry is shared, so exact-ish is fine
	type segKey [4]int32
	keyOf := func(a, b geo.Pt) segKey {
		k := segKey{
			int32(math.Round(a.X / q)), int32(math.Round(a.Y / q)),
			int32(math.Round(b.X / q)), int32(math.Round(b.Y / q)),
		}
		if k[0] > k[2] || (k[0] == k[2] && k[1] > k[3]) {
			k[0], k[1], k[2], k[3] = k[2], k[3], k[0], k[1]
		}
		return k
	}
	seen := map[segKey]bool{}
	var out []Segment
	for _, p := range paths {
		r := p.Pattern.Route
		var run []geo.Pt
		flush := func() {
			if len(run) >= 2 {
				out = append(out, Segment{
					Kind: "steady", Color: "888888",
					Routes: []string{r.ID}, Label: r.ShortName,
					RouteType: r.Type, Mode: mode.Bus.String(),
					NSlots:  1,
					BandMin: mode.Bus.BandFloor(), BandMax: 24,
					Line: geo.NewLine(append([]geo.Pt{}, run...)),
				})
			}
			run = nil
		}
		pts := p.Line.Pts
		for i := 1; i < len(pts); i++ {
			k := keyOf(pts[i-1], pts[i])
			if seen[k] {
				flush()
				continue
			}
			seen[k] = true
			if len(run) == 0 {
				run = append(run, pts[i-1])
			}
			run = append(run, pts[i])
		}
		flush()
	}
	return out
}
