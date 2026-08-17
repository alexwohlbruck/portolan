// Package atlas is the dev workbench: sketch editor, final-map viewer on the
// MapLibre variable-offset fork, corridor-graph overlay, and one-click
// pipeline runs — the permanent fixtures of the dev loop (docs/TOOLS.md).
package atlas

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/pipeline"
	"github.com/alexwohlbruck/portolan/internal/sketch"
	"github.com/alexwohlbruck/portolan/internal/style"
)

//go:embed editor.html
var editorHTML []byte

//go:embed map.html
var mapHTML []byte

//go:embed nav.js
var navJS []byte

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
}

// primaryGTFS: the first feed of the comma list — scenarios and mtime
// checks are primary-feed concepts.
func (f FeedCfg) primaryGTFS() string {
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

// styleFor resolves the effective style for one city through the SAME
// loader the CLI uses — style/_default.json then style/<city>.json. One
// merge implementation, so a dashboard build and a `tools/city.sh build`
// cannot disagree (they used to: the shell merged with jq and silently
// dropped every knob it had not been taught).
func (s *Server) styleFor(feed string) (*style.Set, []string) {
	// a group layers its members' documents under its own, exactly as
	// the CLI build does
	names := []string{feed}
	if fc, ok := s.config().Feeds[feed]; ok && len(fc.Members) > 0 {
		names = append(append([]string{}, fc.Members...), feed)
	}
	set, las, err := style.LoadDir(s.config().StyleDir, names...)
	if err != nil {
		// a broken document must not take the dashboard down; the build
		// log is where the operator will look for it
		return style.New(), nil
	}
	return set, las
}

type Server struct {
	maplibre string // fork dist dir

	cfgMu   sync.Mutex
	cfgPath string
	cfg     Config // last good parse — see config()
	cfgMod  time.Time

	mu       sync.Mutex
	overlays map[string]*overlayCache

	scenMu    sync.Mutex
	scenarios map[string]*scenCache

	actMu    sync.Mutex
	activity map[string]*actCache

	locMu sync.Mutex

	runMu   sync.Mutex
	running bool
	runLog  []string
	runOK   bool
	runDone bool
	runCmd  string
}

type overlayCache struct {
	mod   time.Time
	feats []overlayFeat
}

type scenCache struct {
	mod   time.Time
	scens []gtfs.Scenario
}

type overlayFeat struct {
	raw            json.RawMessage
	minLon, minLat float64
	maxLon, maxLat float64
}

func NewServer(configPath, maplibreDir string) (*Server, error) {
	s := &Server{maplibre: maplibreDir, cfgPath: configPath,
		overlays: map[string]*overlayCache{}, scenarios: map[string]*scenCache{},
		activity: map[string]*actCache{}}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("workbench config: %w (see portolan.json in the repo)", err)
	}
	cfg, err := parseConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("workbench config: %w", err)
	}
	s.cfg = cfg
	if st, err := os.Stat(configPath); err == nil {
		s.cfgMod = st.ModTime()
	}
	return s, nil
}

func parseConfig(raw []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Sketches == "" {
		cfg.Sketches = "sketches"
	}
	return cfg, nil
}

// config re-reads portolan.json when it has changed on disk, so adding a
// city (docs/CITIES.md) shows up on refresh — the same edit-and-refresh
// deal as locations() and asset(). A long-lived atlas that had read the
// feed list exactly once at boot silently hid seven new cities for a day.
// A broken edit mid-save keeps the last good config rather than emptying
// the feed picker.
func (s *Server) config() Config {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	st, err := os.Stat(s.cfgPath)
	if err != nil || st.ModTime().Equal(s.cfgMod) {
		return s.cfg
	}
	s.cfgMod = st.ModTime()
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		log.Printf("atlas: %s unreadable (%v) — keeping the loaded config", s.cfgPath, err)
		return s.cfg
	}
	cfg, err := parseConfig(raw)
	if err != nil {
		log.Printf("atlas: %s invalid (%v) — keeping the loaded config", s.cfgPath, err)
		return s.cfg
	}
	s.cfg = cfg
	log.Printf("atlas: reloaded %s — %d feeds", s.cfgPath, len(cfg.Feeds))
	return s.cfg
}

