package atlas

// The console API: everything the dashboard needs that the old workbench
// pages never had — CRUD over the city list, a readable/writable style
// config, and a route inventory for the color-override picker.
//
// Config edits write portolan.json back to disk. That file is the source
// of truth for the CLI and tools/city.sh too, so the dashboard and a
// terminal build always agree; the write is atomic (temp file + rename)
// and preserves unknown keys, because a hand-edited config may carry
// fields this build does not know about.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexwohlbruck/portolan/internal/gtfs"
	"github.com/alexwohlbruck/portolan/internal/mode"
	"github.com/alexwohlbruck/portolan/internal/style"
)

// fileStat is one input path plus whether it is actually there — the
// dashboard's whole job on the Cities page is telling you what is missing
// before you hit build.
type fileStat struct {
	Path     string `json:"path"`
	OK       bool   `json:"ok"`
	Size     int64  `json:"size,omitempty"`
	Modified string `json:"modified,omitempty"`
}

func statOf(path string) fileStat {
	f := fileStat{Path: path}
	if path == "" {
		return f
	}
	st, err := os.Stat(path)
	if err != nil {
		return f
	}
	f.OK, f.Size = true, st.Size()
	f.Modified = st.ModTime().UTC().Format("2006-01-02T15:04:05Z")
	return f
}

type cityStatus struct {
	GTFS           []fileStat `json:"gtfs"`
	Rail           fileStat   `json:"rail"`
	Streets        *fileStat  `json:"streets,omitempty"`
	Build          *fileStat  `json:"build,omitempty"`
	ScenariosBuilt int        `json:"scenarios_built"`
}

type cityJSON struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	GTFS    string      `json:"gtfs"`
	Rail    string      `json:"rail"`
	Streets string      `json:"streets,omitempty"`
	Stops   string      `json:"stops,omitempty"`
	Out     string      `json:"out"`
	Network string      `json:"network,omitempty"`
	BBox    []float64   `json:"bbox,omitempty"`
	Status  *cityStatus `json:"status,omitempty"`
}

func (s *Server) cityJSONOf(id string, fc FeedCfg) cityJSON {
	c := cityJSON{
		ID: id, Name: fc.Name, GTFS: fc.GTFS, Rail: fc.Rail, Streets: fc.Streets,
		Stops: fc.Stops, Out: fc.Out, Network: fc.Network, BBox: fc.BBox,
	}
	st := &cityStatus{Rail: statOf(fc.Rail)}
	for _, p := range strings.Split(fc.GTFS, ",") {
		if p = strings.TrimSpace(p); p != "" {
			st.GTFS = append(st.GTFS, statOf(p))
		}
	}
	if fc.Streets != "" {
		f := statOf(fc.Streets)
		st.Streets = &f
	}
	if fc.Out != "" {
		f := statOf(fc.Out)
		st.Build = &f
		// scenario layouts sit beside the union build as
		// "<out>.scen-<id>.geojson"; count them rather than deriving
		// scenarios here (that costs a stop_times sweep).
		base := strings.TrimSuffix(fc.Out, ".geojson")
		if matches, err := filepath.Glob(base + ".scen-*.geojson"); err == nil {
			st.ScenariosBuilt = len(matches)
		}
	}
	c.Status = st
	return c
}

func (s *Server) citiesAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cfg := s.config()
	ids := make([]string, 0, len(cfg.Feeds))
	for id := range cfg.Feeds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]cityJSON, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.cityJSONOf(id, cfg.Feeds[id]))
	}
	json.NewEncoder(w).Encode(out)
}

