package yards

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

const (
	outlinePadM = 6.0   // stand-off from the outermost track — "a couple of metres"
	msGridM     = 6.0   // marching-squares field pitch — boundary smoothness
	simplifyM   = 1.0   // ring vertex thinning tolerance
	closeM      = 24.0  // morphological closing radius — gaps under ~2× bridge
	hullTailM   = 30.0  // hot-span end taper on hull input steel
	hullFillM   = 200.0 // a way's mid-span score dips fill through
)

// hullOutline returns the padded offset contour of the region's hot
// member steel: the iso-line of distance-to-steel at outlinePadM, traced
// by marching squares over a fine field. This is the union-of-buffers
// boundary — it hugs the outer tracks with a tad of padding instead of
// the blocky cell staircase, which is what the region footprint MEANS.
// Largest loop wins (interior courtyards are dropped), CCW, thinned.
func hullOutline(steel []*geo.Line, pad float64) []geo.Pt {
	if len(steel) == 0 {
		return nil
	}
	lo := geo.Pt{X: math.Inf(1), Y: math.Inf(1)}
	hi := geo.Pt{X: math.Inf(-1), Y: math.Inf(-1)}
	for _, l := range steel {
		for _, p := range l.Pts {
			lo.X, lo.Y = math.Min(lo.X, p.X), math.Min(lo.Y, p.Y)
			hi.X, hi.Y = math.Max(hi.X, p.X), math.Max(hi.Y, p.Y)
		}
	}
	margin := pad + closeM + 2*msGridM // the closing dilation must never touch the array border
	x0, y0 := lo.X-margin, lo.Y-margin
	nx := int(math.Ceil((hi.X-lo.X+2*margin)/msGridM)) + 1
	ny := int(math.Ceil((hi.Y-lo.Y+2*margin)/msGridM)) + 1
	if nx < 2 || ny < 2 {
		return nil
	}
	// Signed field: distance-to-steel minus pad; negative = inside.
	// STAMPED, not queried: walking the steel at 3 m and writing min
	// distance into the surrounding grid window is union-of-buffers
	// exactly (disc scalloping at 3 m spacing is <8 cm), and it never
	// scans a dense ladder's segment lists — the per-point NearestDist
	// version took minutes on the Kearny complex alone.
	reach := pad + closeM + 2*msGridM
	f := make([]float64, nx*ny)
	for i := range f {
		f[i] = 1e9
	}
	win := int(math.Ceil(reach / msGridM))
	const stampStep = 3.0
	for _, l := range steel {
		total := l.Len()
		for s := 0.0; ; s += stampStep {
			if s > total {
				s = total
			}
			p := l.AtArc(s)
			ci := int(math.Round((p.X - x0) / msGridM))
			cj := int(math.Round((p.Y - y0) / msGridM))
			for j := max(0, cj-win); j <= min(ny-1, cj+win); j++ {
				for i := max(0, ci-win); i <= min(nx-1, ci+win); i++ {
					g := geo.Pt{X: x0 + float64(i)*msGridM, Y: y0 + float64(j)*msGridM}
					if d := g.Dist(p); d < f[j*nx+i] {
						f[j*nx+i] = d
					}
				}
			}
			if s >= total {
				break
			}
		}
	}
	// Morphological CLOSING: contouring raw distance at pad weaves the
	// boundary between tracks wherever coverage dips — the owner's "the
	// shape is fitted too well". Dilate to pad+closeM, then erode by
	// closeM: gaps under ~2×closeM bridge, concave pinches round off, and
	// the outer boundary still sits ~pad from the outermost steel. The
	// erosion is a two-pass chamfer distance from the dilated mask's
	// complement.
	dist2 := make([]float64, nx*ny)
	for i := range dist2 {
		if f[i] <= pad+closeM {
			dist2[i] = 1e9 // inside the dilated mask: distance unknown yet
		}
	}
	w1, w2 := msGridM, msGridM*math.Sqrt2
	relax := func(i, j, di, dj int, w float64) {
		ni, nj := i+di, j+dj
		if ni < 0 || ni >= nx || nj < 0 || nj >= ny {
			return
		}
		if d := dist2[nj*nx+ni] + w; d < dist2[j*nx+i] {
			dist2[j*nx+i] = d
		}
	}
	for j := 0; j < ny; j++ {
		for i := 0; i < nx; i++ {
			relax(i, j, -1, 0, w1)
			relax(i, j, 0, -1, w1)
			relax(i, j, -1, -1, w2)
			relax(i, j, 1, -1, w2)
		}
	}
	for j := ny - 1; j >= 0; j-- {
		for i := nx - 1; i >= 0; i-- {
			relax(i, j, 1, 0, w1)
			relax(i, j, 0, 1, w1)
			relax(i, j, 1, 1, w2)
			relax(i, j, -1, 1, w2)
		}
	}
	for i := range f {
		// The centimetre offset is load-bearing: chamfer distances are
		// sums of grid steps, so an iso value that is an exact multiple
		// of msGridM runs THROUGH grid corners along straight stretches —
		// four cells then share one quantized segment endpoint, the
		// chaining map drops the duplicates, and the loop shatters (a
		// bottom sliver once won "largest loop" over half a ladder).
		v := closeM + 0.01 - dist2[i] // negative = survives the erosion
		if v == 0 {
			v = 1e-9
		}
		f[i] = v
	}

	// Marching squares, inside (v<0) kept on the LEFT so outer loops come
	// out CCW. Corners per cell: A=bl B=br C=tr D=tl.
	type seg struct{ a, b geo.Pt }
	var segs []seg
	at := func(i, j int) geo.Pt {
		return geo.Pt{X: x0 + float64(i)*msGridM, Y: y0 + float64(j)*msGridM}
	}
	interp := func(pa, pb geo.Pt, fa, fb float64) geo.Pt {
		t := fa / (fa - fb)
		return geo.Lerp(pa, pb, t)
	}
	for j := 0; j < ny-1; j++ {
		for i := 0; i < nx-1; i++ {
			fa, fb := f[j*nx+i], f[j*nx+i+1]
			fc, fd := f[(j+1)*nx+i+1], f[(j+1)*nx+i]
			code := 0
			if fa < 0 {
				code |= 1
			}
			if fb < 0 {
				code |= 2
			}
			if fc < 0 {
				code |= 4
			}
			if fd < 0 {
				code |= 8
			}
			if code == 0 || code == 15 {
				continue
			}
			A, B, C, D := at(i, j), at(i+1, j), at(i+1, j+1), at(i, j+1)
			pS := func() geo.Pt { return interp(A, B, fa, fb) }
			pE := func() geo.Pt { return interp(B, C, fb, fc) }
			pN := func() geo.Pt { return interp(D, C, fd, fc) }
			pW := func() geo.Pt { return interp(A, D, fa, fd) }
			emit := func(a, b geo.Pt) { segs = append(segs, seg{a, b}) }
			switch code {
			case 1: // A
				emit(pS(), pW())
			case 2: // B
				emit(pE(), pS())
			case 4: // C
				emit(pN(), pE())
			case 8: // D
				emit(pW(), pN())
			case 3: // A+B
				emit(pE(), pW())
			case 6: // B+C
				emit(pN(), pS())
			case 12: // C+D
				emit(pW(), pE())
			case 9: // D+A
				emit(pS(), pN())
			case 14: // all but A
				emit(pW(), pS())
			case 13: // all but B
				emit(pS(), pE())
			case 11: // all but C
				emit(pE(), pN())
			case 7: // all but D
				emit(pN(), pW())
			case 5: // A+C — ambiguous, resolve by center
				if fa+fb+fc+fd < 0 {
					emit(pS(), pE())
					emit(pN(), pW())
				} else {
					emit(pS(), pW())
					emit(pN(), pE())
				}
			case 10: // B+D
				if fa+fb+fc+fd < 0 {
					emit(pW(), pS())
					emit(pE(), pN())
				} else {
					emit(pE(), pS())
					emit(pW(), pN())
				}
			}
		}
	}
	if len(segs) == 0 {
		return nil
	}

	// Chain segments into loops via mm-quantized endpoints (shared cell
	// edges compute identical floats, so keys match exactly).
	qk := func(p geo.Pt) [2]int64 {
		return [2]int64{int64(math.Round(p.X * 1000)), int64(math.Round(p.Y * 1000))}
	}
	next := map[[2]int64]int{}
	used := make([]bool, len(segs))
	for si, s := range segs {
		if _, dup := next[qk(s.a)]; !dup {
			next[qk(s.a)] = si
		}
	}
	var best []geo.Pt
	bestArea := 0.0
	for si := range segs {
		if used[si] {
			continue
		}
		var loop []geo.Pt
		cur := si
		for !used[cur] {
			used[cur] = true
			loop = append(loop, segs[cur].a)
			nx, ok := next[qk(segs[cur].b)]
			if !ok {
				break
			}
			cur = nx
		}
		if len(loop) < 3 {
			continue
		}
		area := 0.0
		for i, p := range loop {
			q := loop[(i+1)%len(loop)]
			area += p.X*q.Y - q.X*p.Y
		}
		area /= 2
		if area > bestArea {
			bestArea, best = area, loop
		}
	}
	return thinRing(best, simplifyM)
}


