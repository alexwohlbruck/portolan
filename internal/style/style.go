// Package style is the single place that answers "what does this class of
// transit look like, and how do its routes merge" — colors, ribbon weights,
// zoom floors, trunk policy — plus the per-city color overrides that let a
// human fix what a feed gets wrong.
//
// Two problems drove it into one package. Feeds lie about color: an agency
// ships every route as #000000, or brands its commuter rail a hue that
// collides with the metro, and the fix has to be curation — there is no
// signal in the data to compute it from. And the class defaults were
// scattered across three files in two languages (mode.go for trunking,
// fair.go for hex, map.html for width and opacity), so changing how ferries
// look meant editing Go AND JavaScript and hoping they agreed. Now the
// pipeline emits its resolved style alongside the build and the viewer
// renders whatever it is told.
//
// Everything here is config with defaults, so a city that says nothing gets
// exactly the behaviour that shipped before this package existed.
package style

import (
	"encoding/json"
	"strings"
)

// Trunk policies — how a class's routes merge into shared ribbons.
const (
	// TrunkColor groups by route_color: law 5, the metro default. Two
	// routes drawn the same color ARE one line to a rider.
	TrunkColor = "color"
	// TrunkAgency groups every one of an agency's routes into one ribbon
	// (Apple draws the LIRR's twelve branch colors as a single line).
	TrunkAgency = "agency"
	// TrunkRoute never merges — for classes with too few routes to bundle.
	TrunkRoute = "route"
	// TrunkNone skips the junction pipeline entirely: the matched path is
	// already the drawn geometry, so it emits directly. The bus default.
	TrunkNone = "none"
	// TrunkIntercity pools routes across AGENCIES into the one shared
	// intercity line — Apple draws Amtrak, VIA and every international
	// operator as a single ribbon. Opt-in per agency or route (never a
	// class default: commuter rail shares route_type 2 and must keep its
	// per-agency identity). Pooling is also what decouples regions: one
	// key means one slot, so a corridor's ordering never depends on which
	// distant operator's trains happen to ride it.
	TrunkIntercity = "intercity"
)

// Class is the look and merge policy for one transit class. Zero values
// mean "unset" — Resolve fills them from the built-in defaults, so a config
// naming one field changes exactly that field.
type Class struct {
	// Color: canonical hex for the whole class, no "#". Empty means the
	// route keeps its own route_color. Ferries and buses default to a
	// canonical color the way Apple paints them; a harbor of per-route
	// brand colors reads as unrelated lines.
	Color string `json:"color,omitempty"`
	// Width and Opacity are ribbon rendering, relative to a metro's 1.0.
	Width   *float64 `json:"width,omitempty"`
	Opacity *float64 `json:"opacity,omitempty"`
	// BandFloor: lowest FAIR zoom band the class draws in.
	BandFloor *int `json:"band_floor,omitempty"`
	// Trunk: one of the Trunk* policies above.
	Trunk string `json:"trunk,omitempty"`
	// Hidden drops the class from the build entirely.
	Hidden *bool `json:"hidden,omitempty"`
}

// Bullet ordering policies — how a station's route bullets sort
// (docs/STOP-LABELS.md, "Bullet ordering").
const (
	// BulletsColor groups bullets by their color — one trunk reads as one
	// run (NYC's A·C·E then B·D·F·M) — natural order within a group,
	// letter groups before number groups. Systems where every line has
	// its own color degrade to plain natural order, so this is the
	// default everywhere.
	BulletsColor = "color"
	// BulletsFeed obeys the feed's own route_sort_order where present
	// (MBTA, TriMet, PATH ship it), falling back to natural order.
	BulletsFeed = "feed"
	// BulletsNatural is the plain numeric-aware sort: 1 2 10 A B.
	BulletsNatural = "natural"
)

