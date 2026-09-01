package sketch

import (
	"fmt"
	"math"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Gates — the values that survived production tuning.
const (
	FailP90M      = 15.0 // forward p90 deviation
	FailCoverPct  = 95.0 // forward coverage@25m
	FailWobbleP90 = 12.0 // reverse wobble p90
	FailJagMaxDeg = 40.0 // max turn at uniform 12m scale, on-corridor
	FailJagPerKm  = 1.0  // spikes >25° per km, on-corridor
	NearM         = 25.0 // coverage radius
	DupM          = 12.0 // two features this close to one drawn sample = doubled
	FailDupPct    = 8.0  // max % of samples with doubled geometry (lens gate)
)

// BuildFeature is one emitted line-map feature in metric. Routes are the
// ids riding it — the dup gate's identity test, and the only reason a
// feature carries anything but geometry here. Colour is deliberately
// absent: the drawing does not record it, so nothing can be graded on it.
type BuildFeature struct {
	Routes []string
	Line   *geo.Line
}

type LineScore struct {
	Label          string
	Km             float64
	Mean, P90, Max float64
	CoverPct       float64
	DupPct         float64 // doubled-geometry samples (lens detector)
	Fail           bool
	WorstAt        geo.LL   // location of the max deviation — navigable
	DupAt          []geo.LL // doubled-sample locations (capped) — navigable
}

type Spike struct {
	Deg float64
	At  geo.LL
}

type Result struct {
	Lines      []LineScore
	WobbleMean float64
	WobbleP90  float64
	JagMaxDeg  float64
	JagPerKm   float64
	Spikes     []Spike
	FwdMean    float64
	FwdP90     float64
	Failures   int
	Yards      *YardResult // nil when the drawing has no yards
}

// Score grades build features against the drawn network. Geometry-first:
// deviation is measured to the nearest feature, full stop (owner: "don't
// take the line colors into so much consideration"). See docs/TOOLS.md.
func Score(net *Network, feats []BuildFeature, frame geo.Frame) *Result {
	res := &Result{}
	if len(net.Lines) == 0 || len(feats) == 0 {
		res.Failures = 1
		return res
	}
	lines := make([]*geo.Line, len(feats))
	for i, f := range feats {
		lines[i] = f.Line
	}
	grid := geo.NewGrid(lines, 64)

	drawn := make([]*geo.Line, 0, len(net.Lines))
	for _, dl := range net.Lines {
		drawn = append(drawn, geo.NewLine(pts(dl.Coords, frame)))
	}
	drawnGrid := geo.NewGrid(drawn, 64)

	// ---- forward: every drawn line must be matched by build geometry
	var fwdAll []float64
	for li, dl := range net.Lines {
		l := drawn[li]
		if l.Len() < 1 {
			continue
		}
		samples := l.Resample(5)
		var devs []float64
		dupHit := 0
		worstDev, worstAt := -1.0, geo.Pt{}
		var dupAt []geo.Pt
		for _, q := range samples {
			best, bestI := math.Inf(1), -1
			// The lens gate: the SAME SERVICE riding two laterally-separated
			// centerlines is a doubled network. Co-centered per-colour copies
			// of one ribbon (parchment contract) cluster to one point and
			// never fire; distinct services legitimately side by side
			// (Rector St kiss class) share no route and never fire.
			var dupHits []int
			grid.Near(q, 120, func(fi int) {
				d := lines[fi].DistTo(q)
				if d < best {
					best, bestI = d, fi
				}
				if d < DupM {
					dupHits = append(dupHits, fi)
				}
			})
			if bestI < 0 {
				best = 120 // beyond query reach: counts as a hole
			}
			doubled := false
			for a := 0; a < len(dupHits) && !doubled; a++ {
				for b := a + 1; b < len(dupHits); b++ {
					fa, fb := dupHits[a], dupHits[b]
					if !routesIntersect(feats[fa].Routes, feats[fb].Routes) {
						continue
					}
					arcA, _ := lines[fa].ProjectArc(q)
					arcB, _ := lines[fb].ProjectArc(q)
					if lines[fa].AtArc(arcA).Dist(lines[fb].AtArc(arcB)) > 3.0 {
						doubled = true
						break
					}
				}
			}
			if doubled {
				dupHit++ // lens: the network doubled along the drawing
				if len(dupAt) < 25 {
					dupAt = append(dupAt, q)
				}
			}
			devs = append(devs, best)
			if best > worstDev {
				worstDev, worstAt = best, q
			}
		}
		sort.Float64s(devs)
		mean, cover := 0.0, 0
		for _, d := range devs {
			mean += d
			if d <= NearM {
				cover++
			}
		}
		mean /= float64(len(devs))
		p90 := devs[int(0.9*float64(len(devs)-1))]
		coverPct := 100 * float64(cover) / float64(len(devs))
		dupPct := 100 * float64(dupHit) / float64(len(samples))
		ls := LineScore{
			Label: label(dl.Label, 16), Km: l.Len() / 1000,
			Mean: mean, P90: p90, Max: devs[len(devs)-1],
			CoverPct: coverPct,
			DupPct:   dupPct,
			Fail:     p90 > FailP90M || coverPct < FailCoverPct || dupPct > FailDupPct,
			WorstAt:  frame.ToLL(worstAt),
		}
		for _, q := range dupAt {
			ls.DupAt = append(ls.DupAt, frame.ToLL(q))
		}
		if ls.Fail {
			res.Failures++
		}
		res.Lines = append(res.Lines, ls)
		fwdAll = append(fwdAll, devs...)
	}
	sort.Float64s(fwdAll)
	if len(fwdAll) > 0 {
		for _, d := range fwdAll {
			res.FwdMean += d
		}
		res.FwdMean /= float64(len(fwdAll))
		res.FwdP90 = fwdAll[int(0.9*float64(len(fwdAll)-1))]
	}

	// ---- on-corridor features: wobble + jaggedness at map scale
	const corridor = 30.0
	var wob []float64
	jagKm := 0.0
	for _, f := range feats {
		mid := f.Line.AtArc(f.Line.Len() / 2)
		onCorridor := false
		drawnGrid.Near(mid, corridor, func(int) { onCorridor = true })
		if !onCorridor {
			continue
		}
		// wobble: only samples still near the drawing count (a feature
		// legitimately continuing past the drawn area is coverage, not wobble)
		for _, q := range f.Line.Resample(8) {
			best := math.Inf(1)
			drawnGrid.Near(q, corridor*2, func(di int) {
				if d := drawn[di].DistTo(q); d < best {
					best = d
				}
			})
			if best <= corridor*2 {
				wob = append(wob, best)
			}
		}
		// jaggedness: UNIFORM 12m resample, originals dropped — sub-2m
		// micro-spans must not fake 45° turns (docs/LESSONS.md #20)
		if f.Line.Len() < 24 {
			continue
		}
		jagKm += f.Line.Len() / 1000
		rs := f.Line.Resample(12)
		for i := 1; i < len(rs)-1; i++ {
			t := geo.TurnDeg(rs[i-1], rs[i], rs[i+1])
			if t > res.JagMaxDeg {
				res.JagMaxDeg = t
			}
			if t > 25 {
				res.Spikes = append(res.Spikes, Spike{Deg: t, At: frame.ToLL(rs[i])})
			}
		}
	}
	if jagKm > 0 {
		res.JagPerKm = float64(len(res.Spikes)) / jagKm
	}
	sort.Slice(res.Spikes, func(i, j int) bool { return res.Spikes[i].Deg > res.Spikes[j].Deg })
	if res.JagMaxDeg > FailJagMaxDeg || res.JagPerKm > FailJagPerKm {
		res.Failures++
	}
	if len(wob) > 0 {
		sort.Float64s(wob)
		for _, d := range wob {
			res.WobbleMean += d
		}
		res.WobbleMean /= float64(len(wob))
		res.WobbleP90 = wob[int(0.9*float64(len(wob)-1))]
		if res.WobbleP90 > FailWobbleP90 {
			res.Failures++
		}
	}
	return res
}

// Print writes the standard report — spike LOCATIONS included, because a
// number you can't navigate to is a number you won't fix.
func (r *Result) Print() {
	fmt.Printf("  %-16s %6s %6s %6s %6s %6s %5s\n",
		"line", "len_km", "mean", "p90", "max", "cover%", "dup%")
	for _, ls := range r.Lines {
		flag := ""
		if ls.Fail {
			flag = "  <== FAIL"
		}
		fmt.Printf("  %-16s %6.1f %6.1f %6.1f %6.1f %6.1f %5.1f%s\n",
			ls.Label, ls.Km, ls.Mean, ls.P90, ls.Max, ls.CoverPct, ls.DupPct, flag)
	}
	flag := ""
	if r.JagMaxDeg > FailJagMaxDeg || r.JagPerKm > FailJagPerKm {
		flag = "  <== FAIL"
	}
	fmt.Printf("  jaggedness (on-corridor): max turn %.0f°  spikes>25° %d (%.1f/km)%s\n",
		r.JagMaxDeg, len(r.Spikes), r.JagPerKm, flag)
	for i, s := range r.Spikes {
		if i >= 6 {
			break
		}
		fmt.Printf("    spike %.0f° @ %.5f,%.5f\n", s.Deg, s.At.Lat, s.At.Lon)
	}
	flag = ""
	if r.WobbleP90 > FailWobbleP90 {
		flag = "  <== FAIL"
	}
	fmt.Printf("  wobble: mean %.1f m  p90 %.1f m%s\n", r.WobbleMean, r.WobbleP90, flag)
	fmt.Printf("  OVERALL: fwd mean %.1f m  fwd p90 %.1f m\n", r.FwdMean, r.FwdP90)
	if r.Yards != nil {
		r.Yards.Print()
	}
	if r.Failures > 0 {
		fmt.Printf("\nFAIL (%d gate(s) exceeded)\n", r.Failures)
	} else {
		fmt.Printf("\nPASS\n")
	}
}

func routesIntersect(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y && x != "" {
				return true
			}
		}
	}
	return false
}
