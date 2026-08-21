// Package registry reads portolan.json, the feed registry: one entry per
// feed (or feed group), naming its inputs and its window. The atlas serves
// from it, sync reconciles against it — one parser, so the two cannot
// disagree about what a feed entry means.
package registry

import (
	"encoding/json"
	"os"
	"strings"
)

// FeedCfg is one feed (or feed group) in portolan.json.
type FeedCfg struct {
	Name    string    `json:"name"`
	GTFS    string    `json:"gtfs"` // comma list: primary feed, then overlays (Metra, Amtrak)
	Rail    string    `json:"rail"`
	Streets string    `json:"streets"` // optional street extract — enables bus routes
	Stops   string    `json:"stops"`   // optional OSM stop extract — station name/id matching
	Out     string    `json:"out"`
	Network string    `json:"network"` // drawn ground truth for scoring
	BBox    []float64 `json:"bbox"`    // [w,s,e,n] Overpass window + shape clip
	// Members marks a GROUP: the feed keys whose networks this entry
	// builds together (so cross-feed routes bundle on shared track). The
	// group's tileset REPLACES its members' in the global index — a
	// member listed here is skipped there, or the world would draw the
	// same railroad twice.
	Members []string `json:"members,omitempty"`
	// Overlays: wide feeds (Amtrak) riding through a group's window —
	// listed in the group's gtfs but not members, so they keep their own
	// background build with this window cut out.
	Overlays []string `json:"overlays,omitempty"`
	// Derived: the entry was measured into existence by tools/groups.py,
	// not curated by hand — groups.py may rewrite or dissolve it.
	Derived bool `json:"derived,omitempty"`
	// ChartArgs: extra chart flags this build needs, verbatim.
	ChartArgs string `json:"chart_args,omitempty"`
	// Onestop: the feed's Transitland onestop id — how sync asks whether
	// the feed changed upstream. Empty means sync skips the feed.
	Onestop string `json:"onestop,omitempty"`
}

// PrimaryGTFS: the first feed of the comma list — scenarios and mtime
// checks are primary-feed concepts.
func (f FeedCfg) PrimaryGTFS() string {
	if i := strings.IndexByte(f.GTFS, ','); i >= 0 {
		return strings.TrimSpace(f.GTFS[:i])
	}
	return strings.TrimSpace(f.GTFS)
}

type Config struct {
	Feeds    map[string]FeedCfg `json:"feeds"`
	Sketches string             `json:"sketches"`
	// StyleDir: where the curation documents live (default "style").
	// Colours and names are NOT in this file — they are source code, one
	// document per city, so they diff and revert on their own.
	StyleDir string `json:"style_dir,omitempty"`
}

// Parse decodes a registry document and fills defaults.
func Parse(raw []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Sketches == "" {
		cfg.Sketches = "sketches"
	}
	return cfg, nil
}

// Load reads and parses a registry file.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(raw)
}
