package stages

import (
	"encoding/json"
	"os"
)

func readFileBytes(path string) ([]byte, error) { return os.ReadFile(path) }
func jsonUnmarshal(b []byte, v any) error       { return json.Unmarshal(b, v) }

// localGTFS locates a feed zip for the diagnostic tests that need a real
// city. These feeds are large and licensed per agency, so they are not in
// the repo — every caller must os.Stat the result and t.Skip when it is
// absent, or the test fails on any machine but one. (That is exactly how
// CI broke the first time it ran: one test loaded this path directly.)
//
// PORTOLAN_TEST_GTFS overrides the directory, so a contributor whose
// feeds live somewhere else can still run them.
func localGTFS(feed string) string {
	dir := os.Getenv("PORTOLAN_TEST_GTFS")
	if dir == "" {
		dir = os.Getenv("HOME") + "/Documents/code/barrelman/data/gtfs"
	}
	return dir + "/" + feed + ".zip"
}
