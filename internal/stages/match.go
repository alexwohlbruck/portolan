package stages

import (
	"container/heap"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

// MATCH — owner's step 1. Each GTFS pattern becomes a continuous walk over
// the welded track graph (Law 1: never broken). A Viterbi DP chooses the
// walk: emission = shape deviation + heading agreement, transitions exist
// only along graph connectivity (no jumping), turns are penalized so weaving
// across switch ladders loses to staying on one track, and pieces already
// ridden by earlier-matched patterns earn a discount so co-running routes
// converge onto identical geometry. Where the shape finds no track at all,
// a GAP state bridges with shape geometry (OSM is incomplete — LESSONS #8).

type matchParams struct {
	SampleDs   float64 // shape sample spacing
	Reach      float64 // candidate piece radius
	MaxCand    int     // candidate pieces per sample
	WHead      float64 // weight of (1 - cos heading agreement)
	WTurn      float64 // per-degree turn penalty inside transition walks
	WWalk      float64 // per-meter penalty on intermediate walk length
	WHop       float64 // flat penalty per intermediate edge
	UTurnPen   float64 // reversing onto the same piece mid-walk
	BonusRoute float64 // discount on pieces used by the SAME route
	BonusColor float64 // ...by another route of the same color
	BonusOther float64 // ...by any other route
	GapCost    float64 // emission of the GAP state (m-equivalent)
	GapFree    float64 // GAP exists only where no usable track is this close
	GapSwitch  float64 // cost to enter/leave GAP
	MaxWalk    float64 // cap on intermediate walk length
}

// barredGap keeps the DP solvable where the gap gate closes but the graph is
// genuinely impassable — a huge, non-competitive escape hatch.
const barredGap = 1e7

func defaultMatchParams() matchParams {
	return matchParams{
		SampleDs: 20,
		Reach:    dial("match_reach", 90),
		MaxCand:  int(dial("match_cands", 14)),
		WHead:    dial("match_w_head", 35),
		WTurn:    dial("match_w_turn", 0.8),
		WWalk:    0.3,
		WHop:     2.0,
		UTurnPen: 150,
		// affinity-scaled convergence (owner's rule 1.1): a route's own
		// directions MUST land on one track even across a wide 4-track
		// viaduct (25), same-color services bundle at ~18, and unrelated
		// routes only within 12 — below kiss distance, so the first
		// pattern of a distinct line never lands on its neighbor's track
		BonusRoute: dial("match_bonus_route", 25),
		BonusColor: dial("match_bonus_color", 18),
		BonusOther: dial("match_bonus_other", 12),
		// GapCost > the worst legitimate shape chord (Fresh Pond: 64 m off
		// its own track) so a present track always beats bridging; the gate
		// (GapFree) makes GAP unrepresentable where usable track exists —
		// shapes bridge gaps ONLY (LESSONS #8), never arbitrage a jump
		GapCost:   dial("match_gap_cost", 75),
		GapFree:   dial("match_gap_free", 45),
		GapSwitch: 40,
		MaxWalk:   dial("match_max_walk", 350),
	}
}

const gapState = -1

// crossoverWays: ways tagged service=crossover. Crossovers are kept in the
// graph for CONNECTIVITY (dropping them severed the bridge→Broadway link),
// but riding one is an exceptional movement, not service routing — without
// a cost, a sparse shape chord that happens to lie along a crossover
// shortcut wins over the real revenue sweep (the 1 shortcut into South
// Ferry over a reversing crossover). The penalty loses to any genuine
// need (a gap costs thousands) and wins over chord-noise emissions.
var crossoverWays map[string]bool

func SetCrossoverWays(m map[string]bool) { crossoverWays = m }

// wayLevels: vertical class per way (+1 elevated, -1 tunnel, 0 surface).
// A walk step directly between opposite classes is a physical
// impossibility — a train reaches an el from a tunnel through ramps, never
// by teleporting through the street. Stacked alignments (the 60th St tube
// under the Queensboro Plaza el) weld together in 2D and invite exactly
// that arbitrage.
var wayLevels map[string]int

func SetWayLevels(m map[string]int) { wayLevels = m }

// wayRailClass: the OSM railway= class per way. A metro route does not
// ride light-rail steel that happens to hug its shape (the O'Hare
// people-mover parallels the Blue Line exactly where the GTFS shape
// drifts off the subway). The penalty only applies when compatible-class
// track is actually present at the sample — systems that genuinely run on
// another class (SIR on railway=rail) see uniform costs and are unmoved.
var wayRailClass map[string]string

func SetWayRailClass(m map[string]string) { wayRailClass = m }

func classCompat(routeType int, cls string) bool {
	if cls == "" {
		return true
	}
	switch routeType {
	case 1, 401: // metro
		return cls == "subway"
	case 0, 900: // tram / light rail
		return cls == "light_rail" || cls == "tram"
	case 2, 100: // rail
		return cls == "rail" || cls == "narrow_gauge"
	}
	return true
}

const classPen = 100.0

const crossoverPen = 120.0
const levelJumpPen = 300.0

// Match path-matches each GTFS pattern onto the mode-appropriate OSM layer
// (rails for trains, roads for buses, sea routes for ferries). Owner's
// rules: routes on similar paths MUST land on the same matched path; no
// jumping between paths — always follow real ways, with crossover segments
// (short ways connecting longer ways: switches, median turns) penalized;
// mainlines only — spurs and yards ignored except at station terminals.
func Match(patterns []gtfs.Pattern, ways []bundle.Track, frame geo.Frame) ([]Path, error) {
	g := buildTrackGraph(ways)
	p := defaultMatchParams()

	// canonical patterns first: heaviest service defines the shared paths
	// that later patterns converge onto (owner's rule 1.1)
	order := make([]int, len(patterns))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := patterns[order[a]], patterns[order[b]]
		if pa.Trips != pb.Trips {
			return pa.Trips > pb.Trips
		}
		if pa.Route.ID != pb.Route.ID {
			return pa.Route.ID < pb.Route.ID
		}
		return pa.ShapeID < pb.ShapeID
	})

	m := &matcher{g: g, p: p,
		usedBy:    map[int]map[string]bool{},
		usedColor: map[int]map[string]bool{},
		walks:     map[[2]int]walkRes{}}
	var out []Path
	for _, oi := range order {
		if path, ok := m.matchOne(patterns[oi], frame); ok {
			out = append(out, path)
		}
	}
	return out, nil
}

