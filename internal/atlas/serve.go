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
