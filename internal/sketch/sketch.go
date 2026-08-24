// Package sketch holds the hand-drawn ground-truth network and the scorer —
// the regression gate for every geometry change (docs/TOOLS.md).
package sketch

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

// Network is the owner's drawn network (one per feed). The file on disk is
// precious hand work: read it, never regenerate it, write only via the
// editor's atomic save.
type Network struct {
	Lines []DrawnLine `json:"lines"`
}

type DrawnLine struct {
	Coords [][]float64  `json:"coords"` // [lon,lat]
	Routes []DrawnRoute `json:"routes"`
}

type DrawnRoute struct {
	Label string `json:"label"`
	Color string `json:"color"`
}

func LoadNetwork(path string) (*Network, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n Network
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

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

// BuildFeature is one emitted line-map feature (color + geometry in metric).
type BuildFeature struct {
	Color  string
	Routes []string // route ids riding the feature (dup-gate identity)
	Line   *geo.Line
}

type LineScore struct {
	Label            string
	Km               float64
	Mean, P90, Max   float64
	CoverPct, ColPct float64
	DupPct           float64 // doubled-geometry samples (lens detector)
	Fail             bool
	WorstAt          geo.LL   // location of the max deviation — navigable
	DupAt            []geo.LL // doubled-sample locations (capped) — navigable
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
}

// Score grades build features against the drawn network. Geometry-first:
// deviation is measured to the nearest feature of ANY color; color match is
// a diagnostic column only. See docs/TOOLS.md for the full contract.
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
		pts := make([]geo.Pt, len(dl.Coords))
		for i, c := range dl.Coords {
			pts[i] = frame.ToXY(geo.LL{Lon: c[0], Lat: c[1]})
		}
		drawn = append(drawn, geo.NewLine(pts))
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
		colHit, dupHit := 0, 0
		worstDev, worstAt := -1.0, geo.Pt{}
		var dupAt []geo.Pt
		for _, q := range samples {
			best, bestI := math.Inf(1), -1
			// The lens gate: the SAME SERVICE riding two laterally-separated
			// centerlines is a doubled network. Co-centered per-color copies
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
			if bestI >= 0 && colorEq(feats[bestI].Color, dl) {
				colHit++
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
			Label: label(dl), Km: l.Len() / 1000,
			Mean: mean, P90: p90, Max: devs[len(devs)-1],
			CoverPct: coverPct,
			ColPct:   100 * float64(colHit) / float64(len(samples)),
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
	fmt.Printf("  %-16s %6s %6s %6s %6s %6s %5s %5s\n",
		"line", "len_km", "mean", "p90", "max", "cover%", "dup%", "col%")
	for _, ls := range r.Lines {
		flag := ""
		if ls.Fail {
			flag = "  <== FAIL"
		}
		fmt.Printf("  %-16s %6.1f %6.1f %6.1f %6.1f %6.1f %5.1f %5.0f%s\n",
			ls.Label, ls.Km, ls.Mean, ls.P90, ls.Max, ls.CoverPct, ls.DupPct, ls.ColPct, flag)
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
	if r.Failures > 0 {
		fmt.Printf("\nFAIL (%d gate(s) exceeded)\n", r.Failures)
	} else {
		fmt.Printf("\nPASS\n")
	}
}

func label(dl DrawnLine) string {
	s := ""
	for _, r := range dl.Routes {
		if s != "" {
			s += "+"
		}
		if r.Label != "" {
			s += r.Label
		} else {
			s += r.Color
		}
	}
	if len(s) > 16 {
		s = s[:16]
	}
	if s == "" {
		s = "?"
	}
	return s
}

func colorEq(c string, dl DrawnLine) bool {
	for _, r := range dl.Routes {
		if eqHex(r.Color, c) {
			return true
		}
	}
	return false
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

func eqHex(a, b string) bool {
	trim := func(s string) string {
		if len(s) > 0 && s[0] == '#' {
			return s[1:]
		}
		return s
	}
	a, b = trim(a), trim(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'z' {
			ca -= 32
		}
		if 'a' <= cb && cb <= 'z' {
			cb -= 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