// thinRing drops vertices lying within tol of the chord that would
// replace them — marching squares emits one vertex per 6 m cell and a
// straight yard edge doesn't need 80 of them.
//
// EVERY skipped vertex is checked against the chord, not just the most
// recently kept one. The version that checked only the last kept vertex
// could peel back an entire excursion whenever the running chord happened
// to pass near it: one Bronx outline came out of a clean 736-vertex
// contour with a 925 m edge cutting straight across the yard, and 7% of
// all ring length across NYC sat more than 54 m from any member track.
// The contour was never the problem; the simplification was.
func thinRing(ring []geo.Pt, tol float64) []geo.Pt {
	n := len(ring)
	if n < 5 {
		return ring
	}
	out := make([]geo.Pt, 0, n/2)
	out = append(out, ring[0])
	for i := 0; i < n-1; {
		last := i + 1
		for j := i + 2; j < n && chordHolds(ring[i:j+1], tol); j++ {
			last = j
		}
		out = append(out, ring[last])
		i = last
	}
	return out
}

// chordHolds reports whether every interior vertex of seg lies within tol
// of the straight chord from its first point to its last.
func chordHolds(seg []geo.Pt, tol float64) bool {
	a, b := seg[0], seg[len(seg)-1]
	ab := b.Sub(a)
	n2 := ab.Dot(ab)
	for _, p := range seg[1 : len(seg)-1] {
		d := p.Dist(a)
		if n2 > 1e-18 {
			t := math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/n2))
			d = p.Sub(a.Add(ab.Scale(t))).Norm()
		}
		if d >= tol {
			return false
		}
	}
	return true
}

