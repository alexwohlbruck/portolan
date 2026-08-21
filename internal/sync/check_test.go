package sync

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexwohlbruck/portolan/internal/registry"
)

// tlStub plays transitland: one feed record, a direct download that can
// be licence-blocked, and the operator's static URL. The client is
// sequential, so plain counters need no locking.
type tlStub struct {
	sha1      string
	zip       []byte // served by download_latest_feed_version
	noDirect  bool   // …unless licence-blocked: 404
	staticZip []byte // served at /static.zip
	staticURL string

	feedHits, dlHits, staticHits int
	apikey                       string
}

func newTL(t *testing.T, s *tlStub) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.apikey = r.URL.Query().Get("apikey")
		switch {
		case strings.HasSuffix(r.URL.Path, "/download_latest_feed_version"):
			s.dlHits++
			if s.noDirect {
				http.Error(w, "licence", http.StatusNotFound)
				return
			}
			w.Write(s.zip)
		case r.URL.Path == "/static.zip":
			s.staticHits++
			w.Write(s.staticZip)
		case strings.HasPrefix(r.URL.Path, "/feeds/"):
			s.feedHits++
			if strings.Contains(r.URL.Path, "f-bad") {
				// a registered feed transitland has no version for
				fmt.Fprint(w, `{"feeds":[{"feed_state":{},"urls":{}}]}`)
				return
			}
			fmt.Fprintf(w, `{"feeds":[{"feed_state":{"feed_version":{"sha1":%q}},"urls":{"static_current":%q}}]}`,
				s.sha1, s.staticURL)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s.staticURL = srv.URL + "/static.zip"
	return &Client{BaseURL: srv.URL, APIKey: "k", Sleep: func(time.Duration) {}}
}

func metroConfig() registry.Config {
	return registry.Config{Feeds: map[string]registry.FeedCfg{
		"metro": {GTFS: "data/gtfs/metro.zip", Onestop: "f-abc-metro"},
	}}
}

func checkOpts(t *testing.T, c *Client, dry bool) CheckOpts {
	t.Helper()
	dir := t.TempDir()
	return CheckOpts{
		Config:    metroConfig(),
		StatePath: filepath.Join(dir, "build", "sync-state.json"),
		DataDir:   filepath.Join(dir, "data"),
		Client:    c,
		DryRun:    dry,
	}
}

