package stages

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
)

// ORDER — owner's step 4, LOOM-lite. The slot unit is the COLOR GROUP
// (law 5: same-color routes share one ribbon), which on a color-trunked
// system keeps every edge at 1–4 groups — small enough to search each
// edge's permutations exhaustively inside a hill climb. The objective is
// the number of order inversions between colors that continue across a
// node, always compared in the travel frame (storage order flips when an
// edge is walked against its orientation — LESSONS #12).
//
// slots[ei] is the edge's color order left→right in its STORAGE (From→To)
// travel frame.
func Order(n *Network, routes map[string]gtfs.Route) (map[int][]string, error) {
	colorOf := func(rid string) string {
		c := routes[rid].Color
		if c == "" {
			c = "888888"
		}
		return c
	}
	perm := make([][]string, len(n.Edges))
	for ei, e := range n.Edges {
		seen := map[string]bool{}
		for _, r := range e.Routes {
			seen[colorOf(r)] = true
		}
		for c := range seen {
			perm[ei] = append(perm[ei], c)
		}
		sort.Strings(perm[ei])
	}

	// travel-frame position of a color on an edge, standing at node n:
	// arriving (into the node) keeps storage order when the node is To;
	// departing keeps storage order when the node is From. Both flip
	// otherwise — a mirrored frame, never re-derived (LESSONS #12).
	pos := func(ei int, color string, storageOrder bool) int {
		p := perm[ei]
		for i, c := range p {
			if c == color {
				if storageOrder {
					return i
				}
				return len(p) - 1 - i
			}
		}
		return -1
	}
	// outward tangent of an edge standing at node ni (unit vector pointing
	// away from the node along the edge)
	outTan := func(ei, ni int) (float64, float64) {
		pts := n.Edges[ei].Pts
		if len(pts) < 2 {
			return 0, 0
		}
		var a, b int
		if n.Edges[ei].From == ni {
			a, b = 0, 1
			for b < len(pts)-1 && pts[a].Dist(pts[b]) < 12 {
				b++
			}
		} else {
			a, b = len(pts)-1, len(pts)-2
			for b > 0 && pts[a].Dist(pts[b]) < 12 {
				b--
			}
		}
		dx, dy := pts[b].X-pts[a].X, pts[b].Y-pts[a].Y
		l := math.Hypot(dx, dy)
		if l == 0 {
			return 0, 0
		}
		return dx / l, dy / l
	}
	crossingsAt := func(ni int) int {
		adjE := n.Nodes[ni].Adj
		total := 0
		for ai := 0; ai < len(adjE); ai++ {
			for bi := ai + 1; bi < len(adjE); bi++ {
				a, b := adjE[ai], adjE[bi]
				if a == b {
					continue
				}
				// FAN SIBLINGS do not constrain each other's order: when
				// both branches carry the same colors their flows cross
				// inside the junction whatever the slots — counting that
				// noise let a sibling outvote trunk continuity and flip a
				// terminus branch (Nostrand 2·3/4·5). a and b are siblings
				// when some third edge opposes BOTH better than they
				// oppose each other (they fan from a common continuation);
				// a corner with no third edge is a continuation and votes.
				ax, ay := outTan(a, ni)
				bx, by := outTan(b, ni)
				dab := ax*bx + ay*by
				sibling := false
				for _, c := range adjE {
					if c == a || c == b {
						continue
					}
					cx, cy := outTan(c, ni)
					if ax*cx+ay*cy < dab && bx*cx+by*cy < dab {
						sibling = true
						break
					}
				}
				if sibling {
					continue
				}
				// traveling a → node → b
				aStorage := n.Edges[a].To == ni
				bStorage := n.Edges[b].From == ni
				var shared []string
				for _, c := range perm[a] {
					if pos(b, c, true) >= 0 {
						shared = append(shared, c)
					}
				}
				// Flip cost scales with how LITTLE reordering business the
				// seam has (owner's rule: a flip costs more than
				// overlapping lines). Where every shared color's ROUTE
				// membership passes through unchanged, a flip is pure jank
				// (weight 8). Where one color gains members (Montague R·W
				// merging into the trunk's yellows) a crossing is partly
				// absorbed (2). Where the colors themselves split apart to
				// different branches (the DeKalb fan) the reorder is where
				// it belongs (1) — the single necessary flip slides there.
				changed := 0
				for _, c := range shared {
					ra := map[string]bool{}
					for _, r := range n.Edges[a].Routes {
						if colorOf(r) == c {
							ra[r] = true
						}
					}
					rb := map[string]bool{}
					for _, r := range n.Edges[b].Routes {
						if colorOf(r) == c {
							rb[r] = true
						}
					}
					// the discount below exists because a visible line
					// joining or leaving the bundle absorbs a crossing —
					// so the differing routes must actually ARRIVE OR
					// DEPART on another edge at this node. A route that
					// simply terminates here (the M ending at Forest
					// Hills) changes membership without any drawn line
					// to hide a crossing behind: not a change.
					branchHas := func(r string) bool {
						for _, ci := range adjE {
							if ci == a || ci == b {
								continue
							}
							for _, cr := range n.Edges[ci].Routes {
								if cr == r {
									return true
								}
							}
						}
						return false
					}
					vis := false
					for r := range ra {
						if !rb[r] && branchHas(r) {
							vis = true
						}
					}
					for r := range rb {
						if !ra[r] && branchHas(r) {
							vis = true
						}
					}
					if vis {
						changed++
					}
				}
				wt := 1
				switch changed {
				case 0:
					wt = 8
				case 1:
					wt = 2
				}
				for x := 0; x < len(shared); x++ {
					for y := x + 1; y < len(shared); y++ {
						da := pos(a, shared[x], aStorage) - pos(a, shared[y], aStorage)
						db := pos(b, shared[x], bStorage) - pos(b, shared[y], bStorage)
						if da*db < 0 {
							total += wt
						}
					}
				}
			}
		}
		return total
	}

	edgeCost := func(ei int) int {
		return crossingsAt(n.Edges[ei].From) + crossingsAt(n.Edges[ei].To)
	}
	permKey := func(p []string) string {
		s := ""
		for _, c := range p {
			s += c + ","
		}
		return s
	}
	history := make([]map[string]bool, len(n.Edges))
	for ei := range n.Edges {
		history[ei] = map[string]bool{permKey(perm[ei]): true}
	}
	// Phase 1: strict hill climb. Phase 2 (ties=true): also accept
	// equal-cost moves onto never-visited perms — a gratuitous flip
	// pinned between two consistent runs needs one cost-NEUTRAL step
	// (sliding the crossing along the chain) before the improving step
	// exists, and strict climbing refuses it (the Bergen St B/Q flip).
	// The per-edge history makes plateau walking terminate.
	climb := func(passes int, ties bool) {
		improved := true
		for pass := 0; pass < passes && improved; pass++ {
			improved = false
			for ei := range n.Edges {
				if len(perm[ei]) < 2 {
					continue
				}
				base := edgeCost(ei)
				bestPerm := append([]string(nil), perm[ei]...)
				bestCost := base
				tieTaken := false
				permute(perm[ei], func(cand []string) {
					perm[ei] = cand
					c := edgeCost(ei)
					if c < bestCost {
						bestCost = c
						bestPerm = append([]string(nil), cand...)
						tieTaken = false
					} else if ties && !tieTaken && c == bestCost &&
						bestCost == base && !history[ei][permKey(cand)] {
						bestPerm = append([]string(nil), cand...)
						tieTaken = true
					}
				})
				perm[ei] = bestPerm
				history[ei][permKey(bestPerm)] = true
				if bestCost < base || tieTaken {
					improved = true
				}
			}
		}
	}
	climb(25, false)
	climb(15, true)
	climb(10, false)

	// Chain-consistency pass. Hill climbing cannot cross plateaus: a
	// gratuitous flip between two long consistent runs costs the same
	// wherever it sits, so single-edge moves just walk it along the chain
	// (the Bergen St B/Q crossing survived a weight-8 seam this way). For
	// every color pair, flip its relative order across an ENTIRE
	// continuation-connected component at once and keep the cheaper total
	// — exact for the binary choice, plateau-proof.
	swapPair := func(ei int, c1, c2 string) {
		i1, i2 := -1, -1
		for i, c := range perm[ei] {
			if c == c1 {
				i1 = i
			}
			if c == c2 {
				i2 = i
			}
		}
		if i1 >= 0 && i2 >= 0 {
			perm[ei][i1], perm[ei][i2] = perm[ei][i2], perm[ei][i1]
		}
	}
	colorPairs := map[[2]string]bool{}
	for ei := range n.Edges {
		for x := 0; x < len(perm[ei]); x++ {
			for y := x + 1; y < len(perm[ei]); y++ {
				k := [2]string{perm[ei][x], perm[ei][y]}
				if k[0] > k[1] {
					k[0], k[1] = k[1], k[0]
				}
				colorPairs[k] = true
			}
		}
	}
	hasColor := func(ei int, c string) bool {
		for _, x := range perm[ei] {
			if x == c {
				return true
			}
		}
		return false
	}
	totalCost := func(nodes map[int]bool) int {
		t := 0
		for ni := range nodes {
			t += crossingsAt(ni)
		}
		return t
	}
	for k := range colorPairs {
		c1, c2 := k[0], k[1]
		// union-find over edges carrying both colors, joined where the
		// pair continues intact across a node
		parent := map[int]int{}
		var find func(int) int
		find = func(x int) int {
			for parent[x] != x {
				parent[x] = parent[parent[x]]
				x = parent[x]
			}
			return x
		}
		for ei := range n.Edges {
			if hasColor(ei, c1) && hasColor(ei, c2) {
				parent[ei] = ei
			}
		}
		for ni, nd := range n.Nodes {
			_ = ni
			for x := 0; x < len(nd.Adj); x++ {
				for y := x + 1; y < len(nd.Adj); y++ {
					a, b := nd.Adj[x], nd.Adj[y]
					if a == b {
						continue
					}
					if _, ok := parent[a]; !ok {
						continue
					}
					if _, ok := parent[b]; !ok {
						continue
					}
					parent[find(a)] = find(b)
				}
			}
		}
		comps := map[int][]int{}
		for ei := range parent {
			comps[find(ei)] = append(comps[find(ei)], ei)
		}
		for _, edges := range comps {
			nodes := map[int]bool{}
			for _, ei := range edges {
				nodes[n.Edges[ei].From] = true
				nodes[n.Edges[ei].To] = true
			}
			before := totalCost(nodes)
			for _, ei := range edges {
				swapPair(ei, c1, c2)
			}
			if totalCost(nodes) >= before {
				for _, ei := range edges {
					swapPair(ei, c1, c2) // revert
				}
			}
		}
	}

	slots := map[int][]string{}
	for ei := range n.Edges {
		slots[ei] = perm[ei]
	}
	return slots, nil
}

// permute calls fn with every permutation of v (v is scratch space; fn must
// copy if it keeps one).
func permute(v []string, fn func([]string)) {
	var rec func(k int)
	rec = func(k int) {
		if k == len(v) {
			fn(v)
			return
		}
		for i := k; i < len(v); i++ {
			v[k], v[i] = v[i], v[k]
			rec(k + 1)
			v[k], v[i] = v[i], v[k]
		}
	}
	rec(0)
}
