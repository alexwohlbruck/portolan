// Package bundle groups parallel tracks and computes their centerlines —
// the crown jewel of the pipeline. The centerline of a bundle is the MEDIAN
// STRAND of its cross-sections (the owner's hand-drawing rules), refined
// iteratively with exact perpendicular intersections and curve-following
// probes. No raster, no nearest-point projection, no heavy blur.
package bundle

import (
	"fmt"
	"math"
	"os"
	"sort"
	"sync"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// dbgCPt: PORTOLAN_DBGC="x,y" — Refine prints cross-section votes near it.
var dbgCPt *geo.Pt

func init() {
	if v := os.Getenv("PORTOLAN_DBGC"); v != "" {
		var x, y float64
		fmt.Sscanf(v, "%f,%f", &x, &y)
		dbgCPt = &geo.Pt{X: x, Y: y}
	}
}

// Params are the centerline dials. Defaults are the values that survived
// production tuning in attempt two (see docs/LESSONS.md).
type Params struct {
	Reach       float64 // cross-section half-length (m); membership radius
	StrandGap   float64 // offsets farther apart than this are distinct tracks
	SpanProbe   float64 // membership persistence probe distance along arc (m)
	ThroughFrac float64 // member must be present along this fraction to vote
	Iters       int     // refinement iterations
	Damp        float64 // per-iteration move damping
	Step        float64 // working vertex spacing (m)
	OffsetSigma float64 // gaussian over the OFFSET SERIES, in samples
	FinishSigma float64 // final light low-pass over geometry (m)
	MinParallel float64 // |cos| heading agreement for a crossing to count
	SlopeMax    float64 // max lateral-offset slope (m per m of arc)
	FreeStart   bool    // unpin the start (terminus: end rides the group center)
	FreeEnd     bool    // unpin the end
	// SwitchTolerant enables the street-running vote rules: crossover
	// diagonals (laterally migrating passes) are barred from the median,
	// sections with less strand structure than their neighborhood bridge
	// straight, and free ends hold the last stable correction. Metro keeps
	// the production-tuned behavior until the all-modes centering session.
	SwitchTolerant bool
}

func DefaultParams() Params {
	return Params{
		Reach:       25.0,
		StrandGap:   4.5,
		SpanProbe:   75.0,
		ThroughFrac: 0.8,
		Iters:       5,
		Damp:        0.8,
		Step:        6.0,
		OffsetSigma: 5.0,
		// 2.0, not the old 8.0: the finish pass now smooths HEADINGS
		// (geo.SmoothTurning), which targets a weld kink directly — a
		// kink is a step in the heading series — instead of dragging
		// every position toward its chord. It needs a quarter the sigma
		// to do the same job, and the erosion that sigma still causes
		// scales with its square, so an R17 corner loses ~0.1 m where 8.0
		// in the position domain lost ~1.9 m and deadlocked refinement.
		// Measured: NYC jaggedness spikes 1 -> 0, wobble p90 4.9 -> 4.8 m,
		// Chicago SW corner centering error 2.63 -> 0.30 m.
		FinishSigma: 2.0,
		MinParallel: 0.82, // ~35°
		SlopeMax:    0.08, // max lateral-offset slope (m per m of arc)
	}
}

// MedianStrand applies the hand-drawing rules to sorted strand centers:
// 1 → follow, 2 → midpoint, 3 → center, 4 → middle-two midpoint, >4 → drop
// the outermost pair (yard edges) and reapply.
func MedianStrand(centers []float64) float64 {
	k := len(centers)
	for k > 4 {
		centers = centers[1 : k-1]
		k = len(centers)
	}
	if k == 0 {
		return 0
	}
	if k%2 == 1 {
		return centers[k/2]
	}
	return (centers[k/2-1] + centers[k/2]) / 2
}

// strandVote is one cross-section crossing: its signed offset and the
// length of the member strand that cast it (kept alongside the offset
// so future vote-weighing experiments have the data at the median).
type strandVote struct{ off, ln float64 }

// strandVotes clusters votes into distinct tracks exactly like Strands
// and returns each cluster's center plus the LONGEST member length that
// voted in it, both sorted by center.
func strandVotes(votes []strandVote, strandGap float64) ([]float64, []float64) {
	if len(votes) == 0 {
		return nil, nil
	}
	sort.Slice(votes, func(i, j int) bool { return votes[i].off < votes[j].off })
	var centers, lens []float64
	sum, n, maxLn := 0.0, 0, 0.0
	flush := func() {
		if n > 0 {
			centers = append(centers, sum/float64(n))
			lens = append(lens, maxLn)
		}
	}
	prev := votes[0].off
	sum, n, maxLn = votes[0].off, 1, votes[0].ln
	for _, v := range votes[1:] {
		if v.off-prev > strandGap {
			flush()
			sum, n, maxLn = 0, 0, 0
		}
		sum += v.off
		n++
		if v.ln > maxLn {
			maxLn = v.ln
		}
		prev = v.off
	}
	flush()
	return centers, lens
}

// Strands clusters sorted signed offsets into distinct tracks (gap > strandGap
// starts a new strand) and returns each strand's center, sorted.
func Strands(offsets []float64, strandGap float64) []float64 {
	if len(offsets) == 0 {
		return nil
	}
	sort.Float64s(offsets)
	var centers []float64
	start, sum, n := 0, 0.0, 0
	flush := func() {
		if n > 0 {
			centers = append(centers, sum/float64(n))
		}
	}
	_ = start
	prev := offsets[0]
	sum, n = offsets[0], 1
	for _, o := range offsets[1:] {
		if o-prev > strandGap {
			flush()
			sum, n = 0, 0
		}
		sum += o
		n++
		prev = o
	}
	flush()
	return centers
}

// Refine runs iterative cross-section refinement of a centerline against its
// member tracks. It is the direct port of the algorithm that fixed the
// Tribeca windows, with every hard-won rule baked in:
//
//   - exact perpendicular intersections (geo.CrossSection), never projection
//   - span-persistence probes walk THE ARC of the current centerline
//   - through-member scoping: a member present along < ThroughFrac of the
//     line is a fork ramp and does not vote
//   - median strand per cross-section (MedianStrand), never a weighted mean
//   - short median filter + gaussian over the OFFSET SERIES so strand-count
//     changes ramp instead of stepping
//   - damped moves, endpoints pinned, light FinishSigma only
func Refine(cl *geo.Line, members []*geo.Line, p Params) *geo.Line {
	if len(cl.Pts) < 3 || len(members) == 0 {
		return cl
	}
	preThrough := len(members)
	if !p.SwitchTolerant {
		members = throughMembers(cl, members, p)
	}
	if dbgCPt != nil && cl.DistTo(*dbgCPt) < 60 {
		fmt.Printf("REFCALL len=%.0f members pre=%d post=%d lens=", cl.Len(), preThrough, len(members))
		for _, m := range members {
			fmt.Printf("%.0f ", m.Len())
		}
		fmt.Println()
	}
	// corridor steel that BREATHES: a through member can drift past Reach
	// for a stretch (the LIRR's Woodside bend spreads its two track
	// groups to ~31 m between converged ends) — with a fixed gate its
	// vote flickers in and out along that stretch and the median weaves
	// between "one group" and "pair midpoint" regimes, drawn as
	// track-switching wobble. Where a member holds a SUSTAINED run
	// alongside (≥breatheRun within 2×Reach, half of it within Reach)
	// its gate widens to the run's measured envelope so it votes through
	// the whole swell and the median settles on one smooth group center.
	// Divergers (fork ramps, loop limbs, corridors that only kiss) never
	// build such a run past 2×Reach and keep the tight gate.
	type breathe struct{ a0, a1, r float64 }
	widened := make([][]breathe, len(members))
	maxReach := p.Reach
	if !p.SwitchTolerant {
		const breatheDS = 24.0
		const breatheRun = 150.0 // min bracketed-swell span worth widening
		base := cl.Resample(breatheDS)
		if len(base) >= 3 {
			for mi, m := range members {
				capD := 2 * p.Reach
				// distance profile of this member along the edge, then
				// swells = spans BRACKETED BY CONVERGENCE: from one
				// within-Reach sample to the next, drifting past Reach
				// (but under the cap) in between. A lens converges at
				// both former junctions so its swell is bracketed; a
				// concentric loop (South Ferry) holds a sustained band
				// that never closes, and widening for those dragged the
				// loop medians into 28-34° kinks.
				prof := make([]float64, len(base))
				for bi, q := range base {
					if d, ok := m.DistToCapped(q, capD); ok {
						prof[bi] = d
					} else {
						prof[bi] = capD + 1
					}
				}
				last := -1 // previous within-Reach sample
				swell0, swellMax := -1, 0.0
				for bi, d := range prof {
					if d > capD {
						last, swell0, swellMax = -1, -1, 0
						continue
					}
					if d > p.Reach {
						if last >= 0 && swell0 < 0 {
							swell0, swellMax = last, 0
						}
						if d > swellMax {
							swellMax = d
						}
						continue
					}
					if swell0 >= 0 && swellMax > p.Reach-5 {
						span := float64(bi-swell0) * breatheDS
						if span >= breatheRun {
							r := math.Min(capD, swellMax+5)
							widened[mi] = append(widened[mi], breathe{
								a0: float64(swell0) * breatheDS,
								a1: float64(bi) * breatheDS,
								r:  r,
							})
							if r > maxReach {
								maxReach = r
							}
						}
					}
					last, swell0, swellMax = bi, -1, 0
				}
			}
		}
	}
	// interval edges TAPER over breatheTaper of arc — a hard boundary
	// flips the member's vote set in one sample and prints a kink where
	// the regimes meet (an 8° elbow at the Sunnyside throat).
	const breatheTaper = 150.0
	reachAt := func(mi int, arc float64) float64 {
		r := p.Reach
		for _, b := range widened[mi] {
			if arc >= b.a0 && arc <= b.a1 {
				return b.r
			}
			if arc < b.a0 && b.a0-arc < breatheTaper {
				t := (b.a0 - arc) / breatheTaper
				if v := b.r + t*(p.Reach-b.r); v > r {
					r = v
				}
			} else if arc > b.a1 && arc-b.a1 < breatheTaper {
				t := (arc - b.a1) / breatheTaper
				if v := b.r + t*(p.Reach-b.r); v > r {
					r = v
				}
			}
		}
		return r
	}
	cur := cl
	for it := 0; it < p.Iters; it++ {
		line := geo.NewLine(cur.Densify(p.Step))
		pts := line.Pts
		n := len(pts)
		arcOf := line.ArcTable() // the identical cumulative sum, already built
		offStar := make([]float64, n)
		has := make([]bool, n)
		// per-sample work reads only immutable state and writes its own
		// index; strand counts land by index too and are consumed as a
		// sorted multiset (medianInt), so the fan-out is bit-identical
		countAt := make([]int, n)
		widthAt := make([]float64, n)
		var offsPool sync.Pool
		lo, hi := 1, n-1
		if p.FreeStart {
			lo = 0
		}
		if p.FreeEnd {
			hi = n
		}
		geo.ParFor(lo, hi, func(i int) {
			a, b := i-1, i+1
			if a < 0 {
				a = 0
			}
			if b > n-1 {
				b = n - 1
			}
			tan := pts[b].Sub(pts[a]).Unit()
			// curve-following persistence probes (LESSONS #3). Probe spans
			// shrink to the arc actually available toward each end — the
			// old fixed span compared a clamped centerline probe against an
			// UNCLAMPED member point, so a rail running dead parallel past
			// a terminus failed the guard by construction and every line
			// end snapped onto its one ridden rail. Directionality is
			// preserved: the span only shrinks on the side where the line
			// itself ends, so a turnaround arc (South Ferry) still has to
			// hold the full corridor span on the inbound side.
			total := arcOf[n-1]
			da, db := p.SpanProbe, p.SpanProbe
			if p.FreeStart {
				da = math.Min(p.SpanProbe, arcOf[i])
			}
			if p.FreeEnd {
				db = math.Min(p.SpanProbe, total-arcOf[i])
			}
			pa := line.AtArc(arcOf[i] - da)
			pb := line.AtArc(arcOf[i] + db)
			var offs []strandVote
			if v := offsPool.Get(); v != nil {
				offs = (*v.(*[]strandVote))[:0]
			}
			dbgHere := dbgCPt != nil && pts[i].Dist(*dbgCPt) < 40 && it == 0
			var mine []float64 // this member's crossings, before per-track collapse
			for mi, m := range members {
				mr := reachAt(mi, arcOf[i])
				if !m.Within(pts[i], mr) {
					if dbgHere {
						fmt.Printf("REFC3 i=%d member %d len=%.0f SKIP not-within\n", i, mi, m.Len())
					}
					continue
				}
				if !m.Within(pa, mr) || !m.Within(pb, mr) {
					if dbgHere {
						fmt.Printf("REFC3 i=%d member %d len=%.0f SKIP kiss-guard pa=%v pb=%v\n", i, mi, m.Len(), m.Within(pa, mr), m.Within(pb, mr))
					}
					continue // kiss guard: not persistent alongside
				}
				// kiss rule PER PASS: a chained strand can cross the
				// section several times (both directional tracks, a loop
				// limb, a terminal return). Each crossing votes only if
				// THAT pass stays alongside the centerline for the probe
				// span — walk the member's own arc from the crossing and
				// require both probes to land near the centerline's probe
				// points. Corridor tracks persist; turnaround limbs curve
				// away and are dropped exactly where they diverge.
				for _, c := range m.CrossSectionNear(pts[i], tan, mr) {
					if c.Parallel < p.MinParallel {
						if dbgHere {
							fmt.Printf("REFC3 i=%d member %d SKIP parallel=%.2f\n", i, mi, c.Parallel)
						}
						continue
					}
					qa := m.AtArc(c.Arc - da)
					qb := m.AtArc(c.Arc + db)
					near := func(q geo.Pt) bool {
						return q.Dist(pa) < mr*1.5 ||
							q.Dist(pb) < mr*1.5
					}
					// switch tolerance: a crossover diagonal passes the
					// parallel test (a switch is only ~10-15 deg off the
					// corridor) but is LATERALLY MIGRATING — its votes sweep
					// from one rail to the other and drag the median into an
					// S on dead-straight track (the Gold at Sunnyside, at
					// both its crossovers). A corridor track holds its
					// offset; measure the drift over +-30 m of the member's
					// own arc and drop movers.
					// a member too short to measure drift across is switch
					// furniture, not a corridor rail — its clamped probe
					// ends land ON the two rails and read as zero drift
					if p.SwitchTolerant && (c.Arc < 30 || m.Len()-c.Arc < 30) {
						if dbgHere {
							fmt.Printf("REFC3 i=%d member %d SKIP short-arc\n", i, mi)
						}
						continue
					}
					qd1 := m.AtArc(c.Arc - 30)
					qd2 := m.AtArc(c.Arc + 30)
					var dd1, dd2 float64
					var ok1, ok2 bool
					if p.SwitchTolerant {
						dd1, ok1 = line.DistToCapped(qd1, p.Reach)
						dd2, ok2 = line.DistToCapped(qd2, p.Reach)
					}
					// a mover is a SWITCH only if it converges onto the
					// ridden line: a crossover diagonal ends ON a rail, so
					// one probe reads ~0. A street couplet whose separation
					// BREATHES (Charlotte's Gold swings 4→10→5 m through
					// downtown corners) drifts >3 m without ever
					// approaching — dropping it flapped the vote set
					// {pair}→{own}→{pair} block after block and sawtoothed
					// the median at ±5 m (clt_squiggle_2).
					if p.SwitchTolerant && ok1 && ok2 && math.Abs(dd1-dd2) > 3 &&
						math.Min(dd1, dd2) < 2.5 {
						if dbgHere {
							fmt.Printf("REFC3 i=%d member %d SKIP switch-drift %.1f->%.1f\n", i, mi, dd1, dd2)
						}
						continue
					}
					if near(qa) && near(qb) {
						mine = append(mine, c.Offset)
					} else if dbgHere {
						fmt.Printf("REFC3 i=%d member %d SKIP per-pass qa=%v qb=%v off=%.1f\n", i, mi, near(qa), near(qb), c.Offset)
					}
				}
				// ONE VOTE PER MEMBER PER TRACK. A straight cross-section
				// through a curving track crosses it more than once (and a
				// polyline that wobbles across the section does too), so a
				// single physical track cast 2-3 votes and outweighed its
				// neighbours: the SW Loop corner read {-4.4, 0, 0} and
				// centred at -1.5 — a third of the way across the pair,
				// hugging one rail — where the true midpoint is -2.2. The
				// duplicates are also what made the strand count flicker
				// (2 -> 1 -> 2) as the measured gap crossed StrandGap.
				// Cluster this member's own crossings and cast one vote
				// per cluster: duplicates collapse, while a genuine second
				// pass (a balloon limb, the far leg of a loop) sits far
				// beyond StrandGap and still votes separately. With the
				// duplicates gone, a merged pair's mean IS its midpoint,
				// so the residual count flicker no longer moves the line.
				if len(mine) > 0 {
					sort.Float64s(mine)
					sum, n := 0.0, 0
					prev := math.Inf(-1)
					for _, o := range mine {
						if n > 0 && o-prev > p.StrandGap {
							offs = append(offs, strandVote{off: sum / float64(n), ln: m.Len()})
							sum, n = 0, 0
						}
						sum += o
						n++
						prev = o
					}
					if n > 0 {
						offs = append(offs, strandVote{off: sum / float64(n), ln: m.Len()})
					}
					mine = mine[:0]
				}
			}
			if len(offs) == 0 {
				offsPool.Put(&offs)
				return
			}
			// NOTE: a long-haul precedence filter was tried here (strands
			// backed by ≥3 km members outvote yard leads when ≥2 are
			// present) and REVERTED: the filter is binary per section, so
			// at arcs where a long strand's crossing count flips the vote
			// set snaps between filtered and unfiltered — a fresh flicker
			// source (4th Ave 26°→34° spikes, new 28° at Battery) for no
			// measured gain at Woodside. Don't retry without hysteresis.
			st, _ := strandVotes(offs, p.StrandGap)
			countAt[i] = len(st)
			if len(st) > 1 {
				lo2, hi2 := st[0], st[0]
				for _, v := range st {
					lo2 = math.Min(lo2, v)
					hi2 = math.Max(hi2, v)
				}
				widthAt[i] = hi2 - lo2
			}
			o := MedianStrand(st)
			offStar[i] = math.Max(-maxReach, math.Min(maxReach, o))
			has[i] = true
			if dbgCPt != nil && pts[i].Dist(*dbgCPt) < 40 {
				fmt.Printf("REFC3 it=%d i=%d votes=%v strands=%v o=%.1f\n", it, i, offs, st, o)
			}
			offsPool.Put(&offs)
		})
		// a section left with LESS strand structure than its neighborhood
		// is inside a switch window (a mover's votes were dropped, or the
		// swap hides a rail from the perpendicular): bridge it from the
		// stable regimes on both sides rather than follow the one rail
		// that remains — on straight track the bridge IS the straight
		// centerline.
		// a terminus throat is switch furniture end to end — crossovers,
		// funnels, stub ends. Inside the last 60 m of a FREE end the
		// remaining votes are whichever rail's strand happens to survive
		// the X, and a whole tail regime of count-1 defeats the
		// neighborhood test below. Blank it; the hold-forward fill then
		// runs the corridor's stable centered offset straight to the tip.
		if p.SwitchTolerant {
			total := arcOf[n-1]
			for i := range has {
				if p.FreeStart && arcOf[i] < 60 {
					has[i] = false
				}
				if p.FreeEnd && total-arcOf[i] < 60 {
					has[i] = false
				}
			}
		}
		if p.SwitchTolerant {
			win := 10
			was := append([]bool(nil), has...)
			for i := 1; i < n-1; i++ {
				if !was[i] {
					continue
				}
				lo, hi := i-win, i+win
				if lo < 1 {
					lo = 1
				}
				if hi > n-2 {
					hi = n - 2
				}
				var nb []int
				for k := lo; k <= hi; k++ {
					if k != i && was[k] {
						nb = append(nb, countAt[k])
					}
				}
				if len(nb) >= 4 && countAt[i] < medianInt(nb) {
					has[i] = false
				}
			}
		}
		// PINCH BRIDGING, by group width: a street couplet narrows to
		// cross every intersection interlaced — approach ramp, crossing,
		// recede can span 150-250 m, longer than any fixed neighborhood
		// window, and the tracked pair-center faithfully bends around
		// the whole thing. The corridor's TRUE width is an edge-global
		// statistic (p75); runs where the group narrows below 60% of it
		// are switch furniture — blank them (extended to 85%-width
		// shoulders) and let fillGaps run the stable regimes' straight
		// line through. Runs past 400 m are a real convergence and keep.
		if p.SwitchTolerant {
			var ws []float64
			for i := range has {
				if has[i] && widthAt[i] > 0 {
					ws = append(ws, widthAt[i])
				}
			}
			if len(ws) >= 8 {
				sort.Float64s(ws)
				wide := ws[len(ws)*3/4]
				if wide > 4 {
					thresh := 0.6 * wide
					for i := 0; i < n; {
						if !has[i] || widthAt[i] >= thresh {
							i++
							continue
						}
						j := i
						for j < n && (!has[j] || widthAt[j] < thresh) {
							j++
						}
						a, b := i, j
						for a > 0 && has[a-1] && widthAt[a-1] < 0.85*wide {
							a--
						}
						for b < n-1 && has[b] && widthAt[b] < 0.85*wide {
							b++
						}
						if arcOf[min(b, n-1)]-arcOf[a] < 400 {
							for k := a; k < b; k++ {
								has[k] = false
							}
						}
						i = j + 1
					}
				}
			}
		}
		var strandCounts []int
		for i := 1; i < n-1; i++ {
			if has[i] {
				strandCounts = append(strandCounts, countAt[i])
			}
		}
		// street vote sets flap at threshold cliffs — a couplet partner
		// breathing across the roadway gauge, a far rail sliding along the
		// reach boundary — with 10–30 m periods that the ±12 m window
		// passes straight through (Berlin M1 read [0,16.8]→[0]→[0,16.8]
		// and drew an 8 m sawtooth). A ±60 m majority window kills the
		// alternation while leaving regime CHANGES (real divergences,
		// which hold their new offset for hundreds of metres) intact;
		// slopeLimit below ramps whatever step survives.
		medianW := 2
		if p.SwitchTolerant {
			medianW = 10
		}
		filt := medianFilter(offStar, has, medianW)
		// offset must never exceed the base line's local turn radius:
		// street pair-centering can ask for 8-10 m of lateral move, and
		// carrying that through a junction-mouth corner tighter than the
		// offset FOLDS the polyline back over itself (Berlin M1 at
		// Eberswalder drew 175° reversal knots from exactly this — MATCH
		// clean, votes stable, geometry folded). Clamp to 0.8R; the
		// gaussian and slope limit below ramp the clamped pockets.
		if p.SwitchTolerant {
			for i := 1; i < n-1; i++ {
				if !has[i] || filt[i] == 0 {
					continue
				}
				a, b, c := pts[i-1], pts[i], pts[i+1]
				ab, bc, ca := a.Dist(b), b.Dist(c), c.Dist(a)
				// 4*area via cross product; R = abc/(4K), huge when collinear
				k4 := 2 * math.Abs((b.X-a.X)*(c.Y-a.Y)-(b.Y-a.Y)*(c.X-a.X))
				if k4 < 1e-9 {
					continue
				}
				if lim := 0.8 * ab * bc * ca / k4; math.Abs(filt[i]) > lim {
					filt[i] = math.Copysign(lim, filt[i])
				}
			}
		}
		// Stiffness scales with corridor width. On a wide interlocking
		// (W 4 St: two stacked 4-track levels flattened to 2D) the strand
		// count flickers as tracks interleave and every flicker steps the
		// median half a track spacing — at the base sigma that survives as
		// a visible S-wobble. The centerline of a wide bundle is
		// structurally straighter than any of its tracks, so smooth the
		// offset SERIES harder the more strands the sections carry; this
		// preserves constant corrections (and therefore real curves) and
		// leaves 1–2-track corridors at the base sigma.
		sigma := p.OffsetSigma
		if k := medianInt(strandCounts); k > 2 {
			sigma *= float64(k) / 2
		}
		filt, has = gaussianSeries(filt, has, sigma)
		// a drawn line eases lateral shifts. Two step sources: the median
		// jumping when strand membership changes at a divergence, and
		// coverage boundaries (has flipping) where a moved sample abuts a
		// pinned one. Fill coverage gaps by interpolation (uncovered ends
		// ramp to zero) and slope-limit the WHOLE series so every lateral
		// correction eases in at a bounded grade.
		filt = fillGaps(filt, has, p.FreeStart && p.SwitchTolerant, p.FreeEnd && p.SwitchTolerant)
		filt = slopeLimit(filt, nil, p.SlopeMax*p.Step)
		out := make([]geo.Pt, n)
		copy(out, pts)
		moved := 0.0
		for i := lo; i < hi; i++ {
			a, b := i-1, i+1
			if a < 0 {
				a = 0
			}
			if b > n-1 {
				b = n - 1
			}
			nrm := pts[b].Sub(pts[a]).Unit().Perp()
			o := filt[i] * p.Damp
			if math.Abs(o) > moved {
				moved = math.Abs(o)
			}
			out[i] = pts[i].Add(nrm.Scale(o))
		}
		// Finish smoothing runs in the TURNING-ANGLE domain
		// (geo.SmoothTurning): smoothing POSITIONS is curvature-biased
		// and pulled ~8 m off the SW Loop's R17 corner over five passes,
		// which is the chamfer every stage downstream inherited. Heading
		// smoothing has no such bias — a straight run is a constant
		// heading and an arc is a linear ramp, both reproduced exactly —
		// while a weld kink is a STEP in the series, which is precisely
		// what a low-pass rounds off. Same job, no erosion.
		cur = geo.NewLine(geo.SmoothTurning(out, p.FinishSigma))
		if moved < 0.3 {
			break
		}
	}
	return cur
}

// throughMembers keeps members present along ≥ ThroughFrac of the line —
// a branch peeling off mid-corridor is the fork's geometry, not this
// centerline's (it drags the median into an S-wiggle otherwise). The
// requirement is CORRIDOR-scale: on a megachain edge (several corridors
// long), demanding presence along 80% of the whole chain would exclude
// every local track group and freeze refinement there (the B/Q Prospect
// Park tunnel stayed unrefined beside a 14.7 km Brighton edge) — so the
// threshold caps at ~1.5 km of arc.
func throughMembers(cl *geo.Line, members []*geo.Line, p Params) []*geo.Line {
	const corridorScaleM = 1500.0
	base := cl.Resample(12.0)
	if len(base) < 3 {
		return members
	}
	need := p.ThroughFrac * math.Min(float64(len(base)), corridorScaleM/12.0)
	// A SUSTAINED CONTIGUOUS RUN alongside is corridor membership too,
	// whatever fraction of the edge it covers. Edges get chained into
	// megachains (Chicago's Blue line is ONE 44 km edge), and against
	// that length the opposite track — a 2.9 km strand co-running for
	// ~530 m before the chaining takes the two apart — cleared neither
	// the 1.2 km global bar nor the 90%-of-own-length one. It was culled
	// before it could vote, leaving a SINGLE member, and a lone member
	// means offset 0 at every cross-section: the median sat exactly on
	// that one rail. This is the "centerline hugs one track" case.
	// Divergence is still handled where it belongs — per sample, by the
	// SpanProbe persistence guard — so a fork ramp that parallels
	// briefly and peels away still cannot drag the median.
	const runM = 250.0
	var through []*geo.Line
	for _, m := range members {
		near, run, bestRun := 0, 0, 0
		for _, q := range base {
			if m.Within(q, p.Reach) {
				near++
				run++
				if run > bestRun {
					bestRun = run
				}
			} else {
				run = 0
			}
		}
		if float64(bestRun)*12.0 >= runM {
			through = append(through, m)
			continue
		}
		if float64(near) >= need {
			through = append(through, m)
			continue
		}
		// a member that stays alongside for essentially ITS OWN whole
		// length is corridor steel however short it is relative to the
		// edge: the Charlotte Gold's opposite rail is a 945 m strand
		// beside a 6.2 km edge — a fork ramp DIVERGES, this never leaves.
		// The length floor keeps crossover diagonals (which also lie
		// wholly within reach, but are furniture) out of the vote.
		if m.Len() >= 150 {
			ms := m.Resample(12.0)
			on := 0
			for _, q := range ms {
				if cl.Within(q, p.Reach) {
					on++
				}
			}
			if float64(on) >= 0.9*float64(len(ms)) {
				through = append(through, m)
			}
		}
	}
	if len(through) == 0 {
		return members
	}
	return through
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return s[len(s)/2]
}

// medianFilter kills single-sample strand flips (window ±w samples).
func medianFilter(v []float64, has []bool, w int) []float64 {
	n := len(v)
	out := make([]float64, n)
	buf := make([]float64, 0, 2*w+1)
	for i := range v {
		if !has[i] {
			continue
		}
		buf = buf[:0]
		for j := max(0, i-w); j < min(n, i+w+1); j++ {
			if has[j] {
				buf = append(buf, v[j])
			}
		}
		sort.Float64s(buf)
		out[i] = buf[len(buf)/2]
	}
	return out
}

// gaussianSeries low-passes the offset SERIES (σ in samples): a strand-count
// step (express pair peeling) becomes a long ramp; converged sections are ~0
// so it is a no-op there.
// fillGaps produces a fully-defined offset series: interior runs without
// coverage are linearly interpolated between their covered neighbors, and
// uncovered leading/trailing runs are zero (no correction far from any
// evidence — the slope limit then eases the boundary).
func fillGaps(v []float64, has []bool, holdStart, holdEnd bool) []float64 {
	n := len(v)
	out := make([]float64, n)
	last := -1
	first := -1
	for i := 0; i < n; i++ {
		if !has[i] {
			continue
		}
		out[i] = v[i]
		if last < 0 {
			first = i
			// leading uncovered run stays zero (pinned end: the endpoint
			// cannot move, so the correction must ramp away from it)
		} else if last < i-1 {
			for j := last + 1; j < i; j++ {
				t := float64(j-last) / float64(i-last)
				out[j] = v[last]*(1-t) + v[i]*t
			}
		}
		last = i
	}
	// a FREE end holds the last stable correction instead of decaying to
	// zero: ramping to zero walks the tip back onto the seed rail — the
	// terminus finishes straight on the corridor's centered offset.
	if holdStart && first > 0 {
		for j := 0; j < first; j++ {
			out[j] = v[first]
		}
	}
	if holdEnd && last >= 0 && last < n-1 {
		for j := last + 1; j < n; j++ {
			out[j] = v[last]
		}
	}
	return out
}

// slopeLimit bounds |dv/di| to maxDelta per sample, easing steps into
// symmetric ramps: forward and backward clamped passes are averaged, so
// regions already within the limit pass through unchanged.
func slopeLimit(v []float64, has []bool, maxDelta float64) []float64 {
	n := len(v)
	fwd := make([]float64, n)
	bwd := make([]float64, n)
	copy(fwd, v)
	copy(bwd, v)
	prev := -1
	for i := 0; i < n; i++ {
		if has != nil && !has[i] {
			continue
		}
		if prev >= 0 {
			lim := maxDelta * float64(i-prev)
			d := fwd[i] - fwd[prev]
			if d > lim {
				fwd[i] = fwd[prev] + lim
			} else if d < -lim {
				fwd[i] = fwd[prev] - lim
			}
		}
		prev = i
	}
	prev = -1
	for i := n - 1; i >= 0; i-- {
		if has != nil && !has[i] {
			continue
		}
		if prev >= 0 {
			lim := maxDelta * float64(prev-i)
			d := bwd[i] - bwd[prev]
			if d > lim {
				bwd[i] = bwd[prev] + lim
			} else if d < -lim {
				bwd[i] = bwd[prev] - lim
			}
		}
		prev = i
	}
	out := make([]float64, n)
	for i := range v {
		out[i] = 0.5 * (fwd[i] + bwd[i])
	}
	return out
}

func gaussianSeries(v []float64, has []bool, sigma float64) ([]float64, []bool) {
	n := len(v)
	out := make([]float64, n)
	w := int(2 * sigma)
	// kernel depends only on |j-i|: tabulate the identical Exp values once
	// ((j-i)*(j-i) == d*d exactly in int arithmetic)
	kern := make([]float64, w+1)
	for d := 0; d <= w; d++ {
		kern[d] = math.Exp(-float64(d*d) / (sigma * sigma))
	}
	for i := range v {
		if !has[i] {
			continue
		}
		var sw, sv float64
		for j := max(0, i-w); j < min(n, i+w+1); j++ {
			if !has[j] {
				continue
			}
			d := j - i
			if d < 0 {
				d = -d
			}
			g := kern[d]
			sw += g
			sv += v[j] * g
		}
		out[i] = sv / sw
	}
	return out, has
}