// Config is the style block, global or per-city.
type Config struct {
	// Modes: class name (internal/mode's String()) → overrides.
	Modes map[string]Class `json:"modes,omitempty"`
	// Colors: color overrides that beat everything else, keyed
	// "agency:<id or name>" or "route:<id or short name>". Agency and
	// route NAMES are accepted alongside ids because ids are feed
	// bookkeeping ("f1:1") and nobody wants to look those up to recolor
	// Metro-North.
	Colors map[string]string `json:"colors,omitempty"`
	// Names: display-name overrides, keyed exactly like Colors —
	// "agency:<id or name>", "route:<id, short name or long name>",
	// "stop:<id or name>". Same reason as colors: the feed is what got it
	// wrong, and there is no signal in the data to compute the fix from.
	// CTA files its lines as "Red Line"; the bullet wants "Red". CATS
	// files the LYNX Blue Line as route 501; a rider calls it Blue.
	Names map[string]string `json:"names,omitempty"`
	// Shapes: route-bullet outlines, keyed like Colors and Names.
	Shapes map[string]string `json:"shapes,omitempty"`
	// Fonts: bullet label typeface, keyed like Colors and Names —
	// default|mono|bolder|lighter|italic. A network whose bullets carry
	// a code rather than a number reads better monospaced, and a
	// heritage line is conventionally set in italic.
	Fonts map[string]string `json:"fonts,omitempty"`
	// Bordered: draw a contrasting ring around the bullet, keyed like
	// Colors and Names. A white or very pale bullet is invisible on
	// parchment without one.
	Bordered map[string]bool `json:"bordered,omitempty"`
	// Trunks: per-route merge policy, keyed like Colors and Names —
	// the Trunk* values, in practice "route" to opt a route OUT of
	// merging. Trunking is colour-based (law 5), which is right when
	// colour means something and wrong when a caller assigns colours
	// arbitrarily: two unrelated authored routes that happen to share a
	// hex would otherwise merge into one ribbon. This is the escape
	// hatch, per route rather than per class.
	Trunks map[string]string `json:"trunks,omitempty"`
	// RouteTypes: per-route/agency GTFS route_type REPAIRS, keyed like
	// Colors. A feed that mistypes a commuter railway as metro (Mexico
	// City's Tren Suburbano, route_type 1) drags every downstream stage
	// into demanding the wrong steel; the repair applies at feed load so
	// nothing ever sees the lie.
	RouteTypes map[string]int `json:"route_types,omitempty"`
	// Hiddens: routes/agencies dropped from the build (Entity.Hidden).
	Hiddens map[string]bool `json:"hiddens,omitempty"`
	// BulletOrder: one of the Bullets* policies above. Empty inherits
	// (default BulletsColor).
	BulletOrder string `json:"bullet_order,omitempty"`
	// Caterpillars: inline route bullets riding the ribbons. Nil
	// inherits (default on); a city can switch them off over a global
	// on, or vice versa.
	Caterpillars *bool `json:"caterpillars,omitempty"`
	// OSMStopNames: adopt the matched OSM stop's name for a station.
	// Nil inherits (default on). The real gate is whether the city has a
	// stops extract at all — with no extract nothing matches and this
	// knob does nothing. Turn it OFF to keep feed names while still
	// attaching OSM ids, which is what a city with hand-curated station
	// names wants.
	OSMStopNames *bool `json:"osm_stop_names,omitempty"`
	// Yards: detect yard regions and hand the stages the oracle. Nil
	// inherits (default on); a feed whose detection misfires can switch
	// it off and keep the legacy density heuristics everywhere.
	Yards *bool `json:"yards,omitempty"`
}

// defaults are the shipped behaviour: change these and every city moves.
var defaults = map[string]Class{
	"metro":    {Trunk: TrunkColor, Width: f(1.0), Opacity: f(1), BandFloor: i(0)},
	"tram":     {Trunk: TrunkColor, Width: f(0.75), Opacity: f(1), BandFloor: i(0)},
	"regional": {Trunk: TrunkAgency, Width: f(1.0), Opacity: f(1), BandFloor: i(0)},
	"monorail": {Trunk: TrunkColor, Width: f(0.85), Opacity: f(1), BandFloor: i(0)},
	// Aerialways, cable cars and funiculars are FIXED INFRASTRUCTURE, as
	// permanent as track, so they draw from the same band as rail. A
	// floor of 15 hid Mexico City's three Cablebús lines everywhere but
	// the closest zoom while their station dots kept drawing, which read
	// as a broken map rather than as a hidden class.
	"funicular": {Trunk: TrunkRoute, Width: f(0.7), Opacity: f(1), BandFloor: i(0)},
	"cable":     {Trunk: TrunkRoute, Width: f(0.75), Opacity: f(1), BandFloor: i(0)},
	"aerial":    {Trunk: TrunkRoute, Width: f(0.6), Opacity: f(0.75), BandFloor: i(0)},
	"ferry":     {Trunk: TrunkRoute, Width: f(0.7), Opacity: f(0.65), BandFloor: i(13), Color: "4A9EDB"},
	"bus":       {Trunk: TrunkNone, Width: f(0.5), Opacity: f(0.9), BandFloor: i(15), Color: "888888"},
	"unknown":   {Trunk: TrunkColor, Width: f(1.0), Opacity: f(1), BandFloor: i(0)},
}

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

