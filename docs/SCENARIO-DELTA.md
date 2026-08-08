# Scenario delta — one geometry, many timetables *(proposal)*

Status: superseded in direction by [DYNAMIC-SERVICE.md](DYNAMIC-SERVICE.md)
(one layout, timestamps applied at render). The transport half here is
BUILT and remains in use (`/api/build-delta`, commit below);
the pipeline half — stable segmentation and transitions-as-ramps — is
still proposed. Written 2026-08-08 off measurements of the NYC union
build and its `Sat 07–22` scenario.

## The problem

A scenario is stored as a complete redraw. NYC has 15 of them at ~11 MB
each, and switching time in the viewer means fetching a whole new map.
That is a strange price for what a scenario actually *is*: the same city,
with some lines not running and the survivors re-packed.

The measurements say the price is mostly waste:

| measurement | value |
|---|---|
| weekend geometry byte-identical to union geometry | **69%** |
| weekend geometry genuinely new | 31% (435 transitions, 419 steady) |
| shared geometry needing a different offset | **28 features** |
| scenario properties with coordinates stripped | **0.46 MB** vs 10.9 MB |

Twenty-eight features actually move. Everything else in that 10.9 MB is
either identical to the union build or an artefact of how we cut it.

## Why the 31% is not zero

Two causes, both structural rather than fundamental.

**Transitions are recomputed (435 features).** A transition exists only
where a route changes slot, so a different set of running lines produces a
different set of them. But a transition is not really independent
geometry: it is a stretch of corridor drawn with the offset ramping from
one slot to another, and the MapLibre fork already interpolates offset
along `line-progress`. Expressed as *"route R ramps 2→0 over the first
60 m of edge E"* it needs no coordinates at all. Transitions are **2.3%
of drawn length** (117 km of 4,991 km) but a third of the feature count —
they are almost pure bookkeeping.

**Steady runs are re-cut (419 features, 59% of them re-cuts).** SPLIT's
junction set changes when lines vanish — 238 edges on the weekend against
262 in the union — so the same centerline gets divided at different
points. 249 of the 419 new steady features share an endpoint with union
geometry, which is the signature of a re-cut rather than a genuinely
different path.

## The proposed structure

Ship **geometry once per city**, and per scenario ship a **slot table**.

```
build/nyc.corridors.geojson     # edges: id + coordinates, per zoom band
build/nyc.scen-<id>.slots.json  # edge id -> routes, slots, ramps
```

The slot table is roughly:

```json
{
  "scenario": "20f0c0b3",
  "edges": {
    "e1042": {
      "nslots": 3,
      "routes": [
        { "key": "D82233", "slot": 0, "ramp": null },
        { "key": "EB6800", "slot": 1, "ramp": { "to": 2, "over_m": 60 } }
      ]
    }
  }
}
```

The viewer loads corridors once and swaps a ~0.5 MB table per scenario,
applying offsets through `setFeatureState` — offset is already a
data-driven paint property, so this is the representation MapLibre wants.
Switching time becomes a small fetch and a repaint instead of an 11 MB
download.

Two changes make it possible:

1. **Stable segmentation.** Always split on the *union* junction set, so
   edge ids mean the same thing in every scenario. This converts the 249
   re-cut steady features into shared ones.
2. **Transitions as ramps.** Stop emitting transition geometry; emit a
   ramp annotation on the edge the transition rides. Removes 435 features
   and the entire class of scenario-specific geometry they represent.

## What makes this non-trivial

**FAIR's junction work is set-dependent, and stable segmentation breaks
it.** Cuts, fillets and steel-traced turns are computed against the
junction set. Segment on the union's junctions and a scenario gets
junction treatment where nothing actually diverges that day — gaps and
fillets in the middle of what should be a plain through-run. FAIR has to
learn that a junction is *inert* in this scenario: degree counted over
the running routes, not over the edges.

That is the real work in this proposal, and the reason it is a deliberate
change rather than a compression trick.

**ORDER stays on the server.** Slot assignment is a real optimizer
(LOOM-lite slot climb) and porting it to JavaScript would recreate exactly
the two-implementations-drifting problem that internal/style was created
to end. It costs 0.1 s server-side and its output is the table — ship the
result, not the algorithm.

## What is built

The sharing does **not** need the pipeline changes above — it can be done
at the transport layer by content-addressing what the pipeline already
emits, with zero risk to the drawing.

`POST /api/build-delta?feed=&band=&scenario=` takes the list of geometry
hashes the client already holds and returns the feature table plus only
the coordinates it is missing. A geometry's key is a hash of its exact
serialized coordinates, so a cache hit is *always* safe to reuse — and
safe across rebuilds too, since a hash can only ever mean the same
coordinates. A stale entry can be unused, never wrong; there is nothing
to invalidate.

The client assembles features and geometry back into a FeatureCollection.
Measured on NYC band 15, and **verified byte-identical to what
`/api/build.geojson` serves in all three cases**:

| step | over the wire | geometries sent / reused |
|---|---|---|
| union, cold cache | 1.46 MB | 1358 / 0 |
| → weekend scenario | **0.39 MB** | 216 / 1023 |
| → back to union | **0.05 MB** | 0 / 1443 |

Against the 11.54 MB a scenario switch used to cost: **30× for a first
visit, 230× for a revisit.** Switching time is now a fetch small enough to
feel instant, which was the point.

Two earlier transport wins it builds on (commit `c796fbd`): gzip across
the workbench (3.1×), and `?band=N`, since FAIR emits a full copy of the
map per zoom band and one is ever visible.

The pipeline changes above would shrink the *first* visit too, by making
the 31% of new geometry mostly disappear. That is the remaining work, and
it is the part with drawing risk.

## Related

The same dependency structure gives a build-time win, unimplemented and
independent of this: MATCH is 76% of a scenario build (33.8 s of 44.4 s
for NYC) and a pattern's path is *mostly* a property of its own shape, so
one process could match once and lay out every scenario from it. Mostly,
not entirely — `match.go` gives a cost discount to pieces other patterns
already ride, so paths are matched in sequence and a shared match would
change output slightly. Arguably for the better: today a route can land on
a different track between the union and a scenario, so switching time can
make a line jump tracks, which is not a service change at all.