type matcher struct {
	g         *trackGraph
	p         matchParams
	usedBy    map[int]map[string]bool // piece → route ids that ride it
	usedColor map[int]map[string]bool // piece → colors that ride it
	walks     map[[2]int]walkRes
}

type walkRes struct {
	cost float64
	via  []int // intermediate directed edges, exclusive of both ends
	ok   bool
}

type candidate struct {
	edge int     // directed edge id
	emit float64 // emission cost at this sample
}

func (m *matcher) matchOne(pat gtfs.Pattern, frame geo.Frame) (Path, bool) {
	pts := make([]geo.Pt, len(pat.Shape))
	for i, ll := range pat.Shape {
		pts[i] = frame.ToXY(ll)
	}
	shape := geo.NewLine(pts)
	if shape.Len() < 2*m.p.SampleDs {
		return Path{}, false
	}
	samples := shape.Resample(m.p.SampleDs)
	n := len(samples)
	shapeArc := func(i int) float64 { return shape.Len() * float64(i) / float64(n-1) }

	// Positional confidence per sample: a sample interpolated on a long
	// chord between sparse shape vertices is a guess, not a survey — the
	// chord cuts corners and can pass nearer a DIFFERENT line's track than
	// the one actually ridden (an 800 m gap in the R's shape put its chord
	// within reach of the Lexington line and dragged the match two blocks
	// east). Down-weighting those emissions makes the DP ride graph
	// connectivity through sparse stretches instead of chasing chords.
	vertArc := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		vertArc[i] = vertArc[i-1] + pts[i].Dist(pts[i-1])
	}
	const denseSpacing = 60.0 // vertex spacing at which a shape is trusted
	// weight: how much this sample's position can be trusted (1 = fully);
	// spread: its positional uncertainty — half the chord it sits on. The
	// candidate reach grows by the spread, because the track really ridden
	// can be a whole block away from a chord (the R's sparse vertices sat
	// on the Lexington's street; Broadway was outside a fixed 90 m reach,
	// leaving the wrong line as the only representable choice).
	sampleConf := func(arc float64) (weight, spread float64) {
		lo, hi := 0, len(vertArc)-1
		for lo+1 < hi {
			mid := (lo + hi) / 2
			if vertArc[mid] <= arc {
				lo = mid
			} else {
				hi = mid
			}
		}
		seg := vertArc[hi] - vertArc[lo]
		if seg <= denseSpacing {
			return 1, 0
		}
		return math.Max(0.05, denseSpacing / seg), math.Min(400, seg/2)
	}

	// candidates + emissions per sample; the gap gate closes wherever a
	// usable (near, heading-aligned) track exists — GAP is for missing
	// track, never a way to jump between parallel paths
	cands := make([][]candidate, n)
	gapEmit := make([]float64, n)
	for i, q := range samples {
		tan := shape.TangentAtArc(shapeArc(i), 15)
		w, spread := sampleConf(shapeArc(i))
		reach, maxCand := m.p.Reach, m.p.MaxCand
		if spread > 0 {
			reach += spread
			maxCand += 8
		}
		type pc struct {
			piece int
			d     float64
			arc   float64
		}
		var near []pc
		m.g.grid.Near(q, reach, func(piece int) {
			arc, d := m.g.pieces[piece].ProjectArc(q)
			near = append(near, pc{piece, d, arc})
		})
		sort.Slice(near, func(a, b int) bool { return near[a].d < near[b].d })
		if len(near) > maxCand {
			near = near[:maxCand]
		}
		hasCompat := false
		if wayRailClass != nil {
			for _, c := range near {
				if classCompat(pat.Route.Type, wayRailClass[m.g.edges[2*c.piece].Way]) {
					hasCompat = true
					break
				}
			}
		}
		usable := false
		for _, c := range near {
			compat := !hasCompat ||
				classCompat(pat.Route.Type, wayRailClass[m.g.edges[2*c.piece].Way])
			ptan := m.g.pieces[c.piece].TangentAtArc(c.arc, 10)
			dot := ptan.Dot(tan)
			if compat && c.d < m.p.GapFree && math.Abs(dot) >= 0.5 {
				usable = true
			}
			bonus := 0.0
			switch {
			case m.usedBy[c.piece][pat.Route.ID]:
				bonus = m.p.BonusRoute
			case m.usedColor[c.piece][pat.Route.Color]:
				bonus = m.p.BonusColor
			case len(m.usedBy[c.piece]) > 0:
				bonus = m.p.BonusOther
			}
			for dir := 0; dir <= 1; dir++ {
				dd := dot
				if dir == 1 {
					dd = -dd
				}
				e := 2*c.piece + dir
				pen := 0.0
				if !compat {
					pen = classPen
				}
				cands[i] = append(cands[i], candidate{e, w*(c.d+m.p.WHead*(1-dd)+pen) - bonus})
			}
		}
		gapEmit[i] = barredGap
		if !usable {
			gapEmit[i] = m.p.GapCost
		}
	}

	// Viterbi
	type cell struct {
		cost float64
		prev int // state at the previous sample
	}
	dp := make([]map[int]cell, n)
	dp[0] = map[int]cell{gapState: {gapEmit[0], gapState}}
	for _, c := range cands[0] {
		dp[0][c.edge] = cell{c.emit, gapState}
	}
	for i := 1; i < n; i++ {
		dp[i] = map[int]cell{}
		// previous states sorted by cost: transition costs are ≥0, so the
		// scan can stop once the running best beats the remaining prevs
		type pv struct {
			state int
			cost  float64
		}
		var prevs []pv
		for s, c := range dp[i-1] {
			prevs = append(prevs, pv{s, c.cost})
		}
		sort.Slice(prevs, func(a, b int) bool { return prevs[a].cost < prevs[b].cost })

		relax := func(state int, emit float64) {
			best := math.Inf(1)
			bestPrev := gapState
			for _, u := range prevs {
				if u.cost >= best {
					break
				}
				t := m.trans(u.state, state)
				if u.cost+t < best {
					best = u.cost + t
					bestPrev = u.state
				}
			}
			if !math.IsInf(best, 1) {
				dp[i][state] = cell{best + emit, bestPrev}
			}
		}
		for _, c := range cands[i] {
			relax(c.edge, c.emit)
		}
		relax(gapState, gapEmit[i])
	}

	// backtrack
	last, best := gapState, math.Inf(1)
	for s, c := range dp[n-1] {
		if c.cost < best {
			best, last = c.cost, s
		}
	}
	states := make([]int, n)
	states[n-1] = last
	for i := n - 1; i > 0; i-- {
		states[i-1] = dp[i][states[i]].prev
	}
	return m.assemble(pat, shape, states, shapeArc)
}

