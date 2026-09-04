package sync

// Chart-request assembly from a registry entry — the Go port of
// tools/feed.sh `build` (plus tools/groupbuild.sh's preflight), which
// remain the prose originals. The output is an ARGV for `portolan
// chart`, not a ChartOpts: the executor runs each build as a child
// process (builds are process-isolated because chart configuration is
// process state — see `portolan serve`'s serialization note), so the
// child's own flag definitions parse everything, chart_args included.
// feed.sh does exactly the same word-splitting hand-off.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

// buildSpec is one assembled build: the argv after "chart", and what the
// executor needs to know about the outputs.
type buildSpec struct {
	Key  string
	Argv []string
	Out  string
	Zips []string // the gtfs list, split — fingerprint inputs
}

// outPath: the entry's out, defaulting to the same convention feed.sh
// and groups.py assume (build/<key>.geojson).
func outPath(fc registry.FeedCfg, buildDir, key string) string {
	if fc.Out != "" {
		return fc.Out
	}
	return filepath.Join(buildDir, key+".geojson")
}

// assembleChart mirrors feed.sh build line by line: geometry source,
// gtfs list, window, ceded windows, style layering, streets, stops,
// export, chart_args. doc is the order-preserved registry document —
// the exclude-window scan walks entries in file order, exactly as the
// jq derivation does. exportGTFS is empty for builds that must not
// export (groups: their member zips belong to the members' own builds).
func assembleChart(doc *Obj, cfg registry.Config, key, buildDir, styleDir,
	exportGTFS string, logf func(string, ...any)) (*buildSpec, error) {
	fc, ok := cfg.Feeds[key]
	if !ok {
		return nil, fmt.Errorf("%s: not in the registry", key)
	}
	out := outPath(fc, buildDir, key)

	if fc.Corridors != "" {
		if st, err := os.Stat(fc.Corridors); err != nil || st.Size() == 0 {
			return nil, fmt.Errorf("%s: no corridor graph at %s", key, fc.Corridors)
		}
	} else {
		if st, err := os.Stat(fc.Rail); fc.Rail == "" || err != nil || st.Size() == 0 {
			return nil, fmt.Errorf("%s: no rail extract at %s", key, fc.Rail)
		}
	}
	var zips []string
	for _, part := range strings.Split(fc.GTFS, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if st, err := os.Stat(part); err != nil || st.Size() == 0 {
			return nil, fmt.Errorf("%s: no GTFS at %s", key, part)
		}
		zips = append(zips, part)
	}
	if len(zips) == 0 {
		return nil, fmt.Errorf("%s: no gtfs configured", key)
	}

	var argv []string
	if fc.Corridors != "" {
		argv = []string{"--gtfs", fc.GTFS, "--corridors", fc.Corridors, "--out", out}
	} else {
		argv = []string{"--gtfs", fc.GTFS, "--rail", fc.Rail, "--out", out}
	}
	if len(fc.BBox) == 4 {
		argv = append(argv, "--bbox", bboxArg(fc.BBox))
	}
	// a feed that also rides in GROUP builds as an overlay cedes those
	// windows — derived from the groups' gtfs lists, so it cannot rot
	// (feed.sh ~190-201). Members keep their full standalone builds.
	if ex := cededWindows(doc, key, fc.PrimaryGTFS()); ex != "" {
		argv = append(argv, "--exclude-bbox", ex)
	}
	// style: _default → member docs → overlay docs → own doc, through the
	// one Go loader (style.LoadDir) the chart child resolves --feed with
	styleFeed := key
	if names := append(append([]string(nil), fc.Members...), fc.Overlays...); len(names) > 0 {
		styleFeed = strings.Join(append(names, key), ",")
	}
	argv = append(argv, "--style-dir", styleDir, "--feed", styleFeed)
	if fc.Streets != "" {
		if st, err := os.Stat(fc.Streets); err == nil && st.Size() > 0 {
			argv = append(argv, "--streets", fc.Streets)
		} else {
			logf("%s: streets configured but missing at %s — building rail-only", key, fc.Streets)
		}
	}
	if fc.Stops != "" {
		if st, err := os.Stat(fc.Stops); err == nil && st.Size() > 0 {
			argv = append(argv, "--stops", fc.Stops)
		}
	}
	if exportGTFS != "" {
		argv = append(argv, "--export-gtfs", exportGTFS)
	}
	// onestop ids ride in from the registry for every zip in the build —
	// by hand this is chart --onestop (docs/SYNC.md tile contract)
	if ids := onestopArg(cfg, zips); ids != "" {
		argv = append(argv, "--onestop", ids)
	}
	// chart_args: per-feed extra chart flags, word-split exactly as the
	// shell would split them, parsed by chart's own flag set
	argv = append(argv, strings.Fields(fc.ChartArgs)...)
	return &buildSpec{Key: key, Argv: argv, Out: out, Zips: zips}, nil
}

