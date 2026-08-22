package sync

// Cross-feed group derivation — the Go port of tools/groups.py, which
// remains the prose original; the constants and rules here mirror it
// EXACTLY, because `sync patch` re-derives membership over the affected
// component and its answer must be the one a full groups.py run would
// give. Ribboning cannot cross a document: two agencies on the same
// steel draw as two lines unless they are charted together, and WHICH
// agencies those are is a measurement, not curation.
//
// Two roles come out of it, both by extent, because a group is a WINDOW:
//
//	members  — feeds small enough to sit inside one regional window.
//	overlays — feeds too wide for any window (Amtrak, VIA); they join
//	           every group they touch and cede those windows from their
//	           own build.
//
// Grouping is deliberately PERMISSIVE — a false positive costs a
// slightly larger document; a false negative draws the same rails twice.

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/geo"
	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/registry"
)

// railTypes: which route_type STRINGS groups.py treats as rail. Matched
// as strings against the raw CSV cell — "" (absent) is not rail.
var railTypes = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range []string{"0", "1", "2", "5", "6", "7", "12",
		"400", "401", "402", "403", "405", "900"} {
		m[t] = true
	}
	for t := 100; t < 118; t++ {
		m[strconv.Itoa(t)] = true
	}
	return m
}()

// Constants — groups.py's, verbatim. A shared corridor is worth grouping
// at 900 m: well below any real shared corridor (the shortest genuine
// one in North America is the McKinney Avenue streetcar's 1.2 km beside
// DART) and far above a level crossing, which shares one cell.
const (
	cellM      = 60.0  // GTFS shapes wander; 60 m survives it
	minSharedM = 900.0 // sustained-run floor
	stepM      = 30.0  // shape sampling

	// A feed whose rail extent covers more than this is a corridor, not
	// a region. Amtrak is 1247 deg², VIA 1200; the widest regional feed
	// that stays a member is Brightline at 3.4.
	maxMemberArea = 20.0 // square degrees

	degM = 111320.0
	latM = 110540.0

	marginDeg = 0.03 // degrees of slack around the members' own shapes

	// a build this small or smaller is "no build" for the undrawn rule
	// (groups.py: os.path.getsize(b) > 3000, strict)
	minBuildBytes = 3000
)

// Extent is [w, s, e, n].
type Extent [4]float64

func (e Extent) Area() float64 { return (e[2] - e[0]) * (e[3] - e[1]) }

func (e Extent) union(o Extent) Extent {
	return Extent{math.Min(e[0], o[0]), math.Min(e[1], o[1]),
		math.Max(e[2], o[2]), math.Max(e[3], o[3])}
}

// Intersects — closed-interval bbox overlap, the patch prefilter.
func (e Extent) Intersects(o Extent) bool {
	return e[0] <= o[2] && o[0] <= e[2] && e[1] <= o[3] && o[1] <= e[3]
}

// GroupSpec is one derived group: sorted members, sorted overlays, and
// the window — the union of member shape extents AND their configured
// bboxes (the Charlotte rule: a group must never see less of a member
// than the member sees of itself).
type GroupSpec struct {
	Members  []string
	Overlays []string
	Extent   Extent
}

// Derivation is everything one measurement pass established.
type Derivation struct {
	Groups []GroupSpec
	// Measured: feeds with rail shapes, sorted — groups.py's `feeds`.
	Measured []string
	// Extent per measured feed (shape extent, not the configured bbox).
	Extent map[string]Extent
	// Length: each feed's own occupied run in metres (cells × 60).
	Length map[string]float64
	// Pairs: sorted feed pair → shared run in metres, only pairs ≥900 m.
	Pairs map[[2]string]float64
	// Duplicate: held-out feed → the feed it duplicates (>85% of BOTH).
	Duplicate map[string]string
	// Undrawn: feeds held out because no build ≥3000 bytes exists.
	Undrawn []string
}

