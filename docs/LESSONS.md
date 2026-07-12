# Lessons from two dead pipelines

portolan is attempt three. Attempt one ("legacy segments") drew each route's
own shape with offsets — spaghetti at junctions. Attempt two (barrelman
`pipeline/`, Python, ~6 months of work) got genuinely good — first-ever
sketch-score PASS, live on the map — but its architecture had a rotten
foundation that every fix had to compensate for. This file is the distilled
war record. **Read it before touching geometry code.** Every rule here was
paid for.

## The root cause that killed attempt two

**A raster medial-axis skeleton defined the topology.** Route shapes (later,
tracks) were rasterized at ~2 m/px, fused with a thickness parameter, and
skeletonized. Everything else was downstream of that decision, and so were
almost all defects:

- Quantization wobble on every line (then smoothed away with a σ22 m blur
  that also smeared real geometry).
- Junction nodes parked in the *crotch* of blobs — at flying junctions up to
  **130 m** off the physical fork (President St/Nostrand), tilting every
  incident corridor.
- Twin nodes 3 m apart joined by 36 m hairpin edges (Woodside).
- Phantom **kiss-welds**: two lines passing within the fuse width briefly got
  a shared node; both lines warped toward it (Bowling Green, Rector,
  DeKalb…).
- A global thickness parameter that could not both bundle a corridor's own
  4 tracks (~15 m apart) and keep a neighboring line 18 m away separate.
- Tile seams (raster memory forced 4 km tiling; junctions on seams broke).

Each of these got its own repair pass: `_split_kisses`, `_detach_stubs`,
`_dissolve_crossings`, `_prune_redundant_stubs`, `_snap_crossing_nodes`,
`_excise_cusps`, `_drop_microloops`, `_meet_junction_nodes`, waysnap
reconciliation, `_scrub`, `_uncurl`… The bandages interacted; fixing one
site broke another. **Design law: no repair passes. A wrong output means a
wrong stage.**

## Geometry laws (violations produced visible artifacts)

1. **Median, never distance-weighted mean.** A mean has gravitational pull —
   a foreign strand within the window drags the line partway toward it. The
   median either adopts a strand honestly or ignores it. The hand-drawing
   rules (1→follow, 2→midpoint, 3→center, 4→middle-two, >4→ignore yards)
   *are* the median of strand offsets.
2. **Cross-sections by perpendicular intersection, never nearest-point
   projection.** Projection clamps to endpoints (chords at track ends) and
   biases on curves.
3. **Probes walk the arc, never the straight tangent.** Straight ±75 m
   probes leave the corridor on every bend → members excluded exactly at
   curves → refinement freezes → 10 m corner-cuts (Church/Franklin). Found
   only by dumping per-cross-section offsets.
4. **Strand-count changes ramp; they never step.** Smooth the *offset
   series* (σ≈5 samples), not the geometry; scope centerlines to
   through-members (≥80% presence) so peeling ramps don't vote.
5. **Bundling requires sustained parallelism** (≥ ~60 m within ~12 m). This
   single rule makes kisses unrepresentable. Bundling is *visual* (2-D):
   stacked tracks bundle without physical connection (Chicago Blue under
   the El).
6. **Ends ease into nodes over an offset-scaled window** (~5 m of run per
   meter of offset, cosine ramp) — a fixed 8-vertex tie window turned a 12 m
   node offset into a 44° seam kink. And tie ends only into **real, settled**
   nodes (attempt two tied into phantom kiss nodes before the dissolves ran;
   the bend outlived the node — the Greenwich "gravitational bow").
7. **A steady centerline never turns >100° at 5 m spans; rail never turns
   >40° at 12 m scale.** Physically-impossible turns are defects by
   definition — but if you find yourself *excising* them, you're bandaging;
   find the producer.
8. **Track data is incomplete.** OSM lacks whole branches (Nostrand at
   President St). Route shapes bridge gaps — but only gaps, because shapes
   can be map-matching chords 64 m off their own track (M at Fresh Pond).
   Never let a nearby-but-crossing track "fix" a gap (the perpendicular
   Eastern Pkwy trunk is not a substitute for the missing branch): require
   tangent alignment before hugging anything.
9. **Snap-to-nearest-single-track shreds bundle medians.** A median
   legitimately sits *between* tracks; per-vertex nearest-track snapping
   staircased every fan-out (the Bowling Green photo). If a point has track
   on both sides, it is on the bundle — never "stray".
10. **Junction node = the meet of its corridors' centerlines** (least-squares
    of end rays), computed *from* the corridors — and only re-place nodes
    that are provably broken (residual > ~12 m). Re-solving well-placed nodes
    wanders them by tangent noise (6 Av/14 St drifted 15 m and dragged the F
    off the avenue).
