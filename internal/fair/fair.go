// Package fair is stage 6: per zoom band, cut corridors back from their
// nodes and connect continuing lines with G1 fillet transitions. Rules with
// scar tissue behind them (docs/LESSONS.md #14, #17): cuts shrink on curving
// approaches; transition length doubles per zoom-out so junctions hold
// constant on-screen size.
package fair

import (
	"fmt"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/berth"
	"github.com/alexwohlbruck/portolan/internal/bundle"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/order"
)

// Band is one zoom range with its transition length.
type Band struct {
	MinZoom, MaxZoom int
	CutM             float64
}

// DefaultBands — the values tuned in production (base 140 m at z15).
func DefaultBands() []Band {
	return []Band{
		{15, 24, 140},
		{14, 14, 280},
		{13, 13, 560},
		{0, 12, 1120},
	}
}

// Segment is one emitted PER-ROUTE ribbon feature, carrying the parchment
// transit_line_segments offset contract: steady features a constant signed
// offset_px, transitions off_from_px→off_to_px eased along line-progress by
// the MapLibre fork. Offsets are signed in the FEATURE'S OWN travel frame
// (its geometry direction) — clients use them raw, never recompute.
type Segment struct {
	Kind      string // steady | transition | bridge
	Color     string
	Route     string
	Label     string
	RouteType int
	Slot      int
	NSlots    int
	OffsetPx  float64 // steady
	OffFromPx float64 // transition ends
	OffToPx   float64
	Corridor  int // steady: corridor id; transition: from-corridor
	ToCorr    int // transition only
	Band      Band
	Line      *geo.Line
}

// SlotPx is the on-screen ribbon spacing at full zoom (matches parchment's
// 4.4 px slot pitch; the client's zoom-scaled expressions do the rest).
const SlotPx = 4.4

func offsetPx(slot, nslots int) float64 {
	return (float64(slot) - float64(nslots-1)/2) * SlotPx
}

// Build emits all bands.
func Build(g *bundle.Graph, br *berth.Result, slots order.Slots, bands []Band) []Segment {
	var out []Segment
	for _, band := range bands {
		out = append(out, buildBand(g, br, slots, band)...)
	}
	return out
}

