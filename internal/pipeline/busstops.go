package pipeline

import (
	"sort"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
)

// BuildBusStops lists every stop a bus pattern calls at, one entry per
// GTFS stop. Pass the feed's whole pattern list, not the drawn one:
// coverage pruning decides what is worth drawing, not which stops exist. Buses draw no stations, so without this their stops never
// reached OSM matching and kept the feed's bookkeeping names everywhere
// downstream ("BROADWAY/W 42 ST"). A bus stop is its own pole: the two
// directions of one corner are two stops with two OSM nodes, so nothing
// is grouped.
func BuildBusStops(feed *gtfs.Feed, pats []gtfs.Pattern, bbox []float64) []Station {
	in := func(sid string) (gtfs.Stop, bool) {
		st, ok := feed.Stops[sid]
		if !ok {
			return st, false
		}
		if len(bbox) == 4 {
			const margin = 0.02
			ll := st.LL
			if ll.Lon < bbox[0]-margin || ll.Lon > bbox[2]+margin ||
				ll.Lat < bbox[1]-margin || ll.Lat > bbox[3]+margin {
				return st, false
			}
		}
		return st, true
	}
	// a stop any other mode calls at belongs to its drawn station, which
	// names it; a second, bus-only verdict would fight that one
	station := map[string]bool{}
	for _, p := range pats {
		if c := mode.Of(p.Route.Type); c != mode.Bus && !c.Hidden() {
			for _, sid := range p.StopIDs {
				station[sid] = true
			}
		}
	}
	seen := map[string]bool{}
	var ids []string
	for _, p := range pats {
		if mode.Of(p.Route.Type) != mode.Bus {
			continue
		}
		for _, sid := range p.StopIDs {
			if !seen[sid] && !station[sid] {
				seen[sid] = true
				ids = append(ids, sid)
			}
		}
	}
	sort.Strings(ids)
	var out []Station
	for _, sid := range ids {
		st, ok := in(sid)
		if !ok || st.Name == "" {
			continue
		}
		out = append(out, Station{Name: st.Name, LL: st.LL,
			Modes: []string{mode.Bus.String()}, StopIDs: []string{sid}})
	}
	return out
}

// signNames collects the OSM name each renamed station's GTFS stops are to
// carry in the corrected export, keyed by stop id as loadFeeds prefixed it.
// Only a station that actually took an OSM name contributes: a match that
// kept the feed's own spelling leaves its stops alone.
func signNames(groups ...[]Station) map[string]string {
	out := map[string]string{}
	for _, sts := range groups {
		for _, s := range sts {
			if !s.Renamed {
				continue
			}
			for _, sid := range s.StopIDs {
				out[sid] = s.Name
			}
		}
	}
	return out
}

// busSiblingM: how far apart two same-named bus stops may be and still be
// the two directions of one stop.
const busSiblingM = 150.0

// withSiblings extends bus stop matches to their unmatched other half. A
// feed files a stop once per direction under one name, OSM often maps
// only one of the two poles, and a rename must move every stop of a name
// together — so one matched direction held both at the feed's name. The
// sibling takes the same OSM name, which its own name already agrees
// with, but not the OSM id: that node is the pole across the street.
func withSiblings(stops []Station, ms []StopMatch) []StopMatch {
	matched := make([]bool, len(stops))
	for _, m := range ms {
		matched[m.Station] = true
	}
	byName := map[string][]StopMatch{}
	for _, m := range ms {
		byName[stops[m.Station].Name] = append(byName[stops[m.Station].Name], m)
	}
	out := append([]StopMatch(nil), ms...)
	for i, s := range stops {
		if matched[i] {
			continue
		}
		for _, m := range byName[s.Name] {
			if metersBetween(s.LL, stops[m.Station].LL) <= busSiblingM {
				out = append(out, StopMatch{Station: i, FeedName: s.Name,
					OSMName: m.OSMName, Sim: m.Sim, LostInfo: m.LostInfo})
				break
			}
		}
	}
	return out
}
