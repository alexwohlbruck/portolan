# FRESH START — implement the portolan pipeline stages

You are implementing the algorithm stages of **portolan**, a standalone Go
tool that turns GTFS feeds into Apple-Maps-quality interlined transit maps
(Linear **PAR-12**). Three prior architectures were built, measured, and
deleted on 2026-07-13; `internal/stages/stages.go` holds the empty
contracts you are filling. **Read this whole file, then docs/LESSONS.md,
before writing code.** Do not resurrect deleted code from git history —
reuse only what is listed under "What survives".

## The goal (PAR-12, distilled — full text: docs/PAR-12.md)

Interlined routes render as grouped parallel ribbons that stay equidistant
at every zoom (client-side offsets via the parchment MapLibre fork —
`off_from_px`/`off_to_px` eased along `line-progress`), split and merge with
smooth curves at junctions, geometry as true to life as possible. Benchmark:
Apple Maps' Chicago Loop.

## The three source techniques (all read, all relevant)

1. **Transit app blog** ("How we built the world's prettiest auto-generated
   transit maps"): GTFS shapes → OSM path-matching (DP over roads/tracks,
   minimizing distance + stop error) → **skeletonization on a 1px = 1m
   canvas** to merge overlapping lines while preserving topology → **ILP
   line ordering** (0.2s for Chicago after optimization) → **circular-arc
   corner rounding** (arcs, not béziers — strict parallelism across bundles,
   no degenerate cases) → stop integration (collapse physical stops,
   white-bar stations).
