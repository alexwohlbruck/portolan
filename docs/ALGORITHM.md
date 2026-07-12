# The portolan pipeline

Seven stages. Every stage is exact vector geometry in a local metric frame.
There is **no raster step**, **no global optimizer** (except line ordering,
which is local per edge), and — this is a design law, not a preference —
**no repair passes**. If output geometry is wrong, a stage above is wrong.
The previous pipeline died under an ecosystem of bandages (kiss-splitters,
stub-detachers, crossing-dissolvers, way-snappers, cusp-excisers,
micro-loop-droppers, hook-uncurlers); each one treated a symptom of a raster
medial-axis skeleton that should never have existed. We do not rebuild that
zoo. See [LESSONS.md](LESSONS.md).

Naming: stages carry chart-room names because they earn them — *soundings*
are depth probes perpendicular to the hull's course; a *berth* is where a
vessel is assigned to lie; *fairing* is smoothing a hull curve.

```
1 CHART   load & normalize          gtfs.zip + rail.geojson → patterns, tracks (metric)
2 SOUND   cross-section soundings   every track → parallel-neighbor profile
3 BUNDLE  bundling + centerline     tracks → bundle graph (nodes at physical forks)
4 BERTH   route attribution         patterns map-matched onto bundle edges
5 ORDER   line ordering             slots per edge, crossings minimized (LOOM-lite)
6 FAIR    junction fairing          per-zoom cuts + G1 fillets between slot ribbons
7 EMIT    output + gates            geojson / tiles / png, sound-score enforced
```

---

## 1 · CHART — load and normalize

**GTFS** (`internal/gtfs`): read `routes.txt`, `trips.txt`, `shapes.txt`,
`stops.txt` straight from the zip. Group trips by `(route_id, shape_id)` into
**patterns**; keep the smallest set of patterns covering ≥99% of trips per
route (agencies ship dozens of one-off variants; they are noise). Carry
`route_color`, `route_short_name`, `route_type`.

**Rail** (`internal/osm`): a GeoJSON of OSM ways with
`railway ∈ {rail, subway, light_rail, tram}` and **no `service` tag**
(yards, sidings, spurs, crossovers excluded at the door). Keep way ids and
`tunnel`/`bridge`/`layer` tags — layer separates stacked tracks later.

**Frame**: project everything once into a local azimuthal-equidistant frame
centered on the data bbox. All downstream math is x/y meters. (The old
pipeline did per-formula `cos(lat)` scaling in degrees; half its subtle bugs
were unit bugs.)

## 2 · SOUND — cross-section soundings

For every track, sample every ~10 m of arc. At each sample cast a
perpendicular segment (±25 m) and intersect it with every other track within
reach (grid index). A crossing counts as a **parallel neighbor** only if the
other track's heading at the crossing is within 30° of ours. Record
`(neighbor id, signed offset)` per sample.

Two implementation laws proven the hard way:

- **Perpendicular *intersections*, never nearest-point projection.**
  Projection clamps to endpoints and biases on curves; the intersection of
  the normal ray with the neighbor polyline is the true cross-section.
- **Any probe that walks "ahead/behind" walks along the ARC, never along the
  straight tangent.** Tangent extrapolation leaves the corridor on every
  bend; in the old pipeline that silently excluded all members mid-curve and
  froze the centerline into a 10 m corner-cut.

## 3 · BUNDLE — bundling and the median-strand centerline

**Mateship**: tracks A and B are mates over an interval where the sounding
offset stays within `[2.5 m, 12 m]` **sustained for ≥ 60 m of arc**. This is
the kiss rule, promoted from a downstream patch to the definition of
bundling: two lines that pass close for 30 m never bundle, by construction.
Stacked tracks (Chicago Blue under the El) bundle if 2-D parallel — bundling
is visual, not physical connectivity. Same-`layer` pairs may use the tighter
gap.

**Bundles**: union-find mate intervals → maximal groups of pairwise-connected
tracks over an interval. A bundle knows its member tracks and their strand
order (left→right by offset).

**Centerline** (the crown jewel, `internal/bundle`): sweep the bundle using
its longest member as the initial spine; at every cross-section apply the
owner's hand-drawing rules to the strand offsets:

| strands | centerline |
|---|---|
| 1 | follow the track |
| 2 | midpoint |
| 3 | center track |
| 4 | midpoint of middle two |
| >4 | drop outermost (yard edges), reapply |

