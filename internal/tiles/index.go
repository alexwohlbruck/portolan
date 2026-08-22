package tiles

// The world index: every feed with a cut pyramid, with its bounds — the
// global view draws exactly this list and nothing else. Composed HERE,
// once, because two consumers read it: the atlas serves it live at
// /api/tiles/index.json, and sync writes it as a static --tiles/
// index.json after a run. One composer, so the two cannot drift.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

// IndexEntry is one world-index row. The schema is a contract: barrelman
// consumes it, and it carries no URL templates — the consumer knows where
// the pyramids live relative to the index.
type IndexEntry struct {
	Feed    string    `json:"feed"`
	Name    string    `json:"name"`
	Bounds  []float64 `json:"bounds,omitempty"`
	MaxZoom int       `json:"maxzoom"`
}

// Index composes the world index from the registry: every feed whose
// pyramid (dirFor(key)/tiles.json) exists, except members of groups — a
// feed that rides inside a group is drawn BY the group, and listing both
// would double-draw its railroad. Sorted by feed key.
func Index(cfg registry.Config, dirFor func(feed string) string) []IndexEntry {
	grouped := map[string]bool{}
	for _, fc := range cfg.Feeds {
		for _, m := range fc.Members {
			grouped[m] = true
		}
	}
	keys := make([]string, 0, len(cfg.Feeds))
	for k := range cfg.Feeds {
		if !grouped[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := []IndexEntry{}
	for _, k := range keys {
		raw, err := os.ReadFile(filepath.Join(dirFor(k), "tiles.json"))
		if err != nil {
			continue
		}
		var tj struct {
			Bounds  []float64 `json:"bounds"`
			MaxZoom int       `json:"maxzoom"`
		}
		if json.Unmarshal(raw, &tj) != nil {
			continue
		}
		out = append(out, IndexEntry{Feed: k, Name: cfg.Feeds[k].Name,
			Bounds: tj.Bounds, MaxZoom: tj.MaxZoom})
	}
	return out
}