func buildBand(g *bundle.Graph, br *berth.Result, slots order.Slots, band Band) []Segment {
	var segs []Segment

	// per corridor: cut lengths at each end (0 at free ends / terminals)
	cutAt := func(c *bundle.Corridor, node int) float64 {
		if node < 0 || len(g.Nodes[node].Corridors) < 2 {
			return 0 // terminal: draw to the end
		}
		cut := math.Min(band.CutM/2, 0.35*c.Centerline.Len())
		// curvature clamp: never synthesize across a real curve — shrink
		// the cut while the approach turns hard (LESSONS #14)
		cl := c.Centerline
		var arc0, arc1 float64
		if c.NodeA == node {
			arc0, arc1 = 0, cut
		} else {
			arc0, arc1 = cl.Len()-cut, cl.Len()
		}
		t0 := cl.TangentAtArc(arc0, 10)
		t1 := cl.TangentAtArc(arc1, 10)
		turn := math.Acos(math.Max(-1, math.Min(1, t0.Dot(t1)))) * 180 / math.Pi
		if turn > 30 {
			cut *= 30 / turn
		}
		return cut
	}

	type endInfo struct {
		pt  geo.Pt
		tan geo.Pt // pointing INTO the node
		cut float64
	}
	endAt := func(ci, node int) endInfo {
		c := &g.Corridors[ci]
		cl := c.Centerline
		cut := cutAt(c, node)
		if c.NodeB == node {
			a := cl.Len() - cut
			return endInfo{cl.AtArc(a), cl.TangentAtArc(a, 12), cut}
		}
		a := cut
		return endInfo{cl.AtArc(a), cl.TangentAtArc(a, 12).Scale(-1), cut}
	}

	// corridors fully consumed by this band's junction cuts: no steady, and
	// transitions pass THROUGH them to their neighbors
	consumed := map[int]bool{}
	for ci := range g.Corridors {
		c := &g.Corridors[ci]
		// a corridor shorter than the bundling span threshold is junction
		// furniture at EVERY band (the base cut alone is 140 m) — it routes
		// moves but never draws its own body
		if c.Centerline.Len() < 60 ||
			c.Centerline.Len()-cutAt(c, c.NodeA)-cutAt(c, c.NodeB) < 30 {
			consumed[ci] = true
		}
	}

	// steadies: per corridor per color
	for ci := range g.Corridors {
		c := &g.Corridors[ci]
		bs := br.Berths[ci]
		if len(bs) == 0 || consumed[ci] {
			continue
		}
		cA := cutAt(c, c.NodeA)
		cB := cutAt(c, c.NodeB)
		body := bundle.SubLine(c.Centerline, cA, c.Centerline.Len()-cB)
		nslots := len(slots[ci])
		for _, b := range bs {
			slot := slotOf(slots[ci], b.RouteID)
			segs = append(segs, Segment{
				Kind: "steady", Color: b.Color, Route: b.RouteID,
				Label: b.Label, RouteType: b.Type,
				Slot: slot, NSlots: nslots,
				OffsetPx:  offsetPx(slot, nslots),
				OffFromPx: offsetPx(slot, nslots),
				OffToPx:   offsetPx(slot, nslots),
				Corridor:  ci, ToCorr: -1, Band: band, Line: body,
			})
		}
	}

	// transitions: per observed move per color. Moves into a CONSUMED
	// corridor chain through it (depth ≤3) so the fillet spans the whole
	// junction throat instead of ending on a dropped sliver.
	moves := map[[2]int]map[string]bool{}
	for k, rs := range br.Moves {
		moves[k] = rs
	}
	for depth := 0; depth < 3; depth++ {
		added := false
		for k1, r1 := range moves {
			t := k1[1]
			if !consumed[t] {
				continue
			}
			for k2, r2 := range moves {
				if k2[0] != t || k2[1] == k1[0] || consumed[k2[1]] {
					continue
				}
				shared := map[string]bool{}
				for r := range r1 {
					if r2[r] {
						shared[r] = true
					}
				}
				if len(shared) == 0 {
					continue
				}
				nk := [2]int{k1[0], k2[1]}
				if consumed[nk[0]] {
					continue
				}
				if moves[nk] == nil {
					moves[nk] = map[string]bool{}
					added = true
				}
				for r := range shared {
					if !moves[nk][r] {
						moves[nk][r] = true
						added = true
					}
				}
			}
		}
		if !added {
			break
		}
	}
	var keys [][2]int
	for k := range moves {
		if consumed[k[0]] || consumed[k[1]] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		a, b := k[0], k[1]
		if a == b {
			continue
		}
		na := nearSide(g, a, b, consumed)
		nb := nearSide(g, b, a, consumed)
		if na < 0 || nb < 0 {
			continue
		}
		ea := endAt(a, na)
		eb := endAt(b, nb)
		if ea.cut == 0 && eb.cut == 0 {
			continue
		}
		chord := ea.pt.Dist(eb.pt)
		if chord < 1 {
			continue
		}
		// G1 fillet with ALIGNMENT-CLAMPED control points: a control arm
		// following a tangent that opposes the chord loops the bezier into
		// a hook (the GC-hook class) — arms shrink with disagreement
		dir := eb.pt.Sub(ea.pt).Unit()
		arm := math.Min(chord/3, 50)
		da := arm * math.Max(0.05, ea.tan.Dot(dir))  // a's tangent: with the chord
		db := arm * math.Max(0.05, -eb.tan.Dot(dir)) // b's tangent points back INTO the node
		p0, p3 := ea.pt, eb.pt
		p1 := p0.Add(ea.tan.Scale(da))
		p2 := p3.Add(eb.tan.Scale(db))
		bez := bezier(p0, p1, p2, p3, 8)
		var routes []string
		for r := range moves[k] {
			routes = append(routes, r)
		}
		sort.Strings(routes)
		info := map[string]berth.Berth{}
		for _, bb := range br.Berths[a] {
			info[bb.RouteID] = bb
		}
		// travel-frame signs: the transition's geometry runs a-end → b-end.
		// It agrees with a's storage frame iff it leaves a at NodeB, and with
		// b's iff it enters b at NodeA; a disagreeing frame mirrors the
		// offset (LESSONS #12 — never re-derive client-side).
		signA, signB := 1.0, 1.0
		if g.Corridors[a].NodeB != na {
			signA = -1
		}
		if g.Corridors[b].NodeA != nb {
			signB = -1
		}
		nA, nB := len(slots[a]), len(slots[b])
		tline := geo.NewLine(bez)
		for _, r := range routes {
			bb := info[r]
			sa := slotOf(slots[a], r)
			sb := slotOf(slots[b], r)
			segs = append(segs, Segment{
				Kind: "transition", Color: bb.Color, Route: r,
				Label: bb.Label, RouteType: bb.Type,
				Slot: sa, NSlots: nA,
				OffFromPx: offsetPx(sa, nA) * signA,
				OffToPx:   offsetPx(sb, nB) * signB,
				Corridor:  a, ToCorr: b,
				Band: band, Line: tline,
			})
		}
	}

	// bridges: per match gap leg per color (deduped by geometry key)
	seen := map[string]bool{}
	for _, m := range br.Matches {
		for _, leg := range m.Legs {
			if leg.Corridor >= 0 || leg.Bridge == nil || leg.Bridge.Len() < 30 {
				continue
			}
			kk := bridgeKey(m.Pattern.Route.ID, leg.Bridge)
			if seen[kk] {
				continue
			}
			seen[kk] = true
			segs = append(segs, Segment{
				Kind: "bridge", Color: berthColor(br, m.Pattern.Route.ID),
				Route: m.Pattern.Route.ID, Label: m.Pattern.Route.ShortName,
				RouteType: m.Pattern.Route.Type,
				Slot:      0, NSlots: 1,
				Corridor:  -1, ToCorr: -1, Band: band, Line: leg.Bridge,
			})
		}
	}
	return segs
}

