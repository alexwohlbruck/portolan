package sync

// Did the group keep everything its members drew? The Go port of
// tools/groupverify.py, faithful to its constants and sampling: every
// member's own band-15 non-transition centerlines are sampled at 25 m,
// and each sample must land within 30 m of the group's ink. Tested on
// GEOMETRY, not labels — a group re-trunks its lines, so a label check
// would read a rename as a loss. What must not change is the INK.
//
// This is the gate that keeps automatic grouping safe: a group REPLACES
// its members in the world index, so a group that quietly drew less
// than they did would take the difference off the map with nobody
// watching. A failing group is deleted from the registry, which puts
// its members straight back on the map.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

const (
	verifyStepM = 25.0 // sample the member's centrelines this finely
	verifyTolM  = 30.0 // ...and call one covered if the group drew within this
	verifyMin   = 0.90 // each member must retain this fraction of its ink
)

// verifyResult reports one group's gate outcome.
type verifyResult struct {
	Worst float64  // lowest member retention
	Bad   []string // "member 87%" for members under the floor
}

func (v verifyResult) ok() bool { return len(v.Bad) == 0 }

// verifySamples reads a build and samples its band-15 non-transition
// lines every 25 m, exactly as groupverify.py does: n interpolated
// points per segment, endpoints arriving as the next segment's start.
func verifySamples(path string) ([][2]float64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Features []struct {
			Properties struct {
				BandMin *float64 `json:"band_min"`
				Kind    string   `json:"kind"`
			} `json:"properties"`
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out [][2]float64
	sample := func(pts [][2]float64) {
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			mx := 111320 * math.Cos(a[1]*math.Pi/180)
			dx, dy := (b[0]-a[0])*mx, (b[1]-a[1])*110540
			n := int(math.Hypot(dx, dy) / verifyStepM)
			if n < 1 {
				n = 1
			}
			for j := 0; j < n; j++ {
				out = append(out, [2]float64{
					a[0] + (b[0]-a[0])*float64(j)/float64(n),
					a[1] + (b[1]-a[1])*float64(j)/float64(n)})
			}
		}
	}
	for _, f := range fc.Features {
		if f.Properties.BandMin == nil || *f.Properties.BandMin != 15 ||
			f.Properties.Kind == "transition" {
			continue
		}
		switch f.Geometry.Type {
		case "LineString":
			var pts [][2]float64
			if json.Unmarshal(f.Geometry.Coordinates, &pts) == nil {
				sample(pts)
			}
		case "MultiLineString":
			var parts [][][2]float64
			if json.Unmarshal(f.Geometry.Coordinates, &parts) == nil {
				for _, pts := range parts {
					sample(pts)
				}
			}
		}
	}
	return out, nil
}

// verifyGroup runs the gate for one group build: grid the group's own
// samples at the tolerance cell size, then ask what fraction of each
// member's samples lands within 30 m. Members without a build or with
// no band-15 ink are skipped, as the Python does.
func verifyGroup(cfg registry.Config, buildDir, key string) (verifyResult, error) {
	res := verifyResult{Worst: 1.0}
	fc := cfg.Feeds[key]
	groupSamples, err := verifySamples(outPath(fc, buildDir, key))
	if err != nil {
		return res, err
	}
	cx, cy := verifyTolM/111320.0, verifyTolM/110540.0
	type cell [2]int
	grid := map[cell][][2]float64{}
	for _, p := range groupSamples {
		c := cell{int(p[0] / cx), int(p[1] / cy)}
		grid[c] = append(grid[c], p)
	}
	near := func(x, y float64) bool {
		gx, gy := int(x/cx), int(y/cy)
		for i := -1; i <= 1; i++ {
			for j := -1; j <= 1; j++ {
				for _, p := range grid[cell{gx + i, gy + j}] {
					ddx := (p[0] - x) * 111320 * math.Cos(y*math.Pi/180)
					ddy := (p[1] - y) * 110540
					if math.Hypot(ddx, ddy) <= verifyTolM {
						return true
					}
				}
			}
		}
		return false
	}
	for _, m := range fc.Members {
		mc, ok := cfg.Feeds[m]
		if !ok {
			continue
		}
		own := outPath(mc, buildDir, m)
		if _, err := os.Stat(own); err != nil {
			continue
		}
		ss, err := verifySamples(own)
		if err != nil || len(ss) == 0 {
			continue
		}
		hit := 0
		for _, p := range ss {
			if near(p[0], p[1]) {
				hit++
			}
		}
		r := float64(hit) / float64(len(ss))
		if r < verifyMin {
			res.Bad = append(res.Bad, fmt.Sprintf("%s %.0f%%", m, r*100))
		}
		if r < res.Worst {
			res.Worst = r
		}
	}
	return res, nil
}
