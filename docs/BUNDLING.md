# Bundle-centerline spec (owner's hand-drawing rules)

How the owner draws the reference network, written down as the rules the pipeline
must reproduce. Source: hand-drawing NYC/Chicago in the sketch editor and
introspecting the choices. The drawn networks (`sketches/network-*.json`) are the
ground truth; `sketch_score.py` measures conformance.

## Centerline by parallel track count

The unit is the **strand**: one parallel track of the mainline bundle (in our data,
roughly one directional matched shape path).

| strands | centerline rule                                            |
|---------|------------------------------------------------------------|
| 1       | follow the real track geometry exactly                     |
| 2       | midpoint between the two tracks                            |
| 3       | follow the CENTER track                                    |
| 4       | midpoint of the MIDDLE TWO (treat them as a double track)  |
| >4      | extras are yards/spurs, not mainline — IGNORE them         |

Symmetric per-sample averaging of member strands already approximates rules 1-4
(mean of 2 = midpoint; mean of 3 ≈ center; mean of 4 ≈ middle-two midpoint).
Divergence appears when membership is asymmetric (a yard shape joins, a strand
drops in/out near stations) — the fixes are strand counting and yard exclusion,
not a different averaging formula.

## Forks (junctions)

* A junction should appear **each time the physical tracks fork** — and the
  transition through it should know the parallel-track count on each side.
* Quad → two doubles splits two ways:
  - **center/outer** pairs: the through-centerline DOES NOT MOVE (middle two keep
    being the middle two). Easy: no lateral shift, just a colour split.
  - **left/right** pairs: the centerline SHIFTS half a track-pair. The shift is
    rendered with the MapLibre fork's offset transitions (off_from_px → off_to_px
    easing through the junction), not by bending the drawn geometry sharply.
* Detecting which split type: compare each branch's strand set to the parent's
  strand positions (did the branch take {2,3} of 1234 — center — or {1,2} — left).

## Bundling is visual, not just physical

Two lines bundle when they are **drawn on top of each other**, even with no
physical track connection — e.g. the Chicago Loop: the underground Blue line runs
beneath the elevated Loop tracks; they never touch, but in 2D they overlap, so
they form one bundle and fan out by slot. (This is why the kiss/never-touch
dissolve tests 2D separation, not connectivity: tracks that OVERLAP in plan view
stay bundled; tracks that merely pass NEAR each other — a kiss — split.)

## Mapping to the current pipeline

| spec element                | current state                                          |
|-----------------------------|--------------------------------------------------------|
| 1-4 strand centerline       | ~approximated by per-sample weighted averaging          |
| >4 yard exclusion           | partial: skeleton input is matched ROUTE shapes, so    |
|                             | un-serviced yard tracks never enter; a route shape that |
|                             | rides a yard/relay (turnbacks) still pollutes — the     |
|                             | shape-evidence prune helps but strand-aware exclusion   |
|                             | does not exist yet                                      |
| junctions at physical forks | skeleton forks where the raster band splits (thick_m-   |
|                             | dependent), not semantically; kiss-split/dissolve undo  |
|                             | the false positives                                     |
| strand-count at transitions | NOT tracked; the engine knows colour slots, not strands |
| center/outer vs left/right  | NOT classified; all splits treated alike (engine eases  |
|                             | offsets, which happens to handle both, unaware)         |
| overlap => bundle (Loop)    | works: raster union fuses coincident 2D geometry; the   |
|                             | never-touch dissolve keeps overlapping tracks bundled   |

## Optimization loop

1. Owner draws the ideal network (sketch editor, per feed).
2. `sketch_score.py <build> <feed>` grades builds against the drawing.
3. Pipeline changes must improve the score without dropping coverage.
4. When NYC + Chicago score clean, the same tuning generates other cities.