// locations returns the problem-spot list: locations.json in the working
// directory when present (the owner-editable review set — append a row,
// refresh, it's everywhere: map places, sketch bookmarks, visual bench),
// else the embedded default.
func locations() []byte {
	if raw, err := os.ReadFile("locations.json"); err == nil {
		var v []any // validate — a broken row would kill the editor page
		if json.Unmarshal(raw, &v) == nil {
			return bytes.TrimSpace(raw)
		}
		log.Printf("atlas: locations.json invalid, using embedded list")
	}
	return []byte(locsJSON)
}

// asset serves a UI file LIVE FROM DISK when the source tree is present
// (edit internal/atlas/*.html|js → refresh, no rebuild), falling back to
// the embedded copy in a standalone binary.
func asset(name string, embedded []byte) []byte {
	if raw, err := os.ReadFile(filepath.Join("internal", "atlas", name)); err == nil {
		return raw
	}
	return embedded
}

func (s *Server) Handler() http.Handler {
	render := func(name string, embedded []byte, inject, nav bool) []byte {
		body := asset(name, embedded)
		if inject {
			body = bytes.ReplaceAll(body, []byte("%LOCS%"), locations())
			body = bytes.ReplaceAll(body, []byte("%SWATCHES%"), []byte(swatchesJSON))
		}
		if nav {
			body = append(append([]byte(nil), body...),
				[]byte(`<script src="/nav.js"></script>`)...)
		}
		return body
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/sketch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(render("editor.html", editorHTML, true, true))
	})
	mux.HandleFunc("/map", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// the reworked map page owns its chrome — no shared nav bar
		w.Write(render("map.html", mapHTML, false, false))
	})
	mux.HandleFunc("/nav.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(asset("nav.js", navJS))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/map", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	// the MapLibre FORK dist (variable line-offset along line-progress)
	mux.HandleFunc("/vendor/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if name != "maplibre-gl.js" && name != "maplibre-gl.css" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.maplibre, name))
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.config())
	})
	mux.HandleFunc("/api/network", s.network)
	mux.HandleFunc("/api/features", s.features)
	mux.HandleFunc("/api/scenarios", s.scenariosAPI)
	mux.HandleFunc("/api/activity", s.activityAPI)
	mux.HandleFunc("/api/build.geojson", func(w http.ResponseWriter, r *http.Request) {
		fc, _, ok := s.feedCfg(r)
		if !ok {
			http.Error(w, "unknown feed", 404)
			return
		}
		p := fc.Out
		if scen := r.URL.Query().Get("scenario"); scen != "" {
			sp, err := scenOut(fc.Out, scen)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			p = sp
		}
		w.Header().Set("Content-Type", "application/json")
		// stale cached builds have masqueraded as pipeline bugs repeatedly
		w.Header().Set("Cache-Control", "no-store")
		if _, err := os.Stat(p); err != nil {
			fmt.Fprint(w, `{"type":"FeatureCollection","features":[]}`)
			return
		}
		// ?band=N serves one zoom band. FAIR emits a full copy of the map
		// per band and only one is ever visible, so the viewer fetches the
		// band it needs instead of four times the data.
		if band, ok := bandOf(r); ok {
			serveBand(w, p, band)
			return
		}
		http.ServeFile(w, r, p)
	})
	mux.HandleFunc("/api/build-delta", s.buildDelta)
	mux.HandleFunc("/api/rail.geojson", s.fileFor(func(f FeedCfg) string { return f.Rail }))
	for _, st := range []string{"strands", "support", "graph", "nodes", "trackcenter", "paths", "stations"} {
		stage := st
		mux.HandleFunc("/api/"+stage+".geojson",
			s.fileFor(func(f FeedCfg) string { return f.Out + "." + stage + ".geojson" }))
	}
	// tile pyramids cut by `portolan tiles` live beside the build in
	// build/tiles/<feed>/. The viewer streams these for region-scale maps
	// instead of holding whole-document GeoJSON; a 404 on tiles.json is
	// the probe that tells it to fall back to the GeoJSON band protocol.
	mux.HandleFunc("/api/tiles/", func(w http.ResponseWriter, r *http.Request) {
		// index.json is the world: every feed with a cut pyramid, with its
		// bounds — the global view draws exactly this list and nothing else
		if r.URL.Path == "/api/tiles/index.json" {
			type entry struct {
				Feed    string    `json:"feed"`
				Name    string    `json:"name"`
				Bounds  []float64 `json:"bounds,omitempty"`
				MaxZoom int       `json:"maxzoom"`
			}
			cfg := s.config()
			// a feed that rides inside a group is drawn BY the group —
			// listing both would double-draw its railroad
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
			out := []entry{}
			for _, k := range keys {
				fc := cfg.Feeds[k]
				raw, err := os.ReadFile(filepath.Join(filepath.Dir(fc.Out), "tiles", k, "tiles.json"))
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
				out = append(out, entry{Feed: k, Name: fc.Name, Bounds: tj.Bounds, MaxZoom: tj.MaxZoom})
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(out)
			return
		}
		feed, sub, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/tiles/"), "/")
		fc, exists := s.config().Feeds[feed]
		if !ok || !exists {
			http.Error(w, "unknown feed", 404)
			return
		}
		dir := filepath.Join(filepath.Dir(fc.Out), "tiles", feed)
		clean := filepath.Clean("/" + sub) // no escaping the pyramid dir
		p := filepath.Join(dir, clean)
		if sub == "tiles.json" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, p)
			return
		}
		if !strings.HasSuffix(sub, ".mvt") {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(p); err != nil {
			// an empty tile is a valid answer inside the pyramid: the
			// cutter only writes tiles a feature touches
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.mapbox-vector-tile")
		http.ServeFile(w, r, p)
	})
	mux.HandleFunc("/api/locations", s.locationsAPI)
	mux.HandleFunc("/api/refs/status", s.refsStatus)
	// reference screenshots (refs/ is gitignored; local comparison only)
	mux.HandleFunc("/refs/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/refs/")
		dir, file, ok := strings.Cut(name, "/")
		if !ok || (dir != "apple" && dir != "portolan") ||
			!strings.HasSuffix(file, ".png") || strings.ContainsAny(file, "/\\") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join("refs", dir, file))
	})
	mux.HandleFunc("/api/params", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pipeline.DefaultDials())
	})
	// the resolved style for a city: class defaults merged with the city's
	// overrides. The viewer renders widths, opacities and floors from this
	// rather than keeping its own copy of the table.
	mux.HandleFunc("/api/style", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// "global" is not a feed: it is the world context, and its style is
		// the default layer alone — no city has been chosen to override it
		if r.URL.Query().Get("feed") == "global" {
			set, _ := s.styleFor("")
			json.NewEncoder(w).Encode(set)
			return
		}
		_, feed, ok := s.feedCfg(r)
		if !ok {
			http.Error(w, "unknown feed", 404)
			return
		}
		set, _ := s.styleFor(feed)
		json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("/api/feeds", s.citiesAPI)
	mux.HandleFunc("/api/feeds/", s.cityAPI)
	mux.HandleFunc("/api/style/config", s.styleConfigAPI)
	mux.HandleFunc("/api/routes", s.routesAPI)
	mux.HandleFunc("/console/", s.console)
	mux.HandleFunc("/api/run", s.run)
	mux.HandleFunc("/api/run/status", s.runStatus)
	mux.HandleFunc("/api/score", s.score)
	mux.HandleFunc("/api/snap", s.snap)
	return mux
}

