package sync

// The executor: walk a Plan for real. Registry first (the plan's
// rewrite, atomically), then every build in the plan under --jobs
// bounded parallelism, each as a `portolan chart` CHILD PROCESS — chart
// configuration (style, tuning dials) is process state, so two builds
// in one process would read each other's colours; a child per build is
// also exactly how tools/feed.sh and groupbuild.sh have always run, and
// it keeps one build's memory spike from another's. Group builds pass
// the ink-retention gate (verify.go) before they are allowed to stand;
// a failing group is DELETED from the registry, its pyramid removed,
// and its members' standalone builds join the queue. After each
// successful build the pyramid is cut in-process (tiles.Build is pure),
// the resolved style manifest rides into the pyramid as style.json, and
// the state manifest is stamped — incrementally, so an interrupted run
// resumes where it stopped. One build's failure lands in Errors;
// everything else proceeds.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	gosync "sync"

	"github.com/alexwohlbruck/portolan/internal/registry"
	"github.com/alexwohlbruck/portolan/internal/tiles"
)

// RunOpts wires one executor run. Portolan is the binary chart children
// run as; empty means the current executable.
type RunOpts struct {
	ConfigPath string
	StatePath  string
	BuildDir   string
	TilesDir   string
	ExportDir  string // empty = skip export
	StyleDir   string
	Jobs       int    // ≤0 means DefaultJobs()
	Portolan   string // chart child binary; "" = os.Executable()
	Log        func(format string, a ...any)
}

