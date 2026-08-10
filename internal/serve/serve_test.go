package serve

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexwohlbruck/portolan/internal/pipeline"
)

// A tiny authored network, inline — which is the case the server exists
// for: a client editing a graph rather than storing one.
func inlineGraph() map[string]any {
	feat := func(props map[string]any, geom map[string]any) map[string]any {
		return map[string]any{"type": "Feature", "properties": props, "geometry": geom}
	}
	pt := func(lon, lat float64) map[string]any {
		return map[string]any{"type": "Point", "coordinates": []float64{lon, lat}}
	}
	ls := func(cs ...[]float64) map[string]any {
		return map[string]any{"type": "LineString", "coordinates": cs}
	}
	return map[string]any{
		"type": "FeatureCollection",
		"features": []map[string]any{
			feat(map[string]any{"node": "a"}, pt(-74.000, 40.700)),
			feat(map[string]any{"node": "b"}, pt(-73.990, 40.700)),
			feat(map[string]any{"node": "c"}, pt(-73.980, 40.700)),
			feat(map[string]any{"edge": "e0", "from": "a", "to": "b", "routes": "R1,R2"},
				ls([]float64{-74.000, 40.700}, []float64{-73.990, 40.700})),
			feat(map[string]any{"edge": "e1", "from": "b", "to": "c", "routes": "R1"},
				ls([]float64{-73.990, 40.700}, []float64{-73.980, 40.700})),
		},
	}
}

func tinyFeed(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "feed.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	put := func(name, body string) {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	put("agency.txt", "agency_id,agency_name\nA,Authored\n")
	put("routes.txt", "route_id,agency_id,route_short_name,route_type,route_color\n"+
		"R1,A,1,1,EE352E\nR2,A,2,1,0039A6\n")
	put("stops.txt", "stop_id,stop_name,stop_lat,stop_lon\n"+
		"s1,Alpha,40.700,-74.000\ns2,Beta,40.700,-73.990\ns3,Gamma,40.700,-73.980\n")
	zw.Close()
	return path
}

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	feed := tinyFeed(t, dir)
	// an empty style dir: LoadDir must cope, since a caller charting an
	// authored network has no curation to point at
	s := New(filepath.Join(dir, "style"))
	srv := httptest.NewServer(s.mux())
	t.Cleanup(srv.Close)
	return srv, feed
}

func postChart(t *testing.T, srv *httptest.Server, body map[string]any) (string, int) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/chart", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		return buf.String(), resp.StatusCode
	}
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID, resp.StatusCode
}

func TestChartJobRunsAndServesArtifacts(t *testing.T) {
	srv, feed := newTestServer(t)
	id, code := postChart(t, srv, map[string]any{
		"gtfs": feed, "corridors_inline": inlineGraph(),
	})
	if code != http.StatusAccepted {
		t.Fatalf("POST /chart: %d %s", code, id)
	}

	// the progress stream is the supervising process's whole view of the
	// build, so it must carry stages, reach 100, and terminate
	resp, err := http.Get(srv.URL + "/chart/" + id + "/progress")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("progress Content-Type %q, want text/event-stream", ct)
	}
	var stages []string
	lastPct := -1
	sawDone := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			t.Fatalf("bad SSE payload %q: %v", line, err)
		}
		if e.Error != "" {
			t.Fatalf("build failed: %s", e.Error)
		}
		if e.Stage != "" {
			stages = append(stages, e.Stage)
			if e.Pct < lastPct {
				t.Errorf("progress went backwards: %s at %d after %d", e.Stage, e.Pct, lastPct)
			}
			lastPct = e.Pct
		}
		if e.Done {
			sawDone = true
			break
		}
	}
	if !sawDone {
		t.Fatal("progress stream ended without a done event")
	}
	if lastPct != 100 {
		t.Errorf("final pct %d, want 100", lastPct)
	}
	for _, want := range []string{"order", "fair", "emit"} {
		if !contains(stages, want) {
			t.Errorf("stage %q never reported; saw %v", want, stages)
		}
	}

	// artifacts
	for _, artifact := range []string{"", "stations", "style", "trackcenter", "nodes"} {
		u := srv.URL + "/chart/" + id + "/build"
		if artifact != "" {
			u += "?artifact=" + artifact
		}
		r, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Errorf("artifact %q: %d %s", artifact, r.StatusCode, body.String())
			continue
		}
		if body.Len() == 0 {
			t.Errorf("artifact %q is empty", artifact)
		}
	}
	// route ids must survive the whole round trip verbatim
	r, err := http.Get(srv.URL + "/chart/" + id + "/build")
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	body.ReadFrom(r.Body)
	r.Body.Close()
	if !strings.Contains(body.String(), `"R1`) {
		t.Error("route id R1 is absent from the emitted build")
	}
}

