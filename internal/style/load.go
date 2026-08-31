package style

// Curation lives in files, one per city, under style/. Not a database and
// not mixed into the feed registry: a city's colours and names are source
// code — reviewed in a diff, blamed, reverted — while portolan.json stays
// what it always was, the list of inputs and where they live on disk.
//
//	style/_default.json     applies to every city
//	style/<city>.json       one city, layered on top field by field
//
// The document is SUBJECT-KEYED. The older tables were keyed by attribute
// — "route:501" appeared once in `colors` and again in `names` — so one
// line's identity was split across the file and every new attribute meant
// a new table. Here a subject is named once and carries whatever is known
// about it:
//
//	{
//	  "routes": {
//	    "501":       { "name": "Blue", "color": "0067B1" },
//	    "Red Line":  { "name": "Red" }
//	  },
//	  "agencies": { "IDFM:71": { "line_colors": true } },
//	  "stops":    { "place-pktrm": { "name": "Park Street" } },
//	  "modes":    { "tram": { "width": 0.8 } },
//	  "options":  { "caterpillars": true, "osm_stop_names": false }
//	}
//
// Subject keys are matchers, not ids: a route matches on its id, its short
// name OR its long name, an agency on id or name, a stop on id or name,
// all case-insensitively. That is deliberate — ids are feed bookkeeping
// ("f2:1") and the config should say what a human would say.
//
// Both the CLI and the atlas resolve through LoadDir, so there is exactly
// one merge implementation. The previous design had the shell merge the
// layers with jq and Go merge them again for the dashboard, and the two
// drifted: CLI builds silently dropped bullet_order for weeks.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDir is where LoadDir looks unless told otherwise.
const DefaultDir = "style"

// defaultDoc is the base layer's filename, without extension.
const defaultDoc = "_default"

// Entity is what a document knows about one subject — a route, an agency
// or a stop. Empty fields mean "not overridden" and inherit.
type Entity struct {
	// Name replaces the display name: the bullet text and trunk label for
	// a route, the trunk label for an agency, the station identity for a
	// stop.
	Name string `json:"name,omitempty"`
	// Color replaces the drawn colour, hex without '#'.
	Color string `json:"color,omitempty"`
	// LineColors marks an agency whose route colours are real line
	// identities (Paris RER A–E) rather than branch-diagram decoration
	// (the LIRR's twelve). Agencies only; ignored elsewhere.
	LineColors *bool `json:"line_colors,omitempty"`
	// Shape is the route bullet's outline: circle (default), square,
	// rounded, notch, diamond, hexagon, octagon, triangle. Systems brand
	// these — Berlin and Vienna put U-Bahn numerals in SQUARES, NYC uses
	// a diamond for express variants, Madrid a rhombus — and the feed
	// says nothing about it, so it is curation like colour.
	Shape string `json:"shape,omitempty"`
	// Font is the bullet label's typeface: default, mono, bolder,
	// lighter, italic. Curation for the same reason Shape is — a code
	// bullet reads better monospaced and a heritage line is set in
	// italic, and no feed field says so.
	Font string `json:"font,omitempty"`
	// Bordered rings the bullet in a contrasting outline. A white or
	// near-white bullet has no edge against parchment without one.
	Bordered *bool `json:"bordered,omitempty"`
	// Trunk overrides the merge policy for this subject alone —
	// in practice "route", to keep it out of a colour trunk. Law 5
	// merges by colour, which is right where colour carries meaning
	// and wrong where a caller assigns colours arbitrarily.
	Trunk string `json:"trunk,omitempty"`
	// TrunkGroup SCOPES colour trunking instead of replacing it. Law 5's
	// key is the colour ALONE, so two routes that share a hex merge even
	// when they are different kinds of railway — right for a feed whose
	// colours mean one thing, wrong for a caller whose classes are its own
	// and invisible to GTFS. A game files both its heavy and light metro
	// as route_type 1; Amtrak's NEC and PATH's Newark service are
	// genuinely different railways that happen to share #D43E2D.
	//
	// A route carrying a group merges only with routes carrying the SAME
	// group and the same colour. Trunk still decides the POLICY; this only
	// narrows the colour policy's reach. Empty — every real-world feed —
	// leaves the key the bare colour, byte for byte.
	TrunkGroup string `json:"trunk_group,omitempty"`
	// RouteType REPAIRS a feed's route_type where the agency mistyped it —
	// Mexico City files the Tren Suburbano (a commuter railway) as
	// route_type 1, and everything downstream then demands metro-class
	// steel for it. Applied at GTFS load, before any stage looks, so the
	// whole pipeline sees the corrected class. Routes and agencies.
	RouteType *int `json:"route_type,omitempty"`
	// Hidden drops the route from the build entirely — for services whose
	// feed data cannot draw honestly (the Meadowlands event shuttle ships
	// 3-point skeleton shapes) or that a curator judges noise. Routes and
	// agencies, like every override.
	Hidden *bool `json:"hidden,omitempty"`
}

// Options are the whole-city switches.
type Options struct {
	BulletOrder  string `json:"bullet_order,omitempty"`
	Caterpillars *bool  `json:"caterpillars,omitempty"`
	OSMStopNames *bool  `json:"osm_stop_names,omitempty"`
}

// Doc is one style file.
type Doc struct {
	// Schema is accepted and ignored — it lets editors offer completion.
	Schema   string            `json:"$schema,omitempty"`
	Modes    map[string]Class  `json:"modes,omitempty"`
	Agencies map[string]Entity `json:"agencies,omitempty"`
	Routes   map[string]Entity `json:"routes,omitempty"`
	Stops    map[string]Entity `json:"stops,omitempty"`
	Options  Options           `json:"options,omitempty"`
}

