package bundle

import (
	"fmt"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Piece is a stretch of one strand over which its alongside-set is constant.
type Piece struct {
	Strand   int
	From, To float64
	State    []int // the STABLE alongside strand-set defining this piece
}

// Corridor is one edge of the bundle graph: a group of strand pieces that
// ride together, with the refined median-strand centerline. Nodes sit at its
// ends — exactly where membership changed, i.e. physical forks.
type Corridor struct {
	ID           int
	Members      []Piece
	Strands      []int
	Centerline   *geo.Line
	NodeA, NodeB int // index into Graph.Nodes; -1 = dangling (should not persist)
}

// Node is a fork/terminal of the bundle graph.
type Node struct {
	ID        int
	At        geo.Pt
	Corridors []int
}

// Graph is the bundle graph: the output of stage 3.
type Graph struct {
	Strands   []Strand
	Corridors []Corridor
	Nodes     []Node
}

// GraphParams govern corridor construction.
type GraphParams struct {
	Sound    SoundParams
	Center   Params
	MinState float64 // a new alongside-state must persist this long (m)
	NodeTol  float64 // connection points closer than this cluster into one node
	JoinTol  float64 // way-end chaining tolerance (m)
}

func DefaultGraphParams() GraphParams {
	return GraphParams{
		Sound:    DefaultSoundParams(),
		Center:   DefaultParams(),
		MinState: 60.0,
		NodeTol:  40.0,
		JoinTol:  1.0,
	}
}

// BuildGraph runs stages SOUND+BUNDLE at strand granularity: alongside-states
// per sample → hysteresis segmentation → pieces → corridors (union-find with
// lateral correspondence) → nodes at membership changes. No raster, no
// repairs; forks fall out of the data.
func BuildGraph(tracks []Track, p GraphParams) *Graph {
	strands := Chain(tracks, p.JoinTol)
	lines := make([]*geo.Line, len(strands))
	for i, s := range strands {
		lines[i] = s.Line
	}
	grid := geo.NewGrid(lines, 64)

	// ---- per-strand alongside states, sampled every SampleStep.
	// A neighbor is ALONGSIDE only if (a) it crosses the section within
	// [MinGap, MaxGap] with tight heading agreement, and (b) its OFFSET IS
	// STABLE over ±3 samples (~30 m): bundled tracks REMAIN together at
	// constant separation; a shallow crossing's offset sweeps through and
	// must never create a state (it welded the Culver F to the SBK freight
	// line through a 100 m convergence).
	sp := p.Sound
	type stateSeq struct {
		step   float64
		states []string // canonical key of sorted neighbor ids
		sets   [][]int
	}
	seqs := make([]stateSeq, len(strands))
	for si, s := range strands {
		total := s.Line.Len()
		n := int(total/sp.SampleStep) + 1
		if n < 2 {
			n = 2
		}
		step := total / float64(n-1)
		offs := make([]map[int]float64, n)
		for k := 0; k < n; k++ {
			arc := step * float64(k)
			pt := s.Line.AtArc(arc)
			tan := s.Line.TangentAtArc(arc, sp.SampleStep)
			m := map[int]float64{}
			grid.Near(pt, sp.MaxGap+2, func(oi int) {
				if oi == si {
					return
				}
				for _, c := range lines[oi].CrossSection(pt, tan, sp.MaxGap+2) {
					off := math.Abs(c.Offset)
					if off >= sp.MinGap && off <= sp.MaxGap && c.Parallel >= sp.MinParallel {
						m[oi] = c.Offset
						return
					}
				}
			})
			offs[k] = m
		}
		sq := stateSeq{step: step}
		w := int(math.Max(1, 30.0/step))
		for k := 0; k < n; k++ {
			var ids []int
			for oi, o := range offs[k] {
				ka, kb := k-w, k+w
				if ka < 0 {
					ka = 0
				}
				if kb >= n {
					kb = n - 1
				}
				oa, okA := offs[ka][oi]
				ob, okB := offs[kb][oi]
				if !okA || !okB {
					continue
				}
				if math.Abs(ob-oa) > 3.5 || math.Abs(o-oa) > 3.5 || math.Abs(ob-o) > 3.5 {
					continue // sweeping offset: a crossing, not a mate
				}
				ids = append(ids, oi)
			}
			sort.Ints(ids)
			sq.sets = append(sq.sets, ids)
			sq.states = append(sq.states, key(ids))
		}
		seqs[si] = sq
	}

	// ---- hysteresis segmentation: a state must persist MinState to count.
	// Own-cuts first (stable-state changes per strand)…
	ownCuts := make([][]float64, len(strands))
	for si := range strands {
		sq := seqs[si]
		minRun := int(math.Max(1, p.MinState/sq.step))
		n := len(sq.states)
		cur := 0
		k := 1
		for k < n {
			if sq.states[k] == sq.states[cur] {
				k++
				continue
			}
			cand := sq.states[k]
			hit, tot := 0, 0
			for j := k; j < n && j < k+minRun; j++ {
				tot++
				if sq.states[j] == cand {
					hit++
				}
			}
			if tot > 0 && float64(hit) >= 0.8*float64(tot) && (tot >= minRun || k+tot == n) {
				ownCuts[si] = append(ownCuts[si], sq.step*float64(k))
				cur = k
			}
			k++
		}
	}
	// …then FORK EVENTS PROPAGATE ACROSS THE BUNDLE: a cut (or a strand
	// terminal) is a fork for every strand alongside it — project it onto
	// each mate so piece boundaries align bundle-wide. Without this, cuts
	// stagger across parallel strands and transitive union CHAINS pieces
	// down the trunk (65 km member-arc groups with 10 km centerlines).
	induced := make([][]float64, len(strands))
	propagate := func(si int, arc float64) {
		pt := lines[si].AtArc(arc)
		sq := seqs[si]
		k := int(arc / sq.step)
		if k >= len(sq.sets) {
			k = len(sq.sets) - 1
		}
		mates := map[int]bool{}
		for _, oi := range sq.sets[k] {
			mates[oi] = true
		}
		if k > 0 {
			for _, oi := range sq.sets[k-1] {
				mates[oi] = true
			}
		}
		for oi := range mates {
			oarc, d := lines[oi].ProjectArc(pt)
			if d <= sp.MaxGap+2 {
				induced[oi] = append(induced[oi], oarc)
			}
		}
	}
	for si := range strands {
		for _, a := range ownCuts[si] {
			propagate(si, a)
		}
		propagate(si, 0)
		propagate(si, lines[si].Len())
	}
	// merge cut lists (dedupe within 30 m, keep off the ends)
	pieces := make([][]Piece, len(strands))
	for si := range strands {
		cuts := append(append([]float64(nil), ownCuts[si]...), induced[si]...)
		sort.Float64s(cuts)
		total := lines[si].Len()
		var merged []float64
		for _, a := range cuts {
			if a < 30 || a > total-30 {
				continue
			}
			if len(merged) > 0 && a-merged[len(merged)-1] < 30 {
				continue
			}
			merged = append(merged, a)
		}
		sq := seqs[si]
		// piece state = CONSENSUS over the piece's samples (≥60% presence).
		// A midpoint sample stamped a transient 100 m convergence onto a
		// whole 1.2 km piece and welded two different alignments — the kiss
		// rule (sustained parallelism) applies at the piece level too.
		stateAt := func(from, to float64) []int {
			k0 := int(from / sq.step)
			k1 := int(to / sq.step)
			if k1 >= len(sq.sets) {
				k1 = len(sq.sets) - 1
			}
			if k1 < k0 {
				k1 = k0
			}
			counts := map[int]int{}
			n := 0
			for k := k0; k <= k1; k++ {
				n++
				for _, oi := range sq.sets[k] {
					counts[oi]++
				}
			}
			var out []int
			for oi, c := range counts {
				if float64(c) >= 0.6*float64(n) {
					out = append(out, oi)
				}
			}
			sort.Ints(out)
			return out
		}
		from := 0.0
		for _, a := range merged {
			pieces[si] = append(pieces[si], Piece{Strand: si, From: from, To: a, State: stateAt(from, a)})
			from = a
		}
		pieces[si] = append(pieces[si], Piece{Strand: si, From: from, To: total, State: stateAt(from, total)})
	}

	// ---- unify pieces into corridors (union-find, lateral correspondence)
	var all []Piece
	idxOf := map[[2]int]int{} // (strand, pieceIdx) -> flat index
	for si, ps := range pieces {
		for pi, pc := range ps {
			idxOf[[2]int{si, pi}] = len(all)
			all = append(all, pc)
		}
	}
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	// piece lookup per strand by arc
	pieceAt := func(si int, arc float64) int {
		for pi, pc := range pieces[si] {
			if arc >= pc.From-1e-6 && arc <= pc.To+1e-6 {
				return idxOf[[2]int{si, pi}]
			}
		}
		return -1
	}
	// union via the pieces' STABLE states, with MUTUALITY: p unions q only
	// if q's strand is in p's stable set AND p's strand is in q's stable set.
	// Raw per-sample sets welded whole trunks through one kiss sample —
	// the kiss rule (LESSONS #5) gates unions, not just cuts.
	inState := func(pc Piece, strand int) bool {
		for _, s := range pc.State {
			if s == strand {
				return true
			}
		}
		return false
	}
	// SLIVERS (pieces shorter than MinState) are junction-throat fragments:
	// at the brief convergence of two distinct corridors they carry BOTH
	// pairs in their state and transitively weld the corridors. They never
	// drive or accept unions — they become node glue below.
	for fi, pc := range all {
		if pc.To-pc.From < p.MinState {
			continue
		}
		l := strands[pc.Strand].Line
		for _, frac := range []float64{0.25, 0.5, 0.75} {
			arc := pc.From + (pc.To-pc.From)*frac
			pt := l.AtArc(arc)
			for _, oi := range pc.State {
				oarc, d := lines[oi].ProjectArc(pt)
				if d > sp.MaxGap+2 {
					continue
				}
				qi := pieceAt(oi, oarc)
				if qi < 0 || all[qi].To-all[qi].From < p.MinState {
					continue
				}
				if inState(all[qi], pc.Strand) {
					union(fi, qi)
				}
			}
		}
	}
	groups := map[int][]int{}
	for fi := range all {
		groups[find(fi)] = append(groups[find(fi)], fi)
	}

	g := &Graph{Strands: strands}
	corridorOfPiece := make([]int, len(all))
	var roots []int
	for root := range groups {
		roots = append(roots, root)
	}
	sort.Ints(roots) // deterministic corridor ids
	for _, root := range roots {
		fis := groups[root]
		var members []Piece
		strandSet := map[int]bool{}
		for _, fi := range fis {
			pc := all[fi]
			if pc.To-pc.From < p.MinState {
				continue
			}
			members = append(members, pc)
			strandSet[pc.Strand] = true
		}
		if len(members) == 0 {
			for _, fi := range fis {
				corridorOfPiece[fi] = -1
			}
			continue
		}
		// per-strand RUNS: staggered cuts (≤MinState) chain pieces of the
		// same corridor along a trunk — merge each strand's intervals so the
		// spine covers the corridor's full extent, not one piece's
		runOf := map[int][2]float64{}
		for _, m := range members {
			r, ok := runOf[m.Strand]
			if !ok {
				runOf[m.Strand] = [2]float64{m.From, m.To}
				continue
			}
			r[0] = math.Min(r[0], m.From)
			r[1] = math.Max(r[1], m.To)
			runOf[m.Strand] = r
		}
		var memberLines []*geo.Line
		var spine *geo.Line
		for si, r := range runOf {
			sub := SubLine(strands[si].Line, r[0], r[1])
			memberLines = append(memberLines, sub)
			if spine == nil || sub.Len() > spine.Len() {
				spine = sub
			}
		}
		// extend the spine while other member runs continue past its ends —
		// a corridor whose track membership rotates along its length must
		// still get ONE full-extent centerline
		spine = extendSpine(spine, memberLines)
		cp := p.Center
		cp.ThroughFrac = 0 // members are fork-free pieces already; partial
		//                    runs along a chained trunk must still vote
		cl := Refine(spine, memberLines, cp)
		var strandIDs []int
		for si := range strandSet {
			strandIDs = append(strandIDs, si)
		}
		sort.Ints(strandIDs)
		cid := len(g.Corridors)
		g.Corridors = append(g.Corridors, Corridor{
			ID: cid, Members: members, Strands: strandIDs,
			Centerline: cl, NodeA: -1, NodeB: -1,
		})
		for _, fi := range fis {
			corridorOfPiece[fi] = cid
		}
	}

	// ---- nodes: connection points between consecutive REAL pieces of each
	// strand (slivers glue their neighbors together at one point), plus
	// strand terminals
	type conn struct {
		at   geo.Pt
		cids []int
	}
	var conns []conn
	for si, ps := range pieces {
		l := strands[si].Line
		type realPiece struct {
			cid      int
			from, to float64
		}
		var reals []realPiece
		for pi, pc := range ps {
			fi := idxOf[[2]int{si, pi}]
			if corridorOfPiece[fi] >= 0 && pc.To-pc.From >= p.MinState {
				reals = append(reals, realPiece{corridorOfPiece[fi], pc.From, pc.To})
			}
		}
		for i, rp := range reals {
			if i == 0 {
				conns = append(conns, conn{l.AtArc(rp.from), []int{rp.cid}})
			}
			if i+1 < len(reals) {
				next := reals[i+1]
				mid := (rp.to + next.from) / 2
				cs := []int{rp.cid}
				if next.cid != rp.cid {
					cs = append(cs, next.cid)
				}
				conns = append(conns, conn{l.AtArc(mid), cs})
			} else {
				conns = append(conns, conn{l.AtArc(rp.to), []int{rp.cid}})
			}
		}
	}
	// cluster connection points into nodes
	nparent := make([]int, len(conns))
	for i := range nparent {
		nparent[i] = i
	}
	var nfind func(int) int
	nfind = func(x int) int {
		if nparent[x] != x {
			nparent[x] = nfind(nparent[x])
		}
		return nparent[x]
	}
	cell := p.NodeTol
	buckets := map[[2]int][]int{}
	bkey := func(pt geo.Pt) [2]int {
		return [2]int{int(math.Floor(pt.X / cell)), int(math.Floor(pt.Y / cell))}
	}
	for i, c := range conns {
		k := bkey(c.at)
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, j := range buckets[[2]int{k[0] + dx, k[1] + dy}] {
					if conns[j].at.Dist(c.at) <= p.NodeTol {
						nparent[nfind(i)] = nfind(j)
					}
				}
			}
		}
		buckets[k] = append(buckets[k], i)
	}
	clusters := map[int][]int{}
	for i := range conns {
		clusters[nfind(i)] = append(clusters[nfind(i)], i)
	}
	var croots []int
	for r := range clusters {
		croots = append(croots, r)
	}
	sort.Ints(croots)
	nodeOfConn := make([]int, len(conns))
	for _, r := range croots {
		var sum geo.Pt
		cidSet := map[int]bool{}
		for _, i := range clusters[r] {
			sum = sum.Add(conns[i].at)
			for _, c := range conns[i].cids {
				cidSet[c] = true
			}
		}
		nid := len(g.Nodes)
		var cids []int
		for c := range cidSet {
			cids = append(cids, c)
		}
		sort.Ints(cids)
		g.Nodes = append(g.Nodes, Node{
			ID: nid,
			At: sum.Scale(1 / float64(len(clusters[r]))),
			Corridors: cids,
		})
		for _, i := range clusters[r] {
			nodeOfConn[i] = nid
		}
	}

	// ---- attach corridor ends to nodes (pass 1), re-place each node at the
	// MEET of its attached corridor ends (pass 2 — the conn-point centroid
	// is up to NodeTol off, and a tie absorbing 40 m in a 60 m junction
	// throat draws a hairpin), then tie ends in (pass 3)
	for ci := range g.Corridors {
		c := &g.Corridors[ci]
		endA := c.Centerline.Pts[0]
		endB := c.Centerline.Pts[len(c.Centerline.Pts)-1]
		c.NodeA = nearestNodeOf(g, ci, endA)
		c.NodeB = nearestNodeOf(g, ci, endB)
		if c.NodeA < 0 {
			c.NodeA = addNode(g, ci, endA)
		}
		if c.NodeB < 0 {
			c.NodeB = addNode(g, ci, endB)
		}
		if c.NodeA == c.NodeB && endA.Dist(endB) > 0.5*c.Centerline.Len() {
			// a STRAIGHT terminal stub whose ends clustered into one node —
			// tying both ends there closes it into a 180° hairpin blob.
			// Only true rings (balloon loops: endgap << length) share a
			// node; a stub keeps its far end free.
			if endB.Dist(g.Nodes[c.NodeA].At) > endA.Dist(g.Nodes[c.NodeA].At) {
				c.NodeB = addNode(g, ci, endB)
			} else {
				c.NodeA = addNode(g, ci, endA)
			}
		}
	}
	sums := make([]geo.Pt, len(g.Nodes))
	counts := make([]int, len(g.Nodes))
	for ci := range g.Corridors {
		c := &g.Corridors[ci]
		endA := c.Centerline.Pts[0]
		endB := c.Centerline.Pts[len(c.Centerline.Pts)-1]
		sums[c.NodeA] = sums[c.NodeA].Add(endA)
		counts[c.NodeA]++
		sums[c.NodeB] = sums[c.NodeB].Add(endB)
		counts[c.NodeB]++
	}
	for ni := range g.Nodes {
		if counts[ni] > 0 {
			g.Nodes[ni].At = sums[ni].Scale(1 / float64(counts[ni]))
		}
	}
	for ci := range g.Corridors {
		c := &g.Corridors[ci]
		c.Centerline = TieEnds(c.Centerline, g.Nodes[c.NodeA].At, g.Nodes[c.NodeB].At)
	}
	// nodes' corridor lists may have grown; dedupe + drop empty nodes
	rebuildNodeIncidence(g)
	return g
}

