package stages

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

// seawayWeld: how far a ferry lane's end may sit from another lane and
// still be taken as joining it. OSM ferry lanes are mapped route by route,
// and two lanes into one terminal, or a branch off a trunk lane, routinely
// stop tens of metres apart instead of sharing a node. Unwelded, the only
// way onto the branch is round by some other lane end, and a boat bound
// for Vineyard Haven sailed on to Oak Bluffs and back.
const seawayWeld = 60.0

// WeldSeaways extends every ferry lane end within seawayWeld of another
// lane by a short connector onto it, inserting a vertex there when it lands
// mid-segment, so the track graph joins them. The lanes themselves keep
// their exact geometry. Other layers are returned untouched.
func WeldSeaways(tracks []bundle.Track, class map[string]string) []bundle.Track {
	var sea []int
	for i, t := range tracks {
		if class[t.ID] == "seaway" && len(t.Line.Pts) >= 2 {
			sea = append(sea, i)
		}
	}
	if len(sea) < 2 {
		return tracks
	}
	lines := make([]*geo.Line, len(sea))
	pts := make([][]geo.Pt, len(sea))
	for k, i := range sea {
		lines[k] = tracks[i].Line
		pts[k] = append([]geo.Pt(nil), tracks[i].Line.Pts...)
	}
	grid := geo.NewGrid(lines, 200)

	type insert struct {
		seg int
		t   float64
		at  geo.Pt
	}
	inserts := make([][]insert, len(sea))
	lead := make([]bool, len(sea)) // a connector was prepended: segments shift by one
	moved := false
	for k := range sea {
		for _, end := range [2]int{0, len(pts[k]) - 1} {
			p := lines[k].Pts[end]
			best, bj, bs, bt := seawayWeld, -1, -1, 0.0
			var bq geo.Pt
			grid.Near(p, seawayWeld, func(j int) {
				if j == k {
					return
				}
				w := lines[j].Pts
				for s := 0; s+1 < len(w); s++ {
					q, t := project(p, w[s], w[s+1])
					if d := p.Dist(q); d < best {
						best, bj, bs, bt, bq = d, j, s, t, q
					}
				}
			})
			if bj < 0 || best < 1e-6 {
				continue
			}
			w := lines[bj].Pts
			// land on an existing vertex when one is close: no sliver pieces
			switch {
			case bq.Dist(w[bs]) < 2:
				bq = w[bs]
			case bq.Dist(w[bs+1]) < 2:
				bq = w[bs+1]
			default:
				seg := bs
				if lead[bj] {
					seg++
				}
				inserts[bj] = append(inserts[bj], insert{seg, bt, bq})
			}
			if end == 0 {
				pts[k] = append([]geo.Pt{bq}, pts[k]...)
				for x := range inserts[k] {
					inserts[k][x].seg++
				}
				lead[k] = true
			} else {
				pts[k] = append(pts[k], bq)
			}
			moved = true
		}
	}
	if !moved {
		return tracks
	}
	out := append([]bundle.Track(nil), tracks...)
	for k, i := range sea {
		ins := inserts[k]
		sort.Slice(ins, func(a, b int) bool {
			if ins[a].seg != ins[b].seg {
				return ins[a].seg < ins[b].seg
			}
			return ins[a].t < ins[b].t
		})
		var np []geo.Pt
		x := 0
		for s, q := range pts[k] {
			np = append(np, q)
			for ; x < len(ins) && ins[x].seg == s; x++ {
				np = append(np, ins[x].at)
			}
		}
		out[i] = bundle.Track{ID: tracks[i].ID, Line: geo.NewLine(np), Level: tracks[i].Level}
	}
	return out
}

func project(p, a, b geo.Pt) (geo.Pt, float64) {
	v := b.Sub(a)
	l2 := v.Dot(v)
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, p.Sub(a).Dot(v)/l2))
	}
	return geo.Lerp(a, b, t), t
}