// Resolved is a fully-populated class style — no nil fields, no decisions
// left. This is what the pipeline emits and the viewer renders from.
type Resolved struct {
	Color     string  `json:"color,omitempty"`
	Width     float64 `json:"width"`
	Opacity   float64 `json:"opacity"`
	BandFloor int     `json:"band_floor"`
	Trunk     string  `json:"trunk"`
	Hidden    bool    `json:"hidden"`
}

// Set is a resolved style config: the answer for every class, plus the
// override table. Build one with New and hand it to the pipeline.
type Set struct {
	Modes    map[string]Resolved `json:"modes"`
	Colors   map[string]string   `json:"colors,omitempty"`
	Names    map[string]string   `json:"names,omitempty"`
	Shapes   map[string]string   `json:"shapes,omitempty"`
	Fonts    map[string]string   `json:"fonts,omitempty"`
	Bordered map[string]bool     `json:"bordered,omitempty"`
	Trunks   map[string]string   `json:"trunks,omitempty"`
	// RouteTypes: per-route/agency GTFS route_type repairs, keyed like
	// Colors — applied at feed load (see style.Entity.RouteType).
	RouteTypes map[string]int `json:"route_types,omitempty"`
	// Hiddens: routes/agencies dropped from the build (Entity.Hidden).
	Hiddens map[string]bool `json:"hiddens,omitempty"`
	// BulletOrder: resolved Bullets* policy, never empty.
	BulletOrder string `json:"bullet_order"`
	// Caterpillars: resolved on/off for inline route bullets.
	Caterpillars bool `json:"caterpillars"`
	// OSMStopNames: resolved on/off for adopting matched OSM stop names.
	OSMStopNames bool `json:"osm_stop_names"`
	// Yards: resolved on/off for yard-region detection.
	Yards bool `json:"yards"`

	// lookup tables for overrides, lowercased; built by New.
	byAgency map[string]string
	byRoute  map[string]string
	nAgency  map[string]string
	nRoute   map[string]string
	nStop    map[string]string
	sAgency  map[string]string
	sRoute   map[string]string
	fAgency  map[string]string
	fRoute   map[string]string
	bAgency  map[string]bool
	bRoute   map[string]bool
	tAgency  map[string]string
	tRoute   map[string]string
	rtAgency map[string]int
	rtRoute  map[string]int
	hAgency2 map[string]bool
	hRoute2  map[string]bool
}