// extendSpine greedily appends member runs that continue past either spine
// end, so the spine covers the whole corridor extent. Strand orientation is
// arbitrary — every extension must agree with the spine end's TANGENT (an
// anti-oriented member would fold the spine back on itself). Each member
// extends each end at most once.
func extendSpine(spine *geo.Line, members []*geo.Line) *geo.Line {
	const joinReach = 25.0
	used := map[*geo.Line]bool{}
	for guard := 0; guard < 40; guard++ {
		extended := false
		for _, m := range members {
			if m == spine || used[m] {
				continue
			}
			for _, tail := range []bool{true, false} {
				var end geo.Pt
				var etan geo.Pt
				if tail {
					end = spine.Pts[len(spine.Pts)-1]
					etan = spine.TangentAtArc(spine.Len(), 15)
				} else {
					end = spine.Pts[0]
					etan = spine.TangentAtArc(0, 15).Scale(-1) // outward
				}
				arc, d := m.ProjectArc(end)
				if d > joinReach {
					continue
				}
				mtan := m.TangentAtArc(arc, 15)
				var ext *geo.Line
				switch {
				case mtan.Dot(etan) > 0.5 && m.Len()-arc > 60:
					ext = SubLine(m, arc, m.Len())
				case mtan.Dot(etan) < -0.5 && arc > 60:
					ext = geo.NewLine(reversed(SubLine(m, 0, arc).Pts))
				default:
					continue
				}
				// an "extension" that runs ALONGSIDE the spine is the
				// opposite leg of a balloon loop folding back — refining
				// both passes onto the midline draws a 180° hairpin
				if spine.DistTo(ext.AtArc(ext.Len()/2)) < 14 {
					continue
				}
				if tail {
					spine = geo.NewLine(append(append([]geo.Pt(nil), spine.Pts...), ext.Pts[1:]...))
				} else {
					// ext currently runs outward from the head; prepend reversed
					rev := reversed(ext.Pts)
					spine = geo.NewLine(append(append([]geo.Pt(nil), rev[:len(rev)-1]...), spine.Pts...))
				}
				used[m] = true
				extended = true
				break
			}
		}
		if !extended {
			break
		}
	}
	return spine
}

