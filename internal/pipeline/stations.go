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
	"github.com/alexwohlbruck/portolan/internal/stages"
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
	// Acts: per-route weekly activity AT THIS STATION (hex Mask168,
	// aligned with Routes; "" = unknown, fall back to the route mask).
	// Sampled from the snapped segment, which post terminal-cuts carries
	// stop-granular hours — the night M keeps its bullet at Myrtle Av
	// but loses it at Flushing Av, exactly like the ribbons.
	Acts []string

	// Set by SnapStations: where the label anchors (the busiest marker),
	// and one marker per ribbon bundle the station's lines snap to — a
	// complex like Times Sq is one label but three bundles, each with its
	// own marker.
	LabelLL geo.LL
	Markers []Marker
}

// Marker is one drawn station marker, snapped onto the FAIR-adjusted
// geometry. Everything the renderer needs to sit ON the drawn lines:
// the direction of the corridor (so a hub pill can lie across it), the
// bundle's drawn width, and — for a single line calling on a wider
// bundle — which slot its ribbon occupies.
type Marker struct {
	LL       geo.LL
	Routes   []string
	Labels   []string // aligned with Routes — this corridor's own bullets
	RouteHex []string // aligned with Routes
	Modes    []string // aligned with Routes
	Acts     []string // aligned with Routes — hours at this marker ("" unknown)
	Lines    int      // distinct trunks at THIS marker
	Bearing  float64  // corridor direction at the snap, deg cw from north
	// The marker draws as EITHER a white pill spanning the bundle (the
	// station's lines occupy every slot) or one colored dot per occupied
	// ribbon (an express stop on a wider corridor shows a dot per
	// stopping line, not a band over lines that pass it by).
	Pill   bool
	SpanPx float64 // pill: full bundle width (nslots-1)·pitch
	Dots   []Dot   // dots: one per occupied ribbon, at its slot offset
}

// Dot is one stopping line at a marker: the RIBBON's drawn color — the
// segment's resolved color, so an agency trunk's dot matches the drawn
// line, not the branches' own colors — at the ribbon's slot offset.
type Dot struct {
	Hex string
	Off float64
}

