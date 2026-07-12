// Package atlas is the dev server: the sketch editor, window renders, and
// probes — the permanent fixtures of the dev loop (docs/TOOLS.md).
package atlas

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed editor.html
var editorHTML []byte

// Server serves the sketch editor and the network API. Sketches are stored
// per feed under dir as network-<feed>.json — the owner's hand work: writes
// are ATOMIC (temp file + rename), and nothing here ever regenerates one.
type Server struct {
	Dir string // sketches directory
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sketch", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(editorHTML)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/sketch", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/network", s.network)
	mux.HandleFunc("/api/features", func(w http.ResponseWriter, r *http.Request) {
		// build-overlay probe: served once chart output wiring lands;
		// the editor degrades gracefully on an empty collection
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"FeatureCollection","features":[]}`)
	})
	return mux
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