// trans is the cost of moving between states across one sample step.
func (m *matcher) trans(u, v int) float64 {
	switch {
	case u == v:
		return 0
	case u == gapState && v == gapState:
		return 0
	case u == gapState || v == gapState:
		return m.p.GapSwitch
	}
	k := [2]int{u, v}
	w, ok := m.walks[k]
	if !ok {
		w = m.walk(u, v, m.p.MaxWalk)
		m.walks[k] = w
	}
	if !w.ok {
		return math.Inf(1)
	}
	return w.cost
}

// walk finds the cheapest connected walk from directed edge u to directed
// edge v: Dijkstra over directed edges, intermediate lengths + turns
// penalized, capped at maxWalk of intermediate arc.
func (m *matcher) walk(u, v int, maxWalk float64) walkRes {
	g, p := m.g, m.p
	// Pareto labels: a cheapest-cost path that busts the walk budget must
	// not block a within-budget alternative — keep, per edge, the best cost
	// AND the best (shortest) walk length seen, and expand a label when it
	// improves either. Plain Dijkstra here silently disconnected reachable
	// pairs whenever the cheap path was the long way round. Each label
	// carries its own parent chain — a shared prev map would splice
	// fragments of different paths into broken geometry.
	type label struct {
		edge    int
		prevIdx int
		cost    float64
		walkLen float64
	}
	arena := []label{{edge: u, prevIdx: -1}}
	type bestPair struct{ cost, walkLen float64 }
	best := map[int]bestPair{u: {}}
	pq := &walkPQ{{edge: u}}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(walkItem)
		lab := arena[cur.labelIdx]
		if lab.edge == v {
			var via []int
			for li := lab.prevIdx; li >= 0 && arena[li].prevIdx >= 0; li = arena[li].prevIdx {
				via = append(via, arena[li].edge)
			}
			for i, j := 0, len(via)-1; i < j; i, j = i+1, j-1 {
				via[i], via[j] = via[j], via[i]
			}
			return walkRes{cost: lab.cost, via: via, ok: true}
		}
		if b, seen := best[lab.edge]; seen && lab.cost > b.cost && lab.walkLen > b.walkLen {
			continue
		}
		for _, nx := range g.nodes[g.edges[lab.edge].To].Out {
			c := lab.cost + g.turnDeg(lab.edge, nx)*p.WTurn
			wl := lab.walkLen
			if nx == g.rev(lab.edge) {
				c += p.UTurnPen
			}
			if nx != v {
				el := g.edges[nx].Line.Len()
				c += el*p.WWalk + p.WHop
				if crossoverWays[g.edges[nx].Way] {
					c += crossoverPen
				}
				if wayLevels[g.edges[lab.edge].Way]*wayLevels[g.edges[nx].Way] < 0 {
					c += levelJumpPen
				}
				wl += el
				if wl > maxWalk {
					continue
				}
			}
			b, seen := best[nx]
			if seen && c >= b.cost && wl >= b.walkLen {
				continue // dominated
			}
			if !seen {
				best[nx] = bestPair{c, wl}
			} else {
				best[nx] = bestPair{math.Min(c, b.cost), math.Min(wl, b.walkLen)}
			}
			arena = append(arena, label{edge: nx, prevIdx: cur.labelIdx, cost: c, walkLen: wl})
			heap.Push(pq, walkItem{edge: nx, cost: c, walkLen: wl, labelIdx: len(arena) - 1})
		}
	}
	return walkRes{ok: false}
}