// snap saves a browser-rendered PNG (map.getCanvas().toDataURL blob) under
// refs/ — the visual-benchmark capture path (docs/VISUAL-BENCH.md). Names
// are constrained to a flat slug so the endpoint can't write elsewhere.
func (s *Server) snap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", 405)
		return
	}
	name := r.URL.Query().Get("name")
	ok := name != "" && len(name) < 80
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			ok = false
		}
	}
	if !ok {
		http.Error(w, "bad name (want [a-z0-9-_])", 400)
		return
	}
	if err := os.MkdirAll(filepath.Join("refs", "portolan"), 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f, err := os.Create(filepath.Join("refs", "portolan", name+".png"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(r.Body, 20<<20)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

// score runs the scorer in-process and returns the full structured result —
// every deviation, spike, and dup sample carries a location the UI can fly
// to (a number you can't navigate to is a number you won't fix).
func (s *Server) score(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fc, feed, ok := s.feedCfg(r)
	if !ok {
		http.Error(w, `{"error":"unknown feed"}`, 404)
		return
	}
	network := fc.Network
	if network == "" {
		network = filepath.Join(s.config().Sketches, "network-"+feed+".json")
	}
	t0 := time.Now()
	res, err := pipeline.Sound(pipeline.SoundOpts{Network: network, Build: fc.Out})
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"result": res,
		"ms":     time.Since(t0).Milliseconds(),
		"gates": map[string]float64{
			"p90": sketch.FailP90M, "cover": sketch.FailCoverPct,
			"dup": sketch.FailDupPct, "wobble": sketch.FailWobbleP90,
			"jag_max": sketch.FailJagMaxDeg, "jag_per_km": sketch.FailJagPerKm,
		},
	})
}

func (s *Server) feedCfg(r *http.Request) (FeedCfg, string, bool) {
	feed := r.URL.Query().Get("feed")
	if feed == "" {
		feed = "5"
	}
	fc, ok := s.config().Feeds[feed]
	return fc, feed, ok
}

func (s *Server) fileFor(pick func(FeedCfg) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fc, _, ok := s.feedCfg(r)
		if !ok {
			http.Error(w, "unknown feed", 404)
			return
		}
		p := pick(fc)
		if _, err := os.Stat(p); err != nil {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"type":"FeatureCollection","features":[]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// stale cached artifacts have masqueraded as pipeline bugs
		// repeatedly (same rule as /api/build.geojson) — stations from a
		// mid-debug build once put Atlantic Av's markers at Dean St
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, p)
	}
}

