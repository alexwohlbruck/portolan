package sync

// RewriteGroups — the Go port of groups.py's write(): fold a Derivation
// back into the registry document. The document mutates exactly as the
// Python original would have mutated its dict, so MarshalDoc of the
// result is byte-comparable with a groups.py --write of the same data.

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

// chartArgsGroup: --allow-unmatched is not laxity, it is the clip. A
// corridor feed cut at a group's window arrives as fragments, and a
// fragment that is 100% gap would fail the whole document. Members are
// still guarded by tools/groupverify.py, on ink rather than this gate.
const chartArgsGroup = "--set match_gap_cost=150 --allow-unmatched"

// WidenFeedWindows makes every measured feed's window cover the feed's own
// data. A feed's bbox is the Overpass window AND the shape clip, so a window
// smaller than the railroad silently truncates the map: Metro-North carried
// the subway's [-74.26,40.49,-73.7,40.92] — authored when portolan drew only
// New York City — and came out with 25 of its 114 stations, everything past
// Yonkers clipped away with no error. Groups already take their window from
// measured data (see RewriteGroups); nothing did the same for a plain feed,
// so a hand-authored window stayed wrong forever.
//
// Widening only ever grows a window, and only to what the feed's own shapes
// occupy plus the margin groups use. A feed whose authored window is already
// big enough is untouched, so a deliberately generous window survives.
//
// Returns the keys widened, in registry order, for the caller to log.
func WidenFeedWindows(doc *Obj, der *Derivation, scope map[string]bool) []string {
	feeds := feedsObj(doc)
	if feeds == nil || der == nil {
		return nil
	}
	var widened []string
	for _, k := range feeds.Keys() {
		if scope != nil && !scope[k] {
			continue
		}
		ext, ok := der.Extent[k]
		if !ok {
			continue // not measured this run — no data to judge the window by
		}
		v, _ := feeds.Get(k)
		entry, ok := v.(*Obj)
		if !ok {
			continue
		}
		bv, _ := entry.Get("bbox")
		arr, _ := bv.([]any)
		if len(arr) != 4 {
			continue // no authored window: the chart uses the shapes as-is
		}
		cur := make([]float64, 4)
		for i, a := range arr {
			f, ok := toFloat(a)
			if !ok {
				cur = nil
				break
			}
			cur[i] = f
		}
		if cur == nil {
			continue
		}
		// Only a window that genuinely fails to contain the railroad is
		// corrected. Judging containment WITHOUT the margin matters: adding
		// slack first would nudge nearly every feed in the registry by a
		// hair, and each nudge is a rebuild.
		if cur[0] <= ext[0] && cur[1] <= ext[1] && cur[2] >= ext[2] && cur[3] >= ext[3] {
			continue
		}
		want := []float64{
			round4(math.Min(cur[0], ext[0]-marginDeg)),
			round4(math.Min(cur[1], ext[1]-marginDeg)),
			round4(math.Max(cur[2], ext[2]+marginDeg)),
			round4(math.Max(cur[3], ext[3]+marginDeg)),
		}
		entry.Set("bbox", []any{want[0], want[1], want[2], want[3]})
		// A widened feed usually outgrows the extract it was authored with,
		// and that extract is often SHARED — the eight original New York
		// entries all name testdata/nyc-rail.geojson. Give the feed its own
		// derived path so preflight can cut one to the new window without
		// overwriting a fixture other feeds still read.
		if rail := entry.Str("rail"); rail != "" && !strings.HasPrefix(rail, "build/") {
			entry.Set("rail", "build/"+k+"-rail.geojson")
		}
		widened = append(widened, k)
	}
	return widened
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// RewriteGroups mutates doc's "feeds" to carry exactly der's groups:
// updated entries keep their key, curated name and position; groups the
// data no longer supports are deleted; new groups append. Returns the
// kept group keys in write order.
//
// scope, when non-nil, bounds the DELETE rule to derived group entries
// whose members intersect it — a patch run measures only the affected
// component, and a group it did not measure is not thereby unsupported.
// Nil scope is the global run: any derived entry not re-derived goes.
//
// The returned slice is keyed 1:1 with der.Groups: element i is the
// registry key group i landed under.
func RewriteGroups(doc *Obj, der *Derivation, scope map[string]bool) []string {
	feedsV, _ := doc.Get("feeds")
	feeds, ok := feedsV.(*Obj)
	if !ok {
		feeds = NewObj()
		doc.Set("feeds", feeds)
	}

	entryOf := func(k string) *Obj {
		if v, ok := feeds.Get(k); ok {
			if o, ok := v.(*Obj); ok {
				return o
			}
		}
		return nil
	}
	membersOf := func(k string) []string {
		e := entryOf(k)
		if e == nil {
			return nil
		}
		v, _ := e.Get("members")
		arr, _ := v.([]any)
		out := make([]string, 0, len(arr))
		for _, m := range arr {
			if s, ok := m.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	primaryGTFS := func(k string) string {
		e := entryOf(k)
		if e == nil {
			return ""
		}
		return strings.TrimSpace(strings.Split(e.Str("gtfs"), ",")[0])
	}

	// a group entry the data once wrote — deletable if no longer supported
	old := map[string]bool{}
	for _, k := range feeds.Keys() {
		e := entryOf(k)
		if e == nil {
			continue
		}
		if b, _ := e.m["derived"].(bool); !b {
			continue
		}
		if scope != nil {
			touches := false
			for _, m := range membersOf(k) {
				if scope[m] {
					touches = true
					break
				}
			}
			if !touches {
				continue
			}
		}
		old[k] = true
	}

	keyOf := make([]string, 0, len(der.Groups))
	var keptOrder []string
	kept := map[string]*Obj{}
	for _, g := range der.Groups {
		// the label comes from whichever member has one, biggest first —
		// the Northeast component's longest member is NJ Transit, which
		// would have named the whole corridor after it
		ranked := append([]string(nil), g.Members...)
		sort.SliceStable(ranked, func(i, j int) bool {
			return der.Length[ranked[i]] > der.Length[ranked[j]]
		})
		anchor := ranked[0]
		for _, m := range ranked {
			if _, ok := groupLabels[m]; ok {
				anchor = m
				break
			}
		}
		inG := map[string]bool{}
		for _, m := range g.Members {
			inG[m] = true
		}
		// an existing group entry that already covers these members keeps
		// its key, its curated name and its sketch — regrouping must not
		// orphan style/chicago.json or a hand-drawn network
		prev, prevOverlap := "", 0
		for _, k := range feeds.Keys() {
			n := 0
			for _, m := range membersOf(k) {
				if inG[m] {
					n++
				}
			}
			if n > 0 && (prev == "" || prevOverlap < n) {
				prev, prevOverlap = k, n
			}
		}
		var key, name string
		if prev != "" {
			key = prev
			name = entryOf(prev).Str("name")
			if name == "" {
				name = groupLabels[anchor]
				if name == "" {
					name = anchor
				}
			}
		} else {
			name = groupLabels[anchor]
			if name == "" {
				name = entryOf(anchor).Str("name")
				if name == "" {
					name = anchor
				}
			}
			key = slugify(name)
			if feeds.Has(key) {
				key += "-region"
			}
		}

		var gtfs []string
		seen := map[string]bool{}
		for _, m := range append(append([]string(nil), g.Members...), g.Overlays...) {
			p := primaryGTFS(m)
			if !seen[p] {
				seen[p] = true
				gtfs = append(gtfs, p)
			}
		}

		entry := NewObj()
		if e := entryOf(key); e != nil {
			entry = e.Clone()
		}
		orDefault := func(field, def string) string {
			if v := entry.Str(field); v != "" {
				return v
			}
			return def
		}
		entry.Set("name", name)
		entry.Set("gtfs", strings.Join(gtfs, ","))
		entry.Set("rail", orDefault("rail", "build/"+key+"-rail.geojson"))
		entry.Set("stops", orDefault("stops", "build/"+key+"-stops.geojson"))
		entry.Set("out", orDefault("out", "build/"+key+".geojson"))
		entry.Set("bbox", []any{
			round4(g.Extent[0] - marginDeg), round4(g.Extent[1] - marginDeg),
			round4(g.Extent[2] + marginDeg), round4(g.Extent[3] + marginDeg)})
		entry.Set("members", strList(g.Members))
		// the corridor feeds charted into this window. Not members — they
		// keep their own tileset everywhere else — but their CURATION has
		// to ride in, or one railroad changes colour at the group's edge.
		entry.Set("overlays", strList(g.Overlays))
		entry.Set("derived", true)
		entry.Set("chart_args", chartArgsGroup)
		// A member built with a streets extract draws its BUSES; without
		// one the group silently loses them. Members that share an extract
		// resolve to one path; genuinely different ones are merged by
		// groupbuild before the chart runs.
		var st []string
		stSeen := map[string]bool{}
		for _, m := range g.Members {
			if e := entryOf(m); e != nil {
				if s := e.Str("streets"); s != "" && !stSeen[s] {
					stSeen[s] = true
					st = append(st, s)
				}
			}
		}
		switch {
		case len(st) == 1:
			entry.Set("streets", st[0])
		case len(st) > 1:
			entry.Set("streets", "build/"+key+"-streets.geojson")
			entry.Set("streets_from", strList(st))
		default:
			entry.Delete("streets")
			entry.Delete("streets_from")
		}
		if _, dup := kept[key]; !dup {
			keptOrder = append(keptOrder, key)
		}
		kept[key] = entry
		keyOf = append(keyOf, key)
	}

	for k := range old {
		if _, ok := kept[k]; !ok {
			feeds.Delete(k) // a group the data no longer supports
		}
	}
	for _, k := range keptOrder {
		feeds.Set(k, kept[k])
	}
	return keyOf
}

func strList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