11. **Work in one local metric frame.** Degrees-with-cos(lat) math scattered
    unit bugs everywhere.

## Engine/rendering laws

12. **Slot stability along chains**: within-color line order must be
    harmonized in the travel frame along every degree-2 chain, or the engine
    sees F/FX flip slots mid-corridor.
13. **Transitions must never chain-merge into closed rings** (GCT green
    rings), and closed steadies can carry fully-mirrored closure tails
    (`pts[1]==pts[-2]…`) that unwind to a single pass (the "Hudson Yards
    balloon" was a doubled out-and-back stub, not a loop).
14. **Fillets only replace near-straight approaches** (>30° of approach turn
    shrinks the cut) — a 140 m biarc across a real curve cut a city block
    (Battery N/R/W).
15. **`line-cap: butt`** on interlined ribbon layers — round caps bulge past
    feature ends and blunt every V apex (pure style-layer fix).
16. **Balloon loops**: keep exactly one closed pass; a terminal loop is
    served geometry, not a defect.
17. **Zoom bands**: transition length ×2 per zoom-out keeps on-screen curve
    size constant (140 m base at z15 looked right after user tuning).
18. **Continuous-zoom interlining needs client-side offsets** (PAR-12): baked
    offsets breathe when zooming. Parchment carries a MapLibre fork with
    variable line-offset along line-progress; emit travel-frame slots and
    per-vertex offset keys for it.

## Process laws (these mattered more than the geometry)

19. **Hand-drawn ground truth beats every synthetic metric.** The owner
    draws the ideal network in the sketch editor; the scorer gates every
    change against it. Optimize toward the drawings, then generalize.
20. **Score after every pass. No exceptions.** And know what the score
    cannot see: aggregate deviation gates (mean/p90 at 5 m sampling) are
    **blind** to staircases, hooks, and corner-cuts that scream at map zoom.
    The jaggedness gate (uniform 12 m resample turn stats, spike locations
    printed) exists because a visually horrible build once passed.
21. **Instrument before patching.** The one session that reverted wholesale
    was the one that patched three defects "obviously" and blind. Dump the
    actual vertices, actual offsets, actual node degrees at the defect;
    render the window; *then* write the fix. Per-cross-section offset dumps
    (middle-two midpoint must read ≈0 at every sample) are the ground truth
    for centerline quality.
22. **One fix at a time, gated.** Wide "fallback" fixes (4× attribution
    reach) created 400 junk rows and 11 self-intersections.
23. **Beware self-confirming diagnostics.** A "retrace %" metric that
    excluded ±k neighbors then measured distance to the remaining line read
    ~90% for *everything* — the far line bridged the excluded gap. Verify
    per-segment-pair, or don't trust the number.
24. **Check the raw source before blaming the pipeline.** The Battery
    "wrong yellow S" was real GTFS truth (Trinity Pl merges into Greenwich);
    the defect was the fillet cutting the corner, not the path.
25. **Never blanket-filter rescue matches**: 61% of NYC matched shapes were
    `hmm_sparse_rescue`; 14 routes are rescue-only.
26. **Keep per-color km conservation and `ST_IsSimple` as cheap always-on
    gates** — they catch dropped lines and folds instantly.
27. **Visual review is part of the gate.** Render fixed windows
    (before/after vs the tracks) for every geometry change; the scorer's
    aggregates didn't move during the change that fixed the corner-cutting,
    but the pictures did.

## Named test locations (bring them all forward)

Bowling Green / Rector / Greenwich St (kiss + staircase class), City Hall
J/Z×4/5/6 weave, Brooklyn Bridge–City Hall loop, Hoyt–Schermerhorn,
Broadway Junction, Canal A/C/E curve, Central Park N/Q curve
(polygonalization detector), Times Sq red spur (attribution bleed),
6 Av/53 St trident, 8 Av/53 St, 7 Av/57 St, Church/Franklin bend
(corner-cut detector), President St/Nostrand (track gap), Fresh Pond (shape
chord), Woodside Northern Blvd (twin nodes), Kew Gardens, Broad Channel,
South Ferry loop complex, Grand Central S-curve (transition hook — still
open at handoff), Coney Island terminal fan, Park Slope / Culver S-curve
(4-track median), DeKalb (the canonical kiss).

## Still open at handoff from attempt two

- Grand Central green transition hook (~82°): two corridor end tangents
  genuinely oppose after the junction cut — a topology decision (which ends
  pair) rather than a smoothing problem.
- Bundle-vs-route drawing at same-color multi-route junctions (6 Av/53:
  B/D/F/M each drew its own turn → stacked arcs). Draw the *color bundle's*
  dominant path.
- Chicago has no OSM track import in the old database — portolan reads OSM
  directly, dissolving this.
- Stops/stations stage (grouped station pills à la Transit app) — never
  started in attempt two.
