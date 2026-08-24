// Package yards detects rail-yard regions and answers point/way membership
// queries for the whole pipeline. Detection is geometric first — proximate
// parallel tracks: the more of them and the closer they run, the more
// yard-like the spot (owner's rule; LESSONS: >4 strands are yards) — with
// service tags as seeds, because OSM tag coverage is incomplete and a
// detector that only trusted tags would miss whole freight complexes.
//
// A nil *Index answers false/none on every query: the --corridors path
// loads no OSM and opted-out feeds never build one, so call sites need no
// guards.
package yards

import (
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Track is the detector's input: one OSM way in the metric frame. Service
// carries the way's service tag verbatim ("" for revenue track); Level the
// bridge/tunnel class (+1/0/-1) so an elevated structure crossing a yard
// does not join it.
type Track struct {
	ID      string
	Line    *geo.Line
	Service string
	Level   int
}

// Entrance is a spot where connected track crosses a region's outline.
type Entrance struct {
	Pt      geo.Pt   // exactly on the region Outline
	Heading geo.Pt   // unit tangent pointing OUT of the region
	WayIDs  []string // crossing ways merged into this entrance, sorted
}

// Spine is a centerline through the yard between two entrances, riding
// plausible steel. Its endpoints are bit-equal to the entrance points so
// outside geometry welds cleanly.
type Spine struct {
	From, To int // indices into Region.Entrances
	Line     *geo.Line
}

// SkelNode is a vertex of the region's spine skeleton: an entrance
// (Entrance >= 0, Pt bit-equal to it) or an interior fork (-1). The
// skeleton is the pair spines' shared steel contracted into runs — two
// spines through one throat share the trunk section as ONE skeleton edge,
// which is what lets a substitution draw a fork as two corridors meeting
// at a node instead of overlapping ink.
type SkelNode struct {
	Pt       geo.Pt
	Entrance int
}

// SkelEdge is one skeleton run, oriented A→B.
type SkelEdge struct {
	A, B int
	Line *geo.Line
}

// Region is one detected yard: a connected patch of parallel-track density.
type Region struct {
	ID       int
	Outline  []geo.Pt // closed CCW ring, first vertex not repeated
	TrackLen float64  // member steel hot arc inside the region (m)
	Peak     float64  // max parallel-proximity score seen
	Level    int      // dominant member level — the footprint is 2D, and a
	// subway running UNDER a surface yard must not grow entrances into it
	WayIDs    []string // ways with hot samples inside, sorted
	Entrances []Entrance
	Spines    []Spine
	SkelNodes []SkelNode
	Skel      []SkelEdge
}

// Params are the detection dials (pipeline.Dials yard_* keys).
type Params struct {
	ParReach  float64 // cross-section half-reach (m); keep < 32 or every capped line query falls back to full scans
	ParNear   float64 // full-weight neighbor offset (m)
	Hot       float64 // weighted parallel count that makes a sample yard-hot
	MinTagM   float64 // min tagged hot steel to anchor a region (m)
	MinMassM  float64 // min total hot steel for any region (m)
	PeakUntag float64 // peak score an untagged-only region must reach
}

func DefaultParams() Params {
	return Params{ParReach: 30, ParNear: 12, Hot: 5, MinTagM: 500, MinMassM: 2000, PeakUntag: 8}
}

const (
	cellM       = 24.0 // region mask cell; one dilation ring ≈ the between-ladder-tracks margin
	dilateCells = 1
	sampleStepM = 20.0 // score sampling pitch along arc
	tangentWinM = 10.0
	minParallel = 0.94 // ~20°; stricter than bundling's gate — ladder tracks are truly parallel
	seedScore   = 1.0  // a tagged way needs one parallel neighbor to seed cells
)

func svcYard(s string) bool { return s == "yard" || s == "siding" || s == "spur" }

// Index answers the pipeline's yard queries. Immutable after Build.
type Index struct {
	regions    []*Region
	cellRegion map[[2]int]int32  // cells wholly inside a region's outline
	boundary   map[[2]int]int32  // outline passes through — exact test on query
	svc        map[string]string // wayID → service tag, "" absent
	regionWay  map[string]bool   // wayID → hot member of some region
	memberGrid *geo.Grid
}

func cellKey(p geo.Pt) [2]int {
	return [2]int{int(math.Floor(p.X / cellM)), int(math.Floor(p.Y / cellM))}
}

// regionIdxAt: the region whose outline contains p, -1 outside. Interior
// cells answer with one map hit; only outline-crossing cells pay the
// exact point-in-ring test, so the hot paths stay O(1) while the answer
// is exactly consistent with Region.Outline.
func (ix *Index) regionIdxAt(p geo.Pt) int32 {
	c := cellKey(p)
	if id, ok := ix.cellRegion[c]; ok {
		return id
	}
	if id, ok := ix.boundary[c]; ok && pointInRing(ix.regions[id].Outline, p) {
		return id
	}
	return -1
}

// InYard reports whether p lies inside a yard region's outline (interior
// points between ladder tracks count). Consumers must read it as
// "suppress yard heuristics here", never "drop this geometry": revenue
// mainlines run through Sunnyside and stay revenue.
func (ix *Index) InYard(p geo.Pt) bool {
	return ix != nil && ix.regionIdxAt(p) >= 0
}

// RegionAt returns the region containing p, nil outside every region.
func (ix *Index) RegionAt(p geo.Pt) *Region {
	if ix == nil {
		return nil
	}
	id := ix.regionIdxAt(p)
	if id < 0 {
		return nil
	}
	return ix.regions[id]
}

// IsYardWay reports tagged yard steel: service ∈ {yard, siding, spur}.
// True even for a lone rural siding that anchors no region.
func (ix *Index) IsYardWay(id string) bool {
	if ix == nil {
		return false
	}
	return svcYard(ix.svc[id])
}

// WayService returns the way's service tag verbatim, "" when untagged or
// unknown.
func (ix *Index) WayService(id string) string {
	if ix == nil {
		return ""
	}
	return ix.svc[id]
}

// RegionWay reports whether the way has hot samples inside some region —
// tagged or not. This is the untagged-ladder test the twin pool needs;
// unlike IsYardWay it can be true for revenue track riding through a yard,
// so it must only ever gate UNRIDDEN steel.
func (ix *Index) RegionWay(id string) bool {
	if ix == nil {
		return false
	}
	return ix.regionWay[id]
}

// NearestDist is the distance from p to the nearest member steel within
// maxReach (≤ the 64 m index cell), +Inf beyond.
func (ix *Index) NearestDist(p geo.Pt, maxReach float64) float64 {
	if ix == nil || ix.memberGrid == nil {
		return math.Inf(1)
	}
	// Grid.NearestDist reports the best segment in its scanned block even
	// past maxReach; hold this method to its own contract.
	if d := ix.memberGrid.NearestDist(p, maxReach); d <= maxReach {
		return d
	}
	return math.Inf(1)
}

// Regions returns the detected regions ordered by stable ID. Callers must
// not mutate.
func (ix *Index) Regions() []*Region {
	if ix == nil {
		return nil
	}
	return ix.regions
}

var neigh8 = [8][2]int{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}}

