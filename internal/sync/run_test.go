package sync

// Executor tests over REAL builds: three miniature feeds the actual
// pipeline charts in milliseconds — m1 and m2 share 2.6 km of corridor
// (so a group derives once their builds exist), m3 draws alone far
// away. The headline test is the oracle from docs/SYNC.md: after a
// single-feed change, `sync patch` must leave a tiles tree byte-
// identical to a fresh `sync global` over the same inputs, while
// rebuilding strictly less.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	gosync "sync"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/registry"
	"github.com/alexwohlbruck/portolan/internal/tiles"
)

// ------------------------------------------------------------ binary

var testBin struct {
	once gosync.Once
	path string
	err  error
}

// portolanBin builds the real binary once per test run — the executor
// charts through child processes, exactly as production does. Resolved
// from this source file's location so t.Chdir cannot break it.
func portolanBin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	testBin.once.Do(func() {
		dir, err := os.MkdirTemp("", "portolan-test-bin")
		if err != nil {
			testBin.err = err
			return
		}
		_, thisFile, _, _ := runtime.Caller(0)
		root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
		bin := filepath.Join(dir, "portolan")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/portolan")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			testBin.err = fmt.Errorf("go build: %v\n%s", err, out)
			return
		}
		testBin.path = bin
	})
	if testBin.err != nil {
		t.Fatal(testBin.err)
	}
	return testBin.path
}

// ------------------------------------------------------ micro fixtures

type microStop struct {
	id, name string
	lon, lat float64
}