func key(ids []int) string {
	s := ""
	for _, id := range ids {
		s += fmt.Sprintf("%d,", id)
	}
	return s
}

func nearestNodeOf(g *Graph, cid int, at geo.Pt) int {
	best, bestD := -1, math.Inf(1)
	for _, n := range g.Nodes {
		refs := false
		for _, c := range n.Corridors {
			if c == cid {
				refs = true
				break
			}
		}
		if !refs {
			continue
		}
		if d := n.At.Dist(at); d < bestD {
			best, bestD = n.ID, d
		}
	}
	if bestD > 120 { // node cluster too far to be this end's fork
		return -1
	}
	return best
}

func addNode(g *Graph, cid int, at geo.Pt) int {
	nid := len(g.Nodes)
	g.Nodes = append(g.Nodes, Node{ID: nid, At: at, Corridors: []int{cid}})
	return nid
}

func rebuildNodeIncidence(g *Graph) {
	for i := range g.Nodes {
		g.Nodes[i].Corridors = nil
	}
	for ci, c := range g.Corridors {
		if c.NodeA >= 0 {
			g.Nodes[c.NodeA].Corridors = append(g.Nodes[c.NodeA].Corridors, ci)
		}
		if c.NodeB >= 0 && c.NodeB != c.NodeA {
			g.Nodes[c.NodeB].Corridors = append(g.Nodes[c.NodeB].Corridors, ci)
		}
	}
}

