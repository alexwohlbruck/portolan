package pipeline

import (
	"container/heap"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/berth"
	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/support"
)

// attribute map-matches every pattern onto the TRACK-GROUP CENTERLINE
// network as a CONNECTED WALK: per-sample candidates, run smoothing, and
// shortest-path stitching between consecutive edges. A pattern can only
// leave the network through an explicit shape-geometry BRIDGE (true track
// data gap) — routes can never silently break (owner's law #1), and every
// path rides the sketch-precision track centerlines (law #2).
func attribute(tg *support.Graph, pats []gtfs.Pattern, frame geo.Frame,
	reach float64, logf func(string, ...any)) (*bundle.Graph, *berth.Result, int) {

	lines := make([]*geo.Line, len(tg.Edges))
	for i, e := range tg.Edges {
		lines[i] = e.Line()
	}
	grid := geo.NewGrid(lines, 64)

	// graph adapter (corridors = trackcenter edges)
	g := &bundle.Graph{}
	for _, n := range tg.Nodes {
		g.Nodes = append(g.Nodes, bundle.Node{ID: len(g.Nodes), At: n.At})
	}
	for ei, e := range tg.Edges {
		g.Corridors = append(g.Corridors, bundle.Corridor{
			ID: ei, Centerline: lines[ei], NodeA: e.From, NodeB: e.To,
		})
		g.Nodes[e.From].Corridors = append(g.Nodes[e.From].Corridors, ei)
		if e.To != e.From {
			g.Nodes[e.To].Corridors = append(g.Nodes[e.To].Corridors, ei)
		}
	}

	br := &berth.Result{Berths: map[int][]berth.Berth{}, Moves: map[[2]int]map[string]bool{}}
	berthSeen := map[int]map[string]bool{}
	bridges := 0

	for _, pat := range pats {
		pts := make([]geo.Pt, len(pat.Shape))
		for i, ll := range pat.Shape {
			pts[i] = frame.ToXY(ll)
		}
		shape := geo.NewLine(pts)
		if shape.Len() < 60 {
			continue
		}
		samples := shape.Resample(15)
		assign := make([]int, len(samples))
		for i, q := range samples {
			best, bestD := -1, reach
			grid.Near(q, reach, func(ei int) {
				if d := lines[ei].DistTo(q); d < bestD {
					best, bestD = ei, d
				}
			})
			assign[i] = best
		}
		smoothAssign(assign, 3)

		// raw run sequence
		type run struct{ edge, from, to int }
		var runs []run
		i := 0
		for i < len(assign) {
			j := i
			for j+1 < len(assign) && assign[j+1] == assign[i] {
				j++
			}
			if assign[i] >= 0 {
				runs = append(runs, run{assign[i], i, j})
			}
			i = j + 1
		}
		if len(runs) == 0 {
			continue
		}
		// stitch into a connected walk
		var legs []berth.Leg
		addEdge := func(ei int) {
			if len(legs) > 0 && legs[len(legs)-1].Corridor == ei {
				return
			}
			legs = append(legs, berth.Leg{Corridor: ei})
		}
		addEdge(runs[0].edge)
		for ri := 1; ri < len(runs); ri++ {
			prev := legs[len(legs)-1].Corridor
			next := runs[ri].edge
			if prev == next {
				continue
			}
			if sharesNode(tg, prev, next) {
				addEdge(next)
				continue
			}
			if path := edgePath(tg, lines, prev, next, 1500); path != nil {
				for _, ei := range path {
					addEdge(ei)
				}
				addEdge(next)
				continue
			}
			// true gap: bridge with the pattern's own shape geometry
			a0 := runs[ri-1].to
			b0 := runs[ri].from
			if b0 > a0 {
				legs = append(legs, berth.Leg{
					Corridor: -1,
					Bridge:   geo.NewLine(samples[a0 : b0+1]),
				})
				bridges++
			}
			addEdge(next)
		}

		br.Matches = append(br.Matches, berth.Match{Pattern: pat, Legs: legs})
		for li, leg := range legs {
			if leg.Corridor < 0 {
				continue
			}
			if berthSeen[leg.Corridor] == nil {
				berthSeen[leg.Corridor] = map[string]bool{}
			}
			if !berthSeen[leg.Corridor][pat.Route.ID] {
				berthSeen[leg.Corridor][pat.Route.ID] = true
				br.Berths[leg.Corridor] = append(br.Berths[leg.Corridor], berth.Berth{
					RouteID: pat.Route.ID, Color: routeColor(pat.Route),
					Label: pat.Route.ShortName, Type: pat.Route.Type,
				})
			}
			if li > 0 && legs[li-1].Corridor >= 0 {
				k := [2]int{legs[li-1].Corridor, leg.Corridor}
				if br.Moves[k] == nil {
					br.Moves[k] = map[string]bool{}
				}
				br.Moves[k][pat.Route.ID] = true
			}
		}
	}
	for ci := range br.Berths {
		sort.Slice(br.Berths[ci], func(a, b int) bool {
			x, y := br.Berths[ci][a], br.Berths[ci][b]
			if x.Color != y.Color {
				return x.Color < y.Color
			}
			return x.RouteID < y.RouteID
		})
	}
	logf("attribute: %d walks, %d berthed edges, %d moves, %d gap bridges",
		len(br.Matches), len(br.Berths), len(br.Moves), bridges)
	return g, br, bridges
}