// New resolves configs in precedence order — later ones win field by field.
// Pass the global block then the city's, and a city that sets one color
// keeps every other default.
func New(layers ...Config) *Set {
	s := &Set{Modes: map[string]Resolved{}, Colors: map[string]string{},
		Names:    map[string]string{},
		Shapes:   map[string]string{},
		Fonts:    map[string]string{},
		Bordered: map[string]bool{},
		Trunks:   map[string]string{},
		byAgency: map[string]string{}, byRoute: map[string]string{},
		nAgency: map[string]string{}, nRoute: map[string]string{},
		nStop:   map[string]string{},
		sAgency: map[string]string{}, sRoute: map[string]string{},
		fAgency: map[string]string{}, fRoute: map[string]string{},
		bAgency: map[string]bool{}, bRoute: map[string]bool{},
		tAgency: map[string]string{}, tRoute: map[string]string{},
		rtAgency: map[string]int{}, rtRoute: map[string]int{},
		hAgency2: map[string]bool{}, hRoute2: map[string]bool{}}
	for name, d := range defaults {
		merged := d
		for _, l := range layers {
			if c, ok := l.Modes[name]; ok {
				merged = mergeClass(merged, c)
			}
		}
		s.Modes[name] = Resolved{
			Color: merged.Color, Width: *merged.Width, Opacity: *merged.Opacity,
			BandFloor: *merged.BandFloor, Trunk: merged.Trunk,
			Hidden: merged.Hidden != nil && *merged.Hidden,
		}
	}
	s.BulletOrder = BulletsColor
	s.Caterpillars = true
	s.OSMStopNames = true
	s.Yards = true
	for _, l := range layers {
		for k, v := range l.Colors {
			s.Colors[k] = v
		}
		for k, v := range l.Names {
			s.Names[k] = v
		}
		for k, v := range l.Shapes {
			s.Shapes[k] = v
		}
		for k, v := range l.Fonts {
			s.Fonts[k] = v
		}
		for k, v := range l.Bordered {
			s.Bordered[k] = v
		}
		for k, v := range l.Trunks {
			s.Trunks[k] = v
		}
		for k, v := range l.RouteTypes {
			if s.RouteTypes == nil {
				s.RouteTypes = map[string]int{}
			}
			s.RouteTypes[k] = v
		}
		for k, v := range l.Hiddens {
			if s.Hiddens == nil {
				s.Hiddens = map[string]bool{}
			}
			s.Hiddens[k] = v
		}
		if l.BulletOrder != "" {
			s.BulletOrder = l.BulletOrder
		}
		if l.Caterpillars != nil {
			s.Caterpillars = *l.Caterpillars
		}
		if l.OSMStopNames != nil {
			s.OSMStopNames = *l.OSMStopNames
		}
		if l.Yards != nil {
			s.Yards = *l.Yards
		}
	}
	for k, v := range s.Colors {
		key := strings.ToLower(strings.TrimSpace(k))
		hex := strings.TrimPrefix(strings.TrimSpace(v), "#")
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.byAgency[strings.TrimPrefix(key, "agency:")] = hex
		case strings.HasPrefix(key, "route:"):
			s.byRoute[strings.TrimPrefix(key, "route:")] = hex
		}
	}
	for k, v := range s.Shapes {
		key := strings.ToLower(strings.TrimSpace(k))
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.sAgency[strings.TrimPrefix(key, "agency:")] = strings.ToLower(strings.TrimSpace(v))
		case strings.HasPrefix(key, "route:"):
			s.sRoute[strings.TrimPrefix(key, "route:")] = strings.ToLower(strings.TrimSpace(v))
		}
	}
	// fonts, borders and per-route trunk policy split exactly like
	// shapes — same keys, same agency-then-route precedence, so a
	// curator learns one addressing scheme and it holds everywhere
	for k, v := range s.Fonts {
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.ToLower(strings.TrimSpace(v))
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.fAgency[strings.TrimPrefix(key, "agency:")] = val
		case strings.HasPrefix(key, "route:"):
			s.fRoute[strings.TrimPrefix(key, "route:")] = val
		}
	}
	for k, v := range s.Bordered {
		key := strings.ToLower(strings.TrimSpace(k))
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.bAgency[strings.TrimPrefix(key, "agency:")] = v
		case strings.HasPrefix(key, "route:"):
			s.bRoute[strings.TrimPrefix(key, "route:")] = v
		}
	}
	for k, v := range s.Trunks {
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.ToLower(strings.TrimSpace(v))
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.tAgency[strings.TrimPrefix(key, "agency:")] = val
		case strings.HasPrefix(key, "route:"):
			s.tRoute[strings.TrimPrefix(key, "route:")] = val
		}
	}
	for k, v := range s.RouteTypes {
		key := strings.ToLower(strings.TrimSpace(k))
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.rtAgency[strings.TrimPrefix(key, "agency:")] = v
		case strings.HasPrefix(key, "route:"):
			s.rtRoute[strings.TrimPrefix(key, "route:")] = v
		}
	}
	for k, v := range s.Hiddens {
		key := strings.ToLower(strings.TrimSpace(k))
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.hAgency2[strings.TrimPrefix(key, "agency:")] = v
		case strings.HasPrefix(key, "route:"):
			s.hRoute2[strings.TrimPrefix(key, "route:")] = v
		}
	}

	for k, v := range s.Names {
		key := strings.ToLower(strings.TrimSpace(k))
		name := strings.TrimSpace(v)
		switch {
		case strings.HasPrefix(key, "agency:"):
			s.nAgency[strings.TrimPrefix(key, "agency:")] = name
		case strings.HasPrefix(key, "route:"):
			s.nRoute[strings.TrimPrefix(key, "route:")] = name
		case strings.HasPrefix(key, "stop:"):
			s.nStop[strings.TrimPrefix(key, "stop:")] = name
		}
	}
	return s
}

