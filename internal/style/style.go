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
}

// defaults are the shipped behaviour: change these and every city moves.
var defaults = map[string]Class{
	"metro":     {Trunk: TrunkColor, Width: f(1.0), Opacity: f(1), BandFloor: i(0)},
	"tram":      {Trunk: TrunkColor, Width: f(0.75), Opacity: f(1), BandFloor: i(0)},
	"regional":  {Trunk: TrunkAgency, Width: f(1.0), Opacity: f(1), BandFloor: i(0)},
	"monorail":  {Trunk: TrunkColor, Width: f(0.85), Opacity: f(1), BandFloor: i(0)},
	"funicular": {Trunk: TrunkRoute, Width: f(0.7), Opacity: f(1), BandFloor: i(15)},
	"cable":     {Trunk: TrunkRoute, Width: f(0.75), Opacity: f(1), BandFloor: i(15)},
	"aerial":    {Trunk: TrunkRoute, Width: f(0.6), Opacity: f(0.75), BandFloor: i(15)},
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
	Modes  map[string]Resolved `json:"modes"`
	Colors map[string]string   `json:"colors,omitempty"`

	// lookup tables for overrides, lowercased; built by New.
	byAgency map[string]string
	byRoute  map[string]string
}

// New resolves configs in precedence order — later ones win field by field.
// Pass the global block then the city's, and a city that sets one color
// keeps every other default.
func New(layers ...Config) *Set {
	s := &Set{Modes: map[string]Resolved{}, Colors: map[string]string{},
		byAgency: map[string]string{}, byRoute: map[string]string{}}
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
	for _, l := range layers {
		for k, v := range l.Colors {
			s.Colors[k] = v
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
		if hex, ok := s.byRoute[strings.ToLower(id)]; ok {
			return hex, true
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
		if hex, ok := s.byAgency[strings.ToLower(id)]; ok {
			return hex, true
		}
	}
	return "", false
}

// Any reports whether a color override exists at all — lets callers skip
// the lookups entirely on the overwhelmingly common empty config.
func (s *Set) Any() bool {
	return s != nil && (len(s.byAgency) > 0 || len(s.byRoute) > 0)
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
