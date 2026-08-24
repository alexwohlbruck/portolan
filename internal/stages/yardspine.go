package stages

// Yard-spine substitution — the yard rule the twin guard has been waiting
// for. A big terminal or yard hands MATCH a different platform track per
// pattern, all faithfully real, and SPLIT then draws one trunk as five
// parallel weaving strands (Jamaica: ten LIRR services over an
// eight-track ladder). A road map draws the corridor, not the
// interlocking: where a SOLE-TRUNK corridor crosses a detected yard
// region, the in-region weave is replaced by the region's spine skeleton
// — real steel picked by the detector, with fork nodes where branches
// genuinely diverge (Main Line vs Atlantic stays two corridors meeting at
// the throat).
//
// Guards, in order of load-bearing-ness:
//   - sole trunk (trunkweld's rule verbatim): every rider on every touched
//     edge shares one trunk key. DeKalb carries local+express color trunks
//     and is untouchable; shared steel (Amtrak beside the LIRR) never
//     loses another operator's ribbon.
//   - level: the corridor's own ridden steel must sit at the region's
//     dominant level — the E/F run UNDER Sunnyside in tunnels, share one
//     trunk color, and must not surface into the yard.
//   - entrances: substitution only rewires through minted entrance nodes;
//     a boundary crossing that snaps to no entrance aborts its whole
//     component. No entrance pair reached, nothing changes.
// Every abort leaves the network exactly as it was — the legacy weave.

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
	"github.com/alexwohlbruck/portolan/internal/yards"
)

const (
	yspSampleM  = 20.0 // membership sampling pitch along an edge
	yspInterior = 0.8  // arc fraction inside → the edge is yard weave
	yspEntSnapM = 60.0 // boundary crossing → entrance snap radius
	yspBboxPadM = 50.0
)