// bboxArg formats a window the way jq's join(",") formats the registry's
// numbers — shortest float form.
func bboxArg(b []float64) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = trimFloat(v)
	}
	return strings.Join(parts, ",")
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%g", v)
	if math.Abs(v) >= 1e6 { // %g switches to exponent; bboxes never do
		s = fmt.Sprintf("%f", v)
	}
	return s
}

// cededWindows: the exclude-bbox derivation of feed.sh — every OTHER
// entry that is a group (members non-empty), does NOT list this feed as
// a member, and charts this feed's primary zip in its gtfs list, cedes
// its bbox. Windows join with ";" in registry file order.
func cededWindows(doc *Obj, key, primary string) string {
	feeds := feedsObj(doc)
	var wins []string
	for _, k := range feeds.Keys() {
		if k == key {
			continue
		}
		v, _ := feeds.Get(k)
		e, ok := v.(*Obj)
		if !ok {
			continue
		}
		members := strsOf(e, "members")
		if len(members) == 0 {
			continue
		}
		isMember := false
		for _, m := range members {
			if m == key {
				isMember = true
				break
			}
		}
		if isMember {
			continue
		}
		carries := false
		for _, p := range strings.Split(e.Str("gtfs"), ",") {
			if strings.TrimSpace(p) == primary {
				carries = true
				break
			}
		}
		if !carries {
			continue
		}
		b, ok := e.Get("bbox")
		if !ok {
			continue
		}
		arr, _ := b.([]any)
		parts := make([]string, 0, len(arr))
		for _, x := range arr {
			switch t := x.(type) {
			case json.Number:
				parts = append(parts, t.String())
			case float64:
				parts = append(parts, trimFloat(t))
			}
		}
		if len(parts) == 4 {
			wins = append(wins, strings.Join(parts, ","))
		}
	}
	return strings.Join(wins, ";")
}

// onestopArg builds the --onestop value: for each zip of the build, the
// onestop id of the registry feed whose primary gtfs is that zip, keyed
// by zip basename sans ".zip" (the key chart's flag wants).
func onestopArg(cfg registry.Config, zips []string) string {
	byPrimary := map[string]string{}
	for _, fc := range cfg.Feeds {
		if len(fc.Members) > 0 || fc.Onestop == "" {
			continue
		}
		if p := fc.PrimaryGTFS(); p != "" {
			byPrimary[p] = fc.Onestop
		}
	}
	var kv []string
	seen := map[string]bool{}
	for _, z := range zips {
		id := byPrimary[z]
		if id == "" {
			continue
		}
		k := strings.TrimSuffix(filepath.Base(z), ".zip")
		if seen[k] {
			continue
		}
		seen[k] = true
		kv = append(kv, k+"="+id)
	}
	return strings.Join(kv, ",")
}

// ---------------------------------------------------------------- merge