// smoothAssign absorbs runs shorter than minRun between identical neighbors.
func smoothAssign(assign []int, minRun int) {
	n := len(assign)
	i := 0
	for i < n {
		j := i
		for j+1 < n && assign[j+1] == assign[i] {
			j++
		}
		if j-i+1 < minRun && i > 0 && j+1 < n && assign[i-1] == assign[j+1] {
			for k := i; k <= j; k++ {
				assign[k] = assign[i-1]
			}
		}
		i = j + 1
	}
}

func sharesNode(tg *support.Graph, a, b int) bool {
	ea, eb := tg.Edges[a], tg.Edges[b]
	return ea.From == eb.From || ea.From == eb.To ||
		ea.To == eb.From || ea.To == eb.To
}

// edgePath runs Dijkstra over the trackcenter graph from either node of
// edge a to either node of edge b, returning the INTERMEDIATE edge sequence
// (nil if no path within maxLen meters).
func edgePath(tg *support.Graph, lines []*geo.Line, a, b, maxLen int) []int {
	target := map[int]bool{tg.Edges[b].From: true, tg.Edges[b].To: true}
	pq := &pqueue{}
	heap.Init(pq)
	heap.Push(pq, item{tg.Edges[a].From, 0, nil})
	heap.Push(pq, item{tg.Edges[a].To, 0, nil})
	best := map[int]float64{}
	for pq.Len() > 0 {
		it := heap.Pop(pq).(item)
		if d, ok := best[it.node]; ok && d <= it.dist {
			continue
		}
		best[it.node] = it.dist
		if target[it.node] {
			return it.via
		}
		if it.dist > float64(maxLen) {
			continue
		}
		for _, ei := range tg.Nodes[it.node].Adj {
			if ei == a || ei == b {
				continue
			}
			e := tg.Edges[ei]
			other := e.From
			if e.From == it.node {
				other = e.To
			}
			nd := it.dist + lines[ei].Len()
			if d, ok := best[other]; ok && d <= nd {
				continue
			}
			via := append(append([]int(nil), it.via...), ei)
			heap.Push(pq, item{other, nd, via})
		}
	}
	return nil
}

type item struct {
	node int
	dist float64
	via  []int
}

type pqueue []item

func (p pqueue) Len() int           { return len(p) }
func (p pqueue) Less(i, j int) bool { return p[i].dist < p[j].dist }
func (p pqueue) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p *pqueue) Push(x any)        { *p = append(*p, x.(item)) }
func (p *pqueue) Pop() any          { old := *p; n := len(old); x := old[n-1]; *p = old[:n-1]; return x }
