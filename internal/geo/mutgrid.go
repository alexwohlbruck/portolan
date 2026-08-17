package geo

import "math"

// MutGrid is a uniform spatial hash over whole lines that supports removal
// and insertion, addressed by caller-supplied stable keys instead of slice
// positions. Grid rebuilds its whole index; a caller that mutates a few
// lines per round would pay for every line every round.
//
// Membership semantics match Grid exactly — a key is visited when some
// segment of its line lies within reach of the query point, tested with
// the same segWithin predicate over the same cell radius, and visited at
// most once per query. The VISIT ORDER differs from Grid's, so MutGrid is
// only for callers that consume the visited set (or per-query counts),
// never the order — Grid.Near's order is load-bearing for some callers.
type MutGrid struct {
	cell  float64
	cells map[[2]int][]mref
	lines map[int64]*Line
	seen  map[int64]bool // query scratch; MutGrid is single-goroutine
}

// mref carries the line pointer so a query needs no map lookup per segment.
type mref struct {
	key int64
	l   *Line
	seg int
}

func NewMutGrid(cell float64) *MutGrid {
	return &MutGrid{cell: cell, cells: map[[2]int][]mref{},
		lines: map[int64]*Line{}, seen: map[int64]bool{}}
}

func (g *MutGrid) key(p Pt) [2]int {
	return [2]int{int(math.Floor(p.X / g.cell)), int(math.Floor(p.Y / g.cell))}
}

// eachCell visits every cell the segment a→b touches, by endpoint bbox —
// the same rasterization Grid uses, so both index a segment identically.
// eachCell shares Grid's insertion walk (see eachSegCell): Add and Remove
// both come through here, so the two always agree on a segment's cell set.
func (g *MutGrid) eachCell(a, b Pt, fn func([2]int)) {
	eachSegCell(a, b, g.cell, fn)
}

// Add indexes a line under key. Adding a key that is already present
// replaces it.
func (g *MutGrid) Add(key int64, l *Line) {
	if _, ok := g.lines[key]; ok {
		g.Remove(key)
	}
	g.lines[key] = l
	for si := 1; si < len(l.Pts); si++ {
		a, b := l.Pts[si-1], l.Pts[si]
		g.eachCell(a, b, func(c [2]int) {
			g.cells[c] = append(g.cells[c], mref{key, l, si})
		})
	}
}

// Remove drops a key's line from the index.
func (g *MutGrid) Remove(key int64) {
	l, ok := g.lines[key]
	if !ok {
		return
	}
	delete(g.lines, key)
	touched := map[[2]int]bool{}
	for si := 1; si < len(l.Pts); si++ {
		g.eachCell(l.Pts[si-1], l.Pts[si], func(c [2]int) { touched[c] = true })
	}
	for c := range touched {
		refs := g.cells[c]
		kept := refs[:0]
		for _, r := range refs {
			if r.key != key {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			delete(g.cells, c)
		} else {
			g.cells[c] = kept
		}
	}
}

// Near visits every distinct key with at least one segment within reach of
// p. Visits are deduplicated; order is unspecified (see the type comment).
// The dedup scratch is shared, so fn must not call Near again.
func (g *MutGrid) Near(p Pt, reach float64, fn func(key int64)) {
	r := int(math.Ceil(reach/g.cell)) + 1
	k := g.key(p)
	seen := g.seen
	clear(seen)
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			for _, ref := range g.cells[[2]int{k[0] + dx, k[1] + dy}] {
				if seen[ref.key] {
					continue
				}
				if segWithin(p, ref.l.Pts[ref.seg-1], ref.l.Pts[ref.seg], reach) {
					seen[ref.key] = true
					fn(ref.key)
				}
			}
		}
	}
}
