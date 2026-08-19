package tiles

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Opts configures one tiling run. Build is the ribbon GeoJSON `chart`
// wrote; its .stations.geojson sibling is picked up when present.
type Opts struct {
	Build   string
	Out     string // tile directory; {z}/{x}/{y}.mvt is created under it
	MaxZoom int    // top of the pyramid; 15 matches the finest band
	Name    string // tileset name for tiles.json (the feed/region key)
}

// Stats reports what was written. Unchanged and Removed exist because a
// pyramid is rebuilt often (GTFS updates all the time) and everything
// downstream — rsync, a CDN, the viewer's HTTP cache — diffs on file
// identity: a tile that would be byte-identical is not rewritten (mtime
// preserved), and tiles the new build no longer produces are pruned.
type Stats struct {
	Tiles     int // written (new or changed)
	Unchanged int
	Removed   int
	Bytes     int64
}

// Bands are already zoom ranges: a ribbon carries band_min/band_max and a
// tile at zoom z holds exactly the features whose half-open band range
// covers z (clamped to the pyramid top, so band 15 serves z15 and the
// renderer overzooms beyond). Symbols follow the client's own floors —
// stations and bundle markers exist from z11, caterpillar bullets from
// z12 — so low-zoom tiles stay lines-only and small.
const (
	symbolFloor = 11
	catFloor    = 12
	buffer      = 256 // extent units; line-offset reaches ~30 px = 240 units
	// topExtent is the TOP zoom's coordinate resolution. That level is
	// overzoomed to z20 by the viewer, and MVT coordinates are integers:
	// at the default 4096 the grid is ~0.3 m at z15 = 1.4 px at z18, so
	// a smooth corner arc lands on a visible staircase (the SW Loop read
	// bumpy in the console while the atlas drew the same build clean).
	// 32768 puts the floor at ~0.04 m — sub-pixel past z20.
	topExtent = 32768
)