// mergeFC is the tools/mergefc.py port: concatenate GeoJSON
// FeatureCollections, first wins on duplicate feature id — the same OSM
// way must not enter the bundler twice, or the copies read as parallel
// tracks. Features without an id are kept unconditionally. The result
// writes to dst atomically.
func mergeFC(dst string, srcs []string) (int, error) {
	type feature struct {
		ID    any             `json:"id,omitempty"`
		Type  string          `json:"type"`
		Props json.RawMessage `json:"properties,omitempty"`
		Geom  json.RawMessage `json:"geometry"`
	}
	seen := map[string]bool{}
	var feats []feature
	for _, src := range srcs {
		raw, err := os.ReadFile(src)
		if err != nil {
			return 0, err
		}
		var fc struct {
			Features []feature `json:"features"`
		}
		if err := json.Unmarshal(raw, &fc); err != nil {
			return 0, fmt.Errorf("%s: %w", src, err)
		}
		for _, f := range fc.Features {
			if f.ID != nil {
				id := fmt.Sprint(f.ID)
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			feats = append(feats, f)
		}
	}
	out := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection", Features: feats}
	raw, err := json.Marshal(out)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	if err := atomicWrite(dst, append(raw, '\n')); err != nil {
		return 0, err
	}
	return len(feats), nil
}

// featureBBox returns a feature geometry's own extent, or ok=false for a
// geometry this walk does not understand. Shared with railCovers so the
// coverage test and the clip agree on what a feature occupies.
func featureBBox(geomType string, coords json.RawMessage) (w, s, e, n float64, ok bool) {
	w, s = math.Inf(1), math.Inf(1)
	e, n = math.Inf(-1), math.Inf(-1)
	take := func(c [2]float64) {
		w, e = math.Min(w, c[0]), math.Max(e, c[0])
		s, n = math.Min(s, c[1]), math.Max(n, c[1])
	}
	switch geomType {
	case "Point":
		var p [2]float64
		if json.Unmarshal(coords, &p) != nil {
			return 0, 0, 0, 0, false
		}
		take(p)
	case "LineString", "MultiPoint":
		var pts [][2]float64
		if json.Unmarshal(coords, &pts) != nil {
			return 0, 0, 0, 0, false
		}
		for _, c := range pts {
			take(c)
		}
	case "Polygon", "MultiLineString":
		var rings [][][2]float64
		if json.Unmarshal(coords, &rings) != nil {
			return 0, 0, 0, 0, false
		}
		for _, r := range rings {
			for _, c := range r {
				take(c)
			}
		}
	case "MultiPolygon":
		var polys [][][][2]float64
		if json.Unmarshal(coords, &polys) != nil {
			return 0, 0, 0, 0, false
		}
		for _, poly := range polys {
			for _, r := range poly {
				for _, c := range r {
					take(c)
				}
			}
		}
	default:
		return 0, 0, 0, 0, false
	}
	return w, s, e, n, !math.IsInf(w, 1)
}

// clipFC is mergeFC with a window: only features whose own extent meets the
// bbox are kept. Clipping rather than merging matters for memory — the
// Northeast Corridor's rail extract is 166 MB, and a feed that merged the
// whole thing to reach its own corner would chart as heavily as the whole
// corridor does. A feature straddling the edge is kept whole; the chart
// clips geometry itself.
func clipFC(dst string, srcs []string, bbox []float64) (int, error) {
	if len(bbox) != 4 {
		return mergeFC(dst, srcs)
	}
	type feature struct {
		ID    any             `json:"id,omitempty"`
		Type  string          `json:"type"`
		Props json.RawMessage `json:"properties,omitempty"`
		Geom  json.RawMessage `json:"geometry"`
	}
	seen := map[string]bool{}
	var feats []feature
	for _, src := range srcs {
		raw, err := os.ReadFile(src)
		if err != nil {
			return 0, err
		}
		var fc struct {
			Features []struct {
				feature
				Geometry struct {
					Type        string          `json:"type"`
					Coordinates json.RawMessage `json:"coordinates"`
				} `json:"geometry"`
			} `json:"features"`
		}
		if err := json.Unmarshal(raw, &fc); err != nil {
			return 0, fmt.Errorf("%s: %w", src, err)
		}
		for _, f := range fc.Features {
			w, s2, e, n, ok := featureBBox(f.Geometry.Type, f.Geometry.Coordinates)
			if !ok || w > bbox[2] || e < bbox[0] || s2 > bbox[3] || n < bbox[1] {
				continue
			}
			if f.ID != nil {
				id := fmt.Sprint(f.ID)
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			feats = append(feats, f.feature)
		}
	}
	out := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection", Features: feats}
	raw, err := json.Marshal(out)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	if err := atomicWrite(dst, append(raw, '\n')); err != nil {
		return 0, err
	}
	return len(feats), nil
}

// ------------------------------------------------------------ preflight

// groupPreflight is groupbuild.sh's per-group preparation, minus the
// network calls sync must never make: a rail extract that is missing or
// does not cover the window is MERGED from the members' (and overlays')
// own extracts instead of fetched from Overpass; a missing stops extract
// merges the same way; streets_from merges into streets (mergefc, as the
// shell does). Merged extracts overlap where windows meet — mergeFC's
// id-dedup is what keeps the shared way single.
// feedPreflight prepares a PLAIN feed the way groupPreflight prepares a
// group: if its rail extract does not cover its window, cut one that does.
// A group merges from its members; a lone feed has none, so it borrows from
// any feed in the registry whose extract already covers the window — the
// regional group's, in practice — and clips that to its own box.
//
// This exists because a window and an extract have to agree. Widening
// Metro-North to the railroad it actually runs is useless while its extract
// still stops at the New York City line, and sync must never call Overpass.
func feedPreflight(cfg registry.Config, key, buildDir string,
	logf func(string, ...any)) error {
	fc := cfg.Feeds[key]
	if fc.Rail == "" || len(fc.BBox) != 4 || railCovers(fc.Rail, fc.BBox) {
		return nil
	}
	// Widest first: one source that covers the whole window beats several
	// that each cover a corner.
	type cand struct {
		path string
		area float64
	}
	var cands []cand
	seen := map[string]bool{}
	for _, other := range cfg.Feeds {
		p := other.Rail
		if p == "" || p == fc.Rail || seen[p] {
			continue
		}
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			continue
		}
		if !railCovers(p, fc.BBox) {
			continue
		}
		seen[p] = true
		a := 0.0
		if len(other.BBox) == 4 {
			a = (other.BBox[2] - other.BBox[0]) * (other.BBox[3] - other.BBox[1])
		}
		cands = append(cands, cand{p, a})
	}
	if len(cands) == 0 {
		return fmt.Errorf("%s: rail extract at %s does not cover the window and no other extract covers it either", key, fc.Rail)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].area > cands[j].area })
	dst := fc.Rail
	if !strings.HasPrefix(dst, "build/") {
		dst = filepath.Join(buildDir, key+"-rail.geojson")
	}
	n, err := clipFC(dst, []string{cands[0].path}, fc.BBox)
	if err != nil {
		return fmt.Errorf("%s: clipping rail extract: %w", key, err)
	}
	logf("%s: cut a rail extract to its window from %s (%d ways) — sync never calls Overpass", key, cands[0].path, n)
	return nil
}