// DefaultJobs is deliberately modest: chart holds a whole region in
// memory, so NumCPU children of it would swap long before they'd scale.
// The --jobs flag is authoritative when given.
func DefaultJobs() int {
	n := runtime.NumCPU()
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

// TileTally aggregates the differ stats across every pyramid touched.
type TileTally struct {
	Written   int `json:"written"`
	Unchanged int `json:"unchanged"`
	Removed   int `json:"removed"`
}

// RunResult is the machine-readable outcome — the RESULT line's body.
type RunResult struct {
	Changed         []string
	Affected        []string
	Rebuilt         []string // builds actually run (skips excluded)
	GroupsRewritten bool
	Tiles           TileTally
	Exported        []string // "<feedkey>.zip" names present under ExportDir, flat
	Errors          []string
}

// task kinds. Standalone, overlay and member builds are the feed's OWN
// build and export their zips; a group build must not export — its
// member zips belong to the members' own builds, and two builds writing
// one export zip would race.
const (
	taskStandalone = "standalone"
	taskOverlay    = "overlay"
	taskMember     = "member-pyramid"
	taskGroup      = "group"
)

type task struct {
	key  string
	kind string
}

// verifyGroupFn is the gate, swappable by tests to force a failure.
var verifyGroupFn = verifyGroup

// Run executes a Plan. The returned error is a run-level failure (a
// registry that cannot be written, a state manifest that cannot be
// read); per-build failures land in RunResult.Errors instead.
func Run(plan *Plan, o RunOpts) (*RunResult, error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if o.Portolan == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolving the portolan binary: %w", err)
		}
		o.Portolan = exe
	}
	jobs := o.Jobs
	if jobs <= 0 {
		jobs = DefaultJobs()
	}

	// the registry first: every build below reads it, and a crash after
	// this write leaves a registry the next run re-plans from correctly
	var raw []byte
	if plan.RegistryChanged {
		if err := atomicWrite(o.ConfigPath, plan.Registry); err != nil {
			return nil, fmt.Errorf("writing the registry rewrite: %w", err)
		}
		raw = plan.Registry
		logf("registry rewritten (%d bytes)", len(raw))
	} else {
		var err error
		raw, err = os.ReadFile(o.ConfigPath)
		if err != nil {
			return nil, err
		}
	}
	cfg, err := registry.Parse(raw)
	if err != nil {
		return nil, err
	}
	doc, err := ParseDoc(raw)
	if err != nil {
		return nil, err
	}
	st, err := LoadState(o.StatePath)
	if err != nil {
		return nil, err
	}

	res := &RunResult{
		Changed:  emptyNotNil(plan.Changed),
		Affected: emptyNotNil(plan.Affected),
		Rebuilt:  []string{},
		Exported: []string{},
		Errors:   []string{},
	}

	// pyramids the plan retires: deleted groups no longer exist to draw.
	// Member pyramids stay — the world index skips grouped members, but
	// the atlas still serves their per-feed pyramids for feed-scoped
	// viewing, and MemberPyramids rebuilds them when their zip changes.
	for _, g := range plan.GroupsDeleted {
		if err := os.RemoveAll(filepath.Join(o.TilesDir, g)); err != nil {
			res.Errors = append(res.Errors, g+": removing pyramid: "+err.Error())
		} else {
			logf("%s: group dissolved — pyramid removed", g)
		}
		delete(st.Feeds, g)
	}
	if len(plan.GroupsDeleted) > 0 {
		saveStateNow := st.Save(o.StatePath)
		if saveStateNow != nil {
			res.Errors = append(res.Errors, "state manifest: "+saveStateNow.Error())
		}
	}

	var tasks []task
	for _, k := range plan.Standalone {
		tasks = append(tasks, task{k, taskStandalone})
	}
	for _, k := range plan.Overlays {
		tasks = append(tasks, task{k, taskOverlay})
	}
	for _, k := range plan.MemberPyramids {
		tasks = append(tasks, task{k, taskMember})
	}
	for _, k := range plan.Groups {
		tasks = append(tasks, task{k, taskGroup})
	}

	var (
		mu      gosync.Mutex // guards res, st, doc, cascades, built
		built   = map[string]bool{}
		cascade []task
	)
	saveState := func() {
		if err := st.Save(o.StatePath); err != nil {
			res.Errors = append(res.Errors, "state manifest: "+err.Error())
		}
	}
	oops := func(key, what string, err error) {
		mu.Lock()
		res.Errors = append(res.Errors, key+": "+what+": "+err.Error())
		mu.Unlock()
		logf("%s: %s: %v", key, what, err)
	}

	runTask := func(t task) {
		exportDir := o.ExportDir
		if t.kind == taskGroup {
			exportDir = ""
		}
		if t.kind == taskGroup {
			if err := groupPreflight(cfg, t.key, o.BuildDir, logf); err != nil {
				oops(t.key, "preflight", err)
				return
			}
		} else if err := feedPreflight(cfg, t.key, o.BuildDir, logf); err != nil {
			// A plain feed whose window outgrew its extract: cut one from a
			// wider extract rather than draw the railroad short.
			oops(t.key, "preflight", err)
			return
		}
		spec, err := assembleChart(doc, cfg, t.key, o.BuildDir, o.StyleDir, exportDir, logf)
		if err != nil {
			oops(t.key, "assemble", err)
			return
		}
		fp, err := fingerprintBuild(spec)
		if err != nil {
			oops(t.key, "fingerprint", err)
			return
		}
		tileDir := filepath.Join(o.TilesDir, t.key)
		mu.Lock()
		row := st.Feeds[t.key]
		mu.Unlock()
		if row.Built == fp && row.Tiled == fp &&
			(exportDir == "" || row.Exported == fp) &&
			exists(spec.Out) && exists(filepath.Join(tileDir, "tiles.json")) &&
			exists(filepath.Join(tileDir, "style.json")) {
			logf("%s: clean (built, tiled%s) — skipped", t.key,
				map[bool]string{true: ", exported", false: ""}[exportDir != ""])
			return
		}

		logf("%s: chart (%s)", t.key, t.kind)
		cmd := exec.Command(o.Portolan, append([]string{"chart"}, spec.Argv...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			oops(t.key, "build", fmt.Errorf("%v — %s", err, lastLine(out)))
			return
		}
		mu.Lock()
		row = st.Feeds[t.key]
		row.Built = fp
		if exportDir != "" {
			row.Exported = fp
		}
		st.Feeds[t.key] = row
		built[t.key] = true
		res.Rebuilt = append(res.Rebuilt, t.key)
		saveState()
		mu.Unlock()

		if t.kind == taskGroup {
			v, err := verifyGroupFn(cfg, o.BuildDir, t.key)
			if err != nil {
				oops(t.key, "verify", err)
				return
			}
			bad := strings.Join(v.Bad, ", ")
			logf("%s: members retained %.1f%% of drawn ink%s", t.key, v.Worst*100,
				map[bool]string{true: "  LOW: " + bad, false: ""}[len(v.Bad) > 0])
			if !v.ok() {
				// VERIFY FAILED — the group would take its members' ink off
				// the map. Delete it, restore the members to standalone.
				logf("%s: VERIFY FAILED (%s) — DROP: group deleted, members restored to standalone", t.key, bad)
				mu.Lock()
				feedsObj(doc).Delete(t.key)
				res.GroupsRewritten = true
				delete(st.Feeds, t.key)
				if err := atomicWrite(o.ConfigPath, MarshalDoc(doc)); err != nil {
					res.Errors = append(res.Errors, t.key+": rewriting registry after drop: "+err.Error())
				}
				res.Errors = append(res.Errors, t.key+": verify failed ("+bad+") — group dropped")
				saveState()
				for _, m := range cfg.Feeds[t.key].Members {
					if !built[m] {
						cascade = append(cascade, task{m, taskStandalone})
					}
				}
				mu.Unlock()
				if err := os.RemoveAll(filepath.Join(o.TilesDir, t.key)); err != nil {
					oops(t.key, "removing pyramid", err)
				}
				return
			}
		}

		stats, err := tiles.Build(tiles.Opts{Build: spec.Out, Out: tileDir, Name: t.key})
		if err != nil {
			oops(t.key, "tiles", err)
			return
		}
		// the resolved style manifest rides into the pyramid: the tile
		// consumer fetches <feed>/style.json (docs/SYNC.md tile contract)
		sty, err := os.ReadFile(spec.Out + ".style.json")
		if err != nil {
			oops(t.key, "style manifest", err)
			return
		}
		if err := atomicWrite(filepath.Join(tileDir, "style.json"), sty); err != nil {
			oops(t.key, "style manifest", err)
			return
		}
		mu.Lock()
		res.Tiles.Written += stats.Tiles
		res.Tiles.Unchanged += stats.Unchanged
		res.Tiles.Removed += stats.Removed
		row = st.Feeds[t.key]
		row.Tiled = fp
		st.Feeds[t.key] = row
		saveState()
		mu.Unlock()
		logf("%s: %d tiles written, %d unchanged, %d pruned", t.key,
			stats.Tiles, stats.Unchanged, stats.Removed)
	}

	// wave 1: everything the plan ordered. All builds are independent —
	// inputs are zips and extracts, never another build's output — so
	// standalone, overlay, member and group builds share one pool.
	pool := func(ts []task) {
		sem := make(chan struct{}, jobs)
		var wg gosync.WaitGroup
		for _, t := range ts {
			wg.Add(1)
			sem <- struct{}{}
			go func(t task) {
				defer wg.Done()
				defer func() { <-sem }()
				runTask(t)
			}(t)
		}
		wg.Wait()
	}
	pool(tasks)
	// wave 2: members restored by a dropped group. Their standalone
	// builds usually still stand from before the group absorbed them, in
	// which case the clean-skip catches them in milliseconds.
	if len(cascade) > 0 {
		pool(cascade)
	}

	// the world index, static: composed by the SAME helper the atlas
	// serves /api/tiles/index.json from, over the post-run registry (a
	// verify drop has already left doc), so the two cannot drift
	finalRaw := MarshalDoc(doc)
	finalCfg, err := registry.Parse(finalRaw)
	if err != nil {
		return nil, err
	}
	idx := tiles.Index(finalCfg, func(k string) string {
		return filepath.Join(o.TilesDir, k)
	})
	idxRaw, err := jsonMarshal(idx)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.TilesDir, 0o755); err != nil {
		return nil, err
	}
	if err := atomicWrite(filepath.Join(o.TilesDir, "index.json"), idxRaw); err != nil {
		return nil, fmt.Errorf("writing index.json: %w", err)
	}
	logf("index.json: %d feeds", len(idx))
	if len(plan.Widened) > 0 {
		logf("windows widened to cover their own shapes: %s", strings.Join(plan.Widened, " "))
	}

	if o.ExportDir != "" {
		entries, err := os.ReadDir(o.ExportDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".zip") {
					res.Exported = append(res.Exported, e.Name())
				}
			}
			sort.Strings(res.Exported)
		}
	}
	sort.Strings(res.Rebuilt)
	sort.Strings(res.Errors)
	res.GroupsRewritten = res.GroupsRewritten || plan.RegistryChanged
	return res, nil
}

// fingerprintBuild is the identity a state stamp compares against: the
// assembled argv (which folds in the entry, its window, the ceded
// windows and the style layering) plus every input zip's content hash.
// Style documents and extracts are deliberately OUTSIDE it — the
// manifest covers the feed only, by design (docs/SYNC.md).
func fingerprintBuild(spec *buildSpec) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", strings.Join(spec.Argv, "\x00"))
	for _, z := range spec.Zips {
		c, err := ContentHash(z)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00", z, c)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			if len(s) > 200 {
				s = s[:200]
			}
			return s
		}
	}
	return "(no output)"
}

func emptyNotNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

// atomicWrite: temp file in the same directory, then rename — a crash
// mid-write leaves the previous file intact rather than a truncated one.
func atomicWrite(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
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

// jsonMarshal matches what the atlas's json.NewEncoder(w).Encode(out)
// sends over the wire: compact JSON plus a trailing newline, so the
// static file and the live endpoint are byte-identical.
func jsonMarshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