func slotOf(ids []string, r string) int {
	for i, id := range ids {
		if id == r {
			return i
		}
	}
	return 0
}

// nearSide picks the node of corridor a facing corridor b — the shared node
// if directly adjacent, else (chained through consumed corridors) the node
// of a whose position is nearest to b's centerline.
func nearSide(g *bundle.Graph, a, b int, consumed map[int]bool) int {
	if n := commonNode(g, a, b); n >= 0 {
		return n
	}
	ca := g.Corridors[a]
	cb := g.Corridors[b]
	da, db := math.Inf(1), math.Inf(1)
	if ca.NodeA >= 0 {
		da = cb.Centerline.DistTo(g.Nodes[ca.NodeA].At)
	}
	if ca.NodeB >= 0 {
		db = cb.Centerline.DistTo(g.Nodes[ca.NodeB].At)
	}
	if da <= db {
		return ca.NodeA
	}
	return ca.NodeB
}

func commonNode(g *bundle.Graph, a, b int) int {
	ca, cb := g.Corridors[a], g.Corridors[b]
	for _, na := range []int{ca.NodeA, ca.NodeB} {
		for _, nb := range []int{cb.NodeA, cb.NodeB} {
			if na >= 0 && na == nb {
				return na
			}
		}
	}
	return -1
}

func bezier(p0, p1, p2, p3 geo.Pt, step float64) []geo.Pt {
	approx := p0.Dist(p1) + p1.Dist(p2) + p2.Dist(p3)
	n := int(math.Max(2, approx/step))
	out := make([]geo.Pt, 0, n+1)
	for k := 0; k <= n; k++ {
		t := float64(k) / float64(n)
		u := 1 - t
		out = append(out, geo.Pt{
			X: u*u*u*p0.X + 3*u*u*t*p1.X + 3*u*t*t*p2.X + t*t*t*p3.X,
			Y: u*u*u*p0.Y + 3*u*u*t*p1.Y + 3*u*t*t*p2.Y + t*t*t*p3.Y,
		})
	}
	return out
}

func berthColor(br *berth.Result, routeID string) string {
	for _, bs := range br.Berths {
		for _, b := range bs {
			if b.RouteID == routeID {
				return b.Color
			}
		}
	}
	return "555555"
}

func bridgeKey(route string, l *geo.Line) string {
	p := l.Pts[0]
	q := l.Pts[len(l.Pts)-1]
	return route + "|" + fmtPt(p) + "|" + fmtPt(q)
}

func fmtPt(p geo.Pt) string {
	return fmt.Sprintf("%.0f,%.0f", p.X/10, p.Y/10)
}
