# The dev loop and its tools

Attempt two's most valuable products weren't pipeline code — they were the
tools around it. They are first-class citizens here, shipped inside the
binary (`portolan atlas`), documented, and tested. The loop:

```
draw ground truth  ─►  build  ─►  sound (score)  ─►  look (windows)  ─►  fix a stage  ─►  repeat
  (console /sketch)    chart        gates+spikes      before/after
```

## 1 · Sketch editor (console → `/console/sketch`)

A bezier editor in the console (`web/src/views/SketchView.vue`, model in
`web/src/lib/sketch.ts`). The owner hand-draws the ideal map per feed; the
drawing **is** the definition of correct. Two modes over one document:

**Network** — the route centerlines, as before.

**Yards** — a yard is a **closed** bezier boundary traced round the outer
tracks, plus the **centerlines** that run through it. Entrances are *not*
drawn: they are computed where a centerline crosses the boundary, single-link
clustered so a whole ladder reads as one entrance at its average point, with
the heading turned inward. That rule lives twice — `entrancesOf` in
`lib/sketch.ts` for live feedback, `(*Yard).Entrances` in `internal/sketch`
for scoring — and the two must stay in step.

Semantics (hard-won; don't regress them):
- **Illustrator pen**: click = corner anchor (no forward direction);
  click-drag = place *and* pull mirrored curvature handles.
- Handles are angle-locked and length-independent; smooth/broken is decided
  at dragstart; **alt** breaks the pair. Catmull-Rom auto-handles on plain
  anchors — **wrapping** on a closed curve, or the ring would corner at its
  arbitrary first anchor.
- `C` continues a line from either end; split/merge/reverse; insert-anchor on
  segment click; undo/redo per anchor. A ring cannot split or merge.
- **No colour.** The drawing states where track goes, and that is all it is
  graded on. Ink is fixed: red = drawn, blue = what the detector produced
  (toggleable underlay, straight off the build's `.yards.geojson`), amber =
  computed entrance.
- Basemap + build underlay for tracing; locations are camera bookmarks only —
  one continuous network per feed.
- Debounced **autosave, atomic write** through `internal/sketch.Save` to the
  path the registry's `network` field names (falling back to
  `sketches/network-<feed>.json`). **Go owns the schema**: the POST is
  decoded into the model and re-encoded, so a field the model does not carry
  cannot reach disk.
- The file is **committed beside the code it grades**, so a geometry change
  and the ground truth it was tuned against land in one diff. Save writes one
  anchor per line and each baked path on one line, which is the split between
  hand work and machine output. The file on disk is the owner's work product:
  treat it as precious and never regenerate it.

Implementation scar tissue: never rebuild the edit layer inside a drag
handler; one map-level pointer pipeline hit-tests the model in container px.

## 2 · The scorer (`portolan sound`)

Grades a build against the drawn network. Geometry-first (owner: "don't take
the line colors into so much consideration") — colour is not recorded in the
drawing at all, so nothing can be graded on it.

Per drawn line (**forward** — is the drawing matched?):
- `dev`: sample every ~5 m → distance to the nearest build feature
  (mean / p90 / max). Compute true-nearest *plus* a buffer query — bbox
  queries alone return phantom misses on long diagonal features.
- `cover`: % of samples with a feature within 25 m (a hole = missing
  segment).

Corridor-wide (**reverse**): build features riding drawn corridors
(median distance < 30 m), their deviation = **wobble** (only samples still
near the drawing count — a feature legitimately continuing past the drawn
area is coverage, not wobble).

**Jaggedness at map scale** (exists because a hideous build once passed the
distance gates): uniform 12 m arc resample of on-corridor features —
*dropping original vertices*, so sub-2 m micro-spans don't fake 45° turns —
then per-vertex turn stats. Gates: max ≤ 40°, spikes(>25°) ≤ 1/km. **Print
spike locations** — a number you can't navigate to is a number you won't fix.

Also enforced: per-color km conservation (±1%), zero self-intersections,
turn-σ on named curves (polygonalization detector).

**Yards** (only when the drawing has any) grade the detector out of the
build's `.yards.geojson` sidecar. Each drawn yard is matched to the region it
overlaps most:
- `IoU`: intersection over union of the two boundaries, by 2 m scanline
  raster. This is the headline number — the owner's target is **0.98**.
  `FailYardIoU` is a **ratchet**: raise it as the detector earns it, never
  lower it to make a build pass.
- `bnd_p90` / `bnd_max`: symmetric boundary deviation in metres (one-sided,
  a blob that swallows the drawing would score perfectly).
- `ent`: computed drawn entrances matched within 40 m, `+n` for detected
  entrances near the yard that nothing drawn explains.
- `ctr_cov%`: drawn centerline samples with detected centerline within 15 m.
  Reported but **not gated** — the detector emits no yard centerlines yet;
  gate it the day it does.

Exit code 1 on any gate failure. Run it after **every** build. CI runs it on
the NYC fixture.

## 3 · Window renders (`portolan atlas` → `/windows`, and CLI)

Fixed named windows (the test-location list in LESSONS.md) rendered as
PNG/SVG: OSM tracks in grey, build ribbons in color, optionally a second
build side-by-side (**before/after**). This is the review artifact for every
geometry PR. The aggregate score often doesn't move when visual quality
does — the pictures are part of the gate.

## 4 · Cross-section probe (`portolan atlas` → `/xsect`, and CLI)

The centerline ground-truth instrument: given a build line and a point, walk
the line dumping per-cross-section strand offsets
(`lat …: offsets [-5.4, -1.8, 1.9, 9.2]`). Correctness reads directly off
it: the median strand / middle-two midpoint must be ≈0 at every sample. This
tool found the corner-cutting bug that three rounds of theory missed.

## 5 · Vertex dump (CLI)

Print a feature's vertices with spans and turn angles, flagging turns >20°.
First stop for any spike the scorer reports.

## Tuning discipline

- Candidate builds are cheap and named (`--out build/cand-N.geojson`);
  promotion to "the" build is explicit.
- Every change: build → sound → windows. One change at a time.
- When a gate fails: instrument (xsect/vertex dump/window) → find the
  producing stage → fix there. Never add a downstream cleanup.
- The sketch file mid-edit: score against the committed snapshot
  (`git show <rev>:testdata/sketches/nyc.json`).

## The console *(web/, served at /console)*

The dashboard replaces the hand-rolled workbench pages. It is a Vue 3 SPA
built with Vite and Tailwind v4, deliberately sharing **barrelman's console
design system byte for byte** — `web/src/style.css` is copied from
`barrelman/web/src/style.css`, and the primitives (Button, Badge, Card,
Dialog, Select, Switch, Tabs, Progress, Toaster) use the same class strings.
The two are meant to read as one product, and this dashboard is destined to
be embedded in barrelman, where any divergence would be a visible seam.

```bash
cd web && npm install && npm run build   # served by the atlas at /console
cd web && npm run dev                    # port 5180, proxies /api and /vendor
```

The Go server serves `web/dist` from disk (not embedded), so a rebuild
shows up on refresh. A missing dist returns the build instructions rather
than a 404.

**Views.** *Map* is the viewer — ribbons per zoom band, debug layers,
segment inspector, scenario switcher. *Build* runs the pipeline with a
live log and a stage bar driven off the pipeline's own stage lines.
*Service* shows the 7×24 week grid coloured by scenario, with per-scenario
build buttons. *Sketch* is the ground-truth editor (below). *Tuning* is
the pipeline dials. *Cities* is CRUD over the config, flagging inputs that
are missing before a build wastes time on them. *Style* edits class
defaults and colour overrides. *Problem areas* manages the review
bookmarks.

### Sketch editor

A bezier pen over the map: anchors with in/out handles, baked to the
polyline the scorer reads. Gestures are unchanged from the old editor —
click places a corner, click-drag pulls a curve, Enter finishes, drag an
anchor to move it, alt-drag to pull fresh handles, right-click deletes an
anchor, double-click toggles corner ⇄ auto-smooth, clicking a selected
line inserts an anchor. Keys: `d` new, `c` continue, `x` split, `m` merge,
`z`/`Z` undo/redo, `⌫` delete line. Autosaves 900 ms after the last edit.

Three things the rewrite fixed rather than reproduced. Merge used to
reverse anchors **without** swapping `hin`/`hout`, kinking every curve it
joined. Anchor insertion picked the span by midpoint proximity, which
mis-assigns clicks on long or curved spans — it is perpendicular distance
now. And `Cmd+C` fell through to "continue line", so copying text started
drawing.

`coords` IS the contract: `internal/sketch` reads only `coords` and
`routes`, so every mutation re-bakes. `npm run check` (in `web/`) re-bakes
every file in `sketches/` from its anchors and demands the result be
byte-identical to what is on disk — the drawn networks are months of
hand-made ground truth, and a change to the baker would silently move
every reference line and every gate number with it. It also covers the
merge, split, insert and reverse invariants.

### Tuning

`pipeline.Dials` rendered from `/api/params`, grouped by the stage prefix
in each key, so a new dial in the Go struct appears with no UI change.
Values are stored per city in the browser and travel in the build request
body — they are never written to `portolan.json`, so a tuning run cannot
disturb anyone else's build. Values off default are highlighted with a
one-click reset.

**MapLibre is not bundled.** Portolan renders on a fork (variable
line-offset along line-progress); the server exposes it at
`/vendor/maplibre-gl.js` and the map view reads it off `window`. The npm
build cannot draw portolan's output.

**Config is the contract.** Every editor writes `portolan.json`, the same
file the CLI and `tools/feed.sh` read, so a dashboard build and a terminal
build always agree. Writes are atomic and preserve unknown keys, and a
config that would not parse is rejected rather than written.

Known environment quirk: MapLibre defers style loading through
`requestAnimationFrame`, so in a hidden or headless tab the map stays
blank until the tab becomes visible. This affects the old `/map` page
identically — it is not a console bug.