const shortFragM = 400.0 // grade-interruption forgiveness cap

// effectiveLevels smooths grade interruptions: OSM chops a corridor at
// every over- and underpass and tags the fragment bridge/tunnel, and the
// level gate then reads the corridor as ending there — the Bushwick
// branch lost its entrance to a tunnel=yes dip, and Jamaica station
// shattered into shards at every bridge=yes street crossing, minting
// bogus entrances mid-station. A SHORT fragment adopts the length-
// weighted majority level of its endpoint neighbours; iterated, chains
// of bridge fragments heal inward from their surface ends. A genuinely
// stacked corridor (the E/F under Sunnyside, the AirTrain viaduct) has
// same-level neighbours everywhere and keeps its own.
func effectiveLevels(tracks []Track) []int {
	eff := make([]int, len(tracks))
	type endRef struct{ ti int }
	ends := map[[2]int64][]endRef{}
	key := func(p geo.Pt) [2]int64 {
		return [2]int64{int64(math.Round(p.X * 2)), int64(math.Round(p.Y * 2))} // 0.5 m
	}
	for ti := range tracks {
		eff[ti] = tracks[ti].Level
		pts := tracks[ti].Line.Pts
		if len(pts) == 0 {
			continue
		}
		for _, p := range []geo.Pt{pts[0], pts[len(pts)-1]} {
			ends[key(p)] = append(ends[key(p)], endRef{ti})
		}
	}
	for pass := 0; pass < 3; pass++ {
		next := append([]int{}, eff...)
		changed := false
		for ti := range tracks {
			t := &tracks[ti]
			if t.Line.Len() >= shortFragM || len(t.Line.Pts) == 0 {
				continue
			}
			pts := t.Line.Pts
			wByLvl := map[int]float64{}
			for _, p := range []geo.Pt{pts[0], pts[len(pts)-1]} {
				for _, r := range ends[key(p)] {
					if r.ti != ti {
						wByLvl[eff[r.ti]] += tracks[r.ti].Line.Len()
					}
				}
			}
			if len(wByLvl) == 0 {
				continue
			}
			lvls := make([]int, 0, len(wByLvl))
			for l := range wByLvl {
				lvls = append(lvls, l)
			}
			sort.Ints(lvls)
			best, bestW := eff[ti], -1.0
			for _, l := range lvls {
				if wByLvl[l] > bestW {
					best, bestW = l, wByLvl[l]
				}
			}
			if best != eff[ti] {
				next[ti] = best
				changed = true
			}
		}
		eff = next
		if !changed {
			break
		}
	}
	return eff
}

