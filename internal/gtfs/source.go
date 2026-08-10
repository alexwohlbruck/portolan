package gtfs

// Where a feed's tables come from.
//
// GTFS static is defined as a zip, and for a published feed that is what
// it is. But portolan's interactive callers do not have a published
// feed: an editor holds the route and stop tables as live state, where a
// colour change touches routes.txt and every route edit touches
// stop_times.txt. Making those callers serialise a zip to disk on every
// keystroke — in exactly the rebuild loop `serve` exists for — is a
// filesystem round trip bought for nothing.
//
// So a feed is addressed through an `opener` per table rather than a
// *zip.File, and three things can supply them: a zip, a directory of
// .txt files, and a set of in-memory CSV strings. Everything above this
// line is unchanged; the loaders still ask for "routes.txt" and get a
// reader back.

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// opener yields a fresh reader over one table. Fresh per call because
// the loaders read some tables twice and stream others concurrently.
type opener func() (io.ReadCloser, error)

// Tables is a feed held in memory: table name ("routes.txt") → its CSV
// text, header row included. This is what an editor sends instead of
// writing a zip.
type Tables map[string]string

// feedFiles resolves a feed location into its tables. path may be a zip
// or a directory. The returned closer releases the zip; it is a no-op
// for a directory.
func feedFiles(path string) (map[string]opener, func() error, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if st.IsDir() {
		f, err := dirFiles(path)
		return f, func() error { return nil }, err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, err
	}
	files := map[string]opener{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// index by BOTH the stored path and its base name: agencies ship
		// feeds zipped from a containing folder ("gtfs/routes.txt") as
		// often as from the files themselves, and a loader asking for
		// "routes.txt" should find it either way. The full path wins, so
		// a feed that genuinely holds two routes.txt keeps the one it
		// always used.
		files[f.Name] = f.Open
		if base := filepath.Base(f.Name); base != f.Name {
			if _, taken := files[base]; !taken {
				files[base] = f.Open
			}
		}
	}
	return files, zr.Close, nil
}

func dirFiles(dir string) (map[string]opener, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]opener{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		files[e.Name()] = func() (io.ReadCloser, error) { return os.Open(p) }
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("gtfs: %s is an empty directory", dir)
	}
	return files, nil
}

// tableFiles resolves in-memory tables. Names are accepted with or
// without the .txt suffix, because a caller assembling JSON reaches for
// "routes" before "routes.txt" and being strict about it would only
// produce a confusing "missing routes.txt" against a request that
// plainly contains routes.
func tableFiles(t Tables) map[string]opener {
	files := map[string]opener{}
	for name, body := range t {
		n := name
		if !strings.HasSuffix(n, ".txt") {
			n += ".txt"
		}
		b := body
		files[n] = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(b)), nil
		}
	}
	return files
}

// Valid reports whether the tables are enough to chart from, and says
// what is missing when they are not. routes.txt is the only hard
// requirement — it is what route ids mean — and stops.txt is what
// stations are built from, so a feed without it draws lines and no
// stations.
func (t Tables) Valid() error {
	files := tableFiles(t)
	if _, ok := files["routes.txt"]; !ok {
		have := make([]string, 0, len(files))
		for n := range files {
			have = append(have, n)
		}
		return fmt.Errorf("gtfs: inline tables have no routes.txt (got %s)",
			strings.Join(have, ", "))
	}
	return nil
}