// writeMicroGTFS writes a complete, chartable feed: one rail route,
// three trips over one shape, timed stops.
func writeMicroGTFS(t *testing.T, path, route string, stops []microStop, shape [][2]float64) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("agency.txt", "agency_id,agency_name,agency_url,agency_timezone\na1,Micro,https://example.com,America/New_York\n")
	add("routes.txt", fmt.Sprintf("route_id,agency_id,route_short_name,route_long_name,route_type\n%s,a1,%s,%s Line,2\n", route, route, route))
	add("calendar.txt", "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\nwk,1,1,1,1,1,0,0,20250101,20261231\n")
	var sb strings.Builder
	sb.WriteString("stop_id,stop_name,stop_lat,stop_lon\n")
	for _, s := range stops {
		fmt.Fprintf(&sb, "%s,%s,%s,%s\n", s.id, s.name, ff(s.lat), ff(s.lon))
	}
	add("stops.txt", sb.String())
	var shp strings.Builder
	shp.WriteString("shape_id,shape_pt_lat,shape_pt_lon,shape_pt_sequence\n")
	for i, p := range shape {
		fmt.Fprintf(&shp, "shp,%s,%s,%d\n", ff(p[1]), ff(p[0]), i)
	}
	add("shapes.txt", shp.String())
	trips := "route_id,service_id,trip_id,shape_id\n"
	st := "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n"
	for tr := 0; tr < 3; tr++ {
		tid := fmt.Sprintf("t%d", tr)
		trips += fmt.Sprintf("%s,wk,%s,shp\n", route, tid)
		base := 6*3600 + tr*1800
		for i, s := range stops {
			sec := base + i*300
			hms := fmt.Sprintf("%02d:%02d:%02d", sec/3600, (sec/60)%60, sec%60)
			st += fmt.Sprintf("%s,%s,%s,%s,%d\n", tid, hms, hms, s.id, i+1)
		}
	}
	add("trips.txt", trips)
	add("stop_times.txt", st)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRail(t *testing.T, path string, ways map[string][][2]float64) {
	t.Helper()
	type feat struct {
		Type  string         `json:"type"`
		ID    string         `json:"id"`
		Props map[string]any `json:"properties"`
		Geom  map[string]any `json:"geometry"`
	}
	var feats []feat
	ids := make([]string, 0, len(ways))
	for id := range ways {
		ids = append(ids, id)
	}
	// deterministic file bytes
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	for _, id := range ids {
		feats = append(feats, feat{Type: "Feature", ID: id,
			Props: map[string]any{"railway": "rail"},
			Geom:  map[string]any{"type": "LineString", "coordinates": ways[id]}})
	}
	raw, err := json.Marshal(map[string]any{"type": "FeatureCollection", "features": feats})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// curveH/curveV: gently wiggling lines — a perfectly straight corridor
// simplifies to two points and the build lands under the planner's
// 3000-byte "drawn" floor. The sine has period 0.01 deg, so it is zero
// at every 0.005 grid point: stops sit exactly on the curve and the
// branch joins the corridor at a shared node.
func curveH(x0, x1, y0, amp float64) [][2]float64 {
	var pts [][2]float64
	for x := x0; x < x1+0.00025; x += 0.0005 {
		pts = append(pts, [2]float64{x, y0 + amp*mathSin((x-x0)/0.01)})
	}
	return pts
}

func curveV(x0, y0, y1, amp float64) [][2]float64 {
	var pts [][2]float64
	for y := y0; y < y1+0.00025; y += 0.0005 {
		pts = append(pts, [2]float64{x0 + amp*mathSin((y-y0)/0.01), y})
	}
	return pts
}

func mathSin(periods float64) float64 {
	return math.Sin(2 * math.Pi * periods)
}

func corridorAPts() [][2]float64 { return curveH(-100.00, -99.95, 40.00, 0.002) }
func branchBPts() [][2]float64   { return curveV(-99.97, 40.00, 40.03, 0.002) }
func corridorCPts() [][2]float64 { return curveH(-100.50, -100.45, 40.30, 0.002) }

// m2Shape: the corridor shared with m1 up to the junction, then the
// branch north as far as endLat. Truncating endLat is the canonical
// "feed changed upstream" edit — the service really shortens, so the
// drawn ink (which follows the MATCHED RAIL, not the shape polyline)
// actually moves.
func m2Shape(endLat float64) [][2]float64 {
	var shape [][2]float64
	for _, p := range corridorAPts() {
		if p[0] <= -99.97+1e-9 {
			shape = append(shape, p)
		}
	}
	for _, p := range branchBPts()[1:] {
		if p[1] <= endLat+1e-9 {
			shape = append(shape, p)
		}
	}
	return shape
}

// writeM2 writes m2's zip with the branch running to endLat, the
// terminal stop riding along.
func writeM2(t *testing.T, path string, endLat float64) {
	t.Helper()
	writeMicroGTFS(t, path, "r2", []microStop{
		{"s1", "Alpha", -100.00, 40.00}, {"s4", "Delta", -99.97, 40.00},
		{"s5", "Epsilon", -99.97, endLat},
	}, m2Shape(endLat))
}

// writeMicroWorkspace lays down the whole world: three zips, three rail
// extracts, portolan.json. m1 carries an onestop id so the gtfs_ids
// plumbing is exercised end to end.
func writeMicroWorkspace(t *testing.T, dir string) {
	t.Helper()
	for _, sub := range []string{"gtfs", "rail", "build"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	corridorA := corridorAPts()
	branch := branchBPts()
	corridorC := corridorCPts()

	writeMicroGTFS(t, filepath.Join(dir, "gtfs", "m1.zip"), "r1", []microStop{
		{"s1", "Alpha", -100.00, 40.00}, {"s2", "Beta", -99.975, 40.00}, {"s3", "Gamma", -99.95, 40.00},
	}, corridorA)
	writeM2(t, filepath.Join(dir, "gtfs", "m2.zip"), 40.03)
	writeMicroGTFS(t, filepath.Join(dir, "gtfs", "m3.zip"), "r3", []microStop{
		{"s6", "Zeta", -100.50, 40.30}, {"s7", "Eta", -100.475, 40.30}, {"s8", "Theta", -100.45, 40.30},
	}, corridorC)

	writeRail(t, filepath.Join(dir, "rail", "m1.geojson"), map[string][][2]float64{"way/1": corridorA})
	writeRail(t, filepath.Join(dir, "rail", "m2.geojson"), map[string][][2]float64{"way/1": corridorA, "way/2": branch})
	writeRail(t, filepath.Join(dir, "rail", "m3.geojson"), map[string][][2]float64{"way/3": corridorC})

	doc := NewObj()
	doc.Set("sketches", "sketches")
	fo := NewObj()
	entry := func(key, name string, bbox []float64, onestop string) {
		e := NewObj()
		e.Set("name", name)
		e.Set("gtfs", "gtfs/"+key+".zip")
		e.Set("rail", "rail/"+key+".geojson")
		e.Set("out", "build/"+key+".geojson")
		e.Set("bbox", []any{bbox[0], bbox[1], bbox[2], bbox[3]})
		if onestop != "" {
			e.Set("onestop", onestop)
		}
		fo.Set(key, e)
	}
	entry("m1", "Metro One", []float64{-100.01, 39.99, -99.94, 40.01}, "f-test~m1")
	entry("m2", "Metro Two", []float64{-100.01, 39.99, -99.96, 40.04}, "")
	entry("m3", "Metro Three", []float64{-100.51, 40.29, -100.44, 40.31}, "")
	doc.Set("feeds", fo)
	if err := os.WriteFile(filepath.Join(dir, "portolan.json"), MarshalDoc(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ------------------------------------------------------------ helpers

func planAndRun(t *testing.T, changed []string, global bool, bin string) (*Plan, *RunResult) {
	t.Helper()
	raw, err := os.ReadFile("portolan.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := registry.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	st, err := LoadState("build/sync-state.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(PlanOpts{Config: cfg, Doc: doc, State: st,
		Changed: changed, BuildDir: "build", Global: global, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(plan, RunOpts{
		ConfigPath: "portolan.json", StatePath: "build/sync-state.json",
		BuildDir: "build", TilesDir: "build/tiles", ExportDir: "build/export",
		StyleDir: "style", Jobs: 2, Portolan: bin, Log: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan, res
}

// hashTree maps every file under root (relative path, /-separated) to
// its content hash.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		h := sha256.Sum256(raw)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(h[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		q := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(q, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(q)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func diffTrees(t *testing.T, a, b map[string]string, what string) {
	t.Helper()
	for k, ha := range a {
		hb, ok := b[k]
		if !ok {
			t.Errorf("%s: %s only in patch tree", what, k)
		} else if ha != hb {
			t.Errorf("%s: %s differs", what, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("%s: %s only in fresh-global tree", what, k)
		}
	}
}

// -------------------------------------------------------------- tests

// TestPatchEqualsGlobal is the oracle (docs/SYNC.md): after one feed's
// zip changes, patch must leave tiles/export trees and a registry byte-
// identical to a fresh global run over the same inputs — and rebuild
// strictly fewer builds.
func TestPatchEqualsGlobal(t *testing.T) {
	bin := portolanBin(t)
	ws := t.TempDir()
	writeMicroWorkspace(t, ws)
	t.Chdir(ws)

	// run 1: nothing is drawn yet, so no groups derive — three
	// standalone builds
	_, r1 := planAndRun(t, nil, true, bin)
	if len(r1.Errors) != 0 {
		t.Fatalf("run1 errors: %v", r1.Errors)
	}
	if !reflect.DeepEqual(r1.Rebuilt, []string{"m1", "m2", "m3"}) {
		t.Fatalf("run1 rebuilt = %v", r1.Rebuilt)
	}
	// gtfs_ids plumbing: m1's onestop id must reach its station labels
	stations, err := os.ReadFile("build/m1.geojson.stations.geojson")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stations, []byte("f-test~m1")) {
		t.Error("m1 stations carry no gtfs_ids from the registry onestop")
	}

	// run 2: builds exist, m1+m2 share 2.6 km — the group derives,
	// builds, verifies, tiles; the members clean-skip
	p2, r2 := planAndRun(t, nil, true, bin)
	if len(p2.GroupsCreated) != 1 {
		t.Fatalf("run2 groups created = %v", p2.GroupsCreated)
	}
	group := p2.GroupsCreated[0]
	if len(r2.Errors) != 0 {
		t.Fatalf("run2 errors: %v", r2.Errors)
	}
	if !reflect.DeepEqual(r2.Rebuilt, []string{group}) {
		t.Fatalf("run2 rebuilt = %v (members must clean-skip)", r2.Rebuilt)
	}
	if !r2.GroupsRewritten {
		t.Fatal("run2 must rewrite the registry")
	}
	// the world index: the same helper the atlas serves — group + m3,
	// members skipped; and it must equal what the file says
	finalCfg, err := registry.Load("portolan.json")
	if err != nil {
		t.Fatal(err)
	}
	idx := tiles.Index(finalCfg, func(k string) string { return filepath.Join("build/tiles", k) })
	var feeds []string
	for _, e := range idx {
		feeds = append(feeds, e.Feed)
	}
	wantIdx := []string{group, "m3"}
	sort.Strings(wantIdx)
	if !reflect.DeepEqual(feeds, wantIdx) {
		t.Fatalf("index feeds = %v, want %v", feeds, wantIdx)
	}
	want, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("build/tiles/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), want) {
		t.Fatalf("static index.json diverges from the atlas helper:\n%s\n----\n%s", got, want)
	}
	// the pyramid carries the resolved style manifest
	sty, err := os.ReadFile(filepath.Join("build/tiles", group, "style.json"))
	if err != nil {
		t.Fatal(err)
	}
	built, err := os.ReadFile("build/" + group + ".geojson.style.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sty, built) {
		t.Fatal("pyramid style.json is not the build's resolved manifest")
	}

	// run 2b: a settled world reruns to zero work — the resumability
	// guarantee (kill anywhere, rerun, nothing rebuilds twice)
	_, r2b := planAndRun(t, nil, true, bin)
	if len(r2b.Rebuilt) != 0 || len(r2b.Errors) != 0 {
		t.Fatalf("settled rerun rebuilt %v (errors %v)", r2b.Rebuilt, r2b.Errors)
	}

	// the change: m2's branch truncates to 40.02 — a new feed version
	// whose drawn ink genuinely moves
	writeM2(t, "gtfs/m2.zip", 40.02)

	// fresh copy of the post-change workspace for the oracle run
	ws2 := t.TempDir()
	copyTree(t, ws, ws2)
	if err := os.Remove(filepath.Join(ws2, "build", "sync-state.json")); err != nil {
		t.Fatal(err)
	}

	// patch on the live workspace
	_, rp := planAndRun(t, []string{"m2"}, false, bin)
	if len(rp.Errors) != 0 {
		t.Fatalf("patch errors: %v", rp.Errors)
	}
	patchTiles := hashTree(t, "build/tiles")
	patchExport := hashTree(t, "build/export")
	patchReg, err := os.ReadFile("portolan.json")
	if err != nil {
		t.Fatal(err)
	}

	// fresh global on the copy
	t.Chdir(ws2)
	_, rg := planAndRun(t, nil, true, bin)
	if len(rg.Errors) != 0 {
		t.Fatalf("fresh global errors: %v", rg.Errors)
	}
	globalTiles := hashTree(t, "build/tiles")
	globalExport := hashTree(t, "build/export")
	globalReg, err := os.ReadFile("portolan.json")
	if err != nil {
		t.Fatal(err)
	}

	diffTrees(t, patchTiles, globalTiles, "tiles")
	diffTrees(t, patchExport, globalExport, "export")
	if !bytes.Equal(patchReg, globalReg) {
		t.Errorf("registries diverge:\n%s\n----\n%s", patchReg, globalReg)
	}
	if len(rp.Rebuilt) >= len(rg.Rebuilt) {
		t.Errorf("patch rebuilt %v — must be strictly fewer than global's %v", rp.Rebuilt, rg.Rebuilt)
	}
	// RESULT composition: the run reports what it did
	if !reflect.DeepEqual(rp.Changed, []string{"m2"}) {
		t.Errorf("patch changed = %v", rp.Changed)
	}
	if rp.Tiles.Written == 0 {
		t.Error("patch wrote no tiles")
	}
	wantExp := []string{"m1.zip", "m2.zip", "m3.zip"}
	if !reflect.DeepEqual(rp.Exported, wantExp) {
		t.Errorf("exported = %v, want %v", rp.Exported, wantExp)
	}
}

// TestRunResumesIncrementally: state stamps make a rerun skip clean
// feeds — corrupting one feed's stamp rebuilds exactly that feed.
func TestRunResumesIncrementally(t *testing.T) {
	bin := portolanBin(t)
	ws := t.TempDir()
	writeMicroWorkspace(t, ws)
	t.Chdir(ws)

	_, r1 := planAndRun(t, nil, true, bin)
	if len(r1.Errors) != 0 {
		t.Fatalf("run1 errors: %v", r1.Errors)
	}
	// simulate a run killed after m1 and m2: erase m3's stamps
	st, err := LoadState("build/sync-state.json")
	if err != nil {
		t.Fatal(err)
	}
	row := st.Feeds["m3"]
	row.Built, row.Tiled, row.Exported = "", "", ""
	st.Feeds["m3"] = row
	if err := st.Save("build/sync-state.json"); err != nil {
		t.Fatal(err)
	}
	_, r2 := planAndRun(t, nil, true, bin)
	rebuilt := []string{}
	for _, k := range r2.Rebuilt {
		if k == "m1" || k == "m2" || k == "m3" {
			rebuilt = append(rebuilt, k)
		}
	}
	if !reflect.DeepEqual(rebuilt, []string{"m3"}) {
		t.Fatalf("rerun rebuilt %v, want just m3", r2.Rebuilt)
	}
}

// TestRunIsolatesErrors: one feed's broken zip lands in Errors while
// every other build completes and the index still updates.
func TestRunIsolatesErrors(t *testing.T) {
	bin := portolanBin(t)
	ws := t.TempDir()
	writeMicroWorkspace(t, ws)
	t.Chdir(ws)
	if err := os.WriteFile("gtfs/m3.zip", []byte("this is not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, res := planAndRun(t, nil, true, bin)
	if len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0], "m3:") {
		t.Fatalf("errors = %v, want one m3 failure", res.Errors)
	}
	for _, k := range []string{"m1", "m2"} {
		if _, err := os.Stat(filepath.Join("build/tiles", k, "tiles.json")); err != nil {
			t.Errorf("%s pyramid missing after m3's failure", k)
		}
	}
	if _, err := os.Stat("build/tiles/index.json"); err != nil {
		t.Error("index.json missing after a partial run")
	}
}

// TestVerifyDropCascade: a group failing the ink gate is deleted from
// the registry, its pyramid removed, and its members return to the
// world index.
func TestVerifyDropCascade(t *testing.T) {
	bin := portolanBin(t)
	ws := t.TempDir()
	writeMicroWorkspace(t, ws)
	t.Chdir(ws)
	_, r1 := planAndRun(t, nil, true, bin)
	if len(r1.Errors) != 0 {
		t.Fatalf("run1 errors: %v", r1.Errors)
	}
	p2, r2 := planAndRun(t, nil, true, bin)
	if len(p2.GroupsCreated) != 1 || len(r2.Errors) != 0 {
		t.Fatalf("group not created cleanly: %v %v", p2.GroupsCreated, r2.Errors)
	}
	group := p2.GroupsCreated[0]

	// force the gate to fail and change m2 so the group rebuilds
	old := verifyGroupFn
	verifyGroupFn = func(cfg registry.Config, buildDir, key string) (verifyResult, error) {
		return verifyResult{Worst: 0.5, Bad: []string{"m1 50%"}}, nil
	}
	defer func() { verifyGroupFn = old }()
	writeM2(t, "gtfs/m2.zip", 40.02)
	_, rp := planAndRun(t, []string{"m2"}, false, bin)

	hasDropError := false
	for _, e := range rp.Errors {
		if strings.Contains(e, "verify failed") {
			hasDropError = true
		}
	}
	if !hasDropError {
		t.Fatalf("errors = %v, want a verify-failed entry", rp.Errors)
	}
	if !rp.GroupsRewritten {
		t.Error("drop must report a registry rewrite")
	}
	cfg, err := registry.Load("portolan.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Feeds[group]; ok {
		t.Error("dropped group still in the registry")
	}
	if _, err := os.Stat(filepath.Join("build/tiles", group)); !os.IsNotExist(err) {
		t.Error("dropped group's pyramid still on disk")
	}
	idxRaw, err := os.ReadFile("build/tiles/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var idx []tiles.IndexEntry
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		t.Fatal(err)
	}
	var feeds []string
	for _, e := range idx {
		feeds = append(feeds, e.Feed)
	}
	if !reflect.DeepEqual(feeds, []string{"m1", "m2", "m3"}) {
		t.Fatalf("index after drop = %v, want the members restored", feeds)
	}
}

// TestVerifyGroupGate: the faithful groupverify.py port, on crafted
// geometry — a member whose ink the group kept passes, one drawn 300 m
// away fails.
func TestVerifyGroupGate(t *testing.T) {
	ws := t.TempDir()
	t.Chdir(ws)
	if err := os.MkdirAll("build", 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(y float64) []byte {
		fc := map[string]any{"type": "FeatureCollection", "features": []any{
			map[string]any{"type": "Feature",
				"properties": map[string]any{"band_min": 15, "band_max": 16, "kind": "steady"},
				"geometry": map[string]any{"type": "LineString",
					"coordinates": [][2]float64{{-100.00, y}, {-99.98, y}}}},
		}}
		raw, _ := json.Marshal(fc)
		return raw
	}
	os.WriteFile("build/g.geojson", line(40.0), 0o644)
	os.WriteFile("build/near.geojson", line(40.0001), 0o644) // ~11 m off
	os.WriteFile("build/far.geojson", line(40.003), 0o644)   // ~330 m off
	cfg := registry.Config{Feeds: map[string]registry.FeedCfg{
		"g":    {Out: "build/g.geojson", Members: []string{"near", "far"}},
		"near": {Out: "build/near.geojson"},
		"far":  {Out: "build/far.geojson"},
	}}
	v, err := verifyGroup(cfg, "build", "g")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Bad) != 1 || !strings.HasPrefix(v.Bad[0], "far ") {
		t.Fatalf("bad = %v, want far held out", v.Bad)
	}
	if v.Worst > 0.5 {
		t.Fatalf("worst = %.2f", v.Worst)
	}
}
