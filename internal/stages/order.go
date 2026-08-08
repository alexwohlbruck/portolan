package stages

import (
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

// ORDER — owner's step 4, LOOM-lite. The slot unit is the TRUNK GROUP
// (law 5 generalized, docs/MODES.md): for rail the key is the color —
// same-color routes share one ribbon, exactly as before — while colorless
// regional falls back to agency and the singleton modes (ferry, aerial,
// funicular, cable) never merge. On a color-trunked system this keeps
// every edge at 1–4 groups — small enough to search each edge's
// permutations exhaustively inside a hill climb. The objective is the
// number of order inversions between groups that continue across a node,
// always compared in the travel frame (storage order flips when an edge
// is walked against its orientation — LESSONS #12).
//
// slots[ei] is the edge's group-key order left→right in its STORAGE
// (From→To) travel frame.
func Order(n *Network, routes map[string]gtfs.Route) (map[int][]string, error) {
	colorOf := func(rid string) string {
		return mode.TrunkKey(routes[rid])
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
	// away from the node along the edge), sampled `win` metres out
	outTanD := func(ei, ni int, win float64) (float64, float64) {
		pts := n.Edges[ei].Pts
		if len(pts) < 2 {
			return 0, 0
		}
		var a, b int
		if n.Edges[ei].From == ni {
			a, b = 0, 1
			for b < len(pts)-1 && pts[a].Dist(pts[b]) < win {
				b++
			}
		} else {
			a, b = len(pts)-1, len(pts)-2
			for b > 0 && pts[a].Dist(pts[b]) < win {
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
	outTan := func(ei, ni int) (float64, float64) { return outTanD(ei, ni, 12) }
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
				changedSet := map[string]bool{}
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
						changedSet[c] = true
					}
				}
				// the discount is PER PAIR, not per seam: a seam where orange
				// departs must not make brown↔purple↔pink inversions cheap —
				// the continuing group holds its order (owner's rule). Only
				// pairs involving a changed color have a join/leave curve to
				// absorb the crossing.
				for x := 0; x < len(shared); x++ {
					for y := x + 1; y < len(shared); y++ {
						da := pos(a, shared[x], aStorage) - pos(a, shared[y], aStorage)
						db := pos(b, shared[x], bStorage) - pos(b, shared[y], bStorage)
						if da*db < 0 {
							cx, cy := changedSet[shared[x]], changedSet[shared[y]]
							switch {
							case cx && cy:
								total += 1
							case cx || cy:
								total += 2
							default:
								total += 8
							}
						}
					}
				}
			}
		}
		// side-crossing term: a color continuing into an edge that attaches
		// from a SIDE must cross every resident between its slot and that
		// side — invisible to the shared-pair inversions above (a departing
		// color shares nothing with the lines it slices through), so blue
		// exited straight through the bundle for free and entrants shuffled
		// residents instead of appending on their arrival side. Straight
		// continuations (near-collinear) don't constrain a side and skip.
		for ai := 0; ai < len(adjE); ai++ {
			for bi := 0; bi < len(adjE); bi++ {
				if ai == bi {
					continue
				}
				a, b := adjE[ai], adjE[bi]
				if a == b {
					continue
				}
				// side measured 80 m out: the first metres of a departing
				// edge are weld-tail geometry running parallel to the
				// corridor, so a short window reads "no side" and the
				// departure crosses the bundle for free (blue parked
				// mid-ribbon at the Lake join)
				ax, ay := outTanD(a, ni, 80)
				bx, by := outTanD(b, ni, 80)
				// travel direction into the node along a
				dx, dy := -ax, -ay
				cr := dx*by - dy*bx
				if math.Abs(cr) < 0.3 {
					continue // b continues straight — no side
				}
				bLeft := cr > 0
				aStorage := n.Edges[a].To == ni
				for _, c := range perm[a] {
					if pos(b, c, true) < 0 {
						continue // c does not continue into b
					}
					pc := pos(a, c, aStorage)
					for _, d := range perm[a] {
						if d == c || pos(b, d, true) >= 0 {
							continue // d also rides b — ordered by inversions
						}
						pd := pos(a, d, aStorage)
						if (bLeft && pd < pc) || (!bLeft && pd > pc) {
							// weight 3: these are real drawn crossings — at 1
							// a distant tie elsewhere could still park a
							// joining line mid-bundle (blue between G and
							// Pink on Lake); below 8 so a genuinely cheaper
							// continuing order still wins
							total += 3
						}
					}
				}
			}
		}
		return total
	}

	// affinity: how much corridor length two colors share. The tiebreak
	// that keeps same-steel pairs adjacent when crossings can't decide —
	// a joining line must cross the pair once wherever it slots, so
	// crossing count alone lets it split the pair (Blue slotted between
	// the Lake el's G/Pink). Apple keeps the pair together and the
	// newcomer outside. Scaled so crossings stay strictly dominant.
	pairKey := func(a, b string) [2]string {
		if a > b {
			return [2]string{b, a}
		}
		return [2]string{a, b}
	}
	affinity := map[[2]string]float64{}
	for ei, e := range n.Edges {
		L := 0.0
		for i := 1; i < len(e.Pts); i++ {
			L += e.Pts[i].Dist(e.Pts[i-1])
		}
		cs := perm[ei]
		for x := 0; x < len(cs); x++ {
			for y := x + 1; y < len(cs); y++ {
				affinity[pairKey(cs[x], cs[y])] += L
			}
		}
	}
	adjBonus := func(ei int) int {
		b := 0.0
		for i := 0; i+1 < len(perm[ei]); i++ {
			b += affinity[pairKey(perm[ei][i], perm[ei][i+1])]
		}
		return int(b / 50)
	}
	edgeCost := func(ei int) int {
		return (crossingsAt(n.Edges[ei].From)+crossingsAt(n.Edges[ei].To))*4096 -
			adjBonus(ei)
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
	// sorted pair order: each accepted flip changes what later pairs see,
	// so map iteration here made the whole pass run-to-run random
	pairList := make([][2]string, 0, len(colorPairs))
	for k := range colorPairs {
		pairList = append(pairList, k)
	}
	sort.Slice(pairList, func(i, j int) bool {
		if pairList[i][0] != pairList[j][0] {
			return pairList[i][0] < pairList[j][0]
		}
		return pairList[i][1] < pairList[j][1]
	})
	for _, k := range pairList {
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
		compEdges := make([]int, 0, len(parent))
		for ei := range parent {
			compEdges = append(compEdges, ei)
		}
		sort.Ints(compEdges)
		for _, ei := range compEdges {
			comps[find(ei)] = append(comps[find(ei)], ei)
		}
		// sorted component order: flips at shared nodes change what later
		// components measure
		roots := make([]int, 0, len(comps))
		for r := range comps {
			roots = append(roots, r)
		}
		sort.Ints(roots)
		for _, r := range roots {
			edges := comps[r]
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

	// Slide-to-edge pass. A color that joins the bundle on one side and
	// leaves on the other must cross every resident wherever it slots —
	// the cheapest place is an OUTER slot with the crossing concentrated
	// at one junction (owner's rule: rest on the top or bottom edge). But
	// reaching an outer slot from mid-bundle means sliding past several
	// residents, and every intermediate position costs more than either
	// endpoint: single-edge climbing and single-pair component swaps both
	// stall in the valley (blue parked between G and Pink on Lake). Try
	// each color at BOTH extremes of its entire continuation component in
	// one jump, others keeping their order; keep the cheapest.
	colorSet := map[string]bool{}
	for ei := range n.Edges {
		for _, c := range perm[ei] {
			colorSet[c] = true
		}
	}
	removeC := func(p []string, c string) []string {
		out := make([]string, 0, len(p))
		for _, x := range p {
			if x != c {
				out = append(out, x)
			}
		}
		return out
	}
	// sorted color order: each accepted slide changes what later colors see
	colorList := make([]string, 0, len(colorSet))
	for c := range colorSet {
		colorList = append(colorList, c)
	}
	sort.Strings(colorList)
	for _, c := range colorList {
		// component over edges carrying c, with relative storage
		// orientation propagated across shared nodes (To→From aligned,
		// To→To / From→From flipped) so "front" means one consistent
		// geometric side across the whole component
		inComp := map[int]bool{}
		for ei := range n.Edges {
			if hasColor(ei, c) {
				inComp[ei] = true
			}
		}
		visited := map[int]bool{}
		// sorted seeds: the BFS tree (orientation propagation on cyclic
		// components) and the component processing order both depend on it
		seeds := make([]int, 0, len(inComp))
		for seed := range inComp {
			seeds = append(seeds, seed)
		}
		sort.Ints(seeds)
		for _, seed := range seeds {
			if visited[seed] {
				continue
			}
			orient := map[int]bool{seed: true} // true = as-stored
			queue := []int{seed}
			visited[seed] = true
			var comp []int
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				comp = append(comp, cur)
				for _, ni := range []int{n.Edges[cur].From, n.Edges[cur].To} {
					curAtTo := n.Edges[cur].To == ni
					for _, nx := range n.Nodes[ni].Adj {
						if nx == cur || !inComp[nx] || visited[nx] {
							continue
						}
						nxAtFrom := n.Edges[nx].From == ni
						// aligned when travel continues storage-forward
						// on both: cur arrives at To and nx departs at From
						aligned := curAtTo == nxAtFrom
						orient[nx] = orient[cur] == aligned
						visited[nx] = true
						queue = append(queue, nx)
					}
				}
			}
			nodes := map[int]bool{}
			for _, ei := range comp {
				nodes[n.Edges[ei].From] = true
				nodes[n.Edges[ei].To] = true
			}
			saved := map[int][]string{}
			for _, ei := range comp {
				saved[ei] = append([]string(nil), perm[ei]...)
			}
			before := totalCost(nodes)
			// GUESTS get an edge-slot budget: a color whose longest shared
			// corridor with any cohabitant is short (blue rides Lake for
			// 500 m; the circulators share kilometres) is threading someone
			// else's bundle — it crosses everything anyway and reads far
			// cleaner hugging the ribbon edge (owner's rule), worth a
			// crossing or two. Residents need strict improvement, so a
			// later slide can never undo a guest's edge slot in a tie.
			maxShared := 0.0
			for d := range colorSet {
				if d == c {
					continue
				}
				if v := affinity[pairKey(c, d)]; v > maxShared {
					maxShared = v
				}
			}
			budget := 0
			if maxShared < 1500 {
				budget = 6
			}
			bestExtreme := math.MaxInt32
			var bestPerms map[int][]string
			for side := 0; side < 2; side++ {
				for _, ei := range comp {
					rest := removeC(saved[ei], c)
					front := side == 0
					if !orient[ei] {
						front = !front
					}
					if front {
						perm[ei] = append([]string{c}, rest...)
					} else {
						perm[ei] = append(append([]string(nil), rest...), c)
					}
				}
				t := totalCost(nodes)
				if os.Getenv("PORTOLAN_DBGO") == c {
					fmt.Printf("SLIDE3 %s comp=%d edges side=%d cost=%d (before %d)\n",
						c, len(comp), side, t, before)
				}
				if t < bestExtreme {
					bestExtreme = t
					bestPerms = map[int][]string{}
					for _, ei := range comp {
						bestPerms[ei] = append([]string(nil), perm[ei]...)
					}
				}
			}
			accept := bestExtreme < before
			if budget > 0 && bestExtreme <= before+budget {
				accept = true
			}
			if accept {
				for _, ei := range comp {
					perm[ei] = append([]string(nil), bestPerms[ei]...)
				}
			} else {
				for _, ei := range comp {
					perm[ei] = append([]string(nil), saved[ei]...)
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
// copy if it keeps one) — up to 6 elements (720 candidates). Wider groups
// enumerate adjacent-pair swaps instead: LIRR funnels 12 distinctly-colored
// branches through Jamaica, and 12! is half a billion candidates per edge
// per climb pass — the NYC all-modes build sat in ORDER for 2h40m without
// finishing. Swap moves compose across climb passes into longer
// reorderings, the same way the tie-walking plateau steps do.
func permute(v []string, fn func([]string)) {
	if len(v) > 6 {
		fn(v)
		for i := 0; i+1 < len(v); i++ {
			v[i], v[i+1] = v[i+1], v[i]
			fn(v)
			v[i], v[i+1] = v[i+1], v[i]
		}
		return
	}
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
