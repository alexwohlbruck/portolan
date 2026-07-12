// Package atlas is the dev server: the sketch editor, window renders, and
// probes — the permanent fixtures of the dev loop (docs/TOOLS.md).
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
)

//go:embed editor.html
var editorHTML []byte

// Server serves the sketch editor and the network API. Sketches are stored
// per feed under dir as network-<feed>.json — the owner's hand work: writes
// are ATOMIC (temp file + rename), and nothing here ever regenerates one.
type Server struct {
	Dir   string // sketches directory
	Build string // optional chart output GeoJSON for the /api/features overlay

	mu       sync.Mutex
	buildMod time.Time
	feats    []overlayFeat
}

type overlayFeat struct {
	raw          json.RawMessage
	minLon, minLat float64
	maxLon, maxLat float64
}

func (s *Server) Handler() http.Handler {
	page := bytes.ReplaceAll(editorHTML, []byte("%LOCS%"), []byte(locsJSON))
	page = bytes.ReplaceAll(page, []byte("%SWATCHES%"), []byte(swatchesJSON))
	mux := http.NewServeMux()
	mux.HandleFunc("/sketch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/sketch", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/network", s.network)
	mux.HandleFunc("/api/features", s.features)
	return mux
}

// features serves the current chart output (z15 band) around a point, hot
// reloading the file when it changes — rebuild with `portolan chart`,
// refresh the editor, see the new build under the drawing.
func (s *Server) features(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	lat, e1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, e2 := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	rad, e3 := strconv.ParseFloat(r.URL.Query().Get("r"), 64)
	if s.Build == "" || e1 != nil || e2 != nil || e3 != nil {
		fmt.Fprint(w, `{"type":"FeatureCollection","features":[]}`)
		return
	}
	if err := s.loadBuild(); err != nil {
		fmt.Fprint(w, `{"type":"FeatureCollection","features":[]}`)
		return
	}
	dlat := rad / 111320.0
	dlon := rad / (111320.0 * math.Cos(lat*math.Pi/180))
	x0, x1 := lon-dlon, lon+dlon
	y0, y1 := lat-dlat, lat+dlat
	var picked []json.RawMessage
	s.mu.Lock()
	for _, f := range s.feats {
		if f.maxLon < x0 || f.minLon > x1 || f.maxLat < y0 || f.minLat > y1 {
			continue
		}
		picked = append(picked, f.raw)
	}
	s.mu.Unlock()
	out := map[string]any{"type": "FeatureCollection", "features": picked}
	if picked == nil {
		out["features"] = []json.RawMessage{}
	}
	json.NewEncoder(w).Encode(out)
}

func (s *Server) loadBuild() error {
	st, err := os.Stat(s.Build)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.ModTime().Equal(s.buildMod) {
		return nil
	}
	raw, err := os.ReadFile(s.Build)
	if err != nil {
		return err
	}
	var fc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return err
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
	s.feats = feats
	s.buildMod = st.ModTime()
	log.Printf("atlas: overlay loaded %d z15 features from %s", len(feats), s.Build)
	return nil
}

func (s *Server) path(feed string) (string, error) {
	feed = strings.TrimSpace(feed)
	if feed == "" || strings.ContainsAny(feed, "/\\.") {
		return "", fmt.Errorf("bad feed %q", feed)
	}
	return filepath.Join(s.Dir, "network-"+feed+".json"), nil
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
		body := json.NewDecoder(r.Body)
		raw := json.RawMessage{}
		if err := body.Decode(&raw); err != nil {
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
		// atomic save — the sketch is precious
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

func (s *Server) ListenAndServe(addr string) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	log.Printf("atlas: sketch editor at http://%s/sketch (sketches in %s)", addr, s.Dir)
	return http.ListenAndServe(addr, s.Handler())
}
