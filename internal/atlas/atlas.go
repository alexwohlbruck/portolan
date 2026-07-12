// Package atlas is the dev workbench: sketch editor, final-map viewer on the
// MapLibre variable-offset fork, corridor-graph overlay, and one-click
// pipeline runs — the permanent fixtures of the dev loop (docs/TOOLS.md).
package atlas

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexwohlbruck/portolan/internal/pipeline"
)

//go:embed editor.html
var editorHTML []byte

//go:embed map.html
var mapHTML []byte

//go:embed nav.js
var navJS []byte

// FeedCfg is one city in portolan.json.
type FeedCfg struct {
	Name    string `json:"name"`
	GTFS    string `json:"gtfs"`
	Rail    string `json:"rail"`
	Out     string `json:"out"`
	Network string `json:"network"` // drawn ground truth for scoring
}

type Config struct {
	Feeds    map[string]FeedCfg `json:"feeds"`
	Sketches string             `json:"sketches"`
}

type Server struct {
	cfg      Config
	maplibre string // fork dist dir

	mu       sync.Mutex
	overlays map[string]*overlayCache

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

type overlayFeat struct {
	raw            json.RawMessage
	minLon, minLat float64
	maxLon, maxLat float64
}

func NewServer(configPath, maplibreDir string) (*Server, error) {
	s := &Server{maplibre: maplibreDir, overlays: map[string]*overlayCache{}}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("workbench config: %w (see portolan.json in the repo)", err)
	}
	if err := json.Unmarshal(raw, &s.cfg); err != nil {
		return nil, fmt.Errorf("workbench config: %w", err)
	}
	if s.cfg.Sketches == "" {
		s.cfg.Sketches = "sketches"
	}
	return s, nil
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
	render := func(name string, embedded []byte, inject bool) []byte {
		body := asset(name, embedded)
		if inject {
			body = bytes.ReplaceAll(body, []byte("%LOCS%"), []byte(locsJSON))
			body = bytes.ReplaceAll(body, []byte("%SWATCHES%"), []byte(swatchesJSON))
		}
		return append(append([]byte(nil), body...),
			[]byte(`<script src="/nav.js"></script>`)...)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/sketch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(render("editor.html", editorHTML, true))
	})
	mux.HandleFunc("/map", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(render("map.html", mapHTML, false))
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
		json.NewEncoder(w).Encode(s.cfg)
	})
	mux.HandleFunc("/api/network", s.network)
	mux.HandleFunc("/api/features", s.features)
	mux.HandleFunc("/api/build.geojson", s.fileFor(func(f FeedCfg) string { return f.Out }))
	mux.HandleFunc("/api/rail.geojson", s.fileFor(func(f FeedCfg) string { return f.Rail }))
	for _, st := range []string{"strands", "support", "graph", "nodes"} {
		stage := st
		mux.HandleFunc("/api/"+stage+".geojson",
			s.fileFor(func(f FeedCfg) string { return f.Out + "." + stage + ".geojson" }))
	}
	mux.HandleFunc("/api/locations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, locsJSON)
	})
	mux.HandleFunc("/api/params", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pipeline.DefaultDials())
	})
	mux.HandleFunc("/api/run", s.run)
	mux.HandleFunc("/api/run/status", s.runStatus)
	return mux
}

func (s *Server) feedCfg(r *http.Request) (FeedCfg, string, bool) {
	feed := r.URL.Query().Get("feed")
	if feed == "" {
		feed = "5"
	}
	fc, ok := s.cfg.Feeds[feed]
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
		http.ServeFile(w, r, p)
	}
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
	s.runMu.Lock()
	if s.running {
		s.runMu.Unlock()
		http.Error(w, "a run is already in progress", 409)
		return
	}
	s.running, s.runDone, s.runOK = true, false, false
	s.runCmd = cmd + " " + fc.Name
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
			err = pipeline.Chart(pipeline.ChartOpts{
				GTFS: fc.GTFS, Rail: fc.Rail, Out: fc.Out, Dials: dials,
			}, logf)
		case "sound":
			net := fc.Network
			if net == "" {
				net = filepath.Join(s.cfg.Sketches, "network-"+feed+".json")
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
	return filepath.Join(s.cfg.Sketches, "network-"+feed+".json"), nil
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
	if err := os.MkdirAll(s.cfg.Sketches, 0o755); err != nil {
		return err
	}
	log.Printf("atlas: workbench at http://%s/  (map · sketch · run buttons)", addr)
	return http.ListenAndServe(addr, s.Handler())
}