// TieEnds eases a centerline's ends into node positions over a window scaled
// to the offset absorbed (~5 m of run per meter, cosine ramp, min 40 m) —
// LESSONS #6: fixed windows turn node offsets into seam kinks.
func TieEnds(l *geo.Line, na, nb geo.Pt) *geo.Line {
	pts := append([]geo.Pt(nil), l.Densify(8)...)
	n := len(pts)
	if n < 3 {
		return geo.NewLine([]geo.Pt{na, nb})
	}
	arc := make([]float64, n)
	for i := 1; i < n; i++ {
		arc[i] = arc[i-1] + pts[i].Dist(pts[i-1])
	}
	total := arc[n-1]
	apply := func(delta geo.Pt, fromEnd bool) {
		off := delta.Norm()
		if off < 0.01 {
			return
		}
		win := math.Min(math.Max(40, 5*off), total/3)
		for i := 0; i < n; i++ {
			s := arc[i]
			if fromEnd {
				s = total - arc[i]
			}
			if s >= win {
				continue
			}
			f := 0.5 * (1 + math.Cos(math.Pi*s/win))
			pts[i] = pts[i].Add(delta.Scale(f))
		}
	}
	apply(na.Sub(pts[0]), false)
	apply(nb.Sub(pts[n-1]), true)
	pts[0], pts[n-1] = na, nb
	return geo.NewLine(pts)
}