// Build runs the detector. Deterministic by construction: samples walk
// tracks in input order, mask cells are sorted before labeling, components
// are discovered at their lexicographically-smallest cell, and every
// set-shaped intermediate is sorted before it reaches a Region.
func Build(tracks []Track, p Params) *Index {
	lines := make([]*geo.Line, len(tracks))
	for i := range tracks {
		lines[i] = tracks[i].Line
	}
	grid := geo.NewGrid(lines, 64)
	eff := effectiveLevels(tracks)

	// Score pass: parallel-proximity per ~20 m sample. hotCells is the
	// pre-dilation footprint; hots feeds the per-component stats.
	type hotSample struct {
		ti    int
		arc   float64
		score float64
		pitch float64
	}
	var hots []hotSample
	hotCells := map[[2]int]bool{}
	var cand []int
	for ti := range tracks {
		t := &tracks[ti]
		total := t.Line.Len()
		if total < 1e-9 {
			continue
		}
		tagged := svcYard(t.Service)
		n := int(math.Max(1, math.Round(total/sampleStepM)))
		pitch := total / float64(n)
		for k := 0; k < n; k++ {
			s := (float64(k) + 0.5) * pitch
			pt := t.Line.AtArc(s)
			cand = cand[:0]
			grid.Near(pt, p.ParReach, func(li int) {
				if li != ti && eff[li] == eff[ti] {
					cand = append(cand, li)
				}
			})
			// Early-outs make rural single track one near-empty grid
			// probe. A line CAN cross a section twice (a loop), so the
			// count bound is approximate — acceptable: a lone looping
			// lead is not a yard.
			if len(cand) == 0 || (!tagged && float64(len(cand)) < p.Hot) {
				continue
			}
			tan := t.Line.TangentAtArc(s, tangentWinM)
			score := 0.0
			for _, li := range cand {
				for _, c := range tracks[li].Line.CrossSectionNear(pt, tan, p.ParReach) {
					if c.Parallel < minParallel {
						continue
					}
					if off := math.Abs(c.Offset); off <= p.ParNear {
						score++
					} else if off < p.ParReach {
						score += (p.ParReach - off) / (p.ParReach - p.ParNear)
					}
				}
			}
			if score < p.Hot && !(tagged && score >= seedScore) {
				continue
			}
			hots = append(hots, hotSample{ti, s, score, pitch})
			// Rasterize the sample's arc span so footprints have no gaps
			// along the track; 8-connected dilation bridges the corners.
			lo, hi := s-pitch/2, s+pitch/2
			for a := lo; ; a += cellM / 2 {
				if a > hi {
					a = hi
				}
				hotCells[cellKey(t.Line.AtArc(a))] = true
				if a >= hi {
					break
				}
			}
		}
	}

	// Dilate one ring: interior points between ladder tracks are yard too.
	mask := make(map[[2]int]bool, 2*len(hotCells))
	for c := range hotCells {
		mask[c] = true
		for _, d := range neigh8 {
			mask[[2]int{c[0] + d[0], c[1] + d[1]}] = true
		}
	}

	// Connected components, deterministically: seeds in sorted cell order,
	// so each component is discovered at its smallest cell and component
	// order IS min-cell order.
	keys := make([][2]int, 0, len(mask))
	for c := range mask {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	comp := make(map[[2]int]int32, len(mask))
	var comps [][][2]int
	for _, seed := range keys {
		if _, done := comp[seed]; done {
			continue
		}
		id := int32(len(comps))
		comp[seed] = id
		stack := [][2]int{seed}
		var cells [][2]int
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cells = append(cells, c)
			for _, d := range neigh8 {
				nc := [2]int{c[0] + d[0], c[1] + d[1]}
				if mask[nc] {
					if _, ok := comp[nc]; !ok {
						comp[nc] = id
						stack = append(stack, nc)
					}
				}
			}
		}
		comps = append(comps, cells)
	}

	// Per-component evidence from the hot samples.
	type stat struct {
		tagM, plainM, peak float64
		ways               map[int]float64   // track idx → hot arc inside
		arcs               map[int][]float64 // track idx → hot sample arcs (for clipping)
		lvl                map[int]float64   // effective level → hot arc
	}
	stats := make([]stat, len(comps))
	for _, h := range hots {
		id, ok := comp[cellKey(tracks[h.ti].Line.AtArc(h.arc))]
		if !ok {
			continue
		}
		st := &stats[id]
		if svcYard(tracks[h.ti].Service) {
			st.tagM += h.pitch
		} else {
			st.plainM += h.pitch
		}
		if h.score > st.peak {
			st.peak = h.score
		}
		if st.ways == nil {
			st.ways = map[int]float64{}
			st.arcs = map[int][]float64{}
			st.lvl = map[int]float64{}
		}
		st.ways[h.ti] += h.pitch
		st.arcs[h.ti] = append(st.arcs[h.ti], h.arc)
		st.lvl[eff[h.ti]] += h.pitch
	}

	ix := &Index{
		cellRegion: map[[2]int]int32{},
		boundary:   map[[2]int]int32{},
		svc:        map[string]string{},
		regionWay:  map[string]bool{},
	}
	for i := range tracks {
		if tracks[i].Service != "" {
			ix.svc[tracks[i].ID] = tracks[i].Service
		}
	}

	// Keep a component when tagged steel anchors it (enough tagged arc and
	// total mass — the 4-track tagged yard whose geometric score alone
	// would miss) or when the density alone is unmistakable (peak beyond
	// any revenue trunk, enough untagged mass — the unmapped freight
	// complex). Lone sidings fail both.
	var memberLines []*geo.Line
	for ci, cells := range comps {
		st := &stats[ci]
		keep := (st.tagM >= p.MinTagM && st.tagM+st.plainM >= p.MinMassM) ||
			(st.peak >= p.PeakUntag && st.plainM >= p.MinMassM)
		if !keep {
			continue
		}
		rid := int32(len(ix.regions))
		r := &Region{ID: int(rid), Peak: st.peak}
		lvls := make([]int, 0, len(st.lvl))
		for l := range st.lvl {
			lvls = append(lvls, l)
		}
		sort.Ints(lvls)
		bestArc := math.Inf(-1)
		for _, l := range lvls {
			if st.lvl[l] > bestArc {
				bestArc, r.Level = st.lvl[l], l
			}
		}
		tis := make([]int, 0, len(st.ways))
		for ti := range st.ways {
			tis = append(tis, ti)
		}
		sort.Ints(tis)
		// The outline hugs the region's HOT steel: each member way is
		// clipped to its hot arc spans — a through mainline is a member
		// only where it runs the ladder, and its distant kilometres must
		// not drag the hull along the whole line.
		var steel []*geo.Line
		for _, ti := range tis {
			r.WayIDs = append(r.WayIDs, tracks[ti].ID)
			r.TrackLen += st.ways[ti]
			ix.regionWay[tracks[ti].ID] = true
			memberLines = append(memberLines, tracks[ti].Line)
			l := tracks[ti].Line
			total := l.Len()
			pitch := total / math.Max(1, math.Round(total/sampleStepM))
			arcs := st.arcs[ti] // ascending: hots walk each track in order
			a0 := arcs[0]
			prev := arcs[0]
			flush := func(hi float64) {
				// span ends taper: the score falls under hot a little
				// before the steel actually ends, and a hull cutting a
				// ladder mid-track reads as tracks escaping the shape
				sub := subPts(l, math.Max(0, a0-hullTailM), math.Min(total, hi+hullTailM))
				if len(sub) >= 2 {
					steel = append(steel, geo.NewLine(sub))
				}
			}
			for _, a := range arcs[1:] {
				// a way cool in its middle is still one member — only a
				// real departure (the same mainline hot again at the far
				// side of a different ladder) starts a new span
				if a-prev > math.Max(2.5*pitch, hullFillM) {
					flush(prev)
					a0 = a
				}
				prev = a
			}
			flush(prev)
		}
		sort.Strings(r.WayIDs)
		r.Outline = hullOutline(steel, outlinePadM)
		if len(r.Outline) < 3 {
			// degenerate contour: fall back to the raster trace so the
			// region still has a consistent footprint
			cm := make(map[[2]int]bool, len(cells))
			for _, c := range cells {
				cm[c] = true
			}
			r.Outline = traceOutline(cm, cellM)
		}
		interior, bound := classifyCells(r.Outline, hullSeeds(steel))
		iks := make([][2]int, 0, len(interior))
		for c := range interior {
			iks = append(iks, c)
		}
		sort.Slice(iks, func(i, j int) bool {
			if iks[i][0] != iks[j][0] {
				return iks[i][0] < iks[j][0]
			}
			return iks[i][1] < iks[j][1]
		})
		for _, c := range iks {
			ix.cellRegion[c] = rid
		}
		bks := make([][2]int, 0, len(bound))
		for c := range bound {
			bks = append(bks, c)
		}
		sort.Slice(bks, func(i, j int) bool {
			if bks[i][0] != bks[j][0] {
				return bks[i][0] < bks[j][0]
			}
			return bks[i][1] < bks[j][1]
		})
		for _, c := range bks {
			if _, taken := ix.boundary[c]; !taken {
				ix.boundary[c] = rid
			}
		}
		ix.regions = append(ix.regions, r)
	}
	if len(memberLines) > 0 {
		ix.memberGrid = geo.NewGrid(memberLines, 64)
	}
	if len(ix.regions) > 0 {
		ix.buildEntrancesAndSpines(tracks, eff)
	}
	return ix
}