func TestCheckDownloadsNewFeed(t *testing.T) {
	stub := &tlStub{sha1: "s1", zip: zipBytes(t, []member{{"routes.txt", "route_id\nA\n"}})}
	c := newTL(t, stub)
	o := checkOpts(t, c, false)

	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 || res.Changed[0] != "metro" {
		t.Fatalf("changed = %v", res.Changed)
	}
	if stub.apikey != "k" {
		t.Errorf("apikey not sent: %q", stub.apikey)
	}
	if _, err := os.Stat(filepath.Join(o.DataDir, "metro.zip")); err != nil {
		t.Errorf("zip not installed: %v", err)
	}
	st, err := LoadState(o.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	fs := st.Feeds["metro"]
	if fs.SHA1 != "s1" || fs.Content == "" || fs.Onestop != "f-abc-metro" {
		t.Errorf("state = %+v", fs)
	}
	if st.LastCheck == "" {
		t.Error("last_check not recorded")
	}

	// same sha again: quiet, and no second download
	res, err = Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 0 || stub.dlHits != 1 {
		t.Errorf("rerun: changed=%v dlHits=%d", res.Changed, stub.dlHits)
	}
}

func TestCheckRepublishIdenticalContent(t *testing.T) {
	stub := &tlStub{sha1: "s1", zip: zipBytes(t, []member{{"routes.txt", "route_id\nA\n"}})}
	c := newTL(t, stub)
	o := checkOpts(t, c, false)
	if _, err := Check(o); err != nil {
		t.Fatal(err)
	}

	// transitland republishes: new sha, same tables
	stub.sha1 = "s2"
	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 0 {
		t.Errorf("identical content flagged as changed: %v", res.Changed)
	}
	if stub.dlHits != 2 {
		t.Errorf("dlHits = %d, want 2 (must download to compare)", stub.dlHits)
	}
	st, _ := LoadState(o.StatePath)
	if st.Feeds["metro"].SHA1 != "s2" {
		t.Errorf("new sha not recorded: %+v", st.Feeds["metro"])
	}

	// third run: the recorded sha keeps it quiet with no download
	if _, err := Check(o); err != nil {
		t.Fatal(err)
	}
	if stub.dlHits != 2 {
		t.Errorf("republish re-downloaded: dlHits = %d", stub.dlHits)
	}
}

func TestCheckChangedContent(t *testing.T) {
	stub := &tlStub{sha1: "s1", zip: zipBytes(t, []member{{"routes.txt", "route_id\nA\n"}})}
	c := newTL(t, stub)
	o := checkOpts(t, c, false)
	if _, err := Check(o); err != nil {
		t.Fatal(err)
	}
	before, _ := LoadState(o.StatePath)

	stub.sha1 = "s2"
	stub.zip = zipBytes(t, []member{{"routes.txt", "route_id\nB\n"}})
	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 {
		t.Fatalf("changed = %v", res.Changed)
	}
	after, _ := LoadState(o.StatePath)
	if after.Feeds["metro"].Content == before.Feeds["metro"].Content {
		t.Error("content hash did not move")
	}
}

func TestCheckFallbackToStaticCurrent(t *testing.T) {
	z := zipBytes(t, []member{{"routes.txt", "route_id\nA\n"}})
	stub := &tlStub{sha1: "s1", noDirect: true, staticZip: z}
	c := newTL(t, stub)
	o := checkOpts(t, c, false)

	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 || len(res.Errors) != 0 {
		t.Fatalf("changed=%v errors=%v", res.Changed, res.Errors)
	}
	if stub.staticHits != 1 {
		t.Errorf("staticHits = %d", stub.staticHits)
	}
	if _, err := os.Stat(filepath.Join(o.DataDir, "metro.zip")); err != nil {
		t.Errorf("zip not installed via fallback: %v", err)
	}
}

func TestCheckSkipsFeedsWithoutOnestop(t *testing.T) {
	stub := &tlStub{sha1: "s1", zip: zipBytes(t, []member{{"routes.txt", "r\n"}})}
	c := newTL(t, stub)
	o := checkOpts(t, c, false)
	o.Config = registry.Config{Feeds: map[string]registry.FeedCfg{
		"handmade": {GTFS: "data/gtfs/handmade.zip"},
	}}
	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "handmade" {
		t.Errorf("skipped = %v", res.Skipped)
	}
	if stub.feedHits != 0 {
		t.Errorf("asked upstream about a feed with no onestop id")
	}
}

func TestCheckDryRun(t *testing.T) {
	stub := &tlStub{sha1: "s1", zip: zipBytes(t, []member{{"routes.txt", "r\n"}})}
	c := newTL(t, stub)
	o := checkOpts(t, c, true)

	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 {
		t.Fatalf("changed = %v", res.Changed)
	}
	if stub.dlHits != 0 {
		t.Errorf("dry run downloaded: dlHits = %d", stub.dlHits)
	}
	if _, err := os.Stat(o.StatePath); err == nil {
		t.Error("dry run wrote state")
	}
}

func TestCheckOneFailureDoesNotAbort(t *testing.T) {
	stub := &tlStub{sha1: "s1", zip: zipBytes(t, []member{{"routes.txt", "r\n"}})}
	c := newTL(t, stub)
	o := checkOpts(t, c, false)
	// "aaa" sorts before metro and has no upstream version — its error
	// must land in Errors while metro still downloads
	o.Config.Feeds["aaa"] = registry.FeedCfg{GTFS: "x.zip", Onestop: "f-bad"}

	res, err := Check(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0], "aaa:") {
		t.Errorf("errors = %v", res.Errors)
	}
	if len(res.Changed) != 1 || res.Changed[0] != "metro" {
		t.Errorf("changed = %v — the run should have carried on", res.Changed)
	}
}

func TestClient429RetriesOnce(t *testing.T) {
	hits, slept := 0, time.Duration(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "3")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"feeds":[{"feed_state":{"feed_version":{"sha1":"s1"}},"urls":{}}]}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Sleep: func(d time.Duration) { slept = d }}
	info, err := c.Feed("f-abc")
	if err != nil {
		t.Fatal(err)
	}
	if info.SHA1 != "s1" || hits != 2 || slept != 3*time.Second {
		t.Errorf("sha=%q hits=%d slept=%v", info.SHA1, hits, slept)
	}
}

func TestClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Sleep: func(time.Duration) {}}
	if _, err := c.Feed("f-abc"); err == nil {
		t.Error("500 reported as success")
	}
	if err := c.Download("f-abc", "", filepath.Join(t.TempDir(), "x.zip")); err == nil {
		t.Error("download of a 500 reported as success")
	}
}

func TestDownloadRejectsNonZip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>please sign in</html>")
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Sleep: func(time.Duration) {}}
	dest := filepath.Join(t.TempDir(), "x.zip")
	if err := c.Download("f-abc", "", dest); err == nil {
		t.Error("HTML page installed as a feed zip")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("bad download left a file behind")
	}
}