func TestLateSubscriberSeesTheWholeStream(t *testing.T) {
	srv, feed := newTestServer(t)
	id, _ := postChart(t, srv, map[string]any{
		"gtfs": feed, "corridors_inline": inlineGraph(),
	})
	// let it finish before subscribing — a client that connects late
	// must still learn what happened, not hang
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(srv.URL + "/chart/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st.Error != "" {
			t.Fatalf("build failed: %s", st.Error)
		}
		if st.Done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	done := make(chan bool, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/chart/" + id + "/progress")
		if err != nil {
			done <- false
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"done":true`) {
				done <- true
				return
			}
		}
		done <- false
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Error("late subscriber never saw the done event")
		}
	case <-time.After(5 * time.Second):
		t.Error("late subscriber hung — the replay must terminate a finished job's stream")
	}
}

func TestCancelBeforeTheBuildStarts(t *testing.T) {
	srv, feed := newTestServer(t)
	id, _ := postChart(t, srv, map[string]any{
		"gtfs": feed, "corridors_inline": inlineGraph(),
	})
	req, _ := http.NewRequest("POST", srv.URL+"/chart/"+id+"/cancel", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel returned %d", r.StatusCode)
	}
	// whichever way the race falls, the job must REACH a terminal state
	// — a cancelled build that never finishes is the pile-up this exists
	// to prevent
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(srv.URL + "/chart/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Done bool `json:"done"`
		}
		json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st.Done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cancelled job never reached a terminal state")
}

func TestBadRequestsAreRejected(t *testing.T) {
	srv, feed := newTestServer(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"no geometry", map[string]any{"gtfs": feed}},
		{"both geometries", map[string]any{"gtfs": feed, "rail": "r.geojson",
			"corridors_inline": inlineGraph()}},
		{"no gtfs", map[string]any{"corridors_inline": inlineGraph()}},
		{"bad band", map[string]any{"gtfs": feed, "corridors_inline": inlineGraph(), "band": "12"}},
	}
	for _, c := range cases {
		msg, code := postChart(t, srv, c.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: got %d (%s), want 400", c.name, code, strings.TrimSpace(msg))
		}
	}
	r, err := http.Get(srv.URL + "/chart/deadbeef/progress")
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown job: got %d, want 404", r.StatusCode)
	}
	r.Body.Close()
}

func TestBinaryFormatIsServedAsBytes(t *testing.T) {
	srv, feed := newTestServer(t)
	id, code := postChart(t, srv, map[string]any{
		"gtfs": feed, "corridors_inline": inlineGraph(),
		"format": "bin", "band": "15",
	})
	if code != http.StatusAccepted {
		t.Fatalf("POST /chart: %d %s", code, id)
	}
	waitDone(t, srv, id)
	r, err := http.Get(srv.URL + "/chart/" + id + "/build")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type %q, want application/octet-stream", ct)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(r.Body)
	if buf.Len() < 32 || string(buf.Bytes()[:4]) != "PLNB" {
		t.Errorf("body is not a PLNB file (%d bytes, prefix %q)",
			buf.Len(), buf.Bytes()[:min(4, buf.Len())])
	}
}

func waitDone(t *testing.T, srv *httptest.Server, id string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(srv.URL + "/chart/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var st struct {
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st.Error != "" {
			t.Fatalf("build failed: %s", st.Error)
		}
		if st.Done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("build never finished")
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// --- /version and --token -------------------------------------------

func TestVersionReportsTheContract(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "style"))
	s.Version = "1.2.3"
	srv := httptest.NewServer(s.mux())
	defer srv.Close()

	r, err := http.Get(srv.URL + "/version")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var v struct {
		Version string   `json:"version"`
		PLNB    int      `json:"plnb"`
		Formats []string `json:"formats"`
		Bands   []int    `json:"bands"`
		Auth    bool     `json:"auth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	// the bare semver, no "v" and no revision — a caller compares it
	// against the contract its renderer speaks
	if v.Version != "1.2.3" {
		t.Errorf("version %q, want the bare stamp 1.2.3", v.Version)
	}
	if v.PLNB != int(pipeline.BinaryVersion) {
		t.Errorf("plnb %d, want %d — this is the number a renderer must agree with",
			v.PLNB, pipeline.BinaryVersion)
	}
	if len(v.Formats) == 0 || len(v.Bands) == 0 {
		t.Errorf("formats/bands empty: %+v", v)
	}
	if v.Auth {
		t.Error("auth reported true on a server with no token")
	}
}

func TestUnstampedBuildSaysDevel(t *testing.T) {
	s := New(t.TempDir())
	srv := httptest.NewServer(s.mux())
	defer srv.Close()
	r, err := http.Get(srv.URL + "/version")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	json.NewDecoder(r.Body).Decode(&v)
	// never optimistic: an unstamped binary must not claim a release
	if v.Version != "devel" {
		t.Errorf("version %q, want devel", v.Version)
	}
}

func TestTokenGuardsTheFileReadingEndpoints(t *testing.T) {
	dir := t.TempDir()
	feed := tinyFeed(t, dir)
	s := New(filepath.Join(dir, "style"))
	s.Token = "sekrit"
	srv := httptest.NewServer(s.mux())
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"gtfs": feed, "corridors_inline": inlineGraph(),
	})
	post := func(auth string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/chart", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	// /chart names files to read, so it must be closed without the token
	if got := post(""); got != http.StatusUnauthorized {
		t.Errorf("no header: got %d, want 401", got)
	}
	if got := post("Bearer wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", got)
	}
	if got := post("Bearer sekrit"); got != http.StatusAccepted {
		t.Errorf("right token: got %d, want 202", got)
	}

	// a supervisor must be able to tell "not up yet" from "wrong token",
	// so these two stay open
	for _, p := range []string{"/healthz", "/version"} {
		r, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("%s returned %d with a token set; it must stay open", p, r.StatusCode)
		}
	}
	// and /version should advertise that auth is on
	r, err := http.Get(srv.URL + "/version")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Auth bool `json:"auth"`
	}
	json.NewDecoder(r.Body).Decode(&v)
	r.Body.Close()
	if !v.Auth {
		t.Error("auth not advertised on a token-protected server")
	}
}