// BuildStations groups the stops served by the drawn patterns into
// stations. Patterns whose class is bus or hidden contribute nothing
// (bus poles are a different rendering problem); bbox, when present,
// drops stops outside the city window the same way clipPatterns drops
// geometry.
//
// pacts (route+"\x1f"+shape → weekly mask, nil ok) makes station acts
// EXACT: route r is active at a station at hour h iff one of r's
// patterns that STOPS THERE is active then — the night M keeps Myrtle
// Av (the shuttle calls) and loses Flushing Av (it doesn't), straight
// from stop membership, no geometry involved.
func BuildStations(feed *gtfs.Feed, pats []gtfs.Pattern, bbox []float64,
	pacts map[string]gtfs.Mask168) []Station {
	inWindow := func(ll geo.LL) bool { return true }
	if len(bbox) == 4 {
		const margin = 0.02 // ~2 km, matches clipPatterns
		w, s, e, n := bbox[0]-margin, bbox[1]-margin, bbox[2]+margin, bbox[3]+margin
		inWindow = func(ll geo.LL) bool {
			return ll.Lon >= w && ll.Lon <= e && ll.Lat >= s && ll.Lat <= n
		}
	}

	// stop → set of route ids that call there; per pattern, mask lookup
	// key sans the bbox-clip suffix (a clip piece IS its parent pattern)
	maskKey := func(p gtfs.Pattern) string {
		sid := p.ShapeID
		if i := strings.Index(sid, "#clip"); i >= 0 {
			sid = sid[:i]
		}
		return p.Route.ID + "\x1f" + sid
	}
	stopRoutes := map[string]map[string]bool{}
	routeByID := map[string]gtfs.Route{}
	routePats := map[string][]int{}
	for pi, p := range pats {
		c := mode.Of(p.Route.Type)
		if c == mode.Bus || c.Hidden() {
			continue
		}
		routeByID[p.Route.ID] = p.Route
		routePats[p.Route.ID] = append(routePats[p.Route.ID], pi)
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
		stops  []string
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
		g.stops = append(g.stops, sid)
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

	// merge same-named groups into one station — but only where a rider
	// really doesn't pay again. Within one feed, transfers.txt is the
	// ground truth when the feed ships it: NYC's two "Rector St" stations
	// sit a block apart with NO transfer (two stations, two labels), while
	// the four "Fulton St" platforms are one linked complex. Feeds without
	// transfers fall back to a tight 150 m name match (any looser and the
	// two distinct "23 St" stations fold). Across feeds there are no ids
	// or transfers to link, names are all we have, and terminals sprawl —
	// 300 m is what puts the LIRR's Madison concourse under Grand Central
	// (276 m from Metro-North's centroid). Different names never merge:
	// Apple labels Atlantic Terminal and Atlantic Av–Barclays separately,
	// and so do we.
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
	keyIdx := map[string]int{}
	for i, n := range nodes {
		keyIdx[n.g.key] = i
	}
	groupKeyOf := func(stopID string) string {
		if st, ok := feed.Stops[stopID]; ok && st.Parent != "" {
			return st.Parent
		}
		return stopID
	}
	feedHasTr := map[string]bool{}
	for _, tr := range feed.Transfers {
		feedHasTr[feedPrefix(tr[0])] = true
		i, iok := keyIdx[groupKeyOf(tr[0])]
		j, jok := keyIdx[groupKeyOf(tr[1])]
		if iok && jok && i != j && nodes[i].norm == nodes[j].norm {
			parent[find(i)] = find(j)
		}
	}
	byNorm := map[string][]int{}
	for i, n := range nodes {
		byNorm[n.norm] = append(byNorm[n.norm], i)
	}
	for _, idxs := range byNorm {
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				i, j := idxs[a], idxs[b]
				sameFeed := nodes[i].feed == nodes[j].feed
				if sameFeed && feedHasTr[nodes[i].feed] {
					continue // transfers are authoritative for this feed
				}
				lim := 300.0
				if sameFeed {
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
		sortBullets(rids, routeByID)

		st := Station{Name: name, LL: geo.LL{Lon: cx / float64(np), Lat: cy / float64(np)}}
		trunkHexes := map[string]map[string]int{} // trunk key → hex votes
		agencies := map[string]bool{}
		for _, rid := range rids {
			rt := routeByID[rid]
			st.Routes = append(st.Routes, rid)
			// regional routes often ship no short name — the long name is
			// the branch identity ("Hudson", "Babylon Branch")
			st.Labels = append(st.Labels, displayLabel(rt))
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
		// station acts from stop membership: OR the masks of exactly the
		// patterns that STOP at one of this station's platforms
		if pacts != nil {
			stopSet := map[string]bool{}
			for _, i := range members {
				for _, sid := range nodes[i].g.stops {
					stopSet[sid] = true
				}
			}
			st.Acts = make([]string, len(st.Routes))
			for i, rid := range st.Routes {
				var m gtfs.Mask168
				found := false
				for _, pi := range routePats[rid] {
					p := &pats[pi]
					pm, ok := pacts[maskKey(*p)]
					if !ok {
						continue
					}
					for _, sid := range p.StopIDs {
						if stopSet[sid] {
							m = m.Or(pm)
							found = true
							break
						}
					}
				}
				if found {
					st.Acts[i] = m.Hex()
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// SnapStations moves every station onto the DRAWN map. Platform
// centroids sit where the operator built the mezzanine, not where FAIR
// drew the ribbons — markers must sit on the adjusted lines or they
// float beside them. Per station, each member route snaps to the
// nearest band-15 ribbon that carries it; routes sharing one centerline
// become one Marker (a complex spanning several corridors gets several,
// which is how Apple draws Times Square). The label anchors at the
// marker with the most routes.
//
// pitch is FAIR's slot gap in px (the fair_gap_px dial): bundle span =
// (nslots−1)·pitch, and a ribbon's offset is already baked in OffsetPx.
// bbox, when present, marks window-cut line ends: a ribbon running off
// the map is not a terminus and never attracts the terminal clamp.
func SnapStations(sts []Station, segs []stages.Segment, frame geo.Frame,
	pitch float64, routes map[string]gtfs.Route, bbox []float64) {
	nearClip := func(p geo.Pt) bool { return false }
	if len(bbox) == 4 {
		// clipPatterns cuts shapes at bbox ± 0.02°, so an endpoint out in
		// that margin band is a window cut, not real trackage ending
		nearClip = func(p geo.Pt) bool {
			ll := frame.ToLL(p)
			return ll.Lon < bbox[0]-0.015 || ll.Lon > bbox[2]+0.015 ||
				ll.Lat < bbox[1]-0.015 || ll.Lat > bbox[3]+0.015
		}
	}
	// the most detailed band has every class; snap against only that copy
	maxBand := 0
	for i := range segs {
		if segs[i].BandMin > maxBand {
			maxBand = segs[i].BandMin
		}
	}
	routeSegs := map[string][]int{} // route id → snap candidates (steady/bridge)
	routeCont := map[string][]int{} // route id → continuation candidates (+transitions)
	for i := range segs {
		s := &segs[i]
		if s.BandMin != maxBand || s.Line == nil {
			continue
		}
		if s.Kind == "steady" || s.Kind == "bridge" {
			for _, r := range s.Routes {
				routeSegs[r] = append(routeSegs[r], i)
				routeCont[r] = append(routeCont[r], i)
			}
		} else if s.Kind == "transition" {
			// consecutive steady pieces do NOT touch at junctions — a
			// transition bridges the cut-back gap. The terminal clamp's
			// continuation test must see them, or every junction seam
			// reads as a dead end and markers get dragged onto it
			// (Atlantic Av-Barclays' 2/3/4/5 landed 200 m south).
			for _, r := range s.Routes {
				routeCont[r] = append(routeCont[r], i)
			}
		}
	}
	const maxSnapM = 150.0
	// two routes share one marker when they snap to the same SPOT — not
	// the same segment: parallel ribbons of one bundle are split at
	// different junctions per color (the A/C piece and the G piece under
	// Schermerhorn St have different endpoints), so segment identity
	// would split a bundle into stacked markers.
	const sameSpotM = 20.0

	for si := range sts {
		st := &sts[si]
		st.LabelLL = st.LL
		pt := frame.ToXY(st.LL)

		type hit struct {
			seg  int
			arc  float64
			dist float64
		}
		byRoute := map[string]hit{}
		for ri, r := range st.Routes {
			_ = ri
			best := hit{seg: -1, dist: maxSnapM}
			for _, siG := range routeSegs[r] {
				arc, d := segs[siG].Line.ProjectArc(pt)
				if d < best.dist {
					best = hit{seg: siG, arc: arc, dist: d}
				}
			}
			if best.seg >= 0 {
				byRoute[r] = best
			}
		}

		// cluster snapped routes by snap-point proximity (single-link);
		// unsnapped routes fall back to one marker at the platform
		// centroid. Route order keeps clusters deterministic.
		snapPt := map[string]geo.Pt{}
		for r, h := range byRoute {
			snapPt[r] = segs[h.seg].Line.AtArc(h.arc)
		}
		groups := map[string][]string{}
		var order []string
		for _, r := range st.Routes {
			key := "~unsnapped"
			if p, ok := snapPt[r]; ok {
				key = ""
				for _, k := range order {
					if k == "~unsnapped" {
						continue
					}
					for _, m := range groups[k] {
						if q, ok2 := snapPt[m]; ok2 && p.Dist(q) <= sameSpotM {
							key = k
							break
						}
					}
					if key != "" {
						break
					}
				}
				if key == "" {
					key = "g" + r // new cluster, seeded by this route
				}
			}
			if _, seen := groups[key]; !seen {
				order = append(order, key)
			}
			groups[key] = append(groups[key], r)
		}

		modeOf := map[string]string{}
		for i, r := range st.Routes {
			modeOf[r] = st.Modes[i]
		}
		// per-route hours AT this station, from stop membership
		// (BuildStations); markers inherit their routes' entries
		actOf := func(r string) string {
			for i, rr := range st.Routes {
				if rr == r && i < len(st.Acts) {
					return st.Acts[i]
				}
			}
			return ""
		}

		bestN := 0
		for _, key := range order {
			rts := groups[key]
			m := Marker{Routes: rts, LL: st.LL}
			for _, r := range rts {
				m.Modes = append(m.Modes, modeOf[r])
				m.Labels = append(m.Labels, displayLabel(routes[r]))
				m.RouteHex = append(m.RouteHex, routeHex(routes[r]))
				m.Acts = append(m.Acts, actOf(r))
			}
			trunks := map[string]bool{}
			for _, r := range rts {
				trunks[mode.TrunkKey(routes[r])] = true
			}
			m.Lines = len(trunks)
			if key != "~unsnapped" {
				// snap at the closest member's projection
				bh := hit{seg: -1, dist: math.Inf(1)}
				for _, r := range rts {
					if h := byRoute[r]; h.dist < bh.dist {
						bh = h
					}
				}
				sg := &segs[bh.seg]
				// terminus clamp, chain-walking edition. A terminal's
				// drawn line overshoots its stop point (the platforms
				// themselves at Atlantic Terminal, the shape-trim margin
				// elsewhere), and terminal cuts may have seamed it — so
				// from the end nearest the snap, FOLLOW straight-ahead
				// same-route pieces through seams. Chain dead-ends
				// within reach → that far tip is the terminus, put the
				// marker there. Chain diverges at a junction or keeps
				// going → an inline stop, never moved.
				const termM = 250.0    // how far shy of an end the walk may start
				const touchM = 25.0    // endpoint coincidence tolerance
				const probeM = 60.0    // how far along a toucher to look
				const asideM = 30.0    // within this, a toucher is a sibling
				const walkMaxM = 500.0 // chains longer than this are inline track
				L := sg.Line.Len()
				ends := []float64{0, L}
				if bh.arc > L/2 {
					ends = []float64{L, 0} // try the nearer end first
				}
				for _, endArc := range ends {
					if math.Abs(bh.arc-endArc) > termM {
						continue
					}
					cur, curEnd := bh.seg, endArc
					prev := -1
					walked := 0.0
					tipSeg, tipArc := -1, 0.0
					for hops := 0; hops < 5; hops++ {
						sgc := &segs[cur]
						Lc := sgc.Line.Len()
						endPt := sgc.Line.AtArc(curEnd)
						if nearClip(endPt) {
							break // running off the map, not a terminus
						}
						var tan geo.Pt // outgoing direction at this end
						if curEnd < 1 {
							tan = sgc.Line.TangentAtArc(0, 20).Scale(-1)
						} else {
							tan = sgc.Line.TangentAtArc(Lc, 20)
						}
						next, nextEnter := -1, 0.0
						diverges := false
						for _, r := range rts {
							for _, si2 := range routeCont[r] {
								if si2 == cur || si2 == prev {
									continue
								}
								o := segs[si2].Line
								var tArc float64 = -1
								if o.Pts[0].Dist(endPt) < touchM {
									tArc = 0
								} else if o.Pts[len(o.Pts)-1].Dist(endPt) < touchM {
									tArc = o.Len()
								}
								if tArc < 0 {
									continue
								}
								probeArc := math.Min(probeM, o.Len())
								if tArc > 0 {
									probeArc = math.Max(0, o.Len()-probeM)
								}
								probe := o.AtArc(probeArc)
								if _, d := sgc.Line.ProjectArc(probe); d < asideM {
									continue // sibling ribbon ending alongside
								}
								dir := probe.Sub(endPt)
								if dir.Dot(dir) < 1 {
									continue
								}
								if dir.Unit().Dot(tan) > 0.5 {
									if next < 0 {
										next, nextEnter = si2, tArc
									}
								} else {
									diverges = true
								}
							}
							if diverges {
								break
							}
						}
						if diverges {
							break // a junction — inline stop, leave it alone
						}
						if next < 0 {
							tipSeg, tipArc = cur, curEnd // true dead end
							break
						}
						walked += segs[next].Line.Len()
						if walked > walkMaxM {
							break // long continuation — inline track
						}
						prev, cur = cur, next
						if nextEnter < 1 {
							curEnd = segs[next].Line.Len()
						} else {
							curEnd = 0
						}
					}
					if tipSeg >= 0 {
						sg = &segs[tipSeg]
						bh.seg, bh.arc = tipSeg, tipArc
						break
					}
				}
				xy := sg.Line.AtArc(bh.arc)
				m.LL = frame.ToLL(xy)
				t := sg.Line.TangentAtArc(bh.arc, 20)
				m.Bearing = math.Atan2(t.X, t.Y) * 180 / math.Pi
				// which RIBBONS do this marker's routes occupy? Each dot
				// takes the segment's DRAWN color — an agency trunk's dot
				// matches the purple line on the map, not the branches'
				// own colors — at the ribbon's slot offset. Pill only when
				// the lines fill the whole bundle: an express stop on a
				// wider corridor gets a dot per stopping line instead of a
				// band over lines that pass it by.
				seen := map[string]Dot{}
				nslots := 0
				for _, r := range rts {
					s2 := &segs[byRoute[r].seg]
					if s2.NSlots > nslots {
						nslots = s2.NSlots
					}
					k := strconv.FormatFloat(s2.OffsetPx, 'f', 1, 64)
					if _, ok := seen[k]; !ok {
						seen[k] = Dot{Hex: s2.Color, Off: s2.OffsetPx}
					}
				}
				if len(seen) >= nslots && len(seen) > 1 {
					m.Pill = true
					m.SpanPx = float64(nslots-1) * pitch
				} else {
					for _, d := range seen {
						m.Dots = append(m.Dots, d)
					}
					sort.Slice(m.Dots, func(i, j int) bool { return m.Dots[i].Off < m.Dots[j].Off })
				}
			} else {
				// unsnapped fallback: one gray-or-majority dot at center
				hexes := map[string]int{}
				for _, r := range rts {
					hexes[routeHex(routes[r])]++
				}
				var hxs []string
				for h := range hexes {
					hxs = append(hxs, h)
				}
				sort.Strings(hxs)
				best, bestV := "888888", 0
				for _, h := range hxs {
					if hexes[h] > bestV {
						best, bestV = h, hexes[h]
					}
				}
				m.Dots = []Dot{{Hex: best}}
			}
			st.Markers = append(st.Markers, m)
			if len(rts) > bestN {
				bestN = len(rts)
				st.LabelLL = m.LL
			}
		}
	}
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

// displayLabel is the bullet text for one route: short name, falling
// back to the long name (regional branches ship no short name). Aligned
// arrays are comma-joined in the geojson — a comma inside a long name
// would shift every array after it.
func displayLabel(rt gtfs.Route) string {
	l := rt.ShortName
	if l == "" {
		l = rt.LongName
	}
	return strings.ReplaceAll(l, ",", " ")
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

// sortBullets orders a station's routes for display — the order every
// aligned array (labels, colors, modes) and every bullet strip follows.
// Policies (docs/STOP-LABELS.md, "Bullet ordering"):
//
//   - color (default): group by resolved bullet color — a shared trunk
//     reads as one run, NYC-style (A·C·E then B·D·F·M); natural order
//     within a group; letter groups before number groups, matching the
//     MTA's own service listing (…N,Q,R,W before 1,2,3). Systems where
//     every line has its own color collapse to plain natural order.
//   - feed: the feed's route_sort_order where present, natural fallback.
//   - natural: plain numeric-aware sort.
func sortBullets(rids []string, routeByID map[string]gtfs.Route) {
	policy := style.Active().BulletOrder
	label := func(rid string) string { return displayLabel(routeByID[rid]) }
	switch policy {
	case style.BulletsFeed:
		sort.Slice(rids, func(i, j int) bool {
			a, b := routeByID[rids[i]], routeByID[rids[j]]
			sa, sb := a.SortOrder, b.SortOrder
			if sa < 0 {
				sa = math.MaxInt32
			}
			if sb < 0 {
				sb = math.MaxInt32
			}
			if sa != sb {
				return sa < sb
			}
			if c := naturalCmp(label(rids[i]), label(rids[j])); c != 0 {
				return c < 0
			}
			return a.ID < b.ID
		})
	case style.BulletsNatural:
		sort.Slice(rids, func(i, j int) bool {
			if c := naturalCmp(label(rids[i]), label(rids[j])); c != 0 {
				return c < 0
			}
			return rids[i] < rids[j]
		})
	default: // style.BulletsColor
		// each color group is ranked by its first member's label —
		// the NATURAL first, so "7,7X" is represented by "7" and stays
		// a number group — letters before numbers between groups
		rep := map[string]string{}
		for _, rid := range rids {
			hx := routeHex(routeByID[rid])
			l := label(rid)
			if cur, ok := rep[hx]; !ok || naturalCmp(l, cur) < 0 {
				rep[hx] = l
			}
		}
		sort.Slice(rids, func(i, j int) bool {
			ha, hb := routeHex(routeByID[rids[i]]), routeHex(routeByID[rids[j]])
			if ha != hb {
				if c := lettersFirstCmp(rep[ha], rep[hb]); c != 0 {
					return c < 0
				}
				return ha < hb
			}
			if c := naturalCmp(label(rids[i]), label(rids[j])); c != 0 {
				return c < 0
			}
			return rids[i] < rids[j]
		})
	}
}

// lettersFirstCmp ranks color GROUPS: numeric labels sort numerically,
// but letter groups come before number groups — the MTA's service
// listing runs A,C,E … N,Q,R,W then 1,2,3 … 7, and Apple renders
// Columbus Circle as A·C B·D 1.
func lettersFirstCmp(a, b string) int {
	_, ea := strconv.Atoi(a)
	_, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil:
		return naturalCmp(a, b)
	case ea == nil:
		return 1 // a is a number → after letters
	case eb == nil:
		return -1
	}
	return strings.Compare(a, b)
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

// writeStations emits <out>.stations.geojson: one `ftype: "station"`
// label feature per station (anchored at its busiest marker) plus one
// `ftype: "marker"` feature per snapped bundle — a complex is one label
// and as many markers as corridors. Aligned per-route arrays are
// comma-joined like ribbon `routes`.
func writeStations(path string, sts []Station) error {
	fc := collection{Type: "FeatureCollection"}
	pt := func(ll geo.LL) json.RawMessage {
		raw, _ := json.Marshal([2]float64{ll.Lon, ll.Lat})
		return raw
	}
	for _, s := range sts {
		label := s.LabelLL
		if label == (geo.LL{}) {
			label = s.LL
		}
		fc.Features = append(fc.Features, feature{
			Type: "Feature",
			Props: map[string]any{
				"ftype":        "station",
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
				"nmarkers":     len(s.Markers),
				"acts":         strings.Join(s.Acts, ";"),
			},
			Geom: geomJSON{Type: "Point", Coords: pt(label)},
		})
		for _, m := range s.Markers {
			props := map[string]any{
				"ftype":        "marker",
				"name":         s.Name,
				"routes":       strings.Join(m.Routes, ","),
				"labels":       strings.Join(m.Labels, ","),
				"route_colors": strings.Join(m.RouteHex, ","),
				"modes":        strings.Join(m.Modes, ","),
				"acts":         strings.Join(m.Acts, ";"),
				"nlines":       m.Lines,
				"bearing":      math.Round(m.Bearing*10) / 10,
				"rank":         len(s.Routes),
				"nmarkers":     len(s.Markers),
			}
			if m.Pill {
				props["span_px"] = math.Round(m.SpanPx*10) / 10
			} else {
				// "hex@off" per occupied ribbon — the client bakes these
				// into one marker image
				parts := make([]string, len(m.Dots))
				for i, d := range m.Dots {
					parts[i] = d.Hex + "@" + strconv.FormatFloat(math.Round(d.Off*10)/10, 'f', -1, 64)
				}
				props["dots"] = strings.Join(parts, ";")
			}
			fc.Features = append(fc.Features, feature{
				Type:  "Feature",
				Props: props,
				Geom:  geomJSON{Type: "Point", Coords: pt(m.LL)},
			})
		}
	}
	return writeFC(path, fc)
}
