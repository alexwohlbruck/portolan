package pipeline

import (
	"math"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/support"
)

// snapToTracks path-matches every BUNDLED route edge onto the real tracks
// (Transit-app order: bundle the GTFS routes first, then match). Each edge
// picks the CENTER-MOST track of its group — the bundled geometry is the
// average of the routes, so the strand with the smallest mean offset from
// it is the closest-to-center track — and track jumping is effectively
// forbidden: the snap stays on its chosen strand until the strand
// physically ends (a switch), and only then re-picks.
func snapToTracks(sg *support.Graph, strandLines []*geo.Line, reach float64,
	logf func(string, ...any)) {
	grid := geo.NewGrid(strandLines, 64)
	snapped, gaps := 0, 0
	for _, e := range sg.Edges {
		l := e.Line()
		if l.Len() < 30 {
			continue
		}
		samples := l.Resample(12)
		// candidate strands + mean-offset score (center-most = min score)
		cover := map[int][]bool{}
		sum := map[int]float64{}
		n := map[int]int{}
		for i, q := range samples {
			grid.Near(q, reach, func(si int) {
				if cover[si] == nil {
					cover[si] = make([]bool, len(samples))
				}
				d := strandLines[si].DistTo(q)
				cover[si][i] = true
				sum[si] += d
				n[si]++
			})
		}
		if len(cover) == 0 {
			gaps++
			continue // no tracks: bundled geometry stands (data gap)
		}
		score := func(si int) float64 { return sum[si] / float64(n[si]) }
		pick := func(i int) int {
			best, bestS := -1, math.Inf(1)
			for si := range cover {
				if !cover[si][i] {
					continue
				}
				// prefer strands that keep covering ahead (stability) and
				// sit closest to the bundle center
				s := score(si)
				if s < bestS {
					best, bestS = si, s
				}
			}
			return best
		}
		out := make([]geo.Pt, len(samples))
		cur := pick(0)
		for i, q := range samples {
			if cur < 0 || !cover[cur][i] {
				// current strand ended (a switch) — re-pick center-most
				if nx := pick(i); nx >= 0 {
					cur = nx
				}
			}
			if cur >= 0 && cover[cur][i] {
				sl := strandLines[cur]
				arc, _ := sl.ProjectArc(q)
				out[i] = sl.AtArc(arc)
			} else {
				out[i] = q // momentary hole: keep bundle geometry
			}
		}
		sm := geo.GaussianArc(geo.NewLine(out).Densify(8), 8)
		tied := bundle.TieEnds(geo.NewLine(sm),
			sg.Nodes[e.From].At, sg.Nodes[e.To].At)
		e.Pts = tied.Pts
		snapped++
	}
	if logf != nil {
		logf("snap: %d edges snapped to center tracks, %d data gaps", snapped, gaps)
	}
}
