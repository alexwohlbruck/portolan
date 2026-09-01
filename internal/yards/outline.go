package yards

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// traceOutline returns the outer boundary of a cell set as a closed CCW
// ring of cell-corner points (integer corners scaled by cell at the very
// end — no float equality anywhere), collinear runs collapsed, first
// vertex not repeated. Exactly consistent with membership: every cell of
// the set lies inside the ring; every point outside the ring lies in no
// cell of the set (interior holes are dropped — a yard with a courtyard
// is not worth the API surface).
func traceOutline(cells map[[2]int]bool, cell float64) []geo.Pt {
	if len(cells) == 0 {
		return nil
	}
	// One directed edge per exposed cell side, interior on the LEFT, so
	// the outer loop comes out CCW. Fan lists are sorted for determinism.
	edges := map[[2]int][][2]int{}
	for c := range cells {
		x, y := c[0], c[1]
		if !cells[[2]int{x, y - 1}] {
			edges[[2]int{x, y}] = append(edges[[2]int{x, y}], [2]int{x + 1, y})
		}
		if !cells[[2]int{x + 1, y}] {
			edges[[2]int{x + 1, y}] = append(edges[[2]int{x + 1, y}], [2]int{x + 1, y + 1})
		}
		if !cells[[2]int{x, y + 1}] {
			edges[[2]int{x + 1, y + 1}] = append(edges[[2]int{x + 1, y + 1}], [2]int{x, y + 1})
		}
		if !cells[[2]int{x - 1, y}] {
			edges[[2]int{x, y + 1}] = append(edges[[2]int{x, y + 1}], [2]int{x, y})
		}
	}
	for k := range edges {
		fan := edges[k]
		sort.Slice(fan, func(i, j int) bool {
			if fan[i][0] != fan[j][0] {
				return fan[i][0] < fan[j][0]
			}
			return fan[i][1] < fan[j][1]
		})
	}

	var best [][2]int
	bestArea := math.Inf(-1)
	for len(edges) > 0 {
		// Each walk starts at the smallest remaining corner — for the
		// outer loop that is the bottom-left extremity, where a diagonal
		// pinch is impossible (nothing lies below-left of it).
		start := [2]int{math.MaxInt32, math.MaxInt32}
		for k := range edges {
			if k[0] < start[0] || (k[0] == start[0] && k[1] < start[1]) {
				start = k
			}
		}
		loop := walkLoop(edges, start)
		if a := shoelace(loop); a > bestArea {
			bestArea, best = a, loop
		}
	}

	// Collapse collinear runs (integer directions, exact compares).
	n := len(best)
	ring := make([]geo.Pt, 0, n)
	for i := 0; i < n; i++ {
		p, q, r := best[(i+n-1)%n], best[i], best[(i+1)%n]
		if q[0]-p[0] == r[0]-q[0] && q[1]-p[1] == r[1]-q[1] {
			continue
		}
		ring = append(ring, geo.Pt{X: float64(q[0]) * cell, Y: float64(q[1]) * cell})
	}
	return ring
}

// walkLoop walks one boundary loop from start, consuming edges. At a
// diagonal-pinch corner two outgoing edges exist; taking the RIGHTMOST
// turn relative to the incoming direction crosses over to the other cell,
// keeping an 8-connected component enclosed by ONE outer loop (the
// leftmost turn would close a sub-loop around half the component).
func walkLoop(edges map[[2]int][][2]int, start [2]int) [][2]int {
	loop := [][2]int{start}
	cur := start
	var in [2]int // incoming direction; zero at start
	for {
		fan := edges[cur]
		pick := 0
		if len(fan) > 1 {
			bestRank := 5
			for i, next := range fan {
				d := [2]int{next[0] - cur[0], next[1] - cur[1]}
				var rank int
				switch {
				case in == [2]int{}:
					// Loop start: fixed preference keeps it deterministic.
					rank = map[[2]int]int{{1, 0}: 0, {0, 1}: 1, {-1, 0}: 2, {0, -1}: 3}[d]
				case d == [2]int{in[1], -in[0]}: // right turn (CW)
					rank = 0
				case d == in:
					rank = 1
				case d == [2]int{-in[1], in[0]}: // left turn (CCW)
					rank = 2
				default: // U-turn; cannot happen on a valid boundary
					rank = 3
				}
				if rank < bestRank {
					bestRank, pick = rank, i
				}
			}
		}
		next := fan[pick]
		rest := make([][2]int, 0, len(fan)-1)
		rest = append(rest, fan[:pick]...)
		rest = append(rest, fan[pick+1:]...)
		if len(rest) == 0 {
			delete(edges, cur)
		} else {
			edges[cur] = rest
		}
		in = [2]int{next[0] - cur[0], next[1] - cur[1]}
		cur = next
		if cur == start {
			return loop
		}
		loop = append(loop, cur)
	}
}

// shoelace is the signed area of an integer-corner loop; positive = CCW.
func shoelace(loop [][2]int) float64 {
	s := 0.0
	for i, p := range loop {
		q := loop[(i+1)%len(loop)]
		s += float64(p[0]*q[1] - q[0]*p[1])
	}
	return s / 2
}
