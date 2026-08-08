package pipeline

// STATIONS — platforms become stations (docs/STOP-LABELS.md). Raw GTFS
// stops are platform records with no route metadata, which is why maps
// drawn straight from them read as noise. Here the join GTFS forgot is
// recovered from what the pipeline already knows: each drawn pattern's
// stop list (the shapeStops sweep), each route's class, trunk and
// resolved color. The output is one Point per STATION — platforms merged
// under parent_station, then by matching names within walking distance —
// carrying everything the viewer needs to draw markers, rank labels, and
// later render bullets.
//
// Stations are timetable-independent like ribbons: the union build
// carries every station, and the viewer hides those whose routes are all
// asleep using the same /api/activity masks (a station needs no mask of
// its own — OR over member routes at render time).

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
	"github.com/alexwohlbruck/portolan/internal/style"
)

// Station is one merged station: name, position, and aligned per-route
// metadata (routes[i] ↔ labels[i] ↔ routeHex[i] ↔ modes[i]).
type Station struct {
	Name     string
	LL       geo.LL
	Routes   []string // route ids — the activity-mask join key
	Labels   []string // short names — the future bullet text
	RouteHex []string // display color per route (FAIR's precedence)
	Modes    []string // class per route — the class-toggle join key
	Agencies []string // distinct agency display names
	Lines    int      // distinct trunk keys — the marker rule (dot vs disc)
	LineHex  []string // display color per line, distinct, sorted
}

