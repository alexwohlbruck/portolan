package geo

import (
	"math"
)

// lineIndex is a lazy spatial hash over one Line's segments. Lines are
// immutable after NewLine (nothing in the pipeline assigns Pts or arc
// afterwards), so the index is built once, on first capped query, and
// shared by every goroutine touching the line.
//
// Capped queries answer distance questions EXACTLY as the full-scan
// primitives do, provided the consumed answer is within the cap: a segment
// with a point within reach of p is registered in the cell holding that
// point (bbox rasterization covers every cell the segment enters), and
// that cell is within ceil(reach/cell) of p's cell per axis. Every cap the
// pipeline uses is ≤ the cell size, so the scan is the 3×3 neighborhood —
// which is precomputed per cell as one sorted, deduplicated segment list:
// a query is a single map lookup.
type lineIndex struct {
	cell float64
	hood map[[2]int][]int32 // cell → ascending segment indices of its 3×3 block
}

const lineIndexCell = 32.0

// index returns the line's segment index, building it on first use.
func (l *Line) index() *lineIndex {
	l.idxOnce.Do(func() {
		cells := map[[2]int][]int32{}
		key := func(p Pt) [2]int {
			return [2]int{int(math.Floor(p.X / lineIndexCell)), int(math.Floor(p.Y / lineIndexCell))}
		}
		for i := 1; i < len(l.Pts); i++ {
			ka, kb := key(l.Pts[i-1]), key(l.Pts[i])
			x0, x1 := min(ka[0], kb[0]), max(ka[0], kb[0])
			y0, y1 := min(ka[1], kb[1]), max(ka[1], kb[1])
			for x := x0; x <= x1; x++ {
				for y := y0; y <= y1; y++ {
					cells[[2]int{x, y}] = append(cells[[2]int{x, y}], int32(i))
				}
			}
		}
		// hood covers the DILATED cell set: a query may sit in a cell the
		// line never enters while segments occupy a neighbor
		keys := map[[2]int]struct{}{}
		for c := range cells {
			for dx := -1; dx <= 1; dx++ {
				for dy := -1; dy <= 1; dy++ {
					keys[[2]int{c[0] + dx, c[1] + dy}] = struct{}{}
				}
			}
		}
		ix := &lineIndex{cell: lineIndexCell, hood: make(map[[2]int][]int32, len(keys))}
		var buf []int32
		for c := range keys {
			buf = buf[:0]
			for dx := -1; dx <= 1; dx++ {
				for dy := -1; dy <= 1; dy++ {
					buf = append(buf, cells[[2]int{c[0] + dx, c[1] + dy}]...)
				}
			}
			// sort + dedup once at build; queries reuse the list as-is
			for i := 1; i < len(buf); i++ {
				for j := i; j > 0 && buf[j] < buf[j-1]; j-- {
					buf[j], buf[j-1] = buf[j-1], buf[j]
				}
			}
			out := make([]int32, 0, len(buf))
			for i, s := range buf {
				if i == 0 || s != buf[i-1] {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				ix.hood[c] = out
			}
		}
		l.idx = ix
	})
	return l.idx
}

// allSegs lists every segment index, ascending — the exact full-scan
// order, used when a reach exceeds the cell size (dial overrides only;
// no built-in cap does).
func (l *Line) allSegs() []int32 {
	out := make([]int32, len(l.Pts)-1)
	for i := range out {
		out[i] = int32(i + 1)
	}
	return out
}

// candidates returns deduplicated, ascending segment indices — a superset
// of every segment within reach of p when reach ≤ the cell size (every
// built-in cap is; a dial pushing one beyond falls back to the full list).
// Ascending order matches the full scan's segment order, so min/tie
// selection resolves identically. The returned slice is shared and must
// not be modified.
func (l *Line) candidates(p Pt, reach float64) []int32 {
	ix := l.index()
	if reach > ix.cell {
		return l.allSegs()
	}
	return ix.hood[[2]int{int(math.Floor(p.X / ix.cell)), int(math.Floor(p.Y / ix.cell))}]
}

// Within reports DistTo(p) < reach, bit-identically, touching only local
// segments: DistTo(p) < reach iff some segment has segDist < reach, and
// any such segment is among the candidates. Each candidate is tested with
// the banded squared comparison (exact compare on the knife edge only).
func (l *Line) Within(p Pt, reach float64) bool {
	if len(l.Pts) < 2 {
		return false
	}
	for _, seg := range l.candidates(p, reach) {
		if segWithinStrict(p, l.Pts[seg-1], l.Pts[seg], reach) {
			return true
		}
	}
	return false
}

// WithinLE is Within for the non-strict comparison DistTo(p) <= reach.
func (l *Line) WithinLE(p Pt, reach float64) bool {
	if len(l.Pts) < 2 {
		return false
	}
	for _, seg := range l.candidates(p, reach) {
		if segWithin(p, l.Pts[seg-1], l.Pts[seg], reach) {
			return true
		}
	}
	return false
}

// DistToCapped returns (DistTo(p), true) when DistTo(p) < cap, else
// (+Inf, false). When it reports true the value is bit-identical to
// DistTo's: the global argmin segment is within cap of p, hence among the
// candidates, and the same squared-argmin + winner-Hypot selection runs
// over the candidates in the same segment order.
func (l *Line) DistToCapped(p Pt, cap float64) (float64, bool) {
	if len(l.Pts) < 2 {
		return math.Inf(1), false
	}
	best2 := math.Inf(1)
	var bdx, bdy float64
	for _, seg := range l.candidates(p, cap) {
		d2, dx, dy := segDist2(p, l.Pts[seg-1], l.Pts[seg])
		if d2 < best2 {
			best2, bdx, bdy = d2, dx, dy
		}
	}
	if math.IsInf(best2, 1) {
		return math.Inf(1), false
	}
	d := math.Hypot(bdx, bdy)
	if d >= cap {
		return math.Inf(1), false
	}
	return d, true
}

// ProjectArcCapped returns ProjectArc(p) when the projection distance is
// < cap (ok=true) — bit-identical by the same argmin-restriction argument —
// and ok=false when the true distance is ≥ cap.
func (l *Line) ProjectArcCapped(p Pt, cap float64) (arc, d float64, ok bool) {
	if len(l.Pts) < 2 {
		return 0, math.Inf(1), false
	}
	best2 := math.Inf(1)
	bestI := -1
	var bestQ Pt
	var bdx, bdy float64
	for _, s := range l.candidates(p, cap) {
		i := int(s)
		a, b := l.Pts[i-1], l.Pts[i]
		ab := b.Sub(a)
		n2 := ab.Dot(ab)
		t := 0.0
		if n2 > 1e-18 {
			t = math.Max(0, math.Min(1, p.Sub(a).Dot(ab)/n2))
		}
		q := a.Add(ab.Scale(t))
		dx, dy := p.X-q.X, p.Y-q.Y
		if d2 := dx*dx + dy*dy; d2 < best2 {
			best2, bestI, bestQ = d2, i, q
			bdx, bdy = dx, dy
		}
	}
	if bestI < 0 {
		return 0, math.Inf(1), false
	}
	d = math.Hypot(bdx, bdy)
	if d >= cap {
		return 0, math.Inf(1), false
	}
	return l.arc[bestI-1] + l.Pts[bestI-1].Dist(bestQ), d, true
}

// CrossSectionNear returns CrossSection(p, tangent, reach) — identical
// slice, identical order — touching only local segments. Any accepted
// crossing lies on the ±reach section segment through p (within the
// intersection tolerance), so its member segment passes within
// reach + 1e-6 of p and is among the candidates; candidates run in
// ascending segment order, matching the full scan.
func (l *Line) CrossSectionNear(p, tangent Pt, reach float64) []Crossing {
	if len(l.Pts) < 2 {
		return nil
	}
	nrm := tangent.Perp()
	r1 := p.Sub(nrm.Scale(reach))
	r2 := p.Add(nrm.Scale(reach))
	var out []Crossing
	for _, s := range l.candidates(p, reach+1e-6) {
		i := int(s)
		q, ok := SegIntersect(r1, r2, l.Pts[i-1], l.Pts[i])
		if ok {
			dir := l.Pts[i].Sub(l.Pts[i-1]).Unit()
			out = append(out, Crossing{
				Offset:   q.Sub(p).Dot(nrm),
				Parallel: math.Abs(dir.Dot(tangent)),
				At:       q,
				Arc:      l.arc[i-1] + l.Pts[i-1].Dist(q),
			})
		}
	}
	return out
}