// expandSteel re-spans each member way to the full arc intervals it
// spends INSIDE the ring, keeping any interval that carries hot samples.
// The hull rebuilt from these covers each track's whole passage through
// the yard, so the boundary can only cut a track where the track truly
// exits — no clipped steel, no false mid-yard entrances.
func expandSteel(tracks []Track, tis []int, arcs map[int][]float64, ring []geo.Pt) []*geo.Line {
	var out []*geo.Line
	for _, ti := range tis {
		l := tracks[ti].Line
		total := l.Len()
		if total < 1e-9 {
			continue
		}
		hot := arcs[ti]
		if len(hot) == 0 {
			continue
		}
		step := cellM / 2
		type span struct{ a, b float64 }
		var spans []span
		open := -1.0
		prev := 0.0
		for s := 0.0; ; s += step {
			if s > total {
				s = total
			}
			in := pointInRing(ring, l.AtArc(s))
			if in && open < 0 {
				open = s
			}
			if !in && open >= 0 {
				spans = append(spans, span{open, prev})
				open = -1
			}
			prev = s
			if s >= total {
				break
			}
		}
		if open >= 0 {
			spans = append(spans, span{open, total})
		}
		for _, sp := range spans {
			carries := false
			for _, h := range hot {
				if h >= sp.a-step && h <= sp.b+step {
					carries = true
					break
				}
			}
			if !carries {
				continue
			}
			// the taper keeps the way's own end just inside the shape
			a := math.Max(0, sp.a-hullTailM)
			b := math.Min(total, sp.b+hullTailM)
			if pts := subPtsRange(l, a, b); len(pts) >= 2 {
				out = append(out, geo.NewLine(pts))
			}
		}
	}
	return out
}