// BuildStations groups the stops served by the drawn patterns into
// stations. Patterns whose class is bus or hidden contribute nothing
// (bus poles are a different rendering problem); bbox, when present,
// drops stops outside the city window the same way clipPatterns drops
// geometry.
func BuildStations(feed *gtfs.Feed, pats []gtfs.Pattern, bbox []float64) []Station {
	inWindow := func(ll geo.LL) bool { return true }
	if len(bbox) == 4 {
		const margin = 0.02 // ~2 km, matches clipPatterns
		w, s, e, n := bbox[0]-margin, bbox[1]-margin, bbox[2]+margin, bbox[3]+margin
		inWindow = func(ll geo.LL) bool {
			return ll.Lon >= w && ll.Lon <= e && ll.Lat >= s && ll.Lat <= n
		}
	}

	// stop → set of route ids that call there
	stopRoutes := map[string]map[string]bool{}
	routeByID := map[string]gtfs.Route{}
	for _, p := range pats {
		c := mode.Of(p.Route.Type)
		if c == mode.Bus || c.Hidden() {
			continue
		}
		routeByID[p.Route.ID] = p.Route
		for _, sid := range p.StopIDs {
			st, ok := feed.Stops[sid]
			if !ok || !inWindow(st.LL) {
				continue
			}
			if stopRoutes[sid] == nil {
				stopRoutes[sid] = map[string]bool{}
			}
			stopRoutes[sid][p.Route.ID] = true
		}
	}

	// platforms → groups, keyed by parent_station when the feed has one
	type group struct {
		key    string
		names  map[string]int // member name → votes
		lls    []geo.LL
		routes map[string]bool
	}
	groups := map[string]*group{}
	for sid, rts := range stopRoutes {
		st := feed.Stops[sid]
		key := sid
		if st.Parent != "" {
			key = st.Parent
		}
		g := groups[key]
		if g == nil {
			g = &group{key: key, names: map[string]int{}, routes: map[string]bool{}}
			groups[key] = g
		}
		g.names[st.Name]++
		g.lls = append(g.lls, st.LL)
		for r := range rts {
			g.routes[r] = true
		}
	}

	// name + centroid per group; the parent record's own name wins when
	// the feed ships one ("Grand Central" beats "Grand Central Track 21")
	type node struct {
		name string
		norm string
		feed string // "f1", "f2"… for overlay feeds, "" for the primary
		ll   geo.LL
		g    *group
	}
	nodes := make([]node, 0, len(groups))
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		name := ""
		if p, ok := feed.Stops[k]; ok && p.Name != "" && p.Parent == "" {
			name = p.Name
		}
		if name == "" {
			best, bestN := "", 0
			var cand []string
			for n := range g.names {
				cand = append(cand, n)
			}
			sort.Strings(cand)
			for _, n := range cand {
				if g.names[n] > bestN {
					best, bestN = n, g.names[n]
				}
			}
			name = best
		}
		var cx, cy float64
		for _, ll := range g.lls {
			cx += ll.Lon
			cy += ll.Lat
		}
		ll := geo.LL{Lon: cx / float64(len(g.lls)), Lat: cy / float64(len(g.lls))}
		nodes = append(nodes, node{name: name, norm: normName(name),
			feed: feedPrefix(k), ll: ll, g: g})
	}

	// merge groups whose normalized names match within walking distance.
	// The radius is TIERED: within one feed, parent_station already did
	// the grouping, so only a tight 150 m catches parentless
	// cross-platform pairs — any looser and NYC's two distinct "23 St"
	// stations (6 Av and 7 Av, ~260 m apart) fold into one. Across feeds
	// there are no ids to link, names are all we have, and terminals
	// sprawl — 300 m is what it takes to put the LIRR's Madison concourse
	// under Grand Central (276 m from Metro-North's centroid). Different
	// names never merge: Apple labels Atlantic Terminal and Atlantic
	// Av–Barclays separately, and so do we.
	parent := make([]int, len(nodes))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	byNorm := map[string][]int{}
	for i, n := range nodes {
		byNorm[n.norm] = append(byNorm[n.norm], i)
	}
	for _, idxs := range byNorm {
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				i, j := idxs[a], idxs[b]
				lim := 300.0
				if nodes[i].feed == nodes[j].feed {
					lim = 150.0
				}
				if distM(nodes[i].ll, nodes[j].ll) <= lim {
					parent[find(i)] = find(j)
				}
			}
		}
	}
	merged := map[int][]int{}
	for i := range nodes {
		r := find(i)
		merged[r] = append(merged[r], i)
	}

	roots := make([]int, 0, len(merged))
	for r := range merged {
		roots = append(roots, r)
	}
	sort.Ints(roots)

	var out []Station
	for _, r := range roots {
		members := merged[r]
		routes := map[string]bool{}
		var cx, cy float64
		var np int
		nameVotes := map[string]int{}
		for _, i := range members {
			n := nodes[i]
			nameVotes[n.name] += len(n.g.lls)
			for rid := range n.g.routes {
				routes[rid] = true
			}
			for _, ll := range n.g.lls {
				cx += ll.Lon
				cy += ll.Lat
				np++
			}
		}
		name, bestN := "", 0
		var cand []string
		for n := range nameVotes {
			cand = append(cand, n)
		}
		sort.Strings(cand)
		for _, n := range cand {
			if nameVotes[n] > bestN {
				name, bestN = n, nameVotes[n]
			}
		}

		rids := make([]string, 0, len(routes))
		for rid := range routes {
			rids = append(rids, rid)
		}
		sort.Slice(rids, func(i, j int) bool {
			a, b := routeByID[rids[i]], routeByID[rids[j]]
			if c := naturalCmp(a.ShortName, b.ShortName); c != 0 {
				return c < 0
			}
			return a.ID < b.ID
		})

		st := Station{Name: name, LL: geo.LL{Lon: cx / float64(np), Lat: cy / float64(np)}}
		trunkHexes := map[string]map[string]int{} // trunk key → hex votes
		agencies := map[string]bool{}
		for _, rid := range rids {
			rt := routeByID[rid]
			st.Routes = append(st.Routes, rid)
			// regional routes often ship no short name — the long name is
			// the branch identity ("Hudson", "Babylon Branch")
			lbl := rt.ShortName
			if lbl == "" {
				lbl = rt.LongName
			}
			// aligned arrays are comma-joined in the geojson — a comma in
			// a long name would shift every array after it
			st.Labels = append(st.Labels, strings.ReplaceAll(lbl, ",", " "))
			hx := routeHex(rt)
			st.RouteHex = append(st.RouteHex, hx)
			st.Modes = append(st.Modes, mode.Of(rt.Type).String())
			tk := mode.TrunkKey(rt)
			if trunkHexes[tk] == nil {
				trunkHexes[tk] = map[string]int{}
			}
			trunkHexes[tk][hx]++
			if ag := mode.AgencyName(rt.Agency); ag != "" {
				agencies[ag] = true
			}
		}
		st.Lines = len(trunkHexes)
		// one display color per LINE: an agency trunk's twelve branch
		// colors are one line, painted by majority (ties → lexicographic)
		var tks []string
		for tk := range trunkHexes {
			tks = append(tks, tk)
		}
		sort.Strings(tks)
		for _, tk := range tks {
			votes := trunkHexes[tk]
			var hxs []string
			for h := range votes {
				hxs = append(hxs, h)
			}
			sort.Strings(hxs)
			best, bestN := "", 0
			for _, h := range hxs {
				if votes[h] > bestN {
					best, bestN = h, votes[h]
				}
			}
			st.LineHex = append(st.LineHex, best)
		}
		sort.Strings(st.LineHex)
		for a := range agencies {
			st.Agencies = append(st.Agencies, a)
		}
		sort.Strings(st.Agencies)
		out = append(out, st)
	}
	return out
}