type geoFeature struct {
	ID         any            `json:"id,omitempty"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

type featureCollection struct {
	Features []geoFeature `json:"features"`
}

// world maps WGS84 to [0,1)² Web-Mercator world coordinates. The engine's
// local tangent frame never appears here — tiling consumes the emitted
// lat/lon, so it is projection-consistent with any basemap.
func world(lon, lat float64) (float64, float64) {
	x := (lon + 180) / 360
	s := math.Sin(lat * math.Pi / 180)
	y := 0.5 - math.Log((1+s)/(1-s))/(4*math.Pi)
	return x, y
}

type line struct {
	pts                    [][2]float64 // world coords
	props                  map[string]any
	id                     uint64
	kind                   string // steady | transition | bridge
	zmin                   int
	zmax                   int
	minx, miny, maxx, maxy float64
}

type point struct {
	x, y  float64
	props map[string]any
	layer string // stations | markers | cat
	zmin  int
	zmax  int
}

// Build tiles one build's output fan into Out.
func Build(o Opts) (Stats, error) {
	if o.MaxZoom == 0 {
		o.MaxZoom = 15
	}
	lines, err := loadRibbons(o.Build, o.MaxZoom)
	if err != nil {
		return Stats{}, err
	}
	points, err := loadSymbols(o.Build+".stations.geojson", o.MaxZoom)
	if err != nil {
		return Stats{}, err
	}

	var st Stats
	produced := map[string]bool{}
	minx, miny := math.Inf(1), math.Inf(1)
	maxx, maxy := math.Inf(-1), math.Inf(-1)
	for _, l := range lines {
		minx, miny = math.Min(minx, l.minx), math.Min(miny, l.miny)
		maxx, maxy = math.Max(maxx, l.maxx), math.Max(maxy, l.maxy)
	}
	if len(lines) == 0 {
		return Stats{}, fmt.Errorf("tiles: no line features in %s", o.Build)
	}

	for z := 0; z <= o.MaxZoom; z++ {
		type tk [2]int
		scale := float64(int(1) << z)
		ext := extent
		if z == o.MaxZoom {
			ext = topExtent
		}
		buf := buffer * ext / extent               // same on-screen slack at any extent
		pad := float64(buf) / float64(ext) / scale // buffer in world units
		tilesAt := map[tk]map[string]*mvtLayer{}
		layer := func(t tk, name string) *mvtLayer {
			m, ok := tilesAt[t]
			if !ok {
				m = map[string]*mvtLayer{}
				tilesAt[t] = m
			}
			l, ok := m[name]
			if !ok {
				l = newLayer(name, ext)
				m[name] = l
			}
			return l
		}
		// simplification keeps the LOW-zoom tiles from bloating; the TOP
		// zoom serves every overzoom level above it, where one extent
		// unit (~0.3 m at z15) is pixels wide — a corner arc simplified
		// there redraws as 13°-per-vertex chords at z18 (the SW Loop).
		// Top-zoom tiles keep full vertex density; collinear drops only.
		simpTol := 1.0
		if z == o.MaxZoom {
			simpTol = 0
		}

		for i := range lines {
			ln := &lines[i]
			if z < ln.zmin || z > ln.zmax {
				continue
			}
			x0, x1 := tileRange(ln.minx-pad, ln.maxx+pad, z)
			y0, y1 := tileRange(ln.miny-pad, ln.maxy+pad, z)
			for tx := x0; tx <= x1; tx++ {
				for ty := y0; ty <= y1; ty++ {
					local := toLocal(ln.pts, tx, ty, scale, ext)
					var parts [][][2]float64
					if ln.kind == "steady" {
						parts = clipParts(local, ext, buf)
					} else if intersects(local, ext, buf) {
						// transitions and gap bridges ride whole: their
						// offset easing runs over line-progress, and a
						// clip would re-normalise it mid-curve.
						parts = [][][2]float64{local}
					}
					if len(parts) == 0 {
						continue
					}
					l := layer(tk{tx, ty}, "ribbons")
					f := mvtFeature{typ: 2, id: ln.id}
					for _, p := range parts {
						ip := roundPart(simplify(p, simpTol))
						if len(ip) > 1 {
							f.lines = append(f.lines, ip)
						}
					}
					if len(f.lines) == 0 {
						continue
					}
					tagAll(l, &f, ln.props)
					l.feats = append(l.feats, f)
				}
			}
		}

		for i := range points {
			pt := &points[i]
			if z < pt.zmin || z > pt.zmax {
				continue
			}
			tx := int(pt.x * scale)
			ty := int(pt.y * scale)
			// a symbol lands in every tile whose buffer reaches it, so
			// labels near an edge keep their collision context
			x0, x1 := tileRange(pt.x-pad, pt.x+pad, z)
			y0, y1 := tileRange(pt.y-pad, pt.y+pad, z)
			for ax := x0; ax <= x1; ax++ {
				for ay := y0; ay <= y1; ay++ {
					if ax != tx || ay != ty {
						continue // only the owning tile for now: symbols dedupe poorly
					}
					l := layer(tk{ax, ay}, pt.layer)
					px := int32(math.Round((pt.x*scale - float64(ax)) * float64(ext)))
					py := int32(math.Round((pt.y*scale - float64(ay)) * float64(ext)))
					f := mvtFeature{typ: 1, lines: [][][2]int32{{{px, py}}}}
					tagAll(l, &f, pt.props)
					l.feats = append(l.feats, f)
				}
			}
		}

		for t, layers := range tilesAt {
			names := make([]string, 0, len(layers))
			for n := range layers {
				names = append(names, n)
			}
			sort.Strings(names)
			ordered := make([]*mvtLayer, 0, len(names))
			for _, n := range names {
				ordered = append(ordered, layers[n])
			}
			blob := encodeTile(ordered)
			if len(blob) == 0 {
				continue
			}
			dir := filepath.Join(o.Out, fmt.Sprint(z), fmt.Sprint(t[0]))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return st, err
			}
			path := filepath.Join(dir, fmt.Sprintf("%d.mvt", t[1]))
			produced[path] = true
			if old, err := os.ReadFile(path); err == nil && string(old) == string(blob) {
				st.Unchanged++
				continue
			}
			if err := os.WriteFile(path, blob, 0o644); err != nil {
				return st, err
			}
			st.Tiles++
			st.Bytes += int64(len(blob))
		}
	}

	// prune tiles the new build no longer produces — a route that moved
	// leaves stale tiles behind, and a stale tile is a rendering bug the
	// viewer cannot detect
	err = filepath.Walk(o.Out, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(p) != ".mvt" || produced[p] {
			return err
		}
		st.Removed++
		return os.Remove(p)
	})
	if err != nil && !os.IsNotExist(err) {
		return st, err
	}

	return st, writeTileJSON(o, minx, miny, maxx, maxy)
}

func tileRange(lo, hi float64, z int) (int, int) {
	n := int(1) << z
	a := int(math.Floor(lo * float64(n)))
	b := int(math.Floor(hi * float64(n)))
	if a < 0 {
		a = 0
	}
	if b > n-1 {
		b = n - 1
	}
	return a, b
}

func toLocal(pts [][2]float64, tx, ty int, scale float64, ext int) [][2]float64 {
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{(p[0]*scale - float64(tx)) * float64(ext), (p[1]*scale - float64(ty)) * float64(ext)}
	}
	return out
}

func roundPart(p [][2]float64) [][2]int32 {
	out := make([][2]int32, 0, len(p))
	var last [2]int32
	for i, v := range p {
		q := [2]int32{int32(math.Round(v[0])), int32(math.Round(v[1]))}
		if i > 0 && q == last {
			continue
		}
		out = append(out, q)
		last = q
	}
	return out
}

// clipParts clips a polyline (tile-local coords) to the buffered tile
// square, returning the surviving runs. Plain Liang–Barsky per segment,
// with runs stitched while consecutive segments stay inside.
func clipParts(pts [][2]float64, ext, buf int) [][][2]float64 {
	lo, hi := float64(-buf), float64(ext+buf)
	var parts [][][2]float64
	var cur [][2]float64
	for i := 0; i+1 < len(pts); i++ {
		a, b, ok := clipSeg(pts[i], pts[i+1], lo, hi)
		if !ok {
			if len(cur) > 1 {
				parts = append(parts, cur)
			}
			cur = nil
			continue
		}
		if len(cur) == 0 || cur[len(cur)-1] != a {
			if len(cur) > 1 {
				parts = append(parts, cur)
			}
			cur = [][2]float64{a}
		}
		cur = append(cur, b)
	}
	if len(cur) > 1 {
		parts = append(parts, cur)
	}
	return parts
}

func clipSeg(a, b [2]float64, lo, hi float64) ([2]float64, [2]float64, bool) {
	t0, t1 := 0.0, 1.0
	dx, dy := b[0]-a[0], b[1]-a[1]
	for _, e := range [4][2]float64{{-dx, a[0] - lo}, {dx, hi - a[0]}, {-dy, a[1] - lo}, {dy, hi - a[1]}} {
		p, q := e[0], e[1]
		if p == 0 {
			if q < 0 {
				return a, b, false
			}
			continue
		}
		r := q / p
		if p < 0 {
			if r > t1 {
				return a, b, false
			}
			if r > t0 {
				t0 = r
			}
		} else {
			if r < t0 {
				return a, b, false
			}
			if r < t1 {
				t1 = r
			}
		}
	}
	return [2]float64{a[0] + t0*dx, a[1] + t0*dy}, [2]float64{a[0] + t1*dx, a[1] + t1*dy}, true
}

func intersects(pts [][2]float64, ext, buf int) bool {
	lo, hi := float64(-buf), float64(ext+buf)
	for i := 0; i+1 < len(pts); i++ {
		if _, _, ok := clipSeg(pts[i], pts[i+1], lo, hi); ok {
			return true
		}
	}
	return false
}

// simplify is Douglas–Peucker with tolerance in extent units. One unit is
// ~0.3 m at z15 and a whole city block at z5 — it is what keeps band-0
// geometry from bloating the low-zoom tiles.
func simplify(pts [][2]float64, tol float64) [][2]float64 {
	if len(pts) <= 2 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	var rec func(i, j int)
	rec = func(i, j int) {
		if j <= i+1 {
			return
		}
		ax, ay := pts[i][0], pts[i][1]
		bx, by := pts[j][0], pts[j][1]
		dx, dy := bx-ax, by-ay
		den := dx*dx + dy*dy
		best, bi := -1.0, -1
		for k := i + 1; k < j; k++ {
			var d float64
			if den == 0 {
				d = math.Hypot(pts[k][0]-ax, pts[k][1]-ay)
			} else {
				d = math.Abs(dx*(pts[k][1]-ay)-dy*(pts[k][0]-ax)) / math.Sqrt(den)
			}
			if d > best {
				best, bi = d, k
			}
		}
		if best > tol {
			keep[bi] = true
			rec(i, bi)
			rec(bi, j)
		}
	}
	rec(0, len(pts)-1)
	out := pts[:0:0]
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

func tagAll(l *mvtLayer, f *mvtFeature, props map[string]any) {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := props[k]
		if _, ok := v.([]any); ok {
			// MVT values are scalar; lists (caterpillar vec/veclo) ride
			// as JSON text and the viewer decodes them on load
			enc, err := json.Marshal(v)
			if err != nil {
				continue
			}
			v = string(enc)
		}
		l.tag(f, k, v)
	}
}

func loadRibbons(path string, maxZoom int) ([]line, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc featureCollection
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("tiles: %s: %w", path, err)
	}
	var out []line
	for _, gf := range fc.Features {
		if gf.Geometry.Type != "LineString" {
			continue
		}
		var coords [][2]float64
		if err := json.Unmarshal(gf.Geometry.Coordinates, &coords); err != nil {
			return nil, fmt.Errorf("tiles: %s: %w", path, err)
		}
		if len(coords) < 2 {
			continue
		}
		ln := line{props: gf.Properties, kind: str(gf.Properties["kind"])}
		ln.zmin, ln.zmax = bandZooms(gf.Properties, maxZoom)
		if seg, ok := gf.Properties["seg"].(float64); ok {
			ln.id = uint64(seg) + 1 // MVT ids: 0 is "absent"
		}
		ln.minx, ln.miny = math.Inf(1), math.Inf(1)
		ln.maxx, ln.maxy = math.Inf(-1), math.Inf(-1)
		ln.pts = make([][2]float64, len(coords))
		for i, c := range coords {
			x, y := world(c[0], c[1])
			ln.pts[i] = [2]float64{x, y}
			ln.minx, ln.miny = math.Min(ln.minx, x), math.Min(ln.miny, y)
			ln.maxx, ln.maxy = math.Max(ln.maxx, x), math.Max(ln.maxy, y)
		}
		out = append(out, ln)
	}
	return out, nil
}

func loadSymbols(path string, maxZoom int) ([]point, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var fc featureCollection
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("tiles: %s: %w", path, err)
	}
	var out []point
	for _, gf := range fc.Features {
		if gf.Geometry.Type != "Point" {
			continue
		}
		var c [2]float64
		if err := json.Unmarshal(gf.Geometry.Coordinates, &c); err != nil {
			return nil, fmt.Errorf("tiles: %s: %w", path, err)
		}
		x, y := world(c[0], c[1])
		p := point{x: x, y: y, props: gf.Properties}
		switch str(gf.Properties["ftype"]) {
		case "station":
			p.layer, p.zmin, p.zmax = "stations", symbolFloor, maxZoom
		case "marker":
			p.layer, p.zmin, p.zmax = "markers", symbolFloor, maxZoom
		case "cat":
			p.layer = "cat"
			if b, ok := gf.Properties["band"].(float64); ok {
				p.zmin, p.zmax = bandZoomsOf(int(b), maxZoom)
				if p.zmin < catFloor {
					p.zmin = catFloor
				}
			} else {
				p.zmin, p.zmax = catFloor, maxZoom
			}
		default:
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// bandZooms maps a ribbon's half-open [band_min, band_max) onto tile
// zooms, clamped to the pyramid top.
func bandZooms(props map[string]any, maxZoom int) (int, int) {
	bmin, _ := props["band_min"].(float64)
	bmax, ok := props["band_max"].(float64)
	if !ok {
		return 0, maxZoom
	}
	zmin := int(bmin)
	zmax := int(bmax) - 1
	if zmax > maxZoom {
		zmax = maxZoom
	}
	if zmin > maxZoom {
		zmin = maxZoom
	}
	return zmin, zmax
}

// bandZoomsOf is bandZooms for symbols that carry a single band key: the
// FAIR bands are 15, 14, 13 and 0, with 0 meaning "up to 13".
func bandZoomsOf(band, maxZoom int) (int, int) {
	switch band {
	case 0:
		return 0, 12
	default:
		if band >= maxZoom {
			return maxZoom, maxZoom
		}
		return band, band
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func writeTileJSON(o Opts, minx, miny, maxx, maxy float64) error {
	inv := func(x, y float64) (float64, float64) {
		lon := x*360 - 180
		lat := math.Atan(math.Sinh(math.Pi*(1-2*y))) * 180 / math.Pi
		return lon, lat
	}
	w, n := inv(minx, miny)
	e, s := inv(maxx, maxy)
	doc := map[string]any{
		"tilejson": "3.0.0",
		"name":     o.Name,
		"tiles":    []string{"{z}/{x}/{y}.mvt"},
		"minzoom":  0,
		"maxzoom":  o.MaxZoom,
		"bounds":   []float64{w, s, e, n},
		"vector_layers": []map[string]any{
			{"id": "ribbons", "minzoom": 0, "maxzoom": o.MaxZoom},
			{"id": "stations", "minzoom": symbolFloor, "maxzoom": o.MaxZoom},
			{"id": "markers", "minzoom": symbolFloor, "maxzoom": o.MaxZoom},
			{"id": "cat", "minzoom": catFloor, "maxzoom": o.MaxZoom},
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(o.Out, "tiles.json"), raw, 0o644)
}
