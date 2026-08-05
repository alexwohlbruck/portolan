package gtfs

import (
	"math"
	"os"
	"testing"
)

func TestExciseDebug(t *testing.T) {
	path := os.Getenv("HOME") + "/Documents/code/barrelman/data/gtfs/5.zip"
	if _, err := os.Stat(path); err != nil {
		t.Skip()
	}
	feed, err := Load(path, 1.01)
	if err != nil {
		t.Fatal(err)
	}
	// the drawn ring: lat 40.7013-40.7032, lon -74.0148..-74.0131
	inRing := func(ll geo_LL) bool {
		return 40.7010 < ll.Lat && ll.Lat < 40.7033 && -74.0150 < ll.Lon && ll.Lon < -74.0128
	}
	count := 0
	for _, p := range feed.Patterns {
		n := 0
		for _, ll := range p.Shape {
			if inRing(ll) {
				n++
			}
		}
		if n >= 4 {
			count++
			if count <= 6 {
				t.Logf("%s %s ringPts=%d trips=%d", p.Route.ID, p.ShapeID, n, p.Trips)
				// print ring-adjacent geometry: distances of consecutive ring pts
				kx := 111320 * math.Cos(40.70*math.Pi/180)
				for i, ll := range p.Shape {
					if inRing(ll) {
						d := -1.0
						if i > 0 {
							dx := (ll.Lon - p.Shape[i-1].Lon) * kx
							dy := (ll.Lat - p.Shape[i-1].Lat) * 110540
							d = math.Hypot(dx, dy)
						}
						t.Logf("   pt[%d] %.5f,%.5f (step %.0fm)", i, ll.Lat, ll.Lon, d)
					}
				}
				break
			}
		}
	}
	t.Logf("%d patterns still ride the ring", count)
}

type geo_LL = struct{ Lon, Lat float64 }
