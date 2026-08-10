package pipeline

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/alexwohlbruck/portolan/internal/corridor"
	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
	"github.com/alexwohlbruck/portolan/internal/stages"
)

// The corridor path: chart a city whose corridor graph is GIVEN.
//
// BUNDLE and MATCH exist to answer one question — where do the corridors
// run, and which of them does each route ride — from raw OSM track. A
// caller who already knows the answer (a planning tool, a scenario
// editor, a simulator, a re-run of a previous build) should not pay for
// the question, and should not inherit MATCH's probabilistic failure
// modes on data where the truth was free.
//
// So this path skips internal/osm, internal/bundle, MATCH and SPLIT
// outright, and joins the shared pipeline at ORDER. Everything from
// there — ORDER, FAIR, terminal cuts, stations, caterpillars, style,
// every emitter — is the same code the OSM path runs, by construction:
// both call layout().

// stopOnCorridorM is how close a stop must lie to a corridor its route
// rides before the stations stage counts the route as calling there.
// Only edges carrying that route are considered, so this cannot attach a
// stop to a neighbour's line; it only has to span the offset between a
// platform and the track centre it serves.
const stopOnCorridorM = 120.0

// anchorGrid quantizes a derived frame origin, in degrees. See
// frameFor: the point is that the origin must NOT move when the network
// grows, and an extent centre moves every time an edge is added.
const anchorGrid = 0.25

// frameFor picks the projection origin. An explicit anchor is used
// verbatim; otherwise the graph's extent centre is snapped to a fixed
// global grid.
//
// This matters more than it looks. Every metric in the pipeline is
// computed in frame coordinates, so moving the origin perturbs every
// float in the build. An origin derived from the network's own extent
// moves whenever the network grows — which means an editing client that
// adds one corridor at the edge of town gets a different rounding, and
// therefore potentially a different slot order, in the middle of the
// city. Quantizing pins the origin for every network inside one grid
// cell; passing --anchor pins it absolutely.
func frameFor(anchor *geo.LL, w, s, e, n float64) geo.Frame {
	if anchor != nil {
		return geo.NewFrame(*anchor)
	}
	q := func(v float64) float64 { return math.Round(v/anchorGrid) * anchorGrid }
	return geo.NewFrame(geo.LL{Lon: q((w + e) / 2), Lat: q((s + n) / 2)})
}