2. **Brosi & Bast 2024** (Cartographic Journal; loom's `topo`): incremental
   support-graph map construction — 5m sampling, merge-within-d̂ with node
   position averaging, blocking set, line-creep guard (α=sin45°), rounds to
   0.2% length convergence, degree-2 contraction on matching line sets,
   intersection smoothing (crop d̂ around node, re-average, reconnect), line
   turn restrictions from original-path cost comparison (t=500m). Rendering:
   free node areas, reconnect per line with curves (their Fig. 18 = the
   parchment render model).
3. **Bast/Brosi/Storandt LOOM** (SIGSPATIAL'18 / TSAS'19): MLNCM-WS line
   ordering — crossings only at nodes, weighted crossings + separations,
   graph reduction rules → greedy with lookahead → simulated annealing
   polish. (Owner previously judged a LOOM-derived ordering "perfect".)

## The owner's laws (violations get builds rejected)

1. **A GTFS route is never broken** — one continuous line start→finish.
2. **Every path follows real track-GROUP centerlines** — strand rules:
   1 track → follow it; 2 → midpoint; 3 → center track; 4 → middle-two
   midpoint; >4 → ignore outer/yards (implemented + tested:
   `bundle.MedianStrand`, `bundle.Refine`).
3. **Lines that come together bundle into parallel interlined groups**;
   bundling requires SUSTAINED togetherness (~20m for ≥50m, tunable) —
   kisses/crossings never bundle. Bundling is visual (stacked tracks count).
4. Junctions at physical forks; forks divide smoothly (no jogs, no H-bars);
   crossings pass straight through (tracks don't bend).
5. Same-color routes share ONE ribbon (trunk rule).
6. No repair passes — a wrong output means a wrong stage. No spot fixes.
7. Instrument before patching; one change at a time; **run
   `portolan sound` after every build** and look at the map (the scorer's
   history of blindness is why the dup% gate exists).

## What survives the reset (use these, don't rewrite)

- `internal/geo` — metric frame, grid index, **exact perpendicular
  cross-sections** (never nearest-point projection), **arc-walking probes**
  (never straight-tangent), turn/jag helpers, GaussianArc. Unit-tested.
- `internal/bundle` — `Chain` (ways→strands through deg-2 joints),
  `MedianStrand`/`Strands` (the owner's rules), `Refine` (iterative
  cross-section median refinement with curve-following probes — the
  celebrated "?v=29 precision centerline"), `TieEnds` (offset-scaled
  end ramps), `SubLine`. Unit-tested, incl. kiss immunity + corner-cut.
- `internal/gtfs` (zip→patterns, trip-coverage pruning), `internal/osm`
  (GeoJSON rail extract, service excluded).
- `internal/sketch` — the scorer. Gates: forward mean/p90/cover, wobble,
  **jaggedness at uniform 12m scale with spike locations**, **dup%**
  (≥2 features within 12m of a drawn sample = doubled network; the lens
  detector). Ground truth: `testdata/sketches/nyc.json` (owner-drawn,
  precious) + `testdata/nyc-rail.geojson` fixture.
- `internal/atlas` — the workbench (http://127.0.0.1:8765): map on the
  MapLibre fork with stage layers + problem-area dropdown + tuning dials +
  in-process rebuild/score buttons; sketch editor; hot reload (air + live
  assets). `portolan.json` config. Layer toggles persist.
- `internal/pipeline` — loaders/frame/emit scaffolding; `stages.Segment`
  is the parchment `transit_line_segments` contract (already renders
  correctly in the fork — verified).

## What was tried and why it died (details: docs/LESSONS.md + memory)

- **Corridor-state graph** (alongside-states → pieces → union-find):
  drowned in edge cases (stagger chaining, sliver welds, switchback
  doubling) despite reaching all-gates-PASS once; junction quality poor.
- **Support-graph averaging on raw OSM strands**: lens/eye artifacts where
  track gaps widen (dup 8–30%), triangle junctions, crossing welds
  ("exchange bars"); heading-gated merging fixed crossings but shattered
  connectivity (105-component routes) — merge rules can't be both.
- **String-trace sweeps** (seed strand + consumption): tangle-proof and
  fast, but consumption breadth oscillates between duplicate strings and
  swallowed neighbor corridors; branch attachment fragile.
- **Center-track snapping** (snap an already-bundled geometry to the single
  center-most track): off-center by design; owner removed it. NOTE: path
  matching itself is NOT the dead end — it is now step 1 of the process.
  What died was doing it after bundling and pinning to one physical track
  instead of refining the shared path to the group median.
- **FAIR (banded cuts + synthesized transitions) degraded ALL THREE
  networks** — 140m barrelman-era cuts consumed short-edge networks into
  confetti; chain-through transitions chorded across complexes. Verdict:
  implement the **node-front model** instead (small free area per node
  sized to slot count, reconnect per color between slot positions,
  **circular arcs** per the Transit blog).

## THE PROCESS (owner-specified — this is the spec, not a suggestion)

1. **Path matching** — for each GTFS route, path-match it onto existing OSM
   geometry: rails for train types, roads for buses, sea routes for
   ferries.
   - GTFS routes that follow similar paths must ALWAYS land on the same
     matched path (co-running routes share geometry exactly — this is what
     makes bundling possible downstream).
   - It must not be possible to "jump" between paths: always follow real
     OSM ways. Penalize crossover segments — railway switches, road median
     turns — identifiable generically as short ways that connect longer
     ways together.
   - Match onto MAINLINES only; ignore spurs and yards — EXCEPT at station
     terminals, where routes legitimately enter terminal trackage.
2. **Junction detection** — find the places where matched routes intersect,
   and divide the routes at each junction into segments.
3. **Segment attribution** — match GTFS routes to each segment (each
   segment carries the set of routes riding it).
4. **Parallel line rendering + ordering** — offset the routes on each
   segment into parallel lines and run the line-ordering optimization
   (crossing minimization).
5. **Junction connections** — draw the connections at junctions using
   smooth curvature geometry.
6. **Tileset + render** — generate the tileset; the MapLibre fork renders
   the live offset transitions (`line-progress` eased offsets) so lines
   connect perfectly at junctions across every zoom.

Stage contracts in `internal/stages/stages.go` (wired in
`pipeline.Chart`):
- `Match(patterns, ways, frame) → []Path` — step 1. A Path is a continuous
  walk over OSM ways (Law 1: never broken). Similar-path convergence,
  no-jump + crossover penalty, mainline-only-except-terminals all live
  here.
- `Split(paths) → *Network` — steps 2–3. Junction nodes where paths
  intersect/diverge; edges are segments with route sets.
- `Order(net) → slots` — step 4, color-trunked (law 5); LOOM-lite
  (greedy+lookahead+annealing) is fine, exact ILP later.
- `Fair(net, slots) → []Segment` — step 5, node-front junction rendering
  with circular arcs, zoom bands ×2 per zoom-out (base ~50m for short-edge
  networks; make it a dial), butt caps, travel-frame offset signs
  (LESSONS #12).
- Step 6 is scaffolding that already works: `WriteSegmentsGeoJSON` emits
  the parchment contract and the atlas map view renders it on the fork.

Because step 1.1 forces co-running routes onto one shared path, the
track-group centerline law (law 2) applies to that shared path's
geometry — `bundle.Refine` against the strands is the proven tool to move
a matched path onto the group median where multiple parallel tracks carry
it.

Definition of done per stage: builds NYC (`make nyc`) in seconds,
`portolan sound` gates green INCLUDING dup% and continuity, and the map
looks right at the problem-area dropdown sites — DeKalb, City Hall,
Bowling Green, Broadway Junction, Church/Franklin, South Ferry, and the
Chicago Loop (`?feed=29`).