func groupPreflight(cfg registry.Config, key, buildDir string,
	logf func(string, ...any)) error {
	fc := cfg.Feeds[key]
	tied := append(append([]string(nil), fc.Members...), fc.Overlays...)

	collect := func(pathOf func(registry.FeedCfg) string) []string {
		var srcs []string
		seen := map[string]bool{}
		for _, m := range tied {
			mc, ok := cfg.Feeds[m]
			if !ok {
				continue
			}
			p := pathOf(mc)
			if p == "" || seen[p] {
				continue
			}
			if st, err := os.Stat(p); err != nil || st.Size() == 0 {
				continue
			}
			seen[p] = true
			srcs = append(srcs, p)
		}
		return srcs
	}

	if fc.Rail != "" && !railCovers(fc.Rail, fc.BBox) {
		srcs := collect(func(m registry.FeedCfg) string { return m.Rail })
		if len(srcs) == 0 {
			return fmt.Errorf("%s: rail extract at %s does not cover the window and no member extract exists to merge", key, fc.Rail)
		}
		n, err := mergeFC(fc.Rail, srcs)
		if err != nil {
			return fmt.Errorf("%s: merging rail extracts: %w", key, err)
		}
		logf("%s: merged rail extract from %d member extracts (%d ways) — sync never calls Overpass", key, len(srcs), n)
	}
	if fc.Stops != "" {
		if st, err := os.Stat(fc.Stops); err != nil || st.Size() == 0 {
			if srcs := collect(func(m registry.FeedCfg) string { return m.Stops }); len(srcs) > 0 {
				n, err := mergeFC(fc.Stops, srcs)
				if err != nil {
					return fmt.Errorf("%s: merging stops extracts: %w", key, err)
				}
				logf("%s: merged stops extract from %d member extracts (%d stops)", key, len(srcs), n)
			}
		}
	}
	// members with DIFFERENT street extracts need one merged extract, or
	// the group draws only the buses of whichever member's file it names
	if len(fc.StreetsFrom) > 0 && fc.Streets != "" {
		if st, err := os.Stat(fc.Streets); err != nil || st.Size() == 0 {
			n, err := mergeFC(fc.Streets, fc.StreetsFrom)
			if err != nil {
				return fmt.Errorf("%s: merging streets: %w", key, err)
			}
			logf("%s: merged streets from %v (%d ways)", key, fc.StreetsFrom, n)
		}
	}
	return nil
}

// railCovers ports groupbuild.sh's coverage check: the extract exists,
// is more than trivially small, and its feature extent covers the
// group's bbox within 0.05 degrees of slack.
func railCovers(path string, bbox []float64) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() < 2048 {
		return false
	}
	if len(bbox) != 4 {
		return true
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var fc struct {
		Features []struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if json.Unmarshal(raw, &fc) != nil {
		return false
	}
	w, s := math.Inf(1), math.Inf(1)
	e, n := math.Inf(-1), math.Inf(-1)
	take := func(c [2]float64) {
		w = math.Min(w, c[0])
		e = math.Max(e, c[0])
		s = math.Min(s, c[1])
		n = math.Max(n, c[1])
	}
	for _, f := range fc.Features {
		switch f.Geometry.Type {
		case "LineString":
			var pts [][2]float64
			if json.Unmarshal(f.Geometry.Coordinates, &pts) != nil {
				continue
			}
			for _, c := range pts {
				take(c)
			}
		case "MultiLineString":
			var parts [][][2]float64
			if json.Unmarshal(f.Geometry.Coordinates, &parts) != nil {
				continue
			}
			for _, pts := range parts {
				for _, c := range pts {
					take(c)
				}
			}
		}
	}
	return w-0.05 <= bbox[0] && s-0.05 <= bbox[1] && e+0.05 >= bbox[2] && n+0.05 >= bbox[3]
}