// substituteYardSpines runs once, before the first refinement pass, so
// refine/bundle/foldRings all operate on the spine rather than the weave.
// Returns the number of weave edges replaced.
func substituteYardSpines(net *Network, paths []Path) int {
	regions := yardIx.Regions()
	if len(regions) == 0 {
		return 0
	}
	routeOf := map[string]gtfs.Route{}
	for _, pa := range paths {
		routeOf[pa.Pattern.Route.ID] = pa.Pattern.Route
	}
	trunkOf := func(rid string) string {
		r, ok := routeOf[rid]
		if !ok || !mode.Of(r.Type).Trunked() {
			return ""
		}
		return mode.TrunkKey(r)
	}
	// the whole edge must belong to one trunk — shared steel is never
	// deleted from under another operator's ribbon (trunkweld's rule)
	soleTrunk := func(e *Edge) string {
		t := ""
		for _, rid := range e.Routes {
			k := trunkOf(rid)
			if k == "" || (t != "" && k != t) {
				return ""
			}
			t = k
		}
		return t
	}

	replaced := 0
	for _, reg := range regions {
		if len(reg.Skel) == 0 || len(reg.Entrances) < 2 {
			continue
		}
		lo := geo.Pt{X: math.Inf(1), Y: math.Inf(1)}
		hi := geo.Pt{X: math.Inf(-1), Y: math.Inf(-1)}
		for _, p := range reg.Outline {
			lo.X, lo.Y = math.Min(lo.X, p.X), math.Min(lo.Y, p.Y)
			hi.X, hi.Y = math.Max(hi.X, p.X), math.Max(hi.Y, p.Y)
		}
		lo.X, lo.Y, hi.X, hi.Y = lo.X-yspBboxPadM, lo.Y-yspBboxPadM, hi.X+yspBboxPadM, hi.Y+yspBboxPadM

		// ---- candidate census over the pre-substitution edge list
		type cand struct {
			ei       int
			trunk    string
			interior bool
			line     *geo.Line
			inArc    float64 // first in-region sample arc (crossing edges)
			fromIn   bool    // which endpoint is inside
		}
		n0 := len(net.Edges)
		byTrunk := map[string][]cand{}
		for ei := 0; ei < n0; ei++ {
			e := &net.Edges[ei]
			if e.Gap || len(e.Pts) < 2 || len(e.Routes) == 0 {
				continue
			}
			inBox := false
			for _, p := range e.Pts {
				if p.X >= lo.X && p.X <= hi.X && p.Y >= lo.Y && p.Y <= hi.Y {
					inBox = true
					break
				}
			}
			if !inBox {
				continue
			}
			t := soleTrunk(e)
			if t == "" {
				continue
			}
			line := geo.NewLine(e.Pts)
			total := line.Len()
			if total < 1e-6 {
				continue
			}
			nSmp := int(math.Max(2, math.Round(total/yspSampleM)+1))
			in, lvlMiss, lvlHit := 0, 0, 0
			firstIn, lastIn := -1.0, -1.0
			for k := 0; k < nSmp; k++ {
				s := total * float64(k) / float64(nSmp-1)
				q := line.AtArc(s)
				if yardIx.RegionAt(q) != reg {
					continue
				}
				in++
				if firstIn < 0 {
					firstIn = s
				}
				lastIn = s
				if lvl, ok := levelAt(q, e.Routes); ok {
					if lvl == reg.Level {
						lvlHit++
					} else {
						lvlMiss++
					}
				}
			}
			if in == 0 || lvlMiss > lvlHit {
				continue // outside, or the corridor rides another level
			}
			fromIn := yardIx.RegionAt(net.Nodes[e.From].At) == reg
			toIn := yardIx.RegionAt(net.Nodes[e.To].At) == reg
			frac := float64(in) / float64(nSmp)
			switch {
			case frac >= yspInterior && fromIn && toIn:
				byTrunk[t] = append(byTrunk[t], cand{ei: ei, trunk: t, interior: true, line: line})
			case fromIn != toIn:
				// the boundary arc, seen walking From→To: entering keeps
				// the first in-region sample, leaving the last
				arc := firstIn
				if fromIn {
					arc = lastIn
				}
				byTrunk[t] = append(byTrunk[t], cand{ei: ei, trunk: t, line: line, inArc: arc, fromIn: fromIn})
			}
			// both endpoints outside with a dip through the region: left
			// untouched — pre-contraction edges are piece-sized and a
			// whole-region span in one edge is not a real yard crossing
		}
		if len(byTrunk) == 0 {
			continue
		}

		// skeleton adjacency for path-finding between entrances
		skAdj := make([][]int, len(reg.SkelNodes))
		for si, se := range reg.Skel {
			skAdj[se.A] = append(skAdj[se.A], si)
			skAdj[se.B] = append(skAdj[se.B], si)
		}
		entSkel := map[int]int{} // entrance idx → skel node idx
		for ni, n := range reg.SkelNodes {
			if n.Entrance >= 0 {
				entSkel[n.Entrance] = ni
			}
		}

		trunks := make([]string, 0, len(byTrunk))
		for t := range byTrunk {
			trunks = append(trunks, t)
		}
		sort.Strings(trunks)
		for _, t := range trunks {
			cands := byTrunk[t]
			// ---- connected components over shared network nodes
			parent := map[int]int{}
			var find func(int) int
			find = func(x int) int {
				if parent[x] != x {
					parent[x] = find(parent[x])
				}
				return parent[x]
			}
			for _, c := range cands {
				e := &net.Edges[c.ei]
				for _, n := range []int{e.From, e.To} {
					if _, ok := parent[n]; !ok {
						parent[n] = n
					}
				}
				parent[find(e.From)] = find(e.To)
			}
			compOf := map[int][]cand{} // root → members, in cand order
			for _, c := range cands {
				compOf[find(net.Edges[c.ei].From)] = append(compOf[find(net.Edges[c.ei].From)], c)
			}
			roots := make([]int, 0, len(compOf))
			for r := range compOf {
				roots = append(roots, r)
			}
			sort.Ints(roots)

			for _, root := range roots {
				comp := compOf[root]
				// entrance attachment per crossing edge
				type attach struct {
					c   cand
					ent int
				}
				var atts []attach
				ok := true
				entSet := map[int]bool{}
				for _, c := range comp {
					if c.interior {
						continue
					}
					cut := c.line.AtArc(c.inArc)
					best, bestD := -1, yspEntSnapM
					for eidx, ent := range reg.Entrances {
						if d := ent.Pt.Dist(cut); d < bestD {
							best, bestD = eidx, d
						}
					}
					if best < 0 {
						ok = false // unsnappable crossing: leave this component
						break
					}
					if _, has := entSkel[best]; !has {
						ok = false
						break
					}
					atts = append(atts, attach{c, best})
					entSet[best] = true
				}
				if !ok || len(entSet) < 2 {
					continue // mixed, unsnappable, or terminating-inside
				}
				ents := make([]int, 0, len(entSet))
				for e := range entSet {
					ents = append(ents, e)
				}
				sort.Ints(ents)
				// sub-skeleton: union of BFS paths E[0]→E[k]
				usedSkel := map[int]bool{}
				reach := true
				for _, dst := range ents[1:] {
					path := skelPath(reg, skAdj, entSkel[ents[0]], entSkel[dst])
					if path == nil {
						reach = false
						break
					}
					for _, si := range path {
						usedSkel[si] = true
					}
				}
				if !reach {
					continue
				}

				// ---- commit: mint nodes, emit skeleton edges, move riders
				nodeAt := map[int]int{} // skel node idx → network node
				mint := func(sn int) int {
					if id, has := nodeAt[sn]; has {
						return id
					}
					net.Nodes = append(net.Nodes, Node{At: reg.SkelNodes[sn].Pt})
					nodeAt[sn] = len(net.Nodes) - 1
					return nodeAt[sn]
				}
				skIDs := make([]int, 0, len(usedSkel))
				for si := range usedSkel {
					skIDs = append(skIDs, si)
				}
				sort.Ints(skIDs)
				newEdge := make([]int, len(skIDs)) // → net.Edges index
				for i, si := range skIDs {
					se := reg.Skel[si]
					net.Edges = append(net.Edges, Edge{
						From: mint(se.A), To: mint(se.B),
						Pts: append([]geo.Pt{}, se.Line.Pts...),
					})
					newEdge[i] = len(net.Edges) - 1
				}
				// riders: each replaced edge's midpoint votes for its
				// nearest skeleton run — branch weave lands on its branch
				assign := func(c cand, mid geo.Pt) {
					best, bestD := -1, math.Inf(1)
					for i, si := range skIDs {
						if d := reg.Skel[si].Line.DistTo(mid); d < bestD {
							best, bestD = i, d
						}
					}
					if best < 0 {
						return
					}
					dst := &net.Edges[newEdge[best]]
					src := &net.Edges[c.ei]
					dst.Routes = unionRoutes(dst.Routes, src.Routes)
					dst.Acts = mergeActMaps(dst.Acts, src.Acts)
				}
				for _, c := range comp {
					if c.interior {
						assign(c, c.line.AtArc(c.line.Len()/2))
					}
				}
				for _, a := range atts {
					// the crossing edge's riders vote from just inside the
					// boundary, so approach routes land on the skeleton
					// run they actually reach
					vote := a.c.inArc + 40
					if a.c.fromIn {
						vote = math.Max(0, a.c.inArc-40)
					}
					assign(a.c, a.c.line.AtArc(math.Min(a.c.line.Len(), vote)))
				}
				// a skeleton run between used ones that drew no vote still
				// carries the corridor — union everything rather than
				// leave a hole
				compRoutes := []string(nil)
				for _, c := range comp {
					compRoutes = unionRoutes(compRoutes, net.Edges[c.ei].Routes)
				}
				for _, ni := range newEdge {
					if len(net.Edges[ni].Routes) == 0 {
						net.Edges[ni].Routes = append([]string{}, compRoutes...)
					}
				}
				// rewire crossings: truncate at the entrance, weld exactly
				// onto the minted entrance node
				for _, a := range atts {
					e := &net.Edges[a.c.ei]
					entN := mint(entSkel[a.ent])
					pt := reg.Entrances[a.ent].Pt
					if a.c.fromIn {
						// inside → outside: keep the tail past the boundary
						e.Pts = append([]geo.Pt{pt}, subPtsAfter(a.c.line, a.c.inArc)...)
						if len(e.Pts) < 2 {
							e.Pts = []geo.Pt{pt, net.Nodes[e.To].At}
						}
						e.From = entN
					} else {
						e.Pts = append(subPtsBefore(a.c.line, a.c.inArc), pt)
						if len(e.Pts) < 2 {
							e.Pts = []geo.Pt{net.Nodes[e.From].At, pt}
						}
						e.To = entN
					}
				}
				// interior weave vanishes
				for _, c := range comp {
					if c.interior {
						e := &net.Edges[c.ei]
						e.Pts, e.Routes, e.Acts = nil, nil, nil
						replaced++
					}
				}
			}
		}
	}
	if replaced > 0 {
		rebuildAdj(net)
	}
	return replaced
}

