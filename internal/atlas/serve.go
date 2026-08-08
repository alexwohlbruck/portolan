package atlas

// Transport concerns for the workbench: response compression, and serving
// one zoom band of a build instead of all four.
//
// Both exist because the drawn output is big and repetitive. A NYC
// scenario is ~11 MB of GeoJSON that gzips to ~3.4 MB, and 54% of its
// features belong to zoom bands the viewer is not currently showing —
// FAIR emits a complete copy of the map per band, and exactly one band is
// ever visible.

import (
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ---- compression ---------------------------------------------------------

type gzipWriter struct {
	http.ResponseWriter
	gz     *gzip.Writer
	wrote  bool
	status int
}

func (g *gzipWriter) WriteHeader(status int) {
	if g.wrote {
		return
	}
	g.wrote, g.status = true, status
	// Content-Length describes the identity encoding and is meaningless
	// once we compress; leaving it set truncates the response.
	g.Header().Del("Content-Length")
	g.Header().Set("Content-Encoding", "gzip")
	g.Header().Add("Vary", "Accept-Encoding")
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	return g.gz.Write(p)
}

// Flush matters for the run-log poll: a buffered gzip stream would hold
// output back until the writer filled.
func (g *gzipWriter) Flush() {
	g.gz.Flush()
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var gzPool = sync.Pool{
	New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed); return w },
}

// compress wraps a handler in gzip when the client asks for it. Level is
// BestSpeed deliberately: on an 11 MB build the difference between fastest
// and best is a few hundred KB and several hundred milliseconds, and this
// server is on localhost.
func compress(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.ServeHTTP(w, r)
			return
		}
		// already-compressed payloads (the PNG reference captures) only
		// get bigger, and re-encoding them wastes CPU on every diff run
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/refs/"),
			strings.HasSuffix(r.URL.Path, ".png"),
			strings.HasSuffix(r.URL.Path, ".zip"):
			h.ServeHTTP(w, r)
			return
		}
		gz := gzPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close()
			gzPool.Put(gz)
		}()
		h.ServeHTTP(&gzipWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// ---- band slicing --------------------------------------------------------

// bandOf reads the ?band= query parameter. Empty means every band.
func bandOf(r *http.Request) (int, bool) {
	v := r.URL.Query().Get("band")
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// bandCache holds parsed-and-resliced builds keyed by path+band, so the
// viewer walking up and down the zoom range does not re-read and re-parse
// an 11 MB file per crossing.
type bandCache struct {
	mu sync.Mutex
	m  map[string]bandEntry
}

type bandEntry struct {
	mod  int64
	size int64
	body []byte
}

var bands = bandCache{m: map[string]bandEntry{}}

// serveBand writes only the features whose band_min matches, streaming the
// rest away. The file is parsed once per (path, band, mtime); a rebuild
// changes the mtime and invalidates it, which is the same staleness rule
// the rest of the workbench uses.
func serveBand(w http.ResponseWriter, path string, band int) {
	st, err := os.Stat(path)
	if err != nil {
		w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
		return
	}
	key := path + "#" + strconv.Itoa(band)
	bands.mu.Lock()
	e, ok := bands.m[key]
	fresh := ok && e.mod == st.ModTime().UnixNano() && e.size == st.Size()
	if fresh {
		body := e.body
		bands.mu.Unlock()
		w.Write(body)
		return
	}
	bands.mu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
		return
	}
	var fc struct {
		Type     string            `json:"type"`
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		w.Write(raw) // unparseable: hand it over whole rather than nothing
		return
	}
	kept := fc.Features[:0]
	for _, f := range fc.Features {
		var probe struct {
			Props struct {
				BandMin *int `json:"band_min"`
			} `json:"properties"`
		}
		if json.Unmarshal(f, &probe) != nil || probe.Props.BandMin == nil {
			kept = append(kept, f) // no band info: always include
			continue
		}
		if *probe.Props.BandMin == band {
			kept = append(kept, f)
		}
	}
	out, err := json.Marshal(map[string]any{"type": "FeatureCollection", "features": kept})
	if err != nil {
		w.Write(raw)
		return
	}
	bands.mu.Lock()
	bands.m[key] = bandEntry{mod: st.ModTime().UnixNano(), size: st.Size(), body: out}
	bands.mu.Unlock()
	w.Write(out)
}

// ---- content-addressed delta --------------------------------------------

// A scenario is mostly the union map with some lines missing: 69% of a
// NYC scenario's drawn geometry is byte-identical to geometry already in
// the union build, and only 28 features actually need a different offset.
// Serving each scenario as a standalone GeoJSON re-sends all of it.
//
// So: address every geometry by a hash of its coordinates, and let the
// client say which hashes it already holds. It gets the full feature list
// (properties are small — 0.46 MB for a whole NYC scenario, all bands)
// plus only the coordinates it is missing. Switching back to a scenario
// already visited transfers properties alone.
//
// The client assembles features and geometry back into a FeatureCollection
// identical to what /api/build.geojson would have served.

type deltaReq struct {
	Have []string `json:"have"`
}

type deltaResp struct {
	Features []deltaFeature     `json:"features"`
	Geom     map[string][][]any `json:"geom"`
	Stats    map[string]int     `json:"stats"`
}

type deltaFeature struct {
	Props json.RawMessage `json:"p"`
	Geom  string          `json:"g"`
}

// geomHash keys a geometry by its exact serialized coordinates. Exactness
// is the point: two features share a hash only when they would draw the
// same line, so a hit is always safe to reuse.
func geomHash(coords json.RawMessage) string {
	h := sha1.Sum(coords)
	return hex.EncodeToString(h[:9]) // 72 bits — collision-free at these counts
}

// buildDelta answers with the feature table plus the geometry the caller
// lacks. The parse is cached per (path, band, mtime) exactly like
// serveBand, so repeated scenario switches never re-read the file.
func (s *Server) buildDelta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
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
	var req deltaReq
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req) // absent body = client holds nothing
	}
	have := make(map[string]bool, len(req.Have))
	for _, h := range req.Have {
		have[h] = true
	}

	band, wantBand := bandOf(r)
	feats, err := parseFeatures(p)
	if err != nil {
		json.NewEncoder(w).Encode(deltaResp{Features: []deltaFeature{}, Geom: map[string][][]any{}})
		return
	}

	resp := deltaResp{Geom: map[string][][]any{}, Stats: map[string]int{}}
	sent, reused := 0, 0
	for _, f := range feats {
		if wantBand && f.band != band {
			continue
		}
		resp.Features = append(resp.Features, deltaFeature{Props: f.props, Geom: f.hash})
		if have[f.hash] {
			reused++
			continue
		}
		if _, dup := resp.Geom[f.hash]; dup {
			continue
		}
		var coords [][]any
		if json.Unmarshal(f.coords, &coords) == nil {
			resp.Geom[f.hash] = coords
			sent++
		}
	}
	resp.Stats["features"] = len(resp.Features)
	resp.Stats["geometries_sent"] = sent
	resp.Stats["geometries_reused"] = reused
	json.NewEncoder(w).Encode(resp)
}

