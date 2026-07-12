# Centerline / bundling algorithm — research & decision

The owner's diagnosis (2026-07-12): wherever crossovers, junctions, extra
parallel tracks, or widening gaps appear, the corridor centerline is thrown.
Root cause of the bad results. Task: find the documented, proven algorithm.

## What the literature offers

**1. Support-graph map construction — Brosi & Bast, "Large-Scale Generation
of Transit Maps from OpenStreetMap Data", The Cartographic Journal (2024)**
([open access](https://ad-publications.informatik.uni-freiburg.de/Large-Scale_Generation_of_Transit_Maps_from_OpenStreetMap_Data.pdf),
implemented in [ad-freiburg/loom](https://github.com/ad-freiburg/loom)'s
`topo` tool; earlier form in
[Bast/Brosi/Storandt SIGSPATIAL'18](https://arxiv.org/abs/1710.02226)).
The planet-scale production system for exactly our problem. Algorithm:

- Insert each line's geometry (longest first) into a growing support graph:
  dense-sample at **l = 5 m**; each sample merges with the nearest graph
  node within **d̂** (R-tree), and the node's position moves to the AVERAGE
  of its old position and the sample — the centerline emerges as the mean
  of every pass that merged into it. Line labels accumulate on edges.
- **Blocking set**: an edge's own recent samples can't self-merge; a
  **line-creep guard** (α = sin 45°) refuses merges near an edge's endpoints
  at obtuse angles — the documented fix for interlaced merges.
- Contract degree-2 nodes with equal line sets; contract sub-l artifact
  edges; **repeat construction in rounds until the total edge length changes
  < 0.2 %** (empirically validated threshold).
- **Intersection smoothing**: at each node, crop every adjacent edge at
  distance d̂, move the node to the average of the crop points, reconnect —
  the documented fix for "centerline thrown at junctions".
- **Line turn restrictions**: a line connects edge e→f at a node only if its
  ORIGINAL path really does (path-cost comparison, t = 500 m) — the
  documented fix for wrong through-connections at complexes (their worked
  example is the Chicago Loop).
- Rendering: offsets per line around the edge polyline, node areas freed by
  node fronts, connections rebuilt with Bézier curves — identical to the
  parchment/MapLibre-fork render model.

**2. Straight-skeleton / medial-axis collapse — Haunert & Sester,
"Area Collapse and Road Centerlines based on Straight Skeletons" (2008)**
([pdf](https://www1.pub.informatik.uni-wuerzburg.de/pub/haunert/pdf/HaunertSester2008.pdf));
Voronoi medial axis of buffered geometry ([Eppstein, Geometry in Action](https://ics.uci.edu/~eppstein/gina/medial.html)).
Vector skeletons are exact (no raster artifacts) but inherit the same
kiss/spur pruning problems attempt two drowned in, and give topology only —
attribution still needs a separate matching stage.

**3. Carriageway-pairing collapse** (ESRI Merge Divided Roads class of
algorithms): pair parallel strokes, average pairs. Works for clean dual
carriageways; underspecified for 3+ tracks, crossovers, fans.

## Decision

**Adopt (1), the support-graph construction, as stages 3–4.** It replaces
the corridor-state machinery (alongside-states, fork propagation, sliver
glue, piece unions) AND the separate BERTH matcher — attribution is free
because line labels ride along during merging. It satisfies the owner's
three laws by construction:

- *routes are never broken* — every pattern is inserted as one continuous
  path and mapped onto a connected walk in the support graph; turn
  restrictions keep junction connectivity honest;
- *bundling* — within-d̂ merging with label union IS bundling;
- *real track-group centerlines* — node positions average the merged
  passes; and we keep OUR proven cross-section **median-strand refinement**
  (internal/bundle Refine, curve-following probes) as a post-pass that pulls
  each support edge onto the exact OSM track-bundle median — the one thing
  the paper doesn't do (their input is already OSM track geometry; our GTFS
  shapes need the pull).

Kept from our stack: strand chaining (stage 2), median refinement, the
sketch scorer + gates, color-trunked ordering, banded fairing + offset
contract, the atlas workbench.

Dropped (superseded): alongside-state segmentation, fork-event propagation,
piece unions, sliver glue, BERTH sample-matching, consumed-corridor
transition chaining (edges now meet AT nodes; connectors are short node-area
Béziers, never cross-complex chords).

## Parameters (defaults, all in the atlas tuning panel)

| dial | value | source |
|---|---|---|
| sample l | 5 m | paper |
| merge d̂ | 17.5 m | half a 4-track NYC bundle + slack; tune per network |
| convergence gap | 0.2 % | paper (empirical) |
| line-creep α | sin 45° | paper |
| turn-restriction t | 500 m → interval test | paper (we use sample-interval adjacency) |