// skelPath: BFS over skeleton edges from node a to node b, returning the
// edge index path (nil when unreachable). Deterministic: adjacency lists
// are built in edge order.
func skelPath(reg *yards.Region, adj [][]int, a, b int) []int {
	if a == b {
		return []int{}
	}
	prev := make([]int, len(reg.SkelNodes))
	for i := range prev {
		prev[i] = -1
	}
	seen := make([]bool, len(reg.SkelNodes))
	seen[a] = true
	queue := []int{a}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, si := range adj[n] {
			se := reg.Skel[si]
			nx := se.A + se.B - n
			if seen[nx] {
				continue
			}
			seen[nx] = true
			prev[nx] = si
			if nx == b {
				var path []int
				for cur := b; cur != a; {
					si := prev[cur]
					path = append(path, si)
					cur = reg.Skel[si].A + reg.Skel[si].B - cur
				}
				return path
			}
			queue = append(queue, nx)
		}
	}
	return nil
}

// subPtsBefore/After cut an edge polyline at an arc, original vertices
// kept on the surviving side.
func subPtsBefore(l *geo.Line, arc float64) []geo.Pt {
	var out []geo.Pt
	for i, a := range l.ArcTable() {
		if a < arc-1e-9 {
			out = append(out, l.Pts[i])
		}
	}
	return out
}

func subPtsAfter(l *geo.Line, arc float64) []geo.Pt {
	var out []geo.Pt
	for i, a := range l.ArcTable() {
		if a > arc+1e-9 {
			out = append(out, l.Pts[i])
		}
	}
	return out
}
