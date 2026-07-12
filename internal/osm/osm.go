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
}

// Load reads a GeoJSON FeatureCollection and keeps regular-service rail
// LineStrings: railway ∈ {rail,subway,light_rail,tram}, no service tag
// (yards/sidings/spurs excluded at the door — LESSONS: >4 strands are yards).
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
			continue
		}
		if tags["service"] != "" {
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
