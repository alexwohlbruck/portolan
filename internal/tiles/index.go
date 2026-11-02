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
// pyramid (dirFor(key)/tiles.json) exists, except members of a group whose
// own pyramid is listed — a feed that rides inside a group is drawn BY the
// group, and listing both would double-draw its railroad. A group that has
// no pyramid (its build failed, or it has never built) hides nothing: its
// members fall back to their own pyramids. Hiding them anyway is how
// Chicago's CTA, Metra and South Shore all left the map at once when the
// chicago group failed preflight. Sorted by feed key.
func Index(cfg registry.Config, dirFor func(feed string) string) []IndexEntry {
	read := func(k string) (IndexEntry, bool) {
		raw, err := os.ReadFile(filepath.Join(dirFor(k), "tiles.json"))
		if err != nil {
			return IndexEntry{}, false
		}
		var tj struct {
			Bounds  []float64 `json:"bounds"`
			MaxZoom int       `json:"maxzoom"`
		}
		if json.Unmarshal(raw, &tj) != nil {
			return IndexEntry{}, false
		}
		return IndexEntry{Feed: k, Name: cfg.Feeds[k].Name,
			Bounds: tj.Bounds, MaxZoom: tj.MaxZoom}, true
	}
	entries := map[string]IndexEntry{}
	for k := range cfg.Feeds {
		if e, ok := read(k); ok {
			entries[k] = e
		}
	}
	drawnByGroup := map[string]bool{}
	for k, fc := range cfg.Feeds {
		if _, ok := entries[k]; !ok {
			continue
		}
		for _, m := range fc.Members {
			drawnByGroup[m] = true
		}
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		if !drawnByGroup[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := []IndexEntry{}
	for _, k := range keys {
		out = append(out, entries[k])
	}
	return out
}

// Undrawn lists the feeds the index leaves off the map although they have
// GTFS to draw: no pyramid of their own in the index, and no group in the
// index that draws them. Groups themselves are not listed — a group that
// fails shows up through its members. hasGTFS says whether a feed's GTFS
// is on disk; a feed without it has nothing to draw and is not reported.
// Sorted by feed key.
func Undrawn(cfg registry.Config, idx []IndexEntry, hasGTFS func(registry.FeedCfg) bool) []string {
	listed := map[string]bool{}
	for _, e := range idx {
		listed[e.Feed] = true
	}
	for k, fc := range cfg.Feeds {
		if listed[k] {
			for _, m := range fc.Members {
				listed[m] = true
			}
		}
	}
	var out []string
	for k, fc := range cfg.Feeds {
		if len(fc.Members) > 0 || listed[k] || !hasGTFS(fc) {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
