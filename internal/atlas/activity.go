package atlas

// /api/activity — per-route weekly service masks, the data half of
// docs/DYNAMIC-SERVICE.md. One 168-bit mask per route (7 days × 24 hours,
// Monday first, hour 0 = LSB of each day's 24-bit word, hex-encoded as
// 7 × 6 chars) answers "does this route run at (day, hour)" in one bit
// test, which is all the dynamic renderer needs: it hides ribbons whose
// routes are all dark and re-centers the survivors within the union slot
// order. Masks come from the same calendar machinery scenarios use, so
// dated-calendar feeds (LIRR), frequency feeds (AirTrain) and overlay
// prefixes all behave identically in both.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/pipeline"
)

type actCache struct {
	mod   time.Time
	masks map[string]string
}

func (s *Server) activityAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fc, feed, ok := s.feedCfg(r)
	if !ok || fc.GTFS == "" {
		json.NewEncoder(w).Encode(map[string]any{"available": false})
		return
	}
	st, err := os.Stat(fc.primaryGTFS())
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"available": false, "error": err.Error()})
		return
	}
	s.actMu.Lock()
	c := s.activity[feed]
	if c == nil || !c.mod.Equal(st.ModTime()) {
		s.actMu.Unlock()
		si, err := pipeline.LoadServiceInfo(fc.GTFS)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"available": false, "error": err.Error()})
			return
		}
		masks := routeMasks(si)
		s.actMu.Lock()
		c = &actCache{mod: st.ModTime(), masks: masks}
		s.activity[feed] = c
	}
	masks := c.masks
	s.actMu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"available": true, "masks": masks})
}

// routeMasks folds per-pattern activity into one mask per route: a bit is
// set when ANY of the route's patterns has a trip in service that hour.
// Route-level, not pattern-level, because visibility is a route question —
// a ribbon shows when the route runs at all, whichever variant is out.
func routeMasks(si *gtfs.ServiceInfo) map[string]string {
	sum := map[string]*[7][24]int{}
	for key, act := range si.Activity {
		a := sum[key.Route]
		if a == nil {
			a = &[7][24]int{}
			sum[key.Route] = a
		}
		for d := 0; d < 7; d++ {
			for h := 0; h < 24; h++ {
				a[d][h] += act[d][h]
			}
		}
	}
	out := make(map[string]string, len(sum))
	for rid, a := range sum {
		var b strings.Builder
		for d := 0; d < 7; d++ {
			bits := 0
			for h := 0; h < 24; h++ {
				if a[d][h] > 0 {
					bits |= 1 << h
				}
			}
			fmt.Fprintf(&b, "%06x", bits)
		}
		out[rid] = b.String()
	}
	return out
}
