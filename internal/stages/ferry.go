package stages

import (
	"container/heap"
	"math"
	"sort"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

// Ferries are routed pier to pier over the OSM ferry lanes rather than
// map-matched sample by sample. A ferry feed's shape is often a handful of
// straight chords between piers, hundreds of metres off the lane the boat
// really takes; matched at 20 m samples within a 90 m reach, those chords
// walked spurs, doubled back and gap-jumped across land. The stops are the
// part of a ferry feed that is exact, and the lanes connect them, so the
// path is the cheapest lane walk from each stop to the next, with the shape
// only breaking ties between parallel lanes.
//
// A terminal is often served by several lane ends a few metres to a few
// hundred metres apart that OSM never joined, so which lane end stands for
// a pier is decided for the whole trip at once: the lane a boat arrives on
// has to be the lane it leaves on.

// stopCoords: every stop's location, for ferry routing (SetStopCoords).
var stopCoords map[string]geo.LL

// ferryPiers: every stop a ferry pattern of the feed calls at. A walk
// between two piers must not pass through a third the trip skips: OSM's
// lanes often join two piers only by way of another (New Bedford to
// Vineyard Haven only through Oak Bluffs and Woods Hole), and that walk
// draws a boat calling where it does not.
func ferryPiers(pats []gtfs.Pattern, frame geo.Frame) []geo.Pt {
	seen := map[string]bool{}
	var out []geo.Pt
	for _, p := range pats {
		if mode.Of(p.Route.Type) != mode.Ferry {
			continue
		}
		for _, sid := range p.StopSeq {
			if ll, ok := stopCoords[sid]; ok && !seen[sid] {
				seen[sid] = true
				out = append(out, frame.ToXY(ll))
			}
		}
	}
	return out
}

func SetStopCoords(m map[string]geo.LL) { stopCoords = m }

const (
	// ferryAttach: farthest a pier may sit from the lane end that serves it.
	ferryAttach = 600.0
	// ferryAttachCost: cost per metre between a pier and the lane end
	// standing for it, against a metre of lane. Steep, so a lane ending at
	// the pier beats one ending at the next pier along.
	ferryAttachCost = 4.0
	// ferryFree: lane distance from the shape that costs nothing extra. A
	// chord between piers cuts the river's bends by about this much.
	ferryFree = 250.0
	// ferryStray: past ferryFree, every ferryStray metres of distance from
	// the shape doubles the cost of riding a lane.
	ferryStray = 150.0
	// ferryDetour, ferryDetourSlack: a lane walk longer than this against
	// the distance between the same two stops is not the boat's route.
	ferryDetour      = 2.0
	ferryDetourSlack = 2000.0
	// ferryTour, ferryTourSlack: a walk this much longer than the stops
	// are apart that also passes another of the feed's piers is a tour
	// round piers the boat skips. Passing a pier alone is not: in a busy
	// harbour every lane runs by lane ends at piers the boat sails past.
	ferryTour      = 1.4
	ferryTourSlack = 1000.0
	// ferryPierReach: a lane walk through a node this close to another
	// pier passes that pier.
	ferryPierReach = 150.0
	// ferryStep: how finely a lane is sampled against the shape.
	ferryStep = 40.0
)

// ferryWalk: the cheapest lane walk from one lane end to another.
type ferryWalk struct {
	edges []int // directed edges, in riding order
	cost  float64
}

// routeFerry walks a ferry pattern stop to stop over the seaway layer. It
// declines (false) when the pattern has no usable stops, leaving the
// pattern to the sample matcher.
func (m *matcher) routeFerry(pat gtfs.Pattern, frame geo.Frame) (Path, bool) {
	if stopCoords == nil || len(pat.Shape) < 2 {
		return Path{}, false
	}
	shapePts := make([]geo.Pt, len(pat.Shape))
	for i, ll := range pat.Shape {
		shapePts[i] = frame.ToXY(ll)
	}
	shape := geo.NewLine(shapePts)

	// stops in riding order. A bbox-clipped piece carries the whole trip's
	// stop list and answers only for the stops along it; a whole trip
	// answers for every stop, however short of the pier its shape stops.
	clipped := strings.Contains(pat.ShapeID, "#")
	var stops []geo.Pt
	for _, sid := range pat.StopSeq {
		ll, ok := stopCoords[sid]
		if !ok {
			continue
		}
		p := frame.ToXY(ll)
		if clipped && shape.DistTo(p) > ferryAttach {
			continue
		}
		if len(stops) > 0 && stops[len(stops)-1].Dist(p) < 1 {
			continue
		}
		stops = append(stops, p)
	}
	if len(stops) < 2 {
		return Path{}, false
	}
	// the piers this trip does not call at
	var others []geo.Pt
	for _, p := range m.piers {
		calls := false
		for _, st := range stops {
			if st.Dist(p) < 50 {
				calls = true
				break
			}
		}
		if !calls {
			others = append(others, p)
		}
	}

	g := m.g
	costs := map[int]float64{}
	edgeCost := func(e int) float64 {
		p := g.edges[e].Piece
		if c, ok := costs[p]; ok {
			return c
		}
		line := g.pieces[p]
		c := 0.0
		samples := line.Resample(ferryStep)
		seg := line.Len() / math.Max(1, float64(len(samples)-1))
		for k := 1; k < len(samples); k++ {
			mid := geo.Lerp(samples[k-1], samples[k], 0.5)
			over := math.Max(0, shape.DistTo(mid)-ferryFree)
			c += seg * math.Exp2(math.Min(over/ferryStray, 20))
		}
		costs[p] = c
		return c
	}

	// candidate lane ends per stop; a stop no lane reaches stands for itself
	const self = -1
	near := make([][]int, len(stops))
	attach := make([]map[int]float64, len(stops))
	for i, st := range stops {
		attach[i] = m.ferryNodes(st)
		for n := range attach[i] {
			near[i] = append(near[i], n)
		}
		sort.Ints(near[i])
		if len(near[i]) == 0 {
			near[i] = []int{self}
			attach[i] = map[int]float64{self: 0}
		}
	}
	legArc := func(i int) (float64, float64) {
		s0, _ := shape.ProjectArc(stops[i-1])
		s1, _ := shape.ProjectArc(stops[i])
		return s0, s1
	}

	// Viterbi over the lane ends standing for each stop
	type cell struct {
		cost float64
		prev int // index into near[i-1]
		walk *ferryWalk
	}
	dp := make([][]cell, len(stops))
	dp[0] = make([]cell, len(near[0]))
	for k, n := range near[0] {
		dp[0][k] = cell{cost: ferryAttachCost * attach[0][n], prev: -1}
	}
	for i := 1; i < len(stops); i++ {
		s0, s1 := legArc(i)
		// against the shape between the stops, or the stops themselves
		// where the shape stops short of a pier
		leg := math.Max(math.Abs(s1-s0), stops[i-1].Dist(stops[i]))
		limit := ferryDetour*leg + ferryDetourSlack
		// following the shape: only where no lane joins the piers at all
		chord := 1e15 + math.Abs(s1-s0)
		dp[i] = make([]cell, len(near[i]))
		for k := range dp[i] {
			dp[i][k].cost = math.Inf(1)
		}
		for pk, a := range near[i-1] {
			if math.IsInf(dp[i-1][pk].cost, 1) {
				continue
			}
			var walks map[int]*ferryWalk
			if a != self {
				walks = m.ferryWalks(a, attach[i], edgeCost, limit, ferryTour*leg+ferryTourSlack, others)
			}
			for k, b := range near[i] {
				base := dp[i-1][pk].cost + ferryAttachCost*attach[i][b]
				var c float64
				var w *ferryWalk
				switch {
				case a == b && a != self:
					w = &ferryWalk{} // two stops at one lane end
				case walks[b] != nil:
					w = walks[b]
					c = w.cost
				default:
					c = chord
				}
				if base+c < dp[i][k].cost {
					dp[i][k] = cell{cost: base + c, prev: pk, walk: w}
				}
			}
		}
	}

	// backtrack to the lane end chosen at each stop
	pick := make([]int, len(stops))
	best := 0
	for k := range dp[len(stops)-1] {
		if dp[len(stops)-1][k].cost < dp[len(stops)-1][best].cost {
			best = k
		}
	}
	for i := len(stops) - 1; i >= 0; i-- {
		pick[i] = best
		best = dp[i][best].prev
	}

	var pts []geo.Pt
	var wayIDs []string
	var steps []PathStep
	add := func(seg []geo.Pt) {
		for _, q := range seg {
			if len(pts) > 0 && pts[len(pts)-1].Dist(q) < 1e-9 {
				continue
			}
			pts = append(pts, q)
		}
	}
	at := func(i int) geo.Pt {
		if n := near[i][pick[i]]; n != self {
			return g.nodes[n].At
		}
		return stops[i]
	}
	for i := 1; i < len(stops); i++ {
		c := dp[i][pick[i]]
		if c.walk == nil {
			// no lane joins these piers: follow the shape between them
			s0, s1 := legArc(i)
			gp := []geo.Pt{at(i - 1)}
			if s1 > s0 {
				gp = append(gp, subPts(shape, s0, s1)...)
			}
			gp = append(gp, at(i))
			if geo.NewLine(gp).Len() >= 0.5 {
				add(gp)
				wayIDs = append(wayIDs, "gap")
				steps = append(steps, PathStep{Piece: -1, Gap: geo.NewLine(gp)})
			}
			continue
		}
		for _, e := range c.walk.edges {
			piece := g.edges[e].Piece
			if m.usedBy[piece] == nil {
				m.usedBy[piece] = map[string]bool{}
				m.usedColor[piece] = map[string]bool{}
			}
			m.usedBy[piece][pat.Route.ID] = true
			m.usedColor[piece][pat.Route.Color] = true
			add(g.edges[e].Line.Pts)
			wayIDs = append(wayIDs, g.pieceToken(piece))
			steps = append(steps, PathStep{Piece: piece, Rev: e%2 == 1})
		}
	}
	if len(pts) < 2 {
		return Path{}, false
	}
	return Path{Pattern: pat, Line: geo.NewLine(pts), WayIDs: wayIDs, Steps: steps}, true
}

func subPts(l *geo.Line, s0, s1 float64) []geo.Pt {
	out := []geo.Pt{l.AtArc(s0)}
	for k, s := range l.ArcTable() {
		if s > s0 && s < s1 {
			out = append(out, l.Pts[k])
		}
	}
	return append(out, l.AtArc(s1))
}

// ferryNodes: every seaway node within reach of a pier, with its distance.
func (m *matcher) ferryNodes(p geo.Pt) map[int]float64 {
	g := m.g
	out := map[int]float64{}
	g.grid.Near(p, ferryAttach, func(piece int) {
		e := g.edges[2*piece]
		if wayRailClass[e.Way] != "seaway" {
			return
		}
		for _, n := range [2]int{e.From, e.To} {
			if d := g.nodes[n].At.Dist(p); d <= ferryAttach {
				out[n] = d
			}
		}
	})
	return out
}

// ferryWalks: the cheapest seaway walk from node a to each of the targets,
// skipping any whose lane length passes limit.
func (m *matcher) ferryWalks(a int, targets map[int]float64,
	edgeCost func(int) float64, limit, tour float64, others []geo.Pt) map[int]*ferryWalk {
	g := m.g
	dist := map[int]float64{a: 0}
	via := map[int]int{}
	left := len(targets)
	pq := &nodeHeap{{a, 0}}
	for pq.Len() > 0 && left > 0 {
		cur := heap.Pop(pq).(nodeItem)
		if cur.d > dist[cur.n] {
			continue
		}
		if _, ok := targets[cur.n]; ok && cur.n != a {
			left--
		}
		for _, e := range g.nodes[cur.n].Out {
			if wayRailClass[g.edges[e].Way] != "seaway" {
				continue
			}
			nx := g.edges[e].To
			nd := cur.d + edgeCost(e)
			if d, ok := dist[nx]; !ok || nd < d {
				dist[nx] = nd
				via[nx] = e
				heap.Push(pq, nodeItem{nx, nd})
			}
		}
	}
	out := map[int]*ferryWalk{}
	for b := range targets {
		if b == a {
			continue
		}
		if _, ok := dist[b]; !ok {
			continue
		}
		var rev []int
		length := 0.0
		for n := b; n != a; n = g.edges[via[n]].From {
			rev = append(rev, via[n])
			length += g.edges[via[n]].Line.Len()
		}
		if length > limit || (length > tour && m.callsAt(a, rev, others)) {
			// the lanes only join these piers by way of others: OSM lacks
			// the lane this boat takes, and calling at piers it skips is
			// worse than following its own shape
			continue
		}
		edges := make([]int, len(rev))
		for k, e := range rev {
			edges[len(rev)-1-k] = e
		}
		out[b] = &ferryWalk{edges: edges, cost: dist[b]}
	}
	return out
}

// callsAt: whether a walk from node a passes through a lane node by one
// of the piers on its way.
func (m *matcher) callsAt(a int, edges []int, piers []geo.Pt) bool {
	for _, e := range edges {
		n := m.g.edges[e].From
		if n == a {
			continue
		}
		at := m.g.nodes[n].At
		for _, p := range piers {
			if at.Dist(p) < ferryPierReach {
				return true
			}
		}
	}
	return false
}

type nodeItem struct {
	n int
	d float64
}

type nodeHeap []nodeItem

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].d < h[j].d }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x any)        { *h = append(*h, x.(nodeItem)) }
func (h *nodeHeap) Pop() any          { o := *h; x := o[len(o)-1]; *h = o[:len(o)-1]; return x }