// SharedM: the shared run between two feeds, 0 when below the floor.
func (d *Derivation) SharedM(a, b string) float64 {
	if a > b {
		a, b = b, a
	}
	return d.Pairs[[2]string{a, b}]
}

// sampleShapes walks every polyline at ~stepM, exactly as groups.py
// samples: n interpolated points per segment (the endpoint arrives as
// the next segment's start), segments needing >4000 samples skipped as
// teleports, and each polyline's final point appended once.
func sampleShapes(polys [][]geo.LL) [][2]float64 {
	var out [][2]float64
	for _, pts := range polys {
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			mx := degM * math.Cos(a.Lat*math.Pi/180)
			dx, dy := (b.Lon-a.Lon)*mx, (b.Lat-a.Lat)*latM
			n := int(math.Hypot(dx, dy) / stepM)
			if n < 1 {
				n = 1
			}
			if n > 4000 { // a shape with a teleport in it; skip the jump
				continue
			}
			for j := 0; j < n; j++ {
				out = append(out, [2]float64{
					a.Lon + (b.Lon-a.Lon)*float64(j)/float64(n),
					a.Lat + (b.Lat-a.Lat)*float64(j)/float64(n)})
			}
		}
		if len(pts) > 0 {
			last := pts[len(pts)-1]
			out = append(out, [2]float64{last.Lon, last.Lat})
		}
	}
	return out
}

type cell [2]int

// cellOf truncates toward zero, like Python's int() — the two sides of
// the meridian share cell 0 there too, and parity beats prettiness.
func cellOf(p [2]float64) cell {
	const cx, cy = cellM / degM, cellM / latM
	return cell{int(p[0] / cx), int(p[1] / cy)}
}