func chartCorridors(ctx context.Context, o ChartOpts, d Dials, logf func(string, ...any)) error {
	t0 := time.Now()
	step := func(stage string, pct int) {
		if o.Progress != nil {
			o.Progress(stage, pct)
		}
	}
	step("load", 5)
	lap := func(stage string, since time.Time) time.Time {
		logf("  %-14s %6.0f ms", stage, time.Since(since).Seconds()*1000)
		return time.Now()
	}

	mark := time.Now()
	g, err := corridor.LoadPair(o.Corridors, o.CorridorNodes)
	if err != nil {
		return err
	}
	logf("corridors: %d nodes, %d corridors from %s",
		len(g.Nodes), len(g.Edges), o.Corridors)
	mark = lap("load", mark)

	w, s, e, n := g.Bounds()
	frame := frameFor(o.Anchor, w, s, e, n)

	if o.GTFS == "" && len(o.GTFSInline) == 0 {
		return fmt.Errorf("chart --corridors needs --gtfs: the corridor graph names routes by " +
			"route_id, and routes.txt is what those ids mean")
	}
	// THE GRAPH DECIDES which routes load. On the OSM path the feed is
	// filtered by mode — buses gated behind --streets, because without
	// street geometry there is nothing to match them onto. Here the
	// caller has already said exactly which routes ride which corridor,
	// so mode is the wrong question and route membership is the right
	// one: a bus on an authored corridor draws like anything else, and a
	// route the graph never names is not loaded at all.
	//
	// This is also what keeps the corridors path fast. Atlanta's feed is
	// 86 routes of which 5 are rail; admitting every drawable class ran
	// the stop_times sweep over the whole bus network for nothing, a
	// second of load on a build that should take tens of milliseconds.
	drawable := graphRouteFilter(g)
	cover := d.Cover
	if o.Scenario != "" {
		cover = 1.01
	}
	feed, err := loadFeeds(o, o.GTFS, cover, drawable)
	if err != nil {
		return err
	}
	logf("corridors: %d routes, %d stops, %d patterns",
		len(feed.Routes), len(feed.Stops), len(feed.Patterns))
	mark = lap("gtfs", mark)
	step("gtfs", 20)

	net, err := g.Network(frame)
	if err != nil {
		return err
	}
	if err := g.Validate(net, feed.Routes, frame, logf); err != nil {
		return err
	}
	mark = lap("topology", mark)
	step("topology", 30)

	// service scenarios still work: the graph's route membership is
	// static, so building one scenario means dropping the routes that do
	// not run in it from every edge, and any edge left carrying nobody.
	if o.Scenario != "" {
		if err := restrictToScenario(net, feed, o, d, logf); err != nil {
			return err
		}
		mark = lap("scenario", mark)
	}

	// Activity masks are best-effort exactly as on the OSM path. An
	// authored network usually has no calendar at all, in which case
	// every mask stays nil, edges carry no Acts, the terminal-cut pass
	// returns its input untouched, and stations emit no acts property.
	// The viewer then falls back to route-level masks, which for a
	// network with no timetable is the whole truth anyway.
	var patActs map[string]gtfs.Mask168
	if si, err := serviceInfoFor(o); err == nil {
		pm := si.PatternMasks()
		patActs = make(map[string]gtfs.Mask168, len(pm))
		for k, m := range pm {
			patActs[k.Route+"\x1f"+k.Shape] = m
		}
		stages.SetPatternActs(patActs)
		logf("corridors: activity masks for %d patterns", len(pm))
	} else {
		stages.SetPatternActs(nil)
		logf("corridors: no service calendar (%v) — the map is drawn without hours", err)
	}
	// SPLIT normally ORs pattern masks onto the edges it creates. There
	// is no SPLIT here, so the same join is done directly from the
	// graph's own route membership: every pattern of a route donates its
	// hours to every edge that route rides.
	if patActs != nil {
		applyEdgeActs(net, feed, patActs)
	}
	mark = lap("service", mark)

	la := map[string]bool{}
	for _, a := range o.LineAgencies {
		la[strings.TrimSpace(a)] = true
	}
	mode.SetLineAgencies(la)
	stages.SetAgencyNames(feed.Agencies)
	mode.SetAgencyNames(feed.Agencies)
	stages.SetFerryRoutes(routeSetOf(feed.Routes, mode.Ferry))
	if v := os.Getenv("PORTOLAN_DBG3"); v != "" {
		var lat, lon float64
		fmt.Sscanf(v, "%f,%f", &lat, &lon)
		stages.SetDbg3(frame.ToXY(geo.LL{Lat: lat, Lon: lon}))
	}

	paths := g.Traversals(net, feed, frame, logf)
	mark = lap("traversal", mark)
	step("traversal", 45)

	if err := writeNetwork(o.Out, net, frame); err != nil {
		return err
	}
	pats := feed.Patterns
	if len(pats) == 0 {
		pats = stationPatterns(net, feed, paths, frame)
		logf("corridors: %d station patterns synthesized from stop proximity "+
			"(the feed has no trips)", len(pats))
	}
	mark = lap("emit graph", mark)
	_ = mark

	err = layout(layoutIn{
		ctx: ctx, o: o, d: d, frame: frame, t0: t0,
		net: net, feed: feed, rail: paths,
		pats: pats, acts: patActs,
	}, logf)
	if err != nil {
		return err
	}
	logf("corridors: charted in %.0f ms", time.Since(t0).Seconds()*1000)
	return nil
}