// ---- problem areas -------------------------------------------------------

var keyRe = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// locationsAPI: GET returns the problem-area list; POST upserts (or with
// delete:true removes) one area in locations.json — the UI's draw-a-box
// flow lands here. Rows stay position-compatible ([key, name, lat, lon,
// feed] + optional [w,s,e,n] bbox as a 6th element) so jq, the sketch
// bookmarks and the bench scripts keep working untouched.
func (s *Server) locationsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		w.Write(locations())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var req struct {
		Key    string    `json:"key"`
		Name   string    `json:"name"`
		Feed   string    `json:"feed"`
		Lat    float64   `json:"lat"`
		Lon    float64   `json:"lon"`
		BBox   []float64 `json:"bbox"` // [w, s, e, n]
		Delete bool      `json:"delete"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !keyRe.MatchString(req.Key) {
		http.Error(w, `bad key (want [a-z0-9_-]{1,40})`, 400)
		return
	}
	if len(req.BBox) == 4 {
		if req.BBox[0] >= req.BBox[2] || req.BBox[1] >= req.BBox[3] {
			http.Error(w, "bad bbox (want [w,s,e,n])", 400)
			return
		}
		req.Lon = (req.BBox[0] + req.BBox[2]) / 2
		req.Lat = (req.BBox[1] + req.BBox[3]) / 2
	}
	if !req.Delete && (req.Name == "" || req.Feed == "" || req.Lat == 0) {
		http.Error(w, "need name, feed and bbox (or lat/lon)", 400)
		return
	}

	s.locMu.Lock()
	defer s.locMu.Unlock()
	var rows []json.RawMessage
	if err := json.Unmarshal(locations(), &rows); err != nil {
		http.Error(w, "locations.json unreadable: "+err.Error(), 500)
		return
	}
	out := rows[:0]
	for _, raw := range rows {
		var row []any
		if json.Unmarshal(raw, &row) == nil && len(row) > 0 {
			if k, _ := row[0].(string); k == req.Key {
				continue // replaced (or deleted) below
			}
		}
		out = append(out, raw)
	}
	if !req.Delete {
		row := []any{req.Key, req.Name, req.Lat, req.Lon, req.Feed}
		if len(req.BBox) == 4 {
			row = append(row, req.BBox)
		}
		raw, _ := json.Marshal(row)
		out = append(out, raw)
	}
	pretty, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile("locations.json.tmp", pretty, 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.Rename("locations.json.tmp", "locations.json"); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write(pretty)
}

// refsStatus reports, per problem-area key, the mtimes of its two tracked
// screenshots (portolan render + Apple Maps reference), 0 when missing.
func (s *Server) refsStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var rows [][]any
	json.Unmarshal(locations(), &rows)
	type st struct {
		Portolan int64 `json:"portolan"`
		Apple    int64 `json:"apple"`
	}
	out := map[string]st{}
	mt := func(p string) int64 {
		if fi, err := os.Stat(p); err == nil {
			return fi.ModTime().Unix()
		}
		return 0
	}
	for _, row := range rows {
		key, _ := row[0].(string)
		if key == "" {
			continue
		}
		out[key] = st{
			Portolan: mt(filepath.Join("refs", "portolan", key+".png")),
			Apple:    mt(filepath.Join("refs", "apple", key+".png")),
		}
	}
	json.NewEncoder(w).Encode(out)
}

// ---- service scenarios ---------------------------------------------------

// scenOut: the build output path for one scenario — "build/nyc.geojson" →
// "build/nyc.scen-<id>.geojson". IDs are hex hashes; validate so the query
// param can't traverse paths.
func scenOut(out, scen string) (string, error) {
	if len(scen) == 0 || len(scen) > 16 {
		return "", fmt.Errorf("bad scenario id")
	}
	for _, c := range scen {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return "", fmt.Errorf("bad scenario id")
		}
	}
	return strings.TrimSuffix(out, ".geojson") + ".scen-" + scen + ".geojson", nil
}

// scenariosAPI returns the feed's derived service scenarios plus a 7×24
// grid mapping (day, hour) → scenario id and which scenarios already have
// a built layout on disk. Derivation is cached on the GTFS zip's mtime
// (the stop_times scan takes a few seconds on big feeds).
func (s *Server) scenariosAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fc, feed, ok := s.feedCfg(r)
	if !ok || fc.GTFS == "" {
		json.NewEncoder(w).Encode(map[string]any{"available": false})
		return
	}
	st, err := os.Stat(fc.primaryGTFS())
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"available": false, "error": err.Error()})
		return
	}
	s.scenMu.Lock()
	c := s.scenarios[feed]
	if c == nil || !c.mod.Equal(st.ModTime()) {
		s.scenMu.Unlock()
		si, err := pipeline.LoadServiceInfo(fc.GTFS)
		if err != nil {
			log.Printf("atlas: scenarios unavailable for feed %s: %v", feed, err)
			json.NewEncoder(w).Encode(map[string]any{"available": false, "error": err.Error()})
			return
		}
		scens := gtfs.BuildScenarios(si, pipeline.DefaultDials().Cover)
		s.scenMu.Lock()
		c = &scenCache{mod: st.ModTime(), scens: scens}
		s.scenarios[feed] = c
	}
	scens := c.scens
	s.scenMu.Unlock()

	type scenJSON struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Patterns int    `json:"patterns"`
		Built    bool   `json:"built"`
	}
	var list []scenJSON
	var grid [7][24]string
	for _, sc := range scens {
		built := false
		if p, err := scenOut(fc.Out, sc.ID); err == nil {
			if _, err := os.Stat(p); err == nil {
				built = true
			}
		}
		list = append(list, scenJSON{sc.ID, sc.Label, sc.Patterns, built})
		for d := 0; d < 7; d++ {
			for h := 0; h < 24; h++ {
				if sc.Cells[d][h] {
					grid[d][h] = sc.ID
				}
			}
		}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"available": true, "scenarios": list, "grid": grid,
	})
}

// ---- one-click pipeline runs -------------------------------------------

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", 405)
		return
	}
	fc, feed, ok := s.feedCfg(r)
	if !ok {
		http.Error(w, "unknown feed", 404)
		return
	}
	cmd := r.URL.Query().Get("cmd")
	scen := r.URL.Query().Get("scenario")
	out := fc.Out
	if scen != "" {
		p, err := scenOut(fc.Out, scen)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		out = p
	}
	s.runMu.Lock()
	if s.running {
		s.runMu.Unlock()
		http.Error(w, "a run is already in progress", 409)
		return
	}
	s.running, s.runDone, s.runOK = true, false, false
	s.runCmd = cmd + " " + fc.Name
	if scen != "" {
		s.runCmd += " scenario " + scen
	}
	s.runLog = nil
	s.runMu.Unlock()

	logf := func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		s.runMu.Lock()
		s.runLog = append(s.runLog, line)
		s.runMu.Unlock()
		log.Println(line)
	}
	var dials *pipeline.Dials
	if r.Body != nil {
		d := pipeline.DefaultDials()
		if err := json.NewDecoder(r.Body).Decode(&d); err == nil {
			dials = &d
		}
	}
	go func() {
		var err error
		switch cmd {
		case "chart":
			sty, las := s.styleFor(feed)
			err = pipeline.Chart(pipeline.ChartOpts{
				GTFS: fc.GTFS, Rail: fc.Rail, Streets: fc.Streets, Stops: fc.Stops,
				LineAgencies: las,
				BBox:         fc.BBox, Out: out, Dials: dials,
				Style:    sty,
				Scenario: scen,
			}, logf)
		case "sound":
			net := fc.Network
			if net == "" {
				net = filepath.Join(s.config().Sketches, "network-"+feed+".json")
			}
			err = soundToLog(net, fc.Out, logf)
		default:
			err = fmt.Errorf("unknown cmd %q", cmd)
		}
		s.runMu.Lock()
		s.running, s.runDone = false, true
		s.runOK = err == nil
		if err != nil {
			s.runLog = append(s.runLog, "ERROR: "+err.Error())
		} else {
			s.runLog = append(s.runLog, "done")
		}
		s.runMu.Unlock()
	}()
	w.WriteHeader(202)
}

func soundToLog(network, build string, logf func(string, ...any)) error {
	res, err := pipeline.Sound(pipeline.SoundOpts{Network: network, Build: build})
	if err != nil {
		return err
	}
	for _, ls := range res.Lines {
		flag := ""
		if ls.Fail {
			flag = "  <== FAIL"
		}
		logf("%-14s %5.1fkm  mean %4.1f  p90 %4.1f  max %5.1f  cover %5.1f%%%s",
			ls.Label, ls.Km, ls.Mean, ls.P90, ls.Max, ls.CoverPct, flag)
	}
	logf("jaggedness: max %.0f°, %d spikes (%.1f/km)", res.JagMaxDeg, len(res.Spikes), res.JagPerKm)
	for i, sp := range res.Spikes {
		if i >= 5 {
			break
		}
		logf("  spike %.0f° @ %.5f,%.5f", sp.Deg, sp.At.Lat, sp.At.Lon)
	}
	logf("wobble mean %.1f p90 %.1f · fwd mean %.1f p90 %.1f",
		res.WobbleMean, res.WobbleP90, res.FwdMean, res.FwdP90)
	if res.Failures > 0 {
		logf("FAIL (%d gates)", res.Failures)
	} else {
		logf("PASS")
	}
	return nil
}

func (s *Server) runStatus(w http.ResponseWriter, r *http.Request) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"running": s.running, "done": s.runDone,
		"ok": s.runOK, "cmd": s.runCmd, "log": s.runLog,
	})
}

// ---- sketch storage ------------------------------------------------------

func (s *Server) path(feed string) (string, error) {
	feed = strings.TrimSpace(feed)
	if feed == "" || strings.ContainsAny(feed, "/\\.") {
		return "", fmt.Errorf("bad feed %q", feed)
	}
	return filepath.Join(s.config().Sketches, "network-"+feed+".json"), nil
}

func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p, err := s.path(r.URL.Query().Get("feed"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			raw = []byte(`{"lines":[]}`)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	case http.MethodPost:
		var doc struct {
			Feed string `json:"feed"`
		}
		raw := json.RawMessage{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := json.Unmarshal(raw, &doc); err != nil || doc.Feed == "" {
			http.Error(w, "body must carry feed", 400)
			return
		}
		p, err := s.path(doc.Feed)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o644); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := os.Rename(tmp, p); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "method", 405)
	}
}

// ---- editor build overlay (z15 features around a point) ------------------

func (s *Server) features(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fc, feed, ok := s.feedCfg(r)
	lat, e1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, e2 := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	rad, e3 := strconv.ParseFloat(r.URL.Query().Get("r"), 64)
	if !ok || e1 != nil || e2 != nil || e3 != nil {
		fmt.Fprint(w, `{"type":"FeatureCollection","features":[]}`)
		return
	}
	feats, err := s.loadOverlay(feed, fc.Out)
	if err != nil {
		fmt.Fprint(w, `{"type":"FeatureCollection","features":[]}`)
		return
	}
	dlat := rad / 111320.0
	dlon := rad / (111320.0 * math.Cos(lat*math.Pi/180))
	x0, x1 := lon-dlon, lon+dlon
	y0, y1 := lat-dlat, lat+dlat
	picked := []json.RawMessage{}
	for _, f := range feats {
		if f.maxLon < x0 || f.minLon > x1 || f.maxLat < y0 || f.minLat > y1 {
			continue
		}
		picked = append(picked, f.raw)
	}
	json.NewEncoder(w).Encode(map[string]any{
		"type": "FeatureCollection", "features": picked,
	})
}

func (s *Server) loadOverlay(feed, path string) ([]overlayFeat, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.overlays[feed]; ok && st.ModTime().Equal(c.mod) {
		return c.feats, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, err
	}
	var feats []overlayFeat
	for _, fr := range fc.Features {
		var f struct {
			Props struct {
				BandMin *int `json:"band_min"`
				BandMax *int `json:"band_max"`
			} `json:"properties"`
			Geometry struct {
				Coords [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		}
		if err := json.Unmarshal(fr, &f); err != nil {
			continue
		}
		if f.Props.BandMax != nil && *f.Props.BandMax < 15 {
			continue
		}
		if f.Props.BandMin != nil && *f.Props.BandMin > 15 {
			continue
		}
		of := overlayFeat{raw: fr, minLon: 999, minLat: 999, maxLon: -999, maxLat: -999}
		for _, c := range f.Geometry.Coords {
			of.minLon = math.Min(of.minLon, c[0])
			of.maxLon = math.Max(of.maxLon, c[0])
			of.minLat = math.Min(of.minLat, c[1])
			of.maxLat = math.Max(of.maxLat, c[1])
		}
		feats = append(feats, of)
	}
	s.overlays[feed] = &overlayCache{mod: st.ModTime(), feats: feats}
	log.Printf("atlas: overlay cached %d z15 features from %s", len(feats), path)
	return feats, nil
}

func (s *Server) ListenAndServe(addr string) error {
	if err := os.MkdirAll(s.config().Sketches, 0o755); err != nil {
		return err
	}
	log.Printf("atlas: workbench at http://%s/  (map · sketch · run buttons)", addr)
	return http.ListenAndServe(addr, compress(s.Handler()))
}