// DeriveGroups measures the feeds and derives the groups. only, when
// non-nil, restricts which registry feeds are read — the patch planner's
// bbox-prefiltered closure; nil measures every feed whose zip exists
// (the global/groups.py case). buildDir is where the undrawn rule looks
// for default build outputs ("build" matches groups.py). Group entries
// (Members set) are never measured: a group is an output, not an input.
func DeriveGroups(cfg registry.Config, only map[string]bool, buildDir string,
	logf func(string, ...any)) (*Derivation, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	d := &Derivation{
		Extent:    map[string]Extent{},
		Length:    map[string]float64{},
		Pairs:     map[[2]string]float64{},
		Duplicate: map[string]string{},
	}

	// load: feed → sampled points, extent. Rail feeds with shapes only.
	keys := make([]string, 0, len(cfg.Feeds))
	for k := range cfg.Feeds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	samples := map[string][][2]float64{}
	for _, k := range keys {
		fc := cfg.Feeds[k]
		if len(fc.Members) > 0 {
			continue // a group is an output of this, never an input
		}
		if only != nil && !only[k] {
			continue
		}
		zip := fc.PrimaryGTFS()
		if zip == "" {
			continue
		}
		if _, err := os.Stat(zip); err != nil {
			continue
		}
		polys, err := gtfs.RailShapes(zip, railTypes)
		if err != nil { // a corrupt zip is not fatal
			logf("  %s: unreadable (%v)", k, err)
			continue
		}
		pts := sampleShapes(polys)
		if len(pts) == 0 {
			continue
		}
		ext := Extent{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
		for _, p := range pts {
			ext[0] = math.Min(ext[0], p[0])
			ext[1] = math.Min(ext[1], p[1])
			ext[2] = math.Max(ext[2], p[0])
			ext[3] = math.Max(ext[3], p[1])
		}
		samples[k] = pts
		d.Extent[k] = ext
		d.Measured = append(d.Measured, k)
	}

	// shared steel, counted in OCCUPIED CELLS so a pair's shared run and
	// a feed's own length are the same unit — the duplicate test compares
	// the two.
	grid := map[cell]map[string]bool{}
	for _, k := range d.Measured {
		own := map[cell]bool{}
		for _, p := range samples[k] {
			c := cellOf(p)
			own[c] = true
			if grid[c] == nil {
				grid[c] = map[string]bool{}
			}
			grid[c][k] = true
		}
		d.Length[k] = float64(len(own)) * cellM
	}
	hits := map[[2]string]int{}
	for _, fs := range grid {
		if len(fs) < 2 {
			continue
		}
		fl := make([]string, 0, len(fs))
		for k := range fs {
			fl = append(fl, k)
		}
		sort.Strings(fl)
		for i := range fl {
			for j := i + 1; j < len(fl); j++ {
				hits[[2]string{fl[i], fl[j]}]++
			}
		}
	}
	for p, n := range hits {
		if m := float64(n) * cellM; m >= minSharedM {
			d.Pairs[p] = m
		}
	}

	// duplicates: the SAME railway published twice. Charting them
	// together would draw the line twice side by side in one document, so
	// the smaller one is held out and reported for a human to retire.
	pairKeys := make([][2]string, 0, len(d.Pairs))
	for p := range d.Pairs {
		pairKeys = append(pairKeys, p)
	}
	sort.Slice(pairKeys, func(i, j int) bool {
		if pairKeys[i][0] != pairKeys[j][0] {
			return pairKeys[i][0] < pairKeys[j][0]
		}
		return pairKeys[i][1] < pairKeys[j][1]
	})
	for _, p := range pairKeys {
		a, b := p[0], p[1]
		m, la, lb := d.Pairs[p], d.Length[a], d.Length[b]
		if la > 0 && lb > 0 && m/la > 0.85 && m/lb > 0.85 {
			loser, keeper := b, a
			if la < lb {
				loser, keeper = a, b
			}
			d.Duplicate[loser] = keeper
		}
	}

	// undrawn: a feed that does not draw on its own today is not made a
	// member — a pattern that cannot match can gate the whole group
	// build, taking its co-members down with it.
	for _, k := range d.Measured {
		out := cfg.Feeds[k].Out
		if out == "" {
			out = filepath.Join(buildDir, k+".geojson")
		}
		if st, err := os.Stat(out); err != nil || st.Size() <= minBuildBytes {
			d.Undrawn = append(d.Undrawn, k)
		}
	}
	undrawn := map[string]bool{}
	for _, k := range d.Undrawn {
		undrawn[k] = true
	}

	// connected components over the MEMBER-eligible feeds; wide feeds
	// are overlays and deliberately do not join components, or Amtrak
	// would weld the continent into one document. A held-out feed is held
	// out of BOTH roles.
	member := map[string]bool{}
	for _, k := range d.Measured {
		if d.Extent[k].Area() <= maxMemberArea &&
			d.Duplicate[k] == "" && !undrawn[k] {
			member[k] = true
		}
	}
	parent := map[string]string{}
	for k := range member {
		parent[k] = k
	}
	var find func(string) string
	find = func(a string) string {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	for _, p := range pairKeys {
		if member[p[0]] && member[p[1]] {
			ra, rb := find(p[0]), find(p[1])
			if ra != rb {
				parent[ra] = rb
			}
		}
	}
	comps := map[string][]string{}
	for k := range member {
		r := find(k)
		comps[r] = append(comps[r], k)
	}
	roots := make([]string, 0, len(comps))
	for r := range comps {
		roots = append(roots, r)
	}
	sort.Strings(roots)

	cfgBBox := map[string]Extent{}
	for k, v := range cfg.Feeds {
		if len(v.BBox) == 4 {
			cfgBBox[k] = Extent{v.BBox[0], v.BBox[1], v.BBox[2], v.BBox[3]}
		}
	}

	for _, r := range roots {
		ms := comps[r]
		sort.Strings(ms)
		// a lone feed is still a group when a corridor feed rides over it
		// — that is the Hartford Line and the Rail Runner
		overSet := map[string]bool{}
		for _, o := range d.Measured {
			if member[o] || d.Duplicate[o] != "" || undrawn[o] {
				continue
			}
			for _, m := range ms {
				if d.SharedM(o, m) > 0 {
					overSet[o] = true
					break
				}
			}
		}
		over := make([]string, 0, len(overSet))
		for o := range overSet {
			over = append(over, o)
		}
		sort.Strings(over)
		if len(ms)+len(over) < 2 {
			continue
		}
		// The window is the union of the members' shape extents AND of
		// the windows they are already built with — the Charlotte rule.
		ext := d.Extent[ms[0]]
		for _, m := range ms {
			ext = ext.union(d.Extent[m])
			if b, ok := cfgBBox[m]; ok {
				ext = ext.union(b)
			}
		}
		d.Groups = append(d.Groups, GroupSpec{Members: ms, Overlays: over, Extent: ext})
	}
	// biggest first. groups.py's tie order is Python set-hash arbitrary;
	// first-member key is the deterministic stand-in.
	sort.SliceStable(d.Groups, func(i, j int) bool {
		if len(d.Groups[i].Members) != len(d.Groups[j].Members) {
			return len(d.Groups[i].Members) > len(d.Groups[j].Members)
		}
		return d.Groups[i].Members[0] < d.Groups[j].Members[0]
	})
	return d, nil
}

// groupLabels — display names, prose only. Ported as DATA from
// groups.py: the membership is measured, and no entry here can add a
// feed to a group or take one out. A component with no label is named
// after its largest member.
var groupLabels = map[string]string{
	"mta-subway":                         "Northeast Corridor",
	"sf-bay-area-rg":                     "Northern California",
	"chicago-cta":                        "Chicago",
	"wmata":                              "Washington–Baltimore",
	"dallasarearapidtransit":             "Dallas–Fort Worth",
	"gotransit":                          "Golden Horseshoe",
	"socitdetransportdemontral":          "Montréal",
	"exo-reseaudetransportmetropolitain": "Montréal",
	"tri-rail":                           "South Florida",
	"brightline-trails":                  "South Florida",
	"mts":                                "San Diego",
	"soundtransit":                       "Puget Sound",
	"boston":                             "Greater Boston",
	"barcelona-tmb":                      "Barcelona",
	"tokyo-metro":                        "Tokyo",
	"toei":                               "Tokyo",
	"riometroregionaltransitdistrict":    "Rio Grande",
	"floridadepartmentoftransportation":  "Central Florida",
	"uta":                                "Wasatch Front",
	"rtd":                                "Denver",
	"trimet":                             "Portland",
	"nstranslinkca":                      "Vancouver",
	"atlanta":                            "Atlanta",
	"charlotte":                          "Charlotte",
	"rta":                                "Cleveland",
	"metrostlouis":                       "St. Louis",
}

// slugify mirrors groups.py's slug(): NFKD-fold accents to ASCII, keep
// alphanumerics, turn space/dash/en-dash/underscore into "-", collapse.
// The stdlib has no NFKD, so the fold is a table over the Latin accents
// that actually occur in labels and feed names; anything outside it is
// dropped exactly as Python drops unfoldable characters.
func slugify(name string) string {
	var b strings.Builder
	for _, ch := range strings.ToLower(name) {
		if f, ok := latinFold[ch]; ok {
			ch = f
		}
		switch {
		case ch == ' ' || ch == '-' || ch == '_' || ch == '–': // groups.py: " -–_"
			b.WriteByte('-')
		case ch < 128 && (ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'):
			b.WriteRune(ch)
		}
	}
	parts := strings.Split(b.String(), "-")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "-")
}

// latinFold: the NFKD-decompose-and-drop-marks result for the accented
// Latin letters group labels and feed names use (lowercased input).
var latinFold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'ç': 'c', 'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ñ': 'n',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ý': 'y', 'ÿ': 'y',
}
