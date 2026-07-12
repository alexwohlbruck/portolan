package bundle

import (
	"math"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Track is one OSM way in the metric frame.
type Track struct {
	ID   string
	Line *geo.Line
}

// Strand is one continuous physical track: OSM ways chained end-to-end
// through degree-2 joints. Strands are the unit of bundling — ways are
// arbitrary editing artifacts (50–300 m splits) and must never be.
type Strand struct {
	ID   int
	Line *geo.Line
	Ways []string // source way ids, in order
}

// Strands chains ways whose endpoints coincide (within joinTol) at joints
// where EXACTLY two way-ends meet. Three or more way-ends = a switch — a
// real junction, never chained through. Ways shorter than 2 pts are dropped.
func Chain(tracks []Track, joinTol float64) []Strand {
	type endRef struct {
		way int
		end int // 0 = first vertex, 1 = last
	}
	// spatial hash of way endpoints
	cell := math.Max(joinTol, 0.5)
	key := func(p geo.Pt) [2]int {
		return [2]int{int(math.Floor(p.X / cell)), int(math.Floor(p.Y / cell))}
	}
	buckets := map[[2]int][]endRef{}
	endPt := func(r endRef) geo.Pt {
		pts := tracks[r.way].Line.Pts
		if r.end == 0 {
			return pts[0]
		}
		return pts[len(pts)-1]
	}
	for wi, t := range tracks {
		if len(t.Line.Pts) < 2 {
			continue
		}
		for e := 0; e <= 1; e++ {
			r := endRef{wi, e}
			buckets[key(endPt(r))] = append(buckets[key(endPt(r))], r)
		}
	}
	// join partner per way-end: the unique other end within joinTol, or none
	partner := map[endRef]endRef{}
	for wi, t := range tracks {
		if len(t.Line.Pts) < 2 {
			continue
		}
		for e := 0; e <= 1; e++ {
			r := endRef{wi, e}
			p := endPt(r)
			k := key(p)
			var near []endRef
			for dx := -1; dx <= 1; dx++ {
				for dy := -1; dy <= 1; dy++ {
					for _, o := range buckets[[2]int{k[0] + dx, k[1] + dy}] {
						if o.way == wi {
							continue
						}
						if endPt(o).Dist(p) <= joinTol {
							near = append(near, o)
						}
					}
				}
			}
			if len(near) == 1 { // degree-2 joint: chain through
				partner[r] = near[0]
			}
		}
	}
	// symmetric closure: chain only when both ends agree
	mutual := map[endRef]endRef{}
	for a, b := range partner {
		if c, ok := partner[b]; ok && c == a {
			mutual[a] = b
		}
	}

	used := make([]bool, len(tracks))
	var out []Strand
	for wi := range tracks {
		if used[wi] || len(tracks[wi].Line.Pts) < 2 {
			continue
		}
		// walk backward to the chain start
		cur := endRef{wi, 0}
		for {
			prev, ok := mutual[cur]
			if !ok {
				break
			}
			nw := prev.way
			if used[nw] || nw == wi {
				break // ring or revisit: start here
			}
			// entering nw at end prev.end; its far end continues backward
			cur = endRef{nw, 1 - prev.end}
			if nw == wi {
				break
			}
		}
		// walk forward collecting geometry
		var pts []geo.Pt
		var ways []string
		w, entry := cur.way, cur.end
		for {
			if used[w] {
				break
			}
			used[w] = true
			ways = append(ways, tracks[w].ID)
			seg := tracks[w].Line.Pts
			if entry == 1 { // entering at last vertex: reverse
				seg = reversed(seg)
			}
			if len(pts) > 0 && len(seg) > 0 && pts[len(pts)-1].Dist(seg[0]) <= joinTol {
				seg = seg[1:]
			}
			pts = append(pts, seg...)
			next, ok := mutual[endRef{w, 1 - entry}]
			if !ok {
				break
			}
			w, entry = next.way, next.end
		}
		if len(pts) >= 2 {
			out = append(out, Strand{ID: len(out), Line: geo.NewLine(pts), Ways: ways})
		}
	}
	return out
}

func reversed(pts []geo.Pt) []geo.Pt {
	out := make([]geo.Pt, len(pts))
	for i, p := range pts {
		out[len(pts)-1-i] = p
	}
	return out
}

// SubLine extracts the polyline between arc positions [from, to] (clamped).
func SubLine(l *geo.Line, from, to float64) *geo.Line {
	if from > to {
		from, to = to, from
	}
	from = math.Max(0, from)
	to = math.Min(l.Len(), to)
	if to-from < 1e-9 {
		return geo.NewLine([]geo.Pt{l.AtArc(from), l.AtArc(to)})
	}
	pts := []geo.Pt{l.AtArc(from)}
	// walk original vertices strictly inside (from, to)
	arc := 0.0
	for i := 1; i < len(l.Pts); i++ {
		arc += l.Pts[i].Dist(l.Pts[i-1])
		if arc > from && arc < to {
			pts = append(pts, l.Pts[i])
		}
	}
	pts = append(pts, l.AtArc(to))
	return geo.NewLine(pts)
}
