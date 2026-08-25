package sketch

import (
	"fmt"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

const (
	// EntranceClusterM: two centerlines leaving through one throat are one
	// entrance. This is the drawing's statement of the owner's rule — "if
	// multiple parallel tracks enter a yard, the entry is the average
	// centrepoint for all the tracks" — applied to the drawn lines.
	EntranceClusterM = 30.0

	// EntMatchM: a detected entrance this close to a drawn one is the
	// same entrance.
	EntMatchM = 40.0

	// YardCoverM: a drawn centerline sample with detected centerline this
	// close is covered.
	YardCoverM = 15.0

	// FailYardIoU is a RATCHET, not a target. The target the owner set is
	// 0.98; raise this as the detector earns it, and never lower it to
	// make a build pass.
	FailYardIoU = 0.75

	// stepOffM: how far off a boundary crossing to stand when asking
	// which side is inside. Short enough to stay in the crossing's own
	// segment, long enough to clear ring-vertex noise.
	stepOffM = 0.5
)

// Entrance is a computed yard entry/exit: the point where drawn
// centerlines cross the drawn boundary. Never authored by hand.
type Entrance struct {
	At      LL       `json:"at"`
	Heading float64  `json:"heading"` // degrees CCW from east, pointing INTO the yard
	Lines   []string `json:"lines"`   // ids of the centerlines crossing here
}

type crossing struct {
	p    geo.Pt
	dir  geo.Pt // unit, inbound
	line string
}

// Entrances computes the yard's entry/exit points: every crossing of a
// centerline with the boundary, clustered so one throat reads as one
// entrance and the heading points into the yard.
//
// This mirrors entrancesOf() in web/src/lib/sketch.ts, which runs the
// same rule live in the editor. If one changes, change both — the editor
// showing an entrance the scorer does not see is worse than no entrance.
func (y *Yard) Entrances() []Entrance {
	ring := y.Boundary.Ring()
	if len(ring) < 3 {
		return nil
	}
	frame := frameAt(ring[0])
	mring := pts(ring, frame)
	cs := y.crossings(frame, mring)
	if len(cs) == 0 {
		return nil
	}
	// Single-link clustering: crossings within reach of ANY member share a
	// throat. A ladder eight tracks wide is one entrance at their average,
	// which is the owner's rule; a nearest-centroid pass would instead
	// split it wherever the running mean drifted past the radius.
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].p.X != cs[j].p.X {
			return cs[i].p.X < cs[j].p.X
		}
		return cs[i].p.Y < cs[j].p.Y
	})
	parent := make([]int, len(cs))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := range cs {
		for j := i + 1; j < len(cs); j++ {
			if cs[i].p.Dist(cs[j].p) <= EntranceClusterM {
				parent[find(i)] = find(j)
			}
		}
	}
	groups := map[int][]crossing{}
	var roots []int
	for i, c := range cs {
		r := find(i)
		if _, seen := groups[r]; !seen {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], c)
	}
	var out []Entrance
	for _, r := range roots {
		ms := groups[r]
		var sum, dir geo.Pt
		seen := map[string]bool{}
		var ids []string
		for _, m := range ms {
			sum = sum.Add(m.p)
			dir = dir.Add(m.dir)
			if !seen[m.line] {
				seen[m.line] = true
				ids = append(ids, m.line)
			}
		}
		sort.Strings(ids)
		u := dir.Unit()
		ll := frame.ToLL(sum.Scale(1 / float64(len(ms))))
		out = append(out, Entrance{
			At:      LL{ll.Lon, ll.Lat},
			Heading: math.Round(math.Atan2(u.Y, u.X)*180/math.Pi*10) / 10,
			Lines:   ids,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At[0] != out[j].At[0] {
			return out[i].At[0] < out[j].At[0]
		}
		return out[i].At[1] < out[j].At[1]
	})
	return out
}

