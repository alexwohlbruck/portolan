package tiles

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

func indexFixture(t *testing.T, pyramids ...string) (registry.Config, func(string) string) {
	t.Helper()
	cfg, err := registry.Parse([]byte(`{"feeds":{
	  "chicago":{"gtfs":"a.zip,b.zip","members":["chicago-cta","metra"]},
	  "chicago-cta":{"gtfs":"a.zip"},
	  "metra":{"gtfs":"b.zip"},
	  "pace":{"gtfs":"c.zip"},
	  "nowhere":{"gtfs":"d.zip"},
	  "no-zip":{"gtfs":"e.zip"}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, k := range pyramids {
		dir := filepath.Join(root, k)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tiles.json"),
			[]byte(`{"bounds":[-88,41,-87,42],"maxzoom":18}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, func(k string) string { return filepath.Join(root, k) }
}

func feedsOf(idx []IndexEntry) []string {
	out := []string{}
	for _, e := range idx {
		out = append(out, e.Feed)
	}
	return out
}

func TestIndexHidesMembersOfABuiltGroup(t *testing.T) {
	cfg, dirFor := indexFixture(t, "chicago", "chicago-cta", "metra", "pace")
	got := feedsOf(Index(cfg, dirFor))
	if want := []string{"chicago", "pace"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("index = %v, want %v", got, want)
	}
}

// The chicago group failed preflight and never got a pyramid. The index
// still hid its members, so CTA and Metra were on no map at all although
// both had pyramids of their own.
func TestIndexFallsBackToMembersWhenTheGroupHasNoPyramid(t *testing.T) {
	cfg, dirFor := indexFixture(t, "chicago-cta", "metra", "pace")
	got := feedsOf(Index(cfg, dirFor))
	if want := []string{"chicago-cta", "metra", "pace"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("index = %v, want %v", got, want)
	}
}

func TestUndrawnNamesFeedsWithGTFSOnNoMap(t *testing.T) {
	hasGTFS := func(fc registry.FeedCfg) bool { return fc.GTFS != "e.zip" }

	cfg, dirFor := indexFixture(t, "chicago", "chicago-cta", "metra", "pace")
	if got := Undrawn(cfg, Index(cfg, dirFor), hasGTFS); !reflect.DeepEqual(got, []string{"nowhere"}) {
		t.Fatalf("undrawn = %v, want [nowhere]: members are drawn by their group, no-zip has nothing to draw", got)
	}

	// a member with no pyramid of its own and a group that never built
	cfg, dirFor = indexFixture(t, "chicago-cta", "pace")
	if got := Undrawn(cfg, Index(cfg, dirFor), hasGTFS); !reflect.DeepEqual(got, []string{"metra", "nowhere"}) {
		t.Fatalf("undrawn = %v, want [metra nowhere]", got)
	}
}
