// Package sync reconciles the feed fleet against upstream: notice which
// GTFS feeds changed, download them, and (in later phases) rebuild exactly
// the builds whose inputs changed. docs/SYNC.md is the contract.
package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// stateVersion guards the manifest format. A manifest this binary does
// not speak is an error, not a shrug — silently reinterpreting it would
// re-download (or worse, skip) the whole fleet.
const stateVersion = 1

// FeedState is one feed's bookkeeping row in sync-state.json. The content
// hash is the identity that matters: transitland occasionally republishes
// identical bytes under a new sha, and Built/Tiled/Exported compare
// against Content to say whether a stage is clean.
type FeedState struct {
	Onestop  string `json:"onestop,omitempty"`
	SHA1     string `json:"sha1,omitempty"`     // transitland feed_version sha at last download
	Content  string `json:"content,omitempty"`  // sha256 over sorted *.txt members of the zip
	Built    string `json:"built,omitempty"`    // content hash at last successful build
	Tiled    string `json:"tiled,omitempty"`    // content hash at last successful tiling
	Exported string `json:"exported,omitempty"` // content hash at last successful export
}

// State is the sync manifest. It is written after each feed completes, so
// an interrupted run resumes where it stopped.
type State struct {
	Version   int                  `json:"version"`
	LastCheck string               `json:"last_check,omitempty"`
	Feeds     map[string]FeedState `json:"feeds"`
}

// LoadState reads the manifest. A missing file is an empty state — the
// first run of a fresh checkout, not an error.
func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &State{Version: stateVersion, Feeds: map[string]FeedState{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.Version != stateVersion {
		return nil, fmt.Errorf("%s: manifest version %d, this binary speaks %d", path, st.Version, stateVersion)
	}
	if st.Feeds == nil {
		st.Feeds = map[string]FeedState{}
	}
	return &st, nil
}

// Save writes the manifest atomically: temp file in the same directory,
// then rename, so a crash mid-write leaves the previous manifest intact
// rather than a truncated one.
func (st *State) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