type parsedFeature struct {
	props  json.RawMessage
	coords json.RawMessage
	hash   string
	band   int
}

type featCacheEntry struct {
	mod  int64
	size int64
	list []parsedFeature
}

var featCache = struct {
	mu sync.Mutex
	m  map[string]featCacheEntry
}{m: map[string]featCacheEntry{}}

func parseFeatures(path string) ([]parsedFeature, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	featCache.mu.Lock()
	if e, ok := featCache.m[path]; ok && e.mod == st.ModTime().UnixNano() && e.size == st.Size() {
		list := e.list
		featCache.mu.Unlock()
		return list, nil
	}
	featCache.mu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc struct {
		Features []struct {
			Props json.RawMessage `json:"properties"`
			Geom  struct {
				Coords json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, err
	}
	out := make([]parsedFeature, 0, len(fc.Features))
	for _, f := range fc.Features {
		var probe struct {
			BandMin *int `json:"band_min"`
		}
		json.Unmarshal(f.Props, &probe)
		b := -1
		if probe.BandMin != nil {
			b = *probe.BandMin
		}
		out = append(out, parsedFeature{
			props: f.Props, coords: f.Geom.Coords, hash: geomHash(f.Geom.Coords), band: b,
		})
	}
	featCache.mu.Lock()
	featCache.m[path] = featCacheEntry{mod: st.ModTime().UnixNano(), size: st.Size(), list: out}
	featCache.mu.Unlock()
	return out, nil
}