// cityAPI handles GET / POST (upsert) / DELETE for one city.
func (s *Server) cityAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	id := strings.TrimPrefix(r.URL.Path, "/api/cities/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "bad city id", 400)
		return
	}
	switch r.Method {
	case http.MethodGet:
		fc, ok := s.config().Feeds[id]
		if !ok {
			http.Error(w, "unknown city", 404)
			return
		}
		json.NewEncoder(w).Encode(s.cityJSONOf(id, fc))
	case http.MethodPost:
		var in cityJSON
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		err := s.editConfig(func(raw map[string]any) error {
			feeds, _ := raw["feeds"].(map[string]any)
			if feeds == nil {
				feeds = map[string]any{}
				raw["feeds"] = feeds
			}
			row, _ := feeds[id].(map[string]any)
			if row == nil {
				row = map[string]any{}
			}
			setStr(row, "name", in.Name)
			setStr(row, "gtfs", in.GTFS)
			setStr(row, "rail", in.Rail)
			setStr(row, "streets", in.Streets)
			setStr(row, "out", in.Out)
			setStr(row, "network", in.Network)
			if len(in.BBox) == 4 {
				row["bbox"] = in.BBox
			} else {
				delete(row, "bbox")
			}
			setStr(row, "stops", in.Stops)
			feeds[id] = row
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fc := s.config().Feeds[id]
		json.NewEncoder(w).Encode(s.cityJSONOf(id, fc))
	case http.MethodDelete:
		err := s.editConfig(func(raw map[string]any) error {
			feeds, _ := raw["feeds"].(map[string]any)
			if feeds == nil {
				return fmt.Errorf("no feeds in config")
			}
			if _, ok := feeds[id]; !ok {
				return fmt.Errorf("unknown city %q", id)
			}
			delete(feeds, id)
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, POST or DELETE", 405)
	}
}

func setStr(row map[string]any, key, val string) {
	if val == "" {
		delete(row, key)
		return
	}
	row[key] = val
}

// styleConfigAPI reads and writes the RAW style layers (what a human typed)
// rather than the resolved set — an editor that saved resolved values would
// bake today's defaults into every city and freeze them there.
func (s *Server) styleConfigAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, feed, ok := s.feedCfg(r)
	if !ok {
		http.Error(w, "unknown feed", 404)
		return
	}
	switch r.Method {
	case http.MethodGet:
		dir := s.config().StyleDir
		def, _, err := style.ReadDoc(style.DocPath(dir, "_default"))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		city, _, err := style.ReadDoc(style.DocPath(dir, feed))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"global": def, "city": city})
	case http.MethodPost:
		// Only what a human typed is persisted. Names discovered by the
		// OSM stop matcher are derived at build time and never written
		// back — a config that recorded them would freeze one day's OSM
		// against every later build and quietly stop tracking upstream.
		var in struct {
			Global *style.Doc `json:"global"`
			City   *style.Doc `json:"city"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		dir := s.config().StyleDir
		var err error
		if in.Global != nil {
			err = style.WriteDoc(dir, "_default", *in.Global)
		}
		if err == nil && in.City != nil {
			if _, ok := s.config().Feeds[feed]; !ok {
				http.Error(w, fmt.Sprintf("unknown city %q", feed), 400)
				return
			}
			err = style.WriteDoc(dir, feed, *in.City)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

// routesAPI lists a city's routes so the override editor can offer real
// choices instead of asking someone to type a feed-internal id. Loaded at
// cover 1.01 (no pruning) because the point is completeness.
func (s *Server) routesAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fc, _, ok := s.feedCfg(r)
	if !ok || fc.GTFS == "" {
		http.Error(w, "unknown feed", 404)
		return
	}
	type routeJSON struct {
		ID         string `json:"id"`
		ShortName  string `json:"short_name"`
		LongName   string `json:"long_name"`
		Color      string `json:"color"`
		Mode       string `json:"mode"`
		Agency     string `json:"agency"`
		AgencyName string `json:"agency_name"`
	}
	var out []routeJSON
	seen := map[string]bool{}
	for i, p := range strings.Split(fc.GTFS, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := gtfs.LoadFiltered(p, 1.01, func(gtfs.Route) bool { return false })
		if err != nil {
			continue // a missing overlay must not empty the list
		}
		pre := ""
		if i > 0 {
			pre = fmt.Sprintf("f%d:", i)
		}
		for id, rt := range f.Routes {
			key := pre + id
			if seen[key] {
				continue
			}
			seen[key] = true
			ag := rt.Agency
			if ag != "" {
				ag = pre + ag
			}
			out = append(out, routeJSON{
				ID: key, ShortName: rt.ShortName, LongName: rt.LongName,
				Color: rt.Color, Mode: mode.Of(rt.Type).String(),
				Agency: ag, AgencyName: f.Agencies[rt.Agency],
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mode != out[j].Mode {
			return out[i].Mode < out[j].Mode
		}
		if out[i].AgencyName != out[j].AgencyName {
			return out[i].AgencyName < out[j].AgencyName
		}
		return out[i].ID < out[j].ID
	})
	json.NewEncoder(w).Encode(out)
}

// editConfig applies fn to portolan.json as a generic map and writes it
// back atomically. Generic, not typed: a config may hold keys this build
// does not model, and a typed round-trip would silently drop them.
func (s *Server) editConfig(fn func(map[string]any) error) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", s.cfgPath, err)
	}
	if err := fn(doc); err != nil {
		return err
	}
	next, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	next = append(next, '\n')
	// validate before it lands: a config the server cannot parse would
	// take the whole workbench down on next read.
	if _, err := parseConfig(next); err != nil {
		return fmt.Errorf("refusing to write an unparseable config: %w", err)
	}
	tmp := s.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, next, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.cfgPath); err != nil {
		return err
	}
	// force the next config() read to reload rather than trust its mtime
	// cache, which may have second granularity.
	s.cfgMod = s.cfgMod.Add(-1)
	return nil
}

// console serves the built dashboard (web/dist) under /console/. It is a
// single-page app, so any path that is not a real file falls back to
// index.html and the client router takes it from there.
//
// Files come from disk, not from an embed: the dashboard is a dev tool and
// `npm run build` should be visible on refresh. A missing dist is reported
// as instructions rather than a 404 nobody can act on.
func (s *Server) console(w http.ResponseWriter, r *http.Request) {
	const root = "web/dist"
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `<pre style="font:14px ui-monospace;padding:2rem;line-height:1.6">`+
			`The console is not built yet.

  cd web &amp;&amp; npm install &amp;&amp; npm run build

Then reload. For live reload during development run "npm run dev"
(port 5180) — it proxies /api and /vendor back here.

The old workbench pages are still served at /map and /sketch.</pre>`)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/console/")
	if rel == "" {
		rel = "index.html"
	}
	// path containment: the request path is attacker-controlled and this
	// serves straight off the filesystem.
	clean := filepath.Clean(filepath.Join(root, rel))
	if !strings.HasPrefix(clean, filepath.Clean(root)+string(os.PathSeparator)) &&
		clean != filepath.Clean(root) {
		http.Error(w, "bad path", 400)
		return
	}
	if st, err := os.Stat(clean); err != nil || st.IsDir() {
		clean = filepath.Join(root, "index.html") // SPA route
	}
	if strings.HasSuffix(clean, ".html") {
		w.Header().Set("Cache-Control", "no-store")
	}
	http.ServeFile(w, r, clean)
}
