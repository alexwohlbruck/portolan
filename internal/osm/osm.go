// Package osm loads a rail extract from GeoJSON (osmtogeojson/Overpass
// output). portolan reads OSM directly — the old pipeline's dependence on a
// pre-imported database left whole cities (Chicago) without track data.
package osm

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/alexwohlbruck/portolan/internal/geo"
)

type Way struct {
	ID     string
	Coords []geo.LL
	Tags   map[string]string
}

var railValues = map[string]bool{
	"rail": true, "subway": true, "light_rail": true, "tram": true,
	"monorail": true, "funicular": true, "narrow_gauge": true,
}

// aerialValues: cable-supported passenger modes. They carry no railway tag
// at all, so they get a synthetic railway class "aerial" — one string the
// whole pipeline (wayRailClass, classCompat) already knows how to key on.
// Drag/tow lifts are excluded: nobody rides a T-bar to work.
var aerialValues = map[string]bool{
	"cable_car": true, "gondola": true, "mixed_lift": true,
}

// Load reads a GeoJSON FeatureCollection and keeps regular-service rail
// LineStrings: railway ∈ {rail,subway,light_rail,tram,monorail,funicular,
// narrow_gauge} plus aerialway cable modes, no service tag
// (yards/sidings/spurs excluded at the door — LESSONS: >4 strands are yards)
// — EXCEPT service=crossover: crossovers are the short links BETWEEN running
// tracks. They add no corridor geometry (no new strands), but without them
// the graph loses the connectivity that real trains use — the Manhattan
// Bridge tracks reach Broadway only through one, and dropping it forced
// whole patterns onto the wrong track pair and into phantom gap bridges.
func Load(path string) ([]Way, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Features []struct {
			ID       any            `json:"id"`
			Props    map[string]any `json:"properties"`
			Geometry struct {
				Type   string          `json:"type"`
				Coords json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("osm: %s: %w", path, err)
	}
	var out []Way
	for i, f := range fc.Features {
		if f.Geometry.Type != "LineString" {
			continue
		}
		tags := map[string]string{}
		for k, v := range f.Props {
			if s, ok := v.(string); ok {
				tags[k] = s
			}
		}
		if !railValues[tags["railway"]] {
			switch {
			case aerialValues[tags["aerialway"]]:
				tags["railway"] = "aerial"
			case tags["route"] == "ferry":
				// OSM maps ferry LANES as route=ferry ways — real-world
				// geometry for water crossings, the seaway layer
				tags["railway"] = "seaway"
			default:
				continue
			}
		}
		if s := tags["service"]; s != "" && s != "crossover" {
			continue
		}
		var coords [][]float64
		if err := json.Unmarshal(f.Geometry.Coords, &coords); err != nil {
			continue
		}
		if len(coords) < 2 {
			continue
		}
		lls := make([]geo.LL, len(coords))
		for j, c := range coords {
			lls[j] = geo.LL{Lon: c[0], Lat: c[1]}
		}
		id := fmt.Sprint(f.ID)
		if id == "<nil>" || id == "" {
			id = "f" + strconv.Itoa(i)
		}
		out = append(out, Way{ID: id, Coords: lls, Tags: tags})
	}
	return out, nil
}

// streetValues: highway classes buses ride. Service ways, footways and
// paths stay out — a bus terminal loop is furniture, not corridor.
var streetValues = map[string]bool{
	"motorway": true, "trunk": true, "primary": true, "secondary": true,
	"tertiary": true, "unclassified": true, "residential": true,
	"living_street": true, "busway": true, "bus_guideway": true,
	"motorway_link": true, "trunk_link": true, "primary_link": true,
	"secondary_link": true, "tertiary_link": true,
}

// LoadStreets reads a street extract for bus matching. Every kept way gets
// the synthetic railway class "street" — one string classCompat keys on,
// exactly like "aerial" — so downstream code never grows a second tag
// vocabulary. Streets are already drawn road centerlines: no strands, no
// refinement ride on them.
func LoadStreets(path string) ([]Way, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Features []struct {
			ID       any            `json:"id"`
			Props    map[string]any `json:"properties"`
			Geometry struct {
				Type   string          `json:"type"`
				Coords json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("osm: %s: %w", path, err)
	}
	var out []Way
	for i, f := range fc.Features {
		if f.Geometry.Type != "LineString" {
			continue
		}
		tags := map[string]string{}
		for k, v := range f.Props {
			if s, ok := v.(string); ok {
				tags[k] = s
			}
		}
		if !streetValues[tags["highway"]] {
			continue
		}
		var coords [][]float64
		if err := json.Unmarshal(f.Geometry.Coords, &coords); err != nil {
			continue
		}
		if len(coords) < 2 {
			continue
		}
		lls := make([]geo.LL, len(coords))
		for j, c := range coords {
			lls[j] = geo.LL{Lon: c[0], Lat: c[1]}
		}
		tags["railway"] = "street"
		id := fmt.Sprint(f.ID)
		if id == "<nil>" || id == "" {
			id = "s" + strconv.Itoa(i)
		}
		out = append(out, Way{ID: id, Coords: lls, Tags: tags})
	}
	return out, nil
}