// TestFullyInlineChart is the interactive loop the game runs: the
// corridor graph AND the feed tables both live in the request body, so
// a rebuild touches no file the caller had to write.
func TestFullyInlineChart(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "style"))
	srv := httptest.NewServer(s.mux())
	defer srv.Close()

	id, code := postChart(t, srv, map[string]any{
		"corridors_inline": inlineGraph(),
		"gtfs_inline": map[string]string{
			"agency.txt": "agency_id,agency_name\nA,Authored\n",
			"routes.txt": "route_id,agency_id,route_short_name,route_type,route_color,route_text_color\n" +
				"R1,A,1,1,EE352E,FFFFFF\nR2,A,2,1,0039A6,FFFFFF\n",
			"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\n" +
				"s1,Alpha,40.700,-74.000\ns2,Beta,40.700,-73.990\ns3,Gamma,40.700,-73.980\n",
			"trips.txt": "route_id,service_id,trip_id\nR1,wk,t1\nR2,wk,t2\n",
			"stop_times.txt": "trip_id,stop_sequence,stop_id\n" +
				"t1,1,s1\nt1,2,s2\nt1,3,s3\nt2,1,s1\nt2,2,s2\n",
		},
		"format": "bin", "band": "15",
	})
	if code != http.StatusAccepted {
		t.Fatalf("POST /chart: %d %s", code, id)
	}
	waitDone(t, srv, id)

	r, err := http.Get(srv.URL + "/chart/" + id + "/build")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(r.Body)
	if buf.Len() < 32 || string(buf.Bytes()[:4]) != "PLNB" {
		t.Fatalf("expected a PLNB payload, got %d bytes", buf.Len())
	}
	// stations must still be built — they come from stops.txt, which
	// arrived inline like everything else
	sr, err := http.Get(srv.URL + "/chart/" + id + "/build?artifact=stations")
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Body.Close()
	sb := new(bytes.Buffer)
	sb.ReadFrom(sr.Body)
	if !strings.Contains(sb.String(), "Alpha") {
		t.Error("stations were not built from the inline stops.txt")
	}
}

func TestInlineFeedWithoutRoutesIsRejectedBeforeAnyWork(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "style"))
	srv := httptest.NewServer(s.mux())
	defer srv.Close()
	msg, code := postChart(t, srv, map[string]any{
		"corridors_inline": inlineGraph(),
		"gtfs_inline":      map[string]string{"stops.txt": "stop_id\ns1\n"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", code)
	}
	if !strings.Contains(msg, "routes.txt") {
		t.Errorf("the error should name what is missing, got: %s", strings.TrimSpace(msg))
	}
}
