package geo

import (
	"math"
	"sync"
)

// lineIndex is a lazy uniform spatial hash over one Line's segments. Lines
// are immutable after NewLine (nothing in the pipeline assigns Pts or arc
// afterwards), so the index is built once, on first capped query, and
// shared by every goroutine touching the line.
//
// Capped queries answer distance questions EXACTLY as the full-scan
// primitives do, provided the consumed answer is within the cap: every
// segment with a point within reach of p is registered in the cell range
// of its endpoints' bbox, which the scan radius ceil(reach/cell)+1 covers.
type lineIndex struct {
	cell  float64
	cells map[[2]int][]int32 // cell → segment indices i (segment Pts[i-1]..Pts[i])
}

const lineIndexCell = 32.0

// index returns the line's segment index, building it on first use.
func (l *Line) index() *lineIndex {
	l.idxOnce.Do(func() {
		ix := &lineIndex{cell: lineIndexCell, cells: map[[2]int][]int32{}}
		key := func(p Pt) [2]int {
			return [2]int{int(math.Floor(p.X / ix.cell)), int(math.Floor(p.Y / ix.cell))}
		}
		for i := 1; i < len(l.Pts); i++ {
			ka, kb := key(l.Pts[i-1]), key(l.Pts[i])
			x0, x1 := min(ka[0], kb[0]), max(ka[0], kb[0])
			y0, y1 := min(ka[1], kb[1]), max(ka[1], kb[1])
			for x := x0; x <= x1; x++ {
				for y := y0; y <= y1; y++ {
					ix.cells[[2]int{x, y}] = append(ix.cells[[2]int{x, y}], int32(i))
				}
			}
		}
		l.idx = ix
	})
	return l.idx
}

var candBufPool = sync.Pool{New: func() any { s := make([]int32, 0, 64); return &s }}

// candidates collects the deduplicated segment indices registered in cells
// within reach of p, in ascending order — the full scan's segment order, so
// min/tie selection over candidates resolves identically. The returned
// slice must go back via putCand.
func (ix *lineIndex) candidates(p Pt, reach float64) *[]int32 {
	r := int(math.Ceil(reach/ix.cell)) + 1
	kx := int(math.Floor(p.X / ix.cell))
	ky := int(math.Floor(p.Y / ix.cell))
	bp := candBufPool.Get().(*[]int32)
	buf := (*bp)[:0]
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			buf = append(buf, ix.cells[[2]int{kx + dx, ky + dy}]...)
		}
	}
	// insertion sort: candidate sets are small and mostly presorted
	for i := 1; i < len(buf); i++ {
		for j := i; j > 0 && buf[j] < buf[j-1]; j-- {
			buf[j], buf[j-1] = buf[j-1], buf[j]
		}
	}
	// dedup in place (bbox cell ranges register a segment in several cells)
	out := buf[:0]
	for i, s := range buf {
		if i == 0 || s != buf[i-1] {
			out = append(out, s)
		}
	}
	*bp = out
	return bp
}

func putCand(bp *[]int32) { candBufPool.Put(bp) }

// Within reports DistTo(p) < reach, bit-identically, touching only local
// segments: DistTo(p) < reach iff some segment has segDist < reach, and any
// such segment is among the candidates. Each candidate is tested with the
// banded squared comparison (exact compare on the knife edge only).
func (l *Line) Within(p Pt, reach float64) bool {
	if len(l.Pts) < 2 {
		return false
	}
	bp := l.index().candidates(p, reach)
	defer putCand(bp)
	for _, seg := range *bp {
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
	bp := l.index().candidates(p, reach)
	defer putCand(bp)
	for _, seg := range *bp {
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
	bp := l.index().candidates(p, cap)
	defer putCand(bp)
	best2 := math.Inf(1)
	var bdx, bdy float64
	for _, seg := range *bp {
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
	bp := l.index().candidates(p, cap)
	defer putCand(bp)
	best2 := math.Inf(1)
	bestI := -1
	var bestQ Pt
	var bdx, bdy float64
	for _, s := range *bp {
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
	bp := l.index().candidates(p, reach+1e-6)
	defer putCand(bp)
	var out []Crossing
	for _, s := range *bp {
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
