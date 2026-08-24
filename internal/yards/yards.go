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

// Region is one detected yard: a connected patch of parallel-track density.
type Region struct {
	ID        int
	Outline   []geo.Pt // closed CCW ring, first vertex not repeated
	TrackLen  float64  // member steel hot arc inside the region (m)
	Peak      float64  // max parallel-proximity score seen
	WayIDs    []string // ways with hot samples inside, sorted
	Entrances []Entrance
	Spines    []Spine
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
	regions     []*Region
	regionCells []map[[2]int]bool // per region, the dilated mask cells
	cellRegion  map[[2]int]int32
	svc         map[string]string // wayID → service tag, "" absent
	regionWay   map[string]bool   // wayID → hot member of some region
	memberGrid  *geo.Grid
}

func cellKey(p geo.Pt) [2]int {
	return [2]int{int(math.Floor(p.X / cellM)), int(math.Floor(p.Y / cellM))}
}

// InYard reports whether p lies inside a yard region (dilated footprint —
// interior points between ladder tracks count). Consumers must read it as
// "suppress yard heuristics here", never "drop this geometry": revenue
// mainlines run through Sunnyside and stay revenue.
func (ix *Index) InYard(p geo.Pt) bool {
	if ix == nil {
		return false
	}
	_, ok := ix.cellRegion[cellKey(p)]
	return ok
}

// RegionAt returns the region containing p, nil outside every region.
func (ix *Index) RegionAt(p geo.Pt) *Region {
	if ix == nil {
		return nil
	}
	id, ok := ix.cellRegion[cellKey(p)]
	if !ok {
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
				if li != ti && tracks[li].Level == t.Level {
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
		ways               map[int]float64 // track idx → hot arc inside
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
		}
		st.ways[h.ti] += h.pitch
	}

	ix := &Index{
		cellRegion: map[[2]int]int32{},
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
		cm := make(map[[2]int]bool, len(cells))
		for _, c := range cells {
			ix.cellRegion[c] = rid
			cm[c] = true
		}
		tis := make([]int, 0, len(st.ways))
		for ti := range st.ways {
			tis = append(tis, ti)
		}
		sort.Ints(tis)
		for _, ti := range tis {
			r.WayIDs = append(r.WayIDs, tracks[ti].ID)
			r.TrackLen += st.ways[ti]
			ix.regionWay[tracks[ti].ID] = true
			memberLines = append(memberLines, tracks[ti].Line)
		}
		sort.Strings(r.WayIDs)
		ix.regionCells = append(ix.regionCells, cm)
		ix.regions = append(ix.regions, r)
	}
	if len(memberLines) > 0 {
		ix.memberGrid = geo.NewGrid(memberLines, 64)
	}
	return ix
}
