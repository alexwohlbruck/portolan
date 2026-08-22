package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateMissingFileIsEmpty(t *testing.T) {
	st, err := LoadState(filepath.Join(t.TempDir(), "nope", "sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != stateVersion || len(st.Feeds) != 0 {
		t.Errorf("empty state = %+v", st)
	}
}

func TestStateRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "sync-state.json")
	st := &State{Version: stateVersion, LastCheck: "2026-08-20T04:00:00Z", Feeds: map[string]FeedState{
		"mta": {Onestop: "f-dr5r-nyct", SHA1: "aaa", Content: "bbb", Built: "bbb"},
	}}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastCheck != st.LastCheck || got.Feeds["mta"] != st.Feeds["mta"] {
		t.Errorf("roundtrip = %+v", got)
	}

	// saving over an existing manifest must leave no temp droppings —
	// the atomic rename either happened or it did not
	st.Feeds["mta"] = FeedState{SHA1: "ccc"}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "sync-state.json" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("leftover files after save: %v", names)
	}
	got, err = LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Feeds["mta"].SHA1 != "ccc" {
		t.Errorf("second save not visible: %+v", got.Feeds["mta"])
	}
}

func TestStateUnknownVersionRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	os.WriteFile(path, []byte(`{"version": 99, "feeds": {}}`), 0o644)
	_, err := LoadState(path)
	if err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Errorf("want version rejection, got %v", err)
	}
}

func TestStateGarbageRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	os.WriteFile(path, []byte(`{"version": 1, "fee`), 0o644)
	if _, err := LoadState(path); err == nil {
		t.Error("truncated manifest loaded without error")
	}
}
