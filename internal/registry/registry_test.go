package registry

import "testing"

func TestParse(t *testing.T) {
	raw := []byte(`{
		"feeds": {
			"dallas": {
				"name": "Dallas–Fort Worth",
				"gtfs": "data/gtfs/dart.zip,data/gtfs/amtrak.zip",
				"rail": "build/dallas-rail.geojson",
				"out": "build/dallas.geojson",
				"bbox": [-97.5, 32.4, -96.4, 33.3],
				"members": ["dart"],
				"overlays": ["amtrak"],
				"derived": true,
				"chart_args": "--set match_gap_cost=150"
			},
			"dart": {
				"name": "DART",
				"gtfs": "data/gtfs/dart.zip",
				"onestop": "f-9vg-dart"
			}
		}
	}`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sketches != "sketches" {
		t.Errorf("Sketches default = %q, want sketches", cfg.Sketches)
	}
	d := cfg.Feeds["dallas"]
	if d.PrimaryGTFS() != "data/gtfs/dart.zip" {
		t.Errorf("PrimaryGTFS = %q", d.PrimaryGTFS())
	}
	if len(d.Members) != 1 || len(d.Overlays) != 1 || !d.Derived || d.ChartArgs == "" {
		t.Errorf("group fields lost: %+v", d)
	}
	if got := cfg.Feeds["dart"].Onestop; got != "f-9vg-dart" {
		t.Errorf("Onestop = %q", got)
	}
}
