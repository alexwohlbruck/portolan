package stages

// Gap-bridge healing — draw bridges along the corridor, not across it.
//
// A gap bridge is what the map draws where MATCH found no walk over
// running track: the GTFS shape chord, verbatim. Where the shape is dense
// that is a faithful curve. Where it is sparse it is a straight line, and
// a straight line between two points a kilometre apart is the single most
// visible lie the map can tell — Barcelona's FGC drew four of them
// crossing each other, Mexico's Line 1 three, and Paris one 24 km beam
// spanning the whole window.
//
// The steel is almost always there. MATCH is bound by what counts as
// SERVICE — running track only, no yards or sidings, plus class and level
// rules — and rightly so: those rules are what keep trains off spurs. But
// once MATCH has decided a bridge is needed, the question changes from
// "may this route ride here?" to "where does the corridor physically go?"
// and for that every piece of steel is evidence. So healing searches a
// separate GEOMETRY-ONLY pool (osm.LoadCorridor: running track plus the
// service ways) and redraws the bridge along whatever path it finds.
//
// Guards, all shape-independent:
//   - the path may not exceed the chord by much (heal a corridor, never
//     route around a city);
//   - every sample of it must stay inside a corridor of the chord, so a
//     long way round is rejected even when it is short enough;
//   - both ends must sit within a platform's width of real steel.
//
// Bridges that heal nothing keep their chord and fall through to the
// Bézier smoothing in trunkweld.go, which at least makes the fabrication
// tangent-continuous instead of an elbow.

import (
	"container/heap"
	"fmt"
	"math"
	"os"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
)

// corridorTracks is the geometry-only steel pool (SetCorridorTracks, from
// the pipeline). Nil disables healing entirely — a city built without a
// corridor extract behaves exactly as before.
var corridorTracks []bundle.Track

func SetCorridorTracks(t []bundle.Track) { corridorTracks = t }

var dbgHeal = os.Getenv("PORTOLAN_DBGH") != ""

// quantum for welding corridor vertices into graph nodes. Half a metre:
// tight enough that two parallel tracks never merge, loose enough to
// absorb the coordinate noise of ways digitised separately.
const corridorQuantum = 0.5

type corridorNode struct{ x, y int64 }

func quantize(p geo.Pt) corridorNode {
	return corridorNode{
		int64(math.Round(p.X / corridorQuantum)),
		int64(math.Round(p.Y / corridorQuantum)),
	}
}

// the corridor graph, built once per city from corridorTracks. Keyed by
// quantized vertex; arcs carry the neighbour's exact position so the
// healed geometry is the steel's own coordinates, not the lattice.
type corridorArc struct {
	to  corridorNode
	len float64
}

var (
	corAdj   map[corridorNode][]corridorArc
	corPos   map[corridorNode]geo.Pt
	corGrid  map[corridorNode][]corridorNode // 200 m buckets for nearest()
	corBuilt bool
)

const corridorBucket = 200.0

func buildCorridorGraph() {
	corAdj = map[corridorNode][]corridorArc{}
	corPos = map[corridorNode]geo.Pt{}
	corGrid = map[corridorNode][]corridorNode{}
	for _, t := range corridorTracks {
		pts := t.Line.Pts
		for i := 1; i < len(pts); i++ {
			p, q := pts[i-1], pts[i]
			np, nq := quantize(p), quantize(q)
			if np == nq {
				continue
			}
			d := p.Dist(q)
			if _, ok := corPos[np]; !ok {
				corPos[np] = p
				b := bucketOf(p)
				corGrid[b] = append(corGrid[b], np)
			}
			if _, ok := corPos[nq]; !ok {
				corPos[nq] = q
				b := bucketOf(q)
				corGrid[b] = append(corGrid[b], nq)
			}
			corAdj[np] = append(corAdj[np], corridorArc{nq, d})
			corAdj[nq] = append(corAdj[nq], corridorArc{np, d})
		}
	}
	corBuilt = true
}

func bucketOf(p geo.Pt) corridorNode {
	return corridorNode{
		int64(math.Floor(p.X / corridorBucket)),
		int64(math.Floor(p.Y / corridorBucket)),
	}
}

// nearestCorridor finds the closest corridor vertex to p within max.
func nearestCorridor(p geo.Pt, max float64) (corridorNode, float64) {
	b := bucketOf(p)
	reach := int64(math.Ceil(max/corridorBucket)) + 1
	best, bd := corridorNode{}, math.Inf(1)
	for dx := -reach; dx <= reach; dx++ {
		for dy := -reach; dy <= reach; dy++ {
			for _, n := range corGrid[corridorNode{b.x + dx, b.y + dy}] {
				if d := p.Dist(corPos[n]); d < bd {
					best, bd = n, d
				}
			}
		}
	}
	return best, bd
}

