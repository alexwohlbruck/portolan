// Package mode is the transit-mode taxonomy (docs/MODES.md): every GTFS
// route_type — basic and the extended HVT ranges — collapses into a small
// set of classes, and everything downstream keys off the class, never the
// raw number. The class decides three things: whether the pipeline draws
// the route at all, which infrastructure it may match onto, and how its
// routes trunk into ribbons.
package mode

import "github.com/alexwohlbruck/portolan/internal/gtfs"

type Class int

const (
	Unknown Class = iota
	Metro
	Tram
	Regional // commuter, S-Bahn, intercity, high-speed, coach
	Monorail
	Funicular
	Cable // street-running cable car (SF): tram tracks, cable propulsion
	Aerial
	Ferry
	Bus
)

var names = map[Class]string{
	Unknown: "unknown", Metro: "metro", Tram: "tram", Regional: "regional",
	Monorail: "monorail", Funicular: "funicular", Cable: "cable",
	Aerial: "aerial", Ferry: "ferry", Bus: "bus",
}

func (c Class) String() string { return names[c] }

// Of maps a GTFS route_type to its class. Basic types first, then the
// extended ranges (Google's HVT set, common in European feeds).
func Of(routeType int) Class {
	switch routeType {
	case 0:
		return Tram
	case 1:
		return Metro
	case 2:
		return Regional
	case 3, 11: // bus, trolleybus
		return Bus
	case 4:
		return Ferry
	case 5:
		return Cable
	case 6:
		return Aerial
	case 7:
		return Funicular
	case 12:
		return Monorail
	}
	switch {
	case routeType >= 100 && routeType < 300: // rail services + coach
		return Regional
	case routeType == 405:
		return Monorail
	case routeType >= 400 && routeType < 500:
		return Metro
	case routeType >= 700 && routeType < 900: // bus + trolleybus
		return Bus
	case routeType >= 900 && routeType < 1000:
		return Tram
	case routeType >= 1000 && routeType < 1300: // water + air + taxi
		return Ferry
	case routeType >= 1300 && routeType < 1400:
		return Aerial
	case routeType >= 1400 && routeType < 1500:
		return Funicular
	case routeType >= 1700 && routeType < 1800: // misc, incl. 1701 cable
		return Cable
	}
	return Unknown
}

// Drawable reports whether the pipeline charts this class. Bus requires a
// street extract — the pipeline drops bus patterns when the city has none
// configured (streets are opt-in per city, not part of the rail extract).
func (c Class) Drawable() bool {
	return c != Unknown
}

// BandFloor is the lowest FAIR zoom band this class draws in (fair.go
// emits one segment copy per band; bands below the floor skip the class).
// Tram sits at 0 despite the docs/MODES.md inference of 13: GTFS type 0
// covers both streetcars AND light-rail backbones, and a floor of 13
// erased Charlotte's Lynx and LA's A/C/E/K below the default zoom — for
// those cities the "tram" IS the skeleton. Streetcar demotion needs a
// signal GTFS doesn't carry (docs/MODES.md observation pass). The
// remaining floors are still inferences — change them there first,
// then here.
func (c Class) BandFloor() int {
	switch c {
	case Ferry:
		return 13
	case Aerial, Funicular, Cable, Bus:
		return 15
	}
	return 0
}

// lineAgencies: regional agencies whose routes keep PER-LINE identity
// (config: line_agencies in the city's portolan.json row). The default
// for regional is AGENCY trunking — Apple draws the LIRR's twelve
// branch-diagram colors as one line, and 12 distinct groups through
// Jamaica is exactly the bundle blow-up the design forbids — but some
// networks' per-line colors ARE the cartography (Paris RER A–E,
// Transilien's lettered lines), and no computable threshold separates
// branch colors from line brands. That call is curation, so it lives in
// config, the same way Apple makes it with humans.
var lineAgencies map[string]bool

func SetLineAgencies(m map[string]bool) { lineAgencies = m }

// TrunkKey is the slot unit for ORDER and FAIR: routes with equal keys
// share one ribbon (docs/MODES.md, "The trunk key"). Color-trunked rail
// keeps law 5 — the key IS the color string, byte-for-byte, so NYC and
// Chicago's subways are unchanged. Regional trunks by AGENCY unless the
// agency is configured as a line agency; class-level collapse (European
// intercity, TER) falls out for free where the feed colors a class
// uniformly. The singleton classes never merge; bus is the corridor.
func TrunkKey(r gtfs.Route) string {
	c := Of(r.Type)
	switch c {
	case Bus:
		// corridor trunking: grouping is per edge already (ORDER and FAIR
		// group within each edge), so a constant key collapses every bus
		// route on an edge into exactly one ribbon — the street's ribbon
		// count stops depending on how many routes ride it.
		return "bus"
	case Ferry:
		// one trunk per channel, like buses per street: grouping is per
		// edge, and with the seaway channel merge every co-channel ferry
		// lands on one edge — one blue ribbon per waterway
		return "ferry"
	case Aerial, Funicular, Cable:
		return "route:" + r.ID
	case Regional:
		if lineAgencies[r.Agency] || r.Agency == "" {
			if r.Color != "" {
				return r.Color
			}
			if r.Agency != "" {
				return "agency:" + r.Agency
			}
			return "route:" + r.ID
		}
		return "agency:" + r.Agency
	}
	if r.Color == "" {
		return "888888"
	}
	return r.Color
}