// Config flattens a Doc into the prefixed tables the resolver works in.
// This is the ONLY place the "route:"/"agency:"/"stop:" prefixes are
// constructed, so the document format and the lookup format can move
// independently.
func (d Doc) Config() Config {
	c := Config{
		Modes:        d.Modes,
		BulletOrder:  d.Options.BulletOrder,
		Caterpillars: d.Options.Caterpillars,
		OSMStopNames: d.Options.OSMStopNames,
	}
	add := func(prefix string, m map[string]Entity) {
		for k, e := range m {
			key := prefix + ":" + k
			if e.Shape != "" {
				if c.Shapes == nil {
					c.Shapes = map[string]string{}
				}
				c.Shapes[key] = e.Shape
			}
			if e.Font != "" {
				if c.Fonts == nil {
					c.Fonts = map[string]string{}
				}
				c.Fonts[key] = e.Font
			}
			if e.Bordered != nil {
				if c.Bordered == nil {
					c.Bordered = map[string]bool{}
				}
				c.Bordered[key] = *e.Bordered
			}
			if e.Trunk != "" {
				if c.Trunks == nil {
					c.Trunks = map[string]string{}
				}
				c.Trunks[key] = e.Trunk
			}
			if e.TrunkGroup != "" {
				if c.TrunkGroups == nil {
					c.TrunkGroups = map[string]string{}
				}
				c.TrunkGroups[key] = e.TrunkGroup
			}
			if e.RouteType != nil {
				if c.RouteTypes == nil {
					c.RouteTypes = map[string]int{}
				}
				c.RouteTypes[key] = *e.RouteType
			}
			if e.Hidden != nil && *e.Hidden {
				if c.Hiddens == nil {
					c.Hiddens = map[string]bool{}
				}
				c.Hiddens[key] = true
			}
			if e.Name != "" {
				if c.Names == nil {
					c.Names = map[string]string{}
				}
				c.Names[key] = e.Name
			}
			if e.Color != "" {
				if c.Colors == nil {
					c.Colors = map[string]string{}
				}
				c.Colors[key] = strings.TrimPrefix(e.Color, "#")
			}
		}
	}
	add("agency", d.Agencies)
	add("route", d.Routes)
	add("stop", d.Stops)
	return c
}

// LineAgencies lists the agencies this document marks as carrying real
// per-line colours. Curation, like everything else here — it used to be a
// bare array on the feed row, which put one operator decision in a
// different file from every other operator decision.
func (d Doc) LineAgencies() []string {
	var out []string
	for k, e := range d.Agencies {
		if e.LineColors != nil && *e.LineColors {
			out = append(out, k)
		}
	}
	return out
}

// ReadDoc parses one style file. A missing file is not an error: an
// absent layer simply contributes nothing.
func ReadDoc(path string) (Doc, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Doc{}, false, nil
		}
		return Doc{}, false, err
	}
	// STRICT: an unrecognised key is an error, not something to skip.
	//
	// Curation has no output of its own — it only changes how something
	// else draws — so a misspelt or wrongly-shaped document produces a
	// map that builds cleanly, applies nothing, and reports no reason.
	// That is the worst failure mode a config format can have, and it is
	// easy to hit here: `internal/style`.Config holds flat tables keyed
	// "route:<id>" and a reader who meets that struct first will
	// reasonably write `{"shapes": {"route:L": "diamond"}}` into a file
	// that wants `{"routes": {"L": {"shape": "diamond"}}}`. Both parse.
	// Only one does anything.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return Doc{}, false, fmt.Errorf("%s: %w\n"+
			"  a curation document is subject-keyed: "+
			`{"routes": {"L": {"color": "EE352E", "shape": "diamond"}}}`+"\n"+
			"  see docs/CORRIDORS.md — the flat route:/agency: tables are internal", path, err)
	}
	return d, true, nil
}

// LoadDir resolves the layers for one build: the default document, then
// each named document in order, later names winning field by field.
// One name is the ordinary feed build; a GROUP build passes its members
// in gtfs order and its own name last, so member curation rides along
// and the group's own document has the final say. Returns the merged Set
// and the agencies the documents mark as line-coloured.
//
// Every layer is optional. A tree with no style/ directory at all
// resolves to exactly the shipped defaults, which is what every city got
// before curation existed.
func LoadDir(dir string, names ...string) (*Set, []string, error) {
	if dir == "" {
		dir = DefaultDir
	}
	var (
		layers []Config
		las    []string
	)
	for _, name := range append([]string{defaultDoc}, names...) {
		if name == "" {
			continue
		}
		d, ok, err := ReadDoc(DocPath(dir, name))
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		layers = append(layers, d.Config())
		las = append(las, d.LineAgencies()...)
	}
	return New(layers...), las, nil
}

// DocPath is where one layer lives. Centralised so the console writes
// exactly what the loader reads.
func DocPath(dir, name string) string {
	if dir == "" {
		dir = DefaultDir
	}
	return filepath.Join(dir, name+".json")
}

// WriteDoc saves a layer, creating the directory. An empty document is
// deleted rather than left as `{}` — an empty file reads as "this city
// has curation" when it does not.
func WriteDoc(dir, name string, d Doc) error {
	path := DocPath(dir, name)
	if d.Empty() {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Empty reports a document that overrides nothing.
func (d Doc) Empty() bool {
	return len(d.Modes) == 0 && len(d.Agencies) == 0 && len(d.Routes) == 0 &&
		len(d.Stops) == 0 && d.Options == Options{}
}