// routeHex is the display color for ONE route — the bullet color. Same
// precedence FAIR uses for ribbons: config override (route, then agency)
// → class canonical → the route's own → gray.
func routeHex(r gtfs.Route) string {
	sty := style.Active()
	if sty.Any() {
		if h, ok := sty.RouteColor(r.ID, r.ShortName, r.LongName); ok {
			return h
		}
		if h, ok := sty.AgencyColor(r.Agency, mode.AgencyName(r.Agency)); ok {
			return h
		}
	}
	if h := sty.Class(mode.Of(r.Type).String()).Color; h != "" {
		return h
	}
	if r.Color != "" {
		return r.Color
	}
	return "888888"
}

// feedPrefix extracts the overlay tag loadFeeds stamped on an id
// ("f2:237" → "f2"); primary-feed ids return "".
func feedPrefix(id string) string {
	if i := strings.Index(id, ":"); i > 0 {
		p := id[:i]
		if len(p) >= 2 && p[0] == 'f' {
			if _, err := strconv.Atoi(p[1:]); err == nil {
				return p
			}
		}
	}
	return ""
}

// normName folds the differences that keep one station apart in the
// data: case, extra whitespace, and the punctuation feeds disagree on
// ("Av" vs "Av." vs "Avenue" stays distinct on purpose — expanding
// abbreviations invents merges the operator didn't).
func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(".", "", ",", "", "'", "", "’", "", "-", " ", "–", " ", "/", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func distM(a, b geo.LL) float64 {
	kx := 111320 * math.Cos((a.Lat+b.Lat)/2*math.Pi/180)
	dx := (a.Lon - b.Lon) * kx
	dy := (a.Lat - b.Lat) * 111320
	return math.Hypot(dx, dy)
}

// naturalCmp orders route labels the way a rider reads them: "2" before
// "10", numbers before letters, otherwise plain string order.
func naturalCmp(a, b string) int {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil:
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		}
		return 0
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// writeStations emits <out>.stations.geojson — Point features with
// aligned per-route arrays, comma-joined like ribbon `routes`.
func writeStations(path string, sts []Station) error {
	fc := collection{Type: "FeatureCollection"}
	for _, s := range sts {
		raw, _ := json.Marshal([2]float64{s.LL.Lon, s.LL.Lat})
		fc.Features = append(fc.Features, feature{
			Type: "Feature",
			Props: map[string]any{
				"name":         s.Name,
				"routes":       strings.Join(s.Routes, ","),
				"labels":       strings.Join(s.Labels, ","),
				"route_colors": strings.Join(s.RouteHex, ","),
				"modes":        strings.Join(s.Modes, ","),
				"agencies":     strings.Join(s.Agencies, ","),
				"nroutes":      len(s.Routes),
				"nlines":       s.Lines,
				"line_colors":  strings.Join(s.LineHex, ","),
				"rank":         len(s.Routes),
			},
			Geom: geomJSON{Type: "Point", Coords: raw},
		})
	}
	return writeFC(path, fc)
}
