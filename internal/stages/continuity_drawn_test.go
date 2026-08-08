package stages

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// Every color's drawn ribbon must be continuous: an endpoint with no
// same-color partner endpoint nearby, but WITH same-color geometry passing
// close by, is a break (a transition suppressed by bookkeeping — the
// DeKalb fan disconnect shipped twice before this scan existed). True
// termini have nothing beyond them and are fine.
func TestDrawnContinuity(t *testing.T) {
	raw, err := os.ReadFile("../../build/nyc.geojson")
	if err != nil {
		t.Skip(err)
	}
	var fc struct {
		Features []struct {
			Props struct {
				Color   string  `json:"color"`
				Mode    string  `json:"mode"`
				Kind    string  `json:"kind"`
				Label   string  `json:"label"`
				Routes  string  `json:"routes"`
				BandMin int     `json:"band_min"`
				OffPx   float64 `json:"offset_px"`
				OffFrom float64 `json:"off_from_px"`
				OffTo   float64 `json:"off_to_px"`
			} `json:"properties"`
			Geometry struct {
				Coords [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatal(err)
	}
	kx := 111320 * math.Cos(40.7*math.Pi/180)
	const ky = 110540.0
	type feat struct {
		color  string
		label  string
		routes map[string]bool
		bridge bool
		ends   [2][2]float64 // offset-applied endpoint positions (m)
		pts    [][2]float64  // offset-applied polyline
	}
	var feats []feat
	for _, f := range fc.Features {
		if f.Props.BandMin != 15 || len(f.Geometry.Coords) < 2 {
			continue
		}
		// metro+tram keep the strict zero-break baseline (the guard's
		// original mandate: the subway map). Bus and regional are new
		// machinery with known tails — 2 bus corridor ends, and 3 LIRR
		// ends inside the Jamaica interlocking (the LIRR's Tower 18,
		// 27-450 m fragments carrying 4-10 branches) — that get their own
		// baselines when their junction passes happen. They must not mask
		// a subway regression here, nor block on their own.
		if f.Props.Mode != "" && f.Props.Mode != "metro" && f.Props.Mode != "tram" {
			continue
		}
		cs := f.Geometry.Coords
		apply := func(i int) [2]float64 {
			x, y := cs[i][0]*kx, cs[i][1]*ky
			j, k := i+1, i-1
			if j > len(cs)-1 {
				j = len(cs) - 1
			}
			if k < 0 {
				k = 0
			}
			dx, dy := (cs[j][0]-cs[k][0])*kx, (cs[j][1]-cs[k][1])*ky
			l := math.Hypot(dx, dy)
			if l == 0 {
				return [2]float64{x, y}
			}
			o := f.Props.OffPx
			if f.Props.Kind == "transition" {
				tt := float64(i) / float64(len(cs)-1)
				o = f.Props.OffFrom*(1-tt) + f.Props.OffTo*tt
			}
			o *= 1.5
			return [2]float64{x - dy/l*o, y + dx/l*o}
		}
		ft2 := feat{color: f.Props.Color, label: f.Props.Label,
			routes: map[string]bool{}, bridge: f.Props.Kind == "bridge"}
		for _, r := range splitComma(f.Props.Routes) {
			ft2.routes[r] = true
		}
		for i := range cs {
			ft2.pts = append(ft2.pts, apply(i))
		}
		ft2.ends = [2][2]float64{ft2.pts[0], ft2.pts[len(ft2.pts)-1]}
		feats = append(feats, ft2)
	}
	_ = splitComma
	dist := func(a, b [2]float64) float64 {
		return math.Hypot(a[0]-b[0], a[1]-b[1])
	}
	breaks := 0
	for i, f := range feats {
		for _, end := range f.ends {
			joined := false
			passing := false
			for j, g := range feats {
				if i == j || g.color != f.color {
					continue
				}
				shared := false
				for r := range f.routes {
					if g.routes[r] {
						shared = true
						break
					}
				}
				if !shared || f.bridge || g.bridge {
					continue
				}
				for _, ge := range g.ends {
					if dist(end, ge) < 12 {
						joined = true
					}
				}
				if !joined {
					for _, gp := range g.pts {
						if dist(end, gp) < 25 {
							passing = true
							break
						}
					}
				}
				if joined {
					break
				}
			}
			if !joined && passing {
				breaks++
				t.Logf("drawn break: %s %s end at %.5f,%.5f (same-route line passes nearby)",
					f.color, f.label, end[1]/110540, end[0]/(111320*math.Cos(40.7*math.Pi/180)))
			}
		}
	}
	// baseline 2026-08-04: 0 — plan/emit transition dedup + deg-2 chain
	// contraction + tiny-self-loop sweep cleared all 9 prior known breaks
	if breaks > 0 {
		t.Errorf("%d drawn breaks (baseline 0) — a transition went missing", breaks)
	} else {
		t.Logf("%d drawn breaks (baseline 0)", breaks)
	}
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