Then refine: 3–5 iterations where each vertex casts a perpendicular of the
*current* centerline, re-collects strand offsets by intersection, and moves
(damped 0.8) to the median strand. Iterate to convergence; finish with a
light σ≈8 m low-pass. **The median, never a distance-weighted mean** — a mean
has gravitational pull toward whatever is nearby; the median moves only in
honest discrete steps. Where the strand count changes (an express pair
peeling off), smooth the **offset series** (σ≈5 samples) so the step becomes
a long ramp, and scope the median to **through members** (present along ≥80%
of the interval) — a peeling ramp belongs to the fork, not to this
centerline.

**Nodes**: a node exists exactly where bundle membership changes and the
change persists (a track group joins or leaves, or two bundles cross while
sharing a connection). Node position = the meet of the adjacent bundle
centerlines (least-squares of end rays), which is well-posed because the
centerlines are computed before the node is placed. Degenerate twins (two
nodes within ~10 m) merge.

Everything the raster skeleton got wrong is structurally absent here: no
quantization wobble, no medial-axis blobs parking nodes 130 m off a flying
junction, no phantom kiss-welds, no tile seams (there are no tiles — a city
fits in memory as vectors).

**Track gaps**: OSM is incomplete (the Nostrand branch through President St
junction simply isn't mapped). Where a route's shape finds no bundle within
30 m for >50 m (stage 4), the gap is bridged with the *shape geometry*,
flagged `gap`. Shapes define geometry only across gaps — where tracks exist,
tracks win, because shapes can be map-matching chords (the M at Fresh Pond
is 64 m off its own track).

## 4 · BERTH — route attribution

Map-match every pattern shape onto the bundle graph: greedy walk with ~100 m
lookahead; step cost = mean shape-to-centerline distance + heading agreement.
(pfaedle solves this with an HMM; greedy-with-lookahead is enough when the
candidate graph is already a clean bundle graph rather than raw OSM.) Output:
each pattern = a path of bundle edges (+ flagged gap bridges). Each edge
accumulates its **berths** — the set of (route, color) riding it.

Attribution guard: a route berths on an edge only if its shape runs *along*
it (≥60% of samples within reach **and** within reach at the span probes) —
passers-by at a crossing never inherit a berth.

## 5 · ORDER — line ordering

Per edge, assign each berth a slot (−k…+k across the ribbon) minimizing
line crossings at both end nodes — LOOM's core problem. v0 ships a
LOOM-lite: initial order by angular destination at each node, then local
pairwise-swap descent per edge (exact for ≤8 lines via branch-and-bound when
the edge is contested). Two invariants from production:

- **Slot stability along chains**: through a degree-2 node, the travel-frame
  order must not flip storage orientation (harmonize along every chain).
- Order lives in each edge's own **travel frame**; transitions mirror it via
  frame sign, never by re-deriving.

Full LOOM (ILP) is a documented upgrade path, not a v0 dependency.

## 6 · FAIR — junction fairing

Per zoom band (140 m base at z15, ×2 per zoom-out, tuned in the old
pipeline), cut each edge back from its nodes and connect slot-to-slot with
biarc fillets, G1 at the seams. Rules with scar tissue behind them:

- A fillet only replaces **near-straight approach** (approach turning >30°
  shrinks the cut proportionally) — never synthesize across a real curve.
- Endpoint ties ease over a window **scaled to the offset absorbed**
  (~5 m of run per meter), cosine-ramped, at both ends of any blend.
- Terminal stubs, balloon loops (one closed pass, served), and ring edges are
  first-class cases, not surprises.
- Transitions must never chain-merge into closed rings.

## 7 · EMIT — output and gates

GeoJSON per band (debug + hand-off), MVT later, PNG window renders for
review. **The gates run in `portolan sound` and every build prints them**:

1. **Forward**: every hand-drawn line matched — mean/p90/max deviation,
   coverage@25 m (per line, geometry-first; color is diagnostic only).
2. **Wobble** (reverse): on-corridor build features vs the drawing.
3. **Jaggedness at map scale**: uniform 12 m resample of on-corridor
   features; max turn ≤40°, spikes ≤1/km, **with locations printed** —
   aggregate distance gates are provably blind to staircases and hooks.
4. **Conservation**: per-color km within ±1%, zero self-intersections.
5. **Curve smoothness**: turn-σ on known curves (polygonalization detector).

A build that fails a gate does not ship. When a gate fails, the fix goes in
the stage that caused it — adding a stage-8 cleanup is how the last pipeline
died.

---

## Performance envelope

NYC subway: ~700 tracks / ~26 routes / ~450 km. Soundings ≈ 45 k samples ×
~10 neighbor tests against a grid index — single-digit millions of segment
intersections. Go does this in well under a second; the whole build should
stay **< 2 s**, against ~4 min for the Python predecessor. Chicago 'L' is
smaller. Perf budget is a feature: the tuning loop is only as good as its
cycle time.
