package stages

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// For every gap bridge in the build: is the graph walkable between its
// anchors, and at what arc length? Distinguishes MaxWalk-too-small from
// genuine OSM disconnects.
func TestGapConnectivity(t *testing.T) {
	_, tracks, frame := loadNYC(t)
	g := buildTrackGraph(tracks)

	raw, err := os.ReadFile("../../build/nyc.geojson")
	if err != nil {
		t.Skip(err)
	}
	var fc struct {
		Features []struct {
			Props struct {
				Kind    string  `json:"kind"`
				Routes  string  `json:"routes"`
				BandMin int     `json:"band_min"`
				LenM    float64 `json:"len_m"`
			} `json:"properties"`
			Geometry struct {
				Coords [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatal(err)
	}

	nearestNode := func(p geo.Pt) int {
		best, bi := math.Inf(1), -1
		for ni, n := range g.nodes {
			if len(n.Out) == 0 {
				continue
			}
			if d := n.At.Dist(p); d < best {
				best, bi = d, ni
			}
		}
		return bi
	}
	// plain node-level Dijkstra over edge lengths, unbounded
	walkDist := func(from, to int) float64 {
		type qi struct {
			n int
			d float64
		}
		dist := map[int]float64{from: 0}
		pq := &nodePQ{{from, 0}}
		for pq.Len() > 0 {
			cur := heap.Pop(pq).(struct {
				n int
				d float64
			})
			if cur.n == to {
				return cur.d
			}
			if cur.d > dist[cur.n] {
				continue
			}
			for _, e := range g.nodes[cur.n].Out {
				nd := cur.d + g.edges[e].Line.Len()
				nx := g.edges[e].To
				if old, ok := dist[nx]; !ok || nd < old {
					dist[nx] = nd
					heap.Push(pq, struct {
						n int
						d float64
					}{nx, nd})
				}
			}
		}
		return math.Inf(1)
	}

	for _, f := range fc.Features {
		if f.Props.Kind != "bridge" || f.Props.BandMin != 15 || f.Props.LenM < 50 {
			continue
		}
		cs := f.Geometry.Coords
		a := frame.ToXY(geo.LL{Lon: cs[0][0], Lat: cs[0][1]})
		b := frame.ToXY(geo.LL{Lon: cs[len(cs)-1][0], Lat: cs[len(cs)-1][1]})
		na, nb := nearestNode(a), nearestNode(b)
		if na < 0 || nb < 0 {
			continue
		}
		snapA, snapB := g.nodes[na].At.Dist(a), g.nodes[nb].At.Dist(b)
		wd := walkDist(na, nb)
		straight := a.Dist(b)
		fmt.Printf("%-6s bridge %5.0fm  straight %5.0fm  walk %8.0fm  (anchor snap %.0f/%.0fm)\n",
			f.Props.Routes, f.Props.LenM, straight, wd, snapA, snapB)
	}
}

type nodePQ []struct {
	n int
	d float64
}

func (q nodePQ) Len() int           { return len(q) }
func (q nodePQ) Less(a, b int) bool { return q[a].d < q[b].d }
func (q nodePQ) Swap(a, b int)      { q[a], q[b] = q[b], q[a] }
func (q *nodePQ) Push(x any) {
	*q = append(*q, x.(struct {
		n int
		d float64
	}))
}
func (q *nodePQ) Pop() any { old := *q; n := len(old); x := old[n-1]; *q = old[:n-1]; return x }
