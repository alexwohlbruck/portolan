package stages

import (
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Where do the track-graph's disconnected components nearly touch? Small
// closest-approach distances mean weld failures; large ones mean the OSM
// extract is missing track.
func TestComponentGaps(t *testing.T) {
	_, tracks, frame := loadNYC(t)
	g := buildTrackGraph(tracks)

	parent := make([]int, len(g.nodes))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for _, e := range g.edges {
		parent[find(e.From)] = find(e.To)
	}
	compOf := make([]int, len(g.nodes))
	compSize := map[int]int{}
	for i := range g.nodes {
		compOf[i] = find(i)
		compSize[compOf[i]]++
	}
	type cs struct{ id, n int }
	var comps []cs
	for id, n := range compSize {
		comps = append(comps, cs{id, n})
	}
	sort.Slice(comps, func(a, b int) bool { return comps[a].n > comps[b].n })
	main := comps[0].id

	// for each big non-main component: closest node→(main-component piece)
	// approach, top 3 distinct spots
	for _, c := range comps[1:] {
		if c.n < 80 {
			break
		}
		type hit struct {
			d   float64
			at  geo.Pt
			way string
		}
		var hits []hit
		for ni, node := range g.nodes {
			if compOf[ni] != c.id || len(node.Out) == 0 {
				continue
			}
			best, bw := math.Inf(1), ""
			g.grid.Near(node.At, 60, func(piece int) {
				e := 2 * piece
				if compOf[g.edges[e].From] != main {
					return
				}
				_, d := g.pieces[piece].ProjectArc(node.At)
				if d < best {
					best, bw = d, g.edges[e].Way
				}
			})
			if !math.IsInf(best, 1) {
				hits = append(hits, hit{best, node.At, bw})
			}
		}
		sort.Slice(hits, func(a, b int) bool { return hits[a].d < hits[b].d })
		fmt.Printf("\ncomp %d (%d nodes): %d near-approaches <60m to main\n", c.id, c.n, len(hits))
		seen := 0
		for _, h := range hits {
			l := frame.ToLL(h.at)
			fmt.Printf("  %.1fm @ %.5f,%.5f (near %s)\n", h.d, l.Lat, l.Lon, h.way)
			seen++
			if seen >= 4 {
				break
			}
		}
	}
}