type walkItem struct {
	edge     int
	cost     float64
	walkLen  float64
	labelIdx int
}

type walkPQ []walkItem

func (q walkPQ) Len() int           { return len(q) }
func (q walkPQ) Less(a, b int) bool { return q[a].cost < q[b].cost }
func (q walkPQ) Swap(a, b int)      { q[a], q[b] = q[b], q[a] }
func (q *walkPQ) Push(x any)        { *q = append(*q, x.(walkItem)) }
func (q *walkPQ) Pop() any {
	old := *q
	n := len(old)
	x := old[n-1]
	*q = old[:n-1]
	return x
}

// assemble turns the per-sample state sequence into a continuous Path:
// full-edge geometry along the walk, shape geometry across gaps.
func (m *matcher) assemble(pat gtfs.Pattern, shape *geo.Line,
	states []int, shapeArc func(int) float64) (Path, bool) {

	g := m.g
	type event struct {
		edge    int // directed edge, or gapState
		fromArc float64
		toArc   float64
	}
	var events []event
	for i, s := range states {
		if len(events) > 0 && events[len(events)-1].edge == s {
			events[len(events)-1].toArc = shapeArc(i)
			continue
		}
		if len(events) > 0 && s != gapState && events[len(events)-1].edge != gapState {
			// splice the connecting walk's intermediate edges
			w := m.walks[[2]int{events[len(events)-1].edge, s}]
			for _, e := range w.via {
				events = append(events, event{e, shapeArc(i), shapeArc(i)})
			}
		}
		events = append(events, event{s, shapeArc(i), shapeArc(i)})
	}

	var pts []geo.Pt
	var wayIDs []string
	var steps []PathStep
	appendPts := func(seg []geo.Pt) {
		for _, q := range seg {
			if len(pts) > 0 && pts[len(pts)-1].Dist(q) < 1e-9 {
				continue
			}
			pts = append(pts, q)
		}
	}
	for i, ev := range events {
		if ev.edge == gapState {
			// A gap flanked by two on-track states gets ONE more chance to
			// ride real track: a walk bounded by the GAP'S OWN ARC (that is
			// when a long connector detour is physically real — the strict
			// per-sample MaxWalk stays strict everywhere else; a global
			// raise let the DP take absurd 1.5 km detours between 20 m
			// samples and broke corridors elsewhere).
			if i > 0 && i < len(events)-1 &&
				events[i-1].edge != gapState && events[i+1].edge != gapState {
				// connectors wander several times the chord (the Chrystie
				// link needs 740 m of arc for a 154 m gap) — the budget is
				// generous HERE ONLY, where the flanking states are already
				// committed and the worst case is a bad detour replacing a
				// shape chord
				gapArc := ev.toArc - ev.fromArc
				// the walk must cover the physical distance between the
				// committed anchors, which can far exceed the shape arc the
				// DP spent in GAP (a 3-sample gap can sit between anchors a
				// station apart) — budget on whichever is larger
				anchorDist := g.nodes[g.edges[events[i-1].edge].To].At.
					Dist(g.nodes[g.edges[events[i+1].edge].From].At)
				w := m.walk(events[i-1].edge, events[i+1].edge,
					math.Max(m.p.MaxWalk, 4*math.Max(gapArc, anchorDist)+400))
				if w.ok {
					for _, e := range w.via {
						piece := g.edges[e].Piece
						if m.usedBy[piece] == nil {
							m.usedBy[piece] = map[string]bool{}
							m.usedColor[piece] = map[string]bool{}
						}
						m.usedBy[piece][pat.Route.ID] = true
						m.usedColor[piece][pat.Route.Color] = true
						appendPts(g.edges[e].Line.Pts)
						wayIDs = append(wayIDs, g.pieceToken(piece))
						steps = append(steps, PathStep{Piece: piece, Rev: e%2 == 1})
					}
					continue
				}
			}
			// anchor the bridge at the adjacent graph nodes so the walk
			// stays continuous through the gap — a bridge between two edges
			// is NEVER dropped, however short (dropping one disconnects the
			// network exactly where OSM is broken)
			gp := []geo.Pt{}
			if i > 0 && events[i-1].edge != gapState {
				gp = append(gp, g.nodes[g.edges[events[i-1].edge].To].At)
			}
			for _, q := range bundle.SubLine(shape, ev.fromArc, ev.toArc).Pts {
				if len(gp) == 0 || gp[len(gp)-1].Dist(q) > 1e-9 {
					gp = append(gp, q)
				}
			}
			if i < len(events)-1 && events[i+1].edge != gapState {
				np := g.nodes[g.edges[events[i+1].edge].From].At
				if len(gp) == 0 || gp[len(gp)-1].Dist(np) > 1e-9 {
					gp = append(gp, np)
				}
			}
			if len(gp) < 2 || geo.NewLine(gp).Len() < 0.5 {
				continue // start/end-of-route stub with no real extent
			}
			appendPts(gp)
			wayIDs = append(wayIDs, "gap")
			steps = append(steps, PathStep{Piece: -1, Gap: geo.NewLine(gp)})
			continue
		}
		piece := g.edges[ev.edge].Piece
		if m.usedBy[piece] == nil {
			m.usedBy[piece] = map[string]bool{}
			m.usedColor[piece] = map[string]bool{}
		}
		m.usedBy[piece][pat.Route.ID] = true
		m.usedColor[piece][pat.Route.Color] = true
		appendPts(g.edges[ev.edge].Line.Pts)
		wayIDs = append(wayIDs, g.pieceToken(g.edges[ev.edge].Piece))
		steps = append(steps, PathStep{
			Piece: g.edges[ev.edge].Piece, Rev: ev.edge%2 == 1,
		})
	}
	if len(pts) < 2 {
		return Path{}, false
	}
	return Path{Pattern: pat, Line: geo.NewLine(pts), WayIDs: wayIDs, Steps: steps}, true
}