// graphRouteFilter admits exactly the routes the corridor graph names.
//
// The wrinkle is overlay feeds. loadFeeds prefixes an overlay's route
// ids with f1:, f2:… AFTER loading, so a graph naming "f1:26984" meets
// this predicate while the id still reads "26984". Both spellings are
// therefore accepted. Over-accepting is harmless — a route that loads
// and rides nothing simply does not draw — where under-accepting would
// silently lose every overlay ribbon.
func graphRouteFilter(g *corridor.Graph) func(gtfs.Route) bool {
	named := map[string]bool{}
	for _, e := range g.Edges {
		for _, rid := range e.Routes {
			named[rid] = true
			if i := strings.Index(rid, ":"); i > 0 {
				if p := rid[:i]; len(p) > 1 && p[0] == 'f' && isDigits(p[1:]) {
					named[rid[i+1:]] = true
				}
			}
		}
	}
	return func(r gtfs.Route) bool { return named[r.ID] }
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// applyEdgeActs stands in for SPLIT's mask join: a route's hours on an
// edge are the OR of the hours of its patterns. Without SPLIT's walk
// there is no way to tell WHICH pattern rides which part of a route, so
// every pattern donates to every edge the route rides — the same
// best-effort SPLIT falls back to, and the reason short-turn tails do
// not go dark on an authored graph. A caller wanting stop-granular
// hours supplies stop_times.txt, and the terminal-cut pass takes it
// from there.
func applyEdgeActs(net *stages.Network, feed *gtfs.Feed, patActs map[string]gtfs.Mask168) {
	byRoute := map[string]gtfs.Mask168{}
	for _, p := range feed.Patterns {
		m, ok := patActs[p.Route.ID+"\x1f"+p.ShapeID]
		if !ok {
			continue
		}
		cur := byRoute[p.Route.ID]
		byRoute[p.Route.ID] = cur.Or(m)
	}
	if len(byRoute) == 0 {
		return
	}
	for ei := range net.Edges {
		var acts map[string]gtfs.Mask168
		for _, rid := range net.Edges[ei].Routes {
			m, ok := byRoute[rid]
			if !ok || m.Empty() {
				continue
			}
			if acts == nil {
				acts = map[string]gtfs.Mask168{}
			}
			acts[rid] = m
		}
		net.Edges[ei].Acts = acts
	}
}

// restrictToScenario drops the routes that do not run in the named
// scenario from every edge, and then the edges nobody is left riding.
func restrictToScenario(net *stages.Network, feed *gtfs.Feed, o ChartOpts,
	d Dials, logf func(string, ...any)) error {

	si, err := serviceInfoFor(o)
	if err != nil {
		return fmt.Errorf("scenario build: %w", err)
	}
	var sc *gtfs.Scenario
	for _, s := range gtfs.BuildScenarios(si, d.Cover) {
		if s.ID == o.Scenario {
			sc = &s
			break
		}
	}
	if sc == nil {
		return fmt.Errorf("unknown scenario %q", o.Scenario)
	}
	runs := map[string]bool{}
	for k := range si.Select(sc.Cells, d.Cover) {
		runs[k.Route] = true
	}
	var kept []stages.Edge
	dropped := 0
	for _, e := range net.Edges {
		var rs []string
		for _, rid := range e.Routes {
			if runs[rid] {
				rs = append(rs, rid)
			}
		}
		if len(rs) == 0 {
			dropped++
			continue
		}
		e.Routes = rs
		kept = append(kept, e)
	}
	if len(kept) == 0 {
		return fmt.Errorf("scenario %s rides no corridor", sc.ID)
	}
	net.Edges = kept
	stages.RebuildAdj(net)
	var pats []gtfs.Pattern
	for _, p := range feed.Patterns {
		if runs[p.Route.ID] {
			pats = append(pats, p)
		}
	}
	feed.Patterns = pats
	logf("scenario %s (%s): %d routes run, %d corridors carry nobody and were dropped",
		sc.ID, sc.Label, len(runs), dropped)
	return nil
}

// stationPatterns invents the pattern list the stations stage groups
// stops from, for a feed that has no trips at all.
//
// BuildStations wants patterns because on the OSM path that is where
// stop membership lives. Here the corridor graph carries it instead: a
// route calls at a stop when the stop lies on a corridor that route
// rides. One synthetic pattern per route says exactly that, and every
// downstream rule — platform merging, transfer complexes, the
// importance percentile, marker snapping — runs unchanged on it.
//
// Terminals come from the route's own walk where there is one: the
// first and last stop along it. A route with no walk gets no terminals,
// which costs it the terminus label bonus and nothing else.
func stationPatterns(net *stages.Network, feed *gtfs.Feed, paths []stages.Path,
	frame geo.Frame) []gtfs.Pattern {

	byRoute := map[string][]int{}
	for ei, e := range net.Edges {
		for _, rid := range e.Routes {
			byRoute[rid] = append(byRoute[rid], ei)
		}
	}
	// the longest walk per route orders that route's stops
	walk := map[string]*geo.Line{}
	for i := range paths {
		rid := paths[i].Pattern.Route.ID
		if cur, ok := walk[rid]; !ok || paths[i].Line.Len() > cur.Len() {
			walk[rid] = paths[i].Line
		}
	}
	type stopXY struct {
		id string
		p  geo.Pt
	}
	stops := make([]stopXY, 0, len(feed.Stops))
	sids := make([]string, 0, len(feed.Stops))
	for id := range feed.Stops {
		sids = append(sids, id)
	}
	sort.Strings(sids) // determinism: this drives StopIDs and the terminals
	for _, id := range sids {
		st := feed.Stops[id]
		// a parent station record is a roof over platforms, not a place
		// a train stops; BuildStations regroups by parent anyway
		stops = append(stops, stopXY{id, frame.ToXY(st.LL)})
	}

	rids := make([]string, 0, len(byRoute))
	for rid := range byRoute {
		rids = append(rids, rid)
	}
	sort.Strings(rids)

	var out []gtfs.Pattern
	for _, rid := range rids {
		r, ok := feed.Routes[rid]
		if !ok {
			continue
		}
		lines := make([]*geo.Line, 0, len(byRoute[rid]))
		for _, ei := range byRoute[rid] {
			lines = append(lines, geo.NewLine(net.Edges[ei].Pts))
		}
		var called []string
		for _, s := range stops {
			for _, l := range lines {
				if l.Within(s.p, stopOnCorridorM) {
					called = append(called, s.id)
					break
				}
			}
		}
		if len(called) == 0 {
			continue
		}
		pat := gtfs.Pattern{
			Route: r, ShapeID: "corridor:" + rid, Trips: 1,
			StopIDs: append([]string(nil), called...),
		}
		sort.Strings(pat.StopIDs)
		if l, ok := walk[rid]; ok && len(called) >= 2 {
			ordered := append([]string(nil), called...)
			arc := map[string]float64{}
			for _, id := range ordered {
				a, _ := l.ProjectArc(frame.ToXY(feed.Stops[id].LL))
				arc[id] = a
			}
			sort.SliceStable(ordered, func(i, j int) bool {
				if arc[ordered[i]] != arc[ordered[j]] {
					return arc[ordered[i]] < arc[ordered[j]]
				}
				return ordered[i] < ordered[j]
			})
			pat.StopSeq = ordered
			pat.TermAID, pat.TermBID = ordered[0], ordered[len(ordered)-1]
			pat.TermA = feed.Stops[pat.TermAID].LL
			pat.TermB = feed.Stops[pat.TermBID].LL
		}
		out = append(out, pat)
	}
	return out
}