func mergeClass(base, over Class) Class {
	if over.Color != "" {
		base.Color = strings.TrimPrefix(over.Color, "#")
	}
	if over.Width != nil {
		base.Width = over.Width
	}
	if over.Opacity != nil {
		base.Opacity = over.Opacity
	}
	if over.BandFloor != nil {
		base.BandFloor = over.BandFloor
	}
	if over.Trunk != "" {
		base.Trunk = over.Trunk
	}
	if over.Hidden != nil {
		base.Hidden = over.Hidden
	}
	return base
}

// Class returns the resolved style for a class name.
func (s *Set) Class(name string) Resolved {
	if s == nil {
		return New().Modes[name]
	}
	if r, ok := s.Modes[name]; ok {
		return r
	}
	return s.Modes["unknown"]
}

// RouteColor is the color override for a route, if any. Every identifier
// the caller knows gets a try — id, short name, long name — so the config
// can say what a human would say ("route:Metro-North" beats "route:f2:1").
func (s *Set) RouteColor(ids ...string) (string, bool) {
	if s == nil {
		return "", false
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if hex, ok := s.byRoute[key]; ok {
			return hex, true
		}
		if u := unprefix(key); u != key {
			if hex, ok := s.byRoute[u]; ok {
				return hex, true
			}
		}
	}
	return "", false
}

// AgencyColor is the color override for an agency, by id or by name.
func (s *Set) AgencyColor(ids ...string) (string, bool) {
	if s == nil {
		return "", false
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if hex, ok := s.byAgency[key]; ok {
			return hex, true
		}
		if u := unprefix(key); u != key {
			if hex, ok := s.byAgency[u]; ok {
				return hex, true
			}
		}
	}
	return "", false
}

// Any reports whether a color override exists at all — lets callers skip
// the lookups entirely on the overwhelmingly common empty config.
func (s *Set) Any() bool {
	return s != nil && (len(s.byAgency) > 0 || len(s.byRoute) > 0)
}

// unprefix strips the overlay tag loadFeeds stamps on ids ("f2:1" → "1")
// so a member feed's curation keeps matching when the feed rides in a
// group build as an overlay. The exact id is always tried FIRST, so a
// native id that merely looks like a tag cannot be shadowed.
func unprefix(id string) string {
	if len(id) > 2 && id[0] == 'f' {
		i := 1
		for i < len(id) && id[i] >= '0' && id[i] <= '9' {
			i++
		}
		if i > 1 && i < len(id) && id[i] == ':' {
			return id[i+1:]
		}
	}
	return id
}

// lookup walks every identifier the caller knows — id, short name, long
// name — so the config can say what a human would say ("route:Red Line"
// beats "route:f2:1"). Shared by all three name lookups.
func lookup(tbl map[string]string, ids []string) (string, bool) {
	for _, id := range ids {
		if id == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(id))
		if n, ok := tbl[key]; ok {
			return n, true
		}
		if u := unprefix(key); u != key {
			if n, ok := tbl[u]; ok {
				return n, true
			}
		}
	}
	return "", false
}

// RouteName is the display-name override for a route, by id, short name
// or long name. CTA's "Red Line" becomes "Red" this way.
func (s *Set) RouteName(ids ...string) (string, bool) {
	if s == nil {
		return "", false
	}
	return lookup(s.nRoute, ids)
}

// AgencyName is the display-name override for an agency, by id or name.
// An agency trunk is labelled with it (docs/MODES.md).
func (s *Set) AgencyName(ids ...string) (string, bool) {
	if s == nil {
		return "", false
	}
	return lookup(s.nAgency, ids)
}

// StopName is the display-name override for a stop, by id or by the
// feed's own name.
func (s *Set) StopName(ids ...string) (string, bool) {
	if s == nil {
		return "", false
	}
	return lookup(s.nStop, ids)
}

// RouteShape is the bullet-outline override for a route, then its agency.
func (s *Set) RouteShape(routeIDs []string, agencyIDs []string) (string, bool) {
	if s == nil {
		return "", false
	}
	if v, ok := lookup(s.sRoute, routeIDs); ok {
		return v, true
	}
	return lookup(s.sAgency, agencyIDs)
}