// crossings walks each centerline against the ring, recording every
// boundary hit with the direction of travel turned inbound.
func (y *Yard) crossings(frame geo.Frame, mring []geo.Pt) []crossing {
	var out []crossing
	for _, c := range y.Centerlines {
		cp := pts(c.Coords, frame)
		for i := 0; i+1 < len(cp); i++ {
			a, b := cp[i], cp[i+1]
			for j := range mring {
				r1, r2 := mring[j], mring[(j+1)%len(mring)]
				hit, ok := geo.SegIntersect(a, b, r1, r2)
				if !ok {
					continue
				}
				dir := b.Sub(a).Unit()
				// Inbound is decided AT the crossing, by stepping off it —
				// one long segment can enter and leave through opposite
				// edges, and judging by the segment's far end would then
				// point both of its entrances the same way.
				if !pointInRing(mring, hit.Add(dir.Scale(stepOffM))) {
					dir = dir.Scale(-1)
				}
				out = append(out, crossing{p: hit, dir: dir, line: c.ID})
			}
		}
	}
	return out
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

// ---- scoring the detector against the drawing ---------------------------

// DetectedYard is one region read back from a build's .yards.geojson
// sidecar, in metric.
type DetectedYard struct {
	ID          string
	Outline     []geo.Pt // ring, closing vertex dropped
	Entrances   []geo.Pt
	Centerlines []*geo.Line
}

type YardScore struct {
	Label   string
	AreaHa  float64 // drawn area
	IoU     float64 // boundary agreement — the headline number
	BndP90  float64 // symmetric boundary deviation, metres
	BndMax  float64
	WorstAt geo.LL // where the boundary is furthest off — navigable

	EntDrawn int
	EntFound int // drawn entrances a detected one landed on
	EntExtra int // detected entrances with nothing drawn near them

	CtrKm       float64
	CtrMean     float64
	CtrP90      float64
	CtrCoverPct float64

	Fail bool
}

type YardResult struct {
	Yards    []YardScore
	MeanIoU  float64 // area-weighted
	Failures int
}

// ScoreYards grades detected yard regions against the drawn yards. Each
// drawn yard is matched to the detected region it overlaps most; a drawn
// yard with no overlapping region scores IoU 0, which is the truth.
func ScoreYards(net *Network, det []DetectedYard, frame geo.Frame) *YardResult {
	if len(net.Yards) == 0 {
		return nil
	}
	res := &YardResult{}
	areaSum := 0.0
	for _, y := range net.Yards {
		ring := pts(y.Boundary.Ring(), frame)
		if len(ring) < 3 {
			continue
		}
		sc := YardScore{Label: label(y.Label, 16), AreaHa: math.Abs(ringArea(ring)) / 10000}

		best, bestIoU := -1, 0.0
		for i, d := range det {
			if len(d.Outline) < 3 {
				continue
			}
			if iou := ringIoU(ring, d.Outline); iou > bestIoU {
				best, bestIoU = i, iou
			}
		}
		sc.IoU = bestIoU
		if best >= 0 {
			d := det[best]
			sc.BndP90, sc.BndMax, sc.WorstAt = ringDeviation(ring, d.Outline, frame)
			sc.EntDrawn, sc.EntFound, sc.EntExtra = matchEntrances(&y, frame, ring, d.Entrances)
			sc.CtrKm, sc.CtrMean, sc.CtrP90, sc.CtrCoverPct = scoreCenterlines(&y, frame, d.Centerlines)
		} else {
			sc.EntDrawn = len(y.Entrances())
			for _, c := range y.Centerlines {
				if l := c.Line(frame); l != nil {
					sc.CtrKm += l.Len() / 1000
				}
			}
		}
		// Only the boundary is gated. The detector emits centerlines now,
		// but nothing has been drawn to grade them against yet — ratchet
		// CtrCoverPct in here the moment a drawing exists, the same way
		// FailYardIoU ratchets.
		sc.Fail = sc.IoU < FailYardIoU
		if sc.Fail {
			res.Failures++
		}
		res.MeanIoU += sc.IoU * sc.AreaHa
		areaSum += sc.AreaHa
		res.Yards = append(res.Yards, sc)
	}
	if areaSum > 0 {
		res.MeanIoU /= areaSum
	}
	return res
}

func matchEntrances(y *Yard, frame geo.Frame, ring []geo.Pt, found []geo.Pt) (drawn, hit, extra int) {
	var want []geo.Pt
	for _, e := range y.Entrances() {
		want = append(want, frame.ToXY(geo.LL{Lon: e.At[0], Lat: e.At[1]}))
	}
	drawn = len(want)
	used := make([]bool, len(found))
	for _, w := range want {
		bi, bd := -1, EntMatchM
		for i, f := range found {
			if used[i] {
				continue
			}
			if d := w.Dist(f); d <= bd {
				bi, bd = i, d
			}
		}
		if bi >= 0 {
			used[bi] = true
			hit++
		}
	}
	// An entrance found far from the yard belongs to another region; only
	// count the ones this yard should have explained.
	for i, f := range found {
		if !used[i] && pointNearRing(ring, f, EntMatchM) {
			extra++
		}
	}
	return drawn, hit, extra
}

func scoreCenterlines(y *Yard, frame geo.Frame, det []*geo.Line) (km, mean, p90, cover float64) {
	var devs []float64
	for _, c := range y.Centerlines {
		l := c.Line(frame)
		if l == nil || l.Len() < 1 {
			continue
		}
		km += l.Len() / 1000
		for _, q := range l.Resample(5) {
			best := math.Inf(1)
			for _, d := range det {
				if v := d.DistTo(q); v < best {
					best = v
				}
			}
			if math.IsInf(best, 1) {
				best = 200
			}
			devs = append(devs, best)
		}
	}
	if len(devs) == 0 {
		return km, 0, 0, 0
	}
	sort.Float64s(devs)
	hit := 0
	for _, d := range devs {
		mean += d
		if d <= YardCoverM {
			hit++
		}
	}
	mean /= float64(len(devs))
	p90 = devs[int(0.9*float64(len(devs)-1))]
	cover = 100 * float64(hit) / float64(len(devs))
	return km, mean, p90, cover
}

// ringDeviation is symmetric: the worst of "how far is the drawing from
// the detected edge" and its reverse. One-sided, a detected blob that
// swallows the drawing scores perfectly.
func ringDeviation(a, b []geo.Pt, frame geo.Frame) (p90, max float64, worst geo.LL) {
	la, lb := closedLine(a), closedLine(b)
	var devs []float64
	worstD, worstP := -1.0, geo.Pt{}
	walk := func(src *geo.Line, dst *geo.Line) {
		for _, q := range src.Resample(5) {
			d := dst.DistTo(q)
			devs = append(devs, d)
			if d > worstD {
				worstD, worstP = d, q
			}
		}
	}
	walk(la, lb)
	walk(lb, la)
	if len(devs) == 0 {
		return 0, 0, geo.LL{}
	}
	sort.Float64s(devs)
	return devs[int(0.9*float64(len(devs)-1))], devs[len(devs)-1], frame.ToLL(worstP)
}

func closedLine(ring []geo.Pt) *geo.Line {
	return geo.NewLine(append(append([]geo.Pt{}, ring...), ring[0]))
}

func pointNearRing(ring []geo.Pt, p geo.Pt, reach float64) bool {
	return pointInRing(ring, p) || closedLine(ring).DistTo(p) <= reach
}

func ringArea(ring []geo.Pt) float64 {
	a := 0.0
	for i, p := range ring {
		q := ring[(i+1)%len(ring)]
		a += p.X*q.Y - q.X*p.Y
	}
	return a / 2
}

// iouCellM: the raster pitch for area agreement. At 2 m a 600 m yard is
// 300 cells across — well under the boundary deviation we care about, and
// scoring runs over a handful of drawn yards, not every region.
const iouCellM = 2.0

// ringIoU is intersection-over-union of two rings, by scanline raster on
// a shared grid. Polygon clipping would be exact, but it also needs to be
// right about self-touching contours; the detector emits those.
func ringIoU(a, b []geo.Pt) float64 {
	lo := geo.Pt{X: math.Inf(1), Y: math.Inf(1)}
	hi := geo.Pt{X: math.Inf(-1), Y: math.Inf(-1)}
	for _, ring := range [][]geo.Pt{a, b} {
		for _, p := range ring {
			lo.X, lo.Y = math.Min(lo.X, p.X), math.Min(lo.Y, p.Y)
			hi.X, hi.Y = math.Max(hi.X, p.X), math.Max(hi.Y, p.Y)
		}
	}
	nx := int((hi.X-lo.X)/iouCellM) + 2
	ny := int((hi.Y-lo.Y)/iouCellM) + 2
	if nx < 2 || ny < 2 || nx*ny > 40_000_000 {
		return 0
	}
	ma := ringMask(a, lo, nx, ny)
	mb := ringMask(b, lo, nx, ny)
	inter, union := 0, 0
	for i := range ma {
		if ma[i] && mb[i] {
			inter++
		}
		if ma[i] || mb[i] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// ringMask fills a ring by scanline: per row, sort the edge crossings and
// fill between pairs (even-odd, so a self-touching contour still fills
// sanely).
func ringMask(ring []geo.Pt, lo geo.Pt, nx, ny int) []bool {
	m := make([]bool, nx*ny)
	var xs []float64
	for j := 0; j < ny; j++ {
		y := lo.Y + (float64(j)+0.5)*iouCellM
		xs = xs[:0]
		for i := range ring {
			p, q := ring[i], ring[(i+1)%len(ring)]
			if (p.Y > y) == (q.Y > y) {
				continue
			}
			xs = append(xs, p.X+(y-p.Y)*(q.X-p.X)/(q.Y-p.Y))
		}
		if len(xs) < 2 {
			continue
		}
		sort.Float64s(xs)
		for k := 0; k+1 < len(xs); k += 2 {
			i0 := int(math.Ceil((xs[k] - lo.X - 0.5*iouCellM) / iouCellM))
			i1 := int(math.Floor((xs[k+1] - lo.X - 0.5*iouCellM) / iouCellM))
			for i := max(0, i0); i <= min(nx-1, i1); i++ {
				m[j*nx+i] = true
			}
		}
	}
	return m
}

func (r *YardResult) Print() {
	if r == nil || len(r.Yards) == 0 {
		return
	}
	fmt.Printf("  %-16s %7s %6s %7s %7s %7s %8s\n",
		"yard", "area_ha", "IoU", "bnd_p90", "bnd_max", "ent", "ctr_cov%")
	for _, y := range r.Yards {
		flag := ""
		if y.Fail {
			flag = "  <== FAIL"
		}
		ent := fmt.Sprintf("%d/%d", y.EntFound, y.EntDrawn)
		if y.EntExtra > 0 {
			ent += fmt.Sprintf("+%d", y.EntExtra)
		}
		fmt.Printf("  %-16s %7.1f %6.3f %7.1f %7.1f %7s %8.1f%s\n",
			y.Label, y.AreaHa, y.IoU, y.BndP90, y.BndMax, ent, y.CtrCoverPct, flag)
	}
	fmt.Printf("  YARDS: mean IoU %.3f over %d drawn yard(s) (gate %.2f, target 0.98)\n",
		r.MeanIoU, len(r.Yards), FailYardIoU)
	for _, y := range r.Yards {
		if y.Fail {
			fmt.Printf("    %s worst boundary miss %.0f m @ %.5f,%.5f\n",
				y.Label, y.BndMax, y.WorstAt.Lat, y.WorstAt.Lon)
		}
	}
}
