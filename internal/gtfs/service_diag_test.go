package gtfs

import (
	"os"
	"testing"
)

// Diagnostic: print the derived service scenarios for the local feeds.
// Skips when the GTFS zips are absent (CI).
func TestServiceScenarios(t *testing.T) {
	for _, feed := range []string{"5", "29"} {
		path := os.Getenv("HOME") + "/Documents/code/barrelman/data/gtfs/" + feed + ".zip"
		if _, err := os.Stat(path); err != nil {
			t.Skipf("feed %s GTFS not present", feed)
		}
		si, err := LoadService(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("feed %s: %d patterns with activity", feed, len(si.Activity))
		scens := BuildScenarios(si, 0.99)
		t.Logf("feed %s: %d scenarios", feed, len(scens))
		total := 0
		for _, sc := range scens {
			total += cellCount(sc.Cells)
			t.Logf("  %s  %3d patterns  cells %3d  %s",
				sc.ID, sc.Patterns, cellCount(sc.Cells), sc.Label)
		}
		if total > 7*24 {
			t.Errorf("feed %s: scenarios overlap (%d cells over %d)", feed, total, 7*24)
		}
		if len(scens) == 0 || len(scens) > 40 {
			t.Errorf("feed %s: scenario count %d out of sane range", feed, len(scens))
		}
	}
}