// HealGapBridges redraws every gap edge whose endpoints the corridor pool
// connects. Returns the number healed.
func HealGapBridges(net *Network) int {
	if len(corridorTracks) == 0 {
		return 0
	}
	if !corBuilt {
		buildCorridorGraph()
	}
	healed := 0
	for ei := range net.Edges {
		e := &net.Edges[ei]
		if !e.Gap || len(e.Pts) < 2 {
			continue
		}
		l := geo.NewLine(e.Pts)
		// short bridges are already visually honest: a 100 m chord across
		// a station throat reads as a line, not as a lie
		if l.Len() < dial("heal_min_bridge", 150) {
			continue
		}
		pts, ok, why := healOne(e.Pts, l)
		if dbgHeal {
			fmt.Printf("healDBG len=%.0f routes=%v -> %s\n", l.Len(), e.Routes, why)
		}
		if ok {
			e.Pts = pts
			healed++
		}
	}
	if healed > 0 {
		rebuildAdj(net)
	}
	return healed
}

// DropRunawayBridges removes gap bridges too long to be standing in for
// anything. A bridge is a stand-in for track the map cannot see, and the
// longer it is the more it asserts: a 300 m chord across a station throat
// claims almost nothing, while Paris drew a 23.9 km DEAD-HORIZONTAL line
// at constant latitude clear across the window — the signature of a
// broken shape, not of a tunnel.
//
// Past a few kilometres the honest thing is to draw nothing. The two
// ribbon ends simply stop, which reads as "the map does not know", where
// a straight line reads as "the train goes this way" and is false. No
// real urban gap — a tunnel, a river crossing, an unmapped viaduct — runs
// this long, and healing has already had its chance at every one that
// follows a real corridor.
func DropRunawayBridges(net *Network) int {
	bar := dial("bridge_max_draw", 5000)
	dropped := 0
	for i := 0; i < len(net.Edges); {
		e := &net.Edges[i]
		if e.Gap && len(e.Pts) >= 2 && geo.NewLine(e.Pts).Len() > bar {
			net.Edges = append(net.Edges[:i], net.Edges[i+1:]...)
			dropped++
			continue
		}
		i++
	}
	if dropped > 0 {
		compactNodes(net)
		rebuildAdj(net)
	}
	return dropped
}

// healOne searches the corridor for a path between the chord's ends.
func healOne(chord []geo.Pt, l *geo.Line) ([]geo.Pt, bool, string) {
	a, b := chord[0], chord[len(chord)-1]
	span := l.Len()
	// The corridor: how far the real alignment may stray from the chord.
	// A bridge stands in for track between two committed anchors, and
	// real track between two points bulges — a station approach, a river
	// crossing — but it does not leave the neighbourhood. Applying this
	// DURING the search (rather than as a box around it) is what keeps
	// the walk local without a boundary that severs the corridor.
	radius := math.Min(math.Max(dial("heal_corridor_frac", 0.4)*span, 150), 700)
	anchorMax := dial("heal_anchor_max", 80)
	src, ds := nearestCorridor(a, anchorMax)
	dst, dd := nearestCorridor(b, anchorMax)
	if ds > anchorMax || dd > anchorMax || src == dst {
		return nil, false, fmt.Sprintf("anchors %.0f/%.0f m (max %.0f)", ds, dd, anchorMax)
	}

	// Dijkstra, bounded by both the length cap and the corridor
	capLen := span*dial("heal_len_factor", 1.8) + 200
	dist := map[corridorNode]float64{src: 0}
	prev := map[corridorNode]corridorNode{}
	pq := &healPQ{{node: src}}
	found := false
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(healItem)
		if d, ok := dist[cur.node]; ok && cur.d > d {
			continue
		}
		if cur.node == dst {
			found = true
			break
		}
		for _, ar := range corAdj[cur.node] {
			nd := cur.d + ar.len
			if nd > capLen {
				continue
			}
			if l.DistTo(corPos[ar.to]) > radius {
				continue
			}
			if d, ok := dist[ar.to]; ok && d <= nd {
				continue
			}
			dist[ar.to] = nd
			prev[ar.to] = cur.node
			heap.Push(pq, healItem{node: ar.to, d: nd})
		}
	}
	if !found {
		return nil, false, fmt.Sprintf("no path within %.0f m / %.0f m corridor", capLen, radius)
	}

	// rebuild the path, source-first
	var path []geo.Pt
	for n := dst; ; {
		path = append(path, corPos[n])
		p, ok := prev[n]
		if !ok {
			break
		}
		n = p
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	if len(path) < 2 {
		return nil, false, "degenerate path"
	}
	pl := geo.NewLine(path)
	if pl.Len() > capLen {
		return nil, false, "path over cap"
	}
	// stitch to the original endpoints so the edge still meets its
	// neighbours exactly where the network expects it
	out := make([]geo.Pt, 0, len(path)+2)
	out = append(out, a)
	for _, q := range path {
		if out[len(out)-1].Dist(q) > 1e-9 {
			out = append(out, q)
		}
	}
	if out[len(out)-1].Dist(b) > 1e-9 {
		out = append(out, b)
	}
	return out, true, fmt.Sprintf("HEALED %.0f m -> %.0f m", span, pl.Len())
}

type healItem struct {
	node corridorNode
	d    float64
}

type healPQ []healItem

func (q healPQ) Len() int           { return len(q) }
func (q healPQ) Less(a, b int) bool { return q[a].d < q[b].d }
func (q healPQ) Swap(a, b int)      { q[a], q[b] = q[b], q[a] }
func (q *healPQ) Push(x any)        { *q = append(*q, x.(healItem)) }
func (q *healPQ) Pop() any          { o := *q; n := len(o); it := o[n-1]; *q = o[:n-1]; return it }
