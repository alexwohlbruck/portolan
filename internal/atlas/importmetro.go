package atlas

// /api/import/metro — drop a Subway Builder .metro save on the console and
// it becomes a feed.
//
// The conversion itself does NOT live here. A .metro save is the game's
// container, and the only faithful serializer is the game's own adapter
// (exportFeed — the exact GTFS + rail + curation doc the game hands the
// engine at runtime). So this handler shells out to the game repo's
// importer script, which parses the save, runs exportFeed, writes
// data/sb/<key>/ + style/<key>.json into THIS checkout and registers the
// feed in portolan.json. config() picks the new entry up on its own
// (portolan.json is re-read on mtime change); the console then charts it
// like any other city.
//
// The script is found at ../metro-maker4/next-app-2/scripts/
// metro-to-portolan.ts relative to this checkout — the mirror image of the
// game's own "../../portolan" default — or wherever SB_METRO_IMPORTER
// points. No game checkout, no endpoint: 501 with instructions.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// importerScript resolves the game-side converter, env override first.
func (s *Server) importerScript() (string, error) {
	if p := os.Getenv("SB_METRO_IMPORTER"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("SB_METRO_IMPORTER=%s: %w", p, err)
		}
		return p, nil
	}
	cfgAbs, err := filepath.Abs(s.cfgPath)
	if err != nil {
		return "", err
	}
	p := filepath.Join(filepath.Dir(cfgAbs), "..", "metro-maker4", "next-app-2",
		"scripts", "metro-to-portolan.ts")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("no Subway Builder checkout beside this repo (%s) — set SB_METRO_IMPORTER to metro-to-portolan.ts", p)
	}
	return p, nil
}

// npx is not necessarily on the atlas process's PATH — `make atlas` from a
// GUI-launched terminal misses the homebrew bin dirs.
func findNpx() (string, error) {
	if p, err := exec.LookPath("npx"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/npx", "/usr/local/bin/npx"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("npx not found — the importer runs via `npx tsx` from the game repo")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func (s *Server) importMetro(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", 405)
		return
	}
	script, err := s.importerScript()
	if err != nil {
		http.Error(w, err.Error(), 501)
		return
	}
	npx, err := findNpx()
	if err != nil {
		http.Error(w, err.Error(), 501)
		return
	}

	// Keep the uploaded basename: when a save predates city codes in the
	// header, the script derives the feed key from the filename.
	name := unsafeName.ReplaceAllString(filepath.Base(r.URL.Query().Get("name")), "-")
	if name == "" || name == "." || name == "-" {
		name = "upload.metro"
	}
	tmpDir, err := os.MkdirTemp("", "metro-import-*")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer os.RemoveAll(tmpDir)
	tmp := filepath.Join(tmpDir, name)
	f, err := os.Create(tmp)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, 512<<20))
	f.Close()
	if err != nil {
		http.Error(w, "upload failed: "+err.Error(), 400)
		return
	}

	// One import at a time: the script read-modify-writes portolan.json.
	s.importMu.Lock()
	defer s.importMu.Unlock()

	cfgAbs, _ := filepath.Abs(s.cfgPath)
	repo := filepath.Dir(cfgAbs)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, npx, "tsx", script, tmp, "--repo", repo, "--json")
	cmd.Dir = filepath.Dir(filepath.Dir(script)) // the game app dir — node_modules lives there
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// the last stderr line is the script's own message; the lines
		// above it are npm/tsx noise nobody needs in a toast
		if lines := strings.Split(msg, "\n"); len(lines) > 1 {
			msg = strings.TrimSpace(lines[len(lines)-1])
		}
		http.Error(w, "import failed: "+msg, 422)
		return
	}

	// stdout's last non-empty line is the result object
	var resLine string
	for _, l := range strings.Split(stdout.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			resLine = l
		}
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(resLine), &res); err != nil || res["key"] == nil {
		http.Error(w, "importer returned no result — ran an old script without --json?", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// deleteImport removes an uploaded save: the portolan.json entry and every
// file the importer wrote for it. The exact inverse of the import above,
// and deliberately no more than that — a build output or a sketch drawn
// over the save is the user's work, so those go too only because the feed
// they belong to is going.
//
// GUARDED to entries the importer made (FeedCfg.Imported). The console
// only offers delete on those, but the endpoint cannot trust that: this is
// the one route that removes a config entry and files from the checkout,
// and a mistyped key must not be able to unregister the MTA. The marker is
// set by the importer alone, so a curated feed can never match.
func (s *Server) deleteImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE", 405)
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("feed"))
	if key == "" {
		http.Error(w, "missing ?feed=", 400)
		return
	}
	fc, ok := s.config().Feeds[key]
	if !ok {
		http.Error(w, "no such feed: "+key, 404)
		return
	}
	if !fc.Imported {
		http.Error(w, key+" is a curated feed, not an uploaded save — refusing to delete", 403)
		return
	}

	// Serialize with import: both read-modify-write portolan.json, and the
	// file sweep below must not race a re-import of the same key.
	s.importMu.Lock()
	defer s.importMu.Unlock()

	if err := s.editConfig(func(doc map[string]any) error {
		feeds, _ := doc["feeds"].(map[string]any)
		if feeds == nil {
			return fmt.Errorf("config has no feeds map")
		}
		delete(feeds, key)
		return nil
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Paths come from the ENTRY, not from a rebuilt guess at what the
	// importer would have written: a hand-edited entry pointing somewhere
	// else must take its own files, and only its own.
	cfgAbs, _ := filepath.Abs(s.cfgPath)
	repo := filepath.Dir(cfgAbs)
	inRepo := func(p string) (string, bool) {
		if p == "" {
			return "", false
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(repo, p)
		}
		abs = filepath.Clean(abs)
		// never escape the checkout, whatever the entry says
		rel, err := filepath.Rel(repo, abs)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return "", false
		}
		return abs, true
	}
	var removed []string
	drop := func(p string) {
		abs, ok := inRepo(p)
		if !ok {
			return
		}
		if err := os.RemoveAll(abs); err == nil {
			if rel, err := filepath.Rel(repo, abs); err == nil {
				removed = append(removed, rel)
			}
		}
	}
	// the importer writes data/sb/<key>/{gtfs,rail.geojson}; drop the feed
	// DIRECTORY rather than the two paths, so nothing is orphaned
	if abs, ok := inRepo(fc.GTFS); ok {
		drop(filepath.Dir(abs))
	}
	drop(fc.Rail)
	drop(filepath.Join("style", key+".json"))
	drop(fc.Network)
	// build outputs: <out> plus the sidecars chart writes beside it
	if abs, ok := inRepo(fc.Out); ok {
		matches, _ := filepath.Glob(abs + "*")
		for _, m := range matches {
			drop(m)
		}
	}
	drop(filepath.Join("build", "tiles", key))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"key": key, "removed": removed})
}