// memberSteel is the region's own track: every member way clipped to the
// arc intervals it spends inside the ring. Unlike expandSteel it asks for
// no hot samples and adds no tail — this is "what track is in this yard",
// which is what the overlay draws and what a centerline algorithm has to
// walk.
//
// It is deliberately NOT derived from the entrance walk. That walk is
// level-gated (a subway under a surface yard is not in it), so a region
// whose members sit at mixed effective levels drew almost none of its own
// track: 7% of all NYC ring length had no yard track within 54 m of it,
// which read as the hull bulging into empty space when the hull was in
// fact 25 m from real member steel the whole way round.
func memberSteel(tracks []Track, tis []int, ring []geo.Pt) []*geo.Line {
	var out []*geo.Line
	step := cellM / 2
	for _, ti := range tis {
		l := tracks[ti].Line
		total := l.Len()
		if total < 1e-9 {
			continue
		}
		open := -1.0
		prev := 0.0
		for s := 0.0; ; s += step {
			if s > total {
				s = total
			}
			in := pointInRing(ring, l.AtArc(s))
			if in && open < 0 {
				open = s
			}
			if !in && open >= 0 {
				if pts := subPtsRange(l, open, prev); len(pts) >= 2 {
					out = append(out, geo.NewLine(pts))
				}
				open = -1
			}
			prev = s
			if s >= total {
				break
			}
		}
		if open >= 0 {
			if pts := subPtsRange(l, open, total); len(pts) >= 2 {
				out = append(out, geo.NewLine(pts))
			}
		}
	}
	return out
}

// subPtsRange is subPts without importing the spine file's helper name
// into the hull's vocabulary — vertices between the arcs, exact ends.
func subPtsRange(l *geo.Line, a0, a1 float64) []geo.Pt {
	pts := []geo.Pt{l.AtArc(a0)}
	for i, arc := range l.ArcTable() {
		if arc > a0+1e-9 && arc < a1-1e-9 {
			pts = append(pts, l.Pts[i])
		}
	}
	return append(pts, l.AtArc(a1))
}

// pointInRing is even-odd ray casting (last→first edge implied).
func pointInRing(ring []geo.Pt, p geo.Pt) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		a, b := ring[j], ring[i]
		if (a.Y > p.Y) != (b.Y > p.Y) {
			if a.X+(p.Y-a.Y)*(b.X-a.X)/(b.Y-a.Y) > p.X {
				in = !in
			}
		}
	}
	return in
}

// classifyCells splits the plane under a ring into interior cells (every
// point inside — one map hit answers InYard) and boundary cells (the ring
// passes through — queries pay an exact point-in-ring test). Boundary
// cells come from walking the ring; the interior floods 4-connected from
// the seed cells, never crossing a boundary cell — and a flood can only
// leak outside by crossing the ring, whose cells are all marked.
func classifyCells(ring []geo.Pt, seeds [][2]int) (interior, boundary map[[2]int]bool) {
	boundary = map[[2]int]bool{}
	n := len(ring)
	clo, chi := [2]int{1 << 30, 1 << 30}, [2]int{-(1 << 30), -(1 << 30)}
	for i := 0; i < n; i++ {
		a, b := ring[i], ring[(i+1)%n]
		steps := int(b.Sub(a).Norm()/(cellM/4)) + 1
		for k := 0; k <= steps; k++ {
			c := cellKey(geo.Lerp(a, b, float64(k)/float64(steps)))
			boundary[c] = true
			clo[0], clo[1] = min(clo[0], c[0]), min(clo[1], c[1])
			chi[0], chi[1] = max(chi[0], c[0]), max(chi[1], c[1])
		}
	}
	interior = map[[2]int]bool{}
	var stack [][2]int
	for _, c := range seeds {
		// A region whose steel splits into detached contour blobs keeps
		// only the largest loop — seeds from the other blobs sit OUTSIDE
		// the ring, and an unvalidated seed once flooded the plane.
		if boundary[c] || interior[c] {
			continue
		}
		center := geo.Pt{X: (float64(c[0]) + 0.5) * cellM, Y: (float64(c[1]) + 0.5) * cellM}
		if !pointInRing(ring, center) {
			continue
		}
		interior[c] = true
		stack = append(stack, c)
	}
	dirs := [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range dirs {
			nc := [2]int{c[0] + d[0], c[1] + d[1]}
			// the bbox bound makes a boundary gap a local misread, never
			// a plane flood
			if nc[0] < clo[0] || nc[0] > chi[0] || nc[1] < clo[1] || nc[1] > chi[1] {
				continue
			}
			if !boundary[nc] && !interior[nc] {
				interior[nc] = true
				stack = append(stack, nc)
			}
		}
	}
	return interior, boundary
}

// hullSeeds: cells under the steel itself — every steel point is inside
// the pad contour by construction, so these are safe flood seeds.
func hullSeeds(steel []*geo.Line) [][2]int {
	set := map[[2]int]bool{}
	for _, l := range steel {
		total := l.Len()
		for s := 0.0; ; s += cellM / 2 {
			if s > total {
				s = total
			}
			set[cellKey(l.AtArc(s))] = true
			if s >= total {
				break
			}
		}
	}
	out := make([][2]int, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