// RouteFont is the bullet typeface override for a route, then its
// agency — default|mono|bolder|lighter|italic.
func (s *Set) RouteFont(routeIDs []string, agencyIDs []string) (string, bool) {
	if s == nil {
		return "", false
	}
	if v, ok := lookup(s.fRoute, routeIDs); ok {
		return v, true
	}
	return lookup(s.fAgency, agencyIDs)
}

// RouteBordered reports whether a route's bullet takes a contrasting
// ring, and whether anything said so at all.
func (s *Set) RouteBordered(routeIDs []string, agencyIDs []string) (bool, bool) {
	if s == nil {
		return false, false
	}
	if v, ok := lookupBool(s.bRoute, routeIDs); ok {
		return v, true
	}
	if v, ok := lookupBool(s.bAgency, agencyIDs); ok {
		return v, true
	}
	return false, false
}

// RouteTrunk is the per-route merge-policy override, then its agency's.
// Its reason for existing is TrunkRoute: colour trunking is law 5 and
// correct when colour is meaningful, but an authored network may assign
// colours arbitrarily, and two unrelated routes sharing a hex must not
// silently become one ribbon.
func (s *Set) RouteTrunk(routeIDs []string, agencyIDs []string) (string, bool) {
	if s == nil {
		return "", false
	}
	if v, ok := lookup(s.tRoute, routeIDs); ok {
		return v, true
	}
	return lookup(s.tAgency, agencyIDs)
}

// AnyTrunk reports whether any per-route trunk override exists — the
// cheap skip for the overwhelmingly common case of none, on a lookup
// that would otherwise run per route per edge.
func (s *Set) AnyTrunk() bool {
	return s != nil && (len(s.tRoute) > 0 || len(s.tAgency) > 0)
}

// AnyName reports whether any name override exists — the same cheap skip
// Any() gives the color path.
func (s *Set) AnyName() bool {
	return s != nil && (len(s.nAgency) > 0 || len(s.nRoute) > 0 || len(s.nStop) > 0)
}

// MarshalManifest renders the resolved set for the viewer to render from.
func (s *Set) MarshalManifest() ([]byte, error) { return json.MarshalIndent(s, "", "  ") }

// active is the process-wide set, mirroring how the other config knobs
// (mode.SetLineAgencies, stages.SetTuning) reach deep stage code without
// threading a parameter through every signature.
var active = New()

func Set_(s *Set) {
	if s == nil {
		s = New()
	}
	active = s
}

// Active is the current process-wide style set.
func Active() *Set { return active }

// AnyRouteType reports whether any route_type repair exists — the cheap
// skip mirroring AnyTrunk, on a lookup that would run per route at load.
func (s *Set) AnyRouteType() bool {
	return s != nil && (len(s.rtRoute) > 0 || len(s.rtAgency) > 0)
}

// RouteTypeOf resolves a route_type repair for a route, id-or-name like
// every other override; route keys beat agency keys.
func (s *Set) RouteTypeOf(routeIDs []string, agencyIDs []string) (int, bool) {
	if s == nil {
		return 0, false
	}
	if v, ok := lookupInt(s.rtRoute, routeIDs); ok {
		return v, true
	}
	return lookupInt(s.rtAgency, agencyIDs)
}

func lookupInt(m map[string]int, keys []string) (int, bool) {
	for _, k := range keys {
		if k == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		if v, ok := m[key]; ok {
			return v, true
		}
		if u := unprefix(key); u != key {
			if v, ok := m[u]; ok {
				return v, true
			}
		}
	}
	return 0, false
}

// RouteHidden reports whether curation drops this route from the build.
func (s *Set) RouteHidden(routeIDs []string, agencyIDs []string) bool {
	if s == nil {
		return false
	}
	if _, ok := lookupBool(s.hRoute2, routeIDs); ok {
		return true
	}
	_, ok := lookupBool(s.hAgency2, agencyIDs)
	return ok
}

func lookupBool(m map[string]bool, keys []string) (bool, bool) {
	for _, k := range keys {
		if k == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		if v, ok := m[key]; ok {
			return v, true
		}
		if u := unprefix(key); u != key {
			if v, ok := m[u]; ok {
				return v, true
			}
		}
	}
	return false, false
}
