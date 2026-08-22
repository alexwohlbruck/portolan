# Dynamic service — one layout, any timestamp

Status: **design, adopted 2026-08-08** (owner call: the target is a map of
the world with every GTFS feed combined, so per-timetable prebuilds cannot
be the product). Grounded in measurements below; implementation staged at
the end. Supersedes the prebuild-centric framing of
docs/SERVICE-SCENARIOS.md — the scenario machinery survives, but as a
derivation and QA tool, not as the rendering unit.

## The constraint

At world scale there is no such thing as "build the map for Saturday":
every region partitions the week differently, feeds update on their own
cadence, and a tile pyramid cannot be patchworked out of per-region
timetable variants. The renderable artifact has to be **one layout per
region**, with time applied at render.

One fact keeps this from being combinatorially scary: service is local.
NYC's weekend and Chicago's weekday never interact — a world map at time T
is each region resolved independently. The problem was never a
cross-product of schedules; it is that even linear per-region prebuilds
are the wrong shape for tiles and for a viewer that should answer any
timestamp.

## The measurement that makes it possible

The fear was that hiding lines requires re-running ORDER (the slot
optimizer) per timestamp — which would mean either prebuilding layouts or
porting an optimizer to the client. Measured on NYC, union build vs the
Sat 07–22 scenario, 821 shared steady edges at band 15:

- **Relative slot order is preserved on 821 of 821 edges (100%).** A
  scenario only ever REMOVES lines from a bundle; it never reorders the
  survivors. ORDER's answer on the union is ORDER's answer on every
  subset.
- **93.5% of scenario offsets are reproduced exactly** by dropping hidden
  ribbons and re-centering the survivors at the union pitch. The rest are
  corridor bundles spanning twin edges where per-geometry grouping
  undercounts the bundle — a grouping refinement, not an order change.

So the dynamic rule is arithmetic, not optimization:

```
visible = ribbons on this corridor with any route active at T
offset(r) = (rank of r among visible, in UNION slot order − (n−1)/2) · pitch
```

No optimizer in the client. The union layout is the single source of
order; time only filters and re-centers.

## Architecture

**1. One union layout per region.** MATCH → SPLIT → ORDER → FAIR run once,
over every pattern that ever runs — exactly today's union build. This is
the only artifact with geometry in it. Junction work, refinement, turn
tracing: all here, all timetable-independent.

**2. Activity is data on the ribbon, not new geometry.** Each ribbon
carries, per route, a **168-bit weekly activity mask** (7 days × 24
hours) derived from the same calendar machinery scenarios use
(internal/gtfs/service.go — day masks × trip spans, calendar_dates
fallback, frequencies). Masks answer "does route R run at (day, hour)"
in one bit test. GTFS's own structure is weekly, so any date in the year
resolves through its weekday; holidays deliberately follow regular
service.

Masks are **per segment, not per route** (`acts` on each feature): SPLIT
ORs each pattern's mask onto the edges that pattern actually rides, so a
short-turned route's tail carries only the full-length pattern's hours
and goes dark when only the short variant runs. Transitions carry the
AND of their two sides — a movement runs only when the route rides both.
Route-level masks (/api/activity) remain as the toolbar summary and the
fallback for builds that predate acts.

Each ribbon also carries `ridx`, naming the slot every route owns in its
own `acts` string: `"A=00;C=01;E=02"`. The masks ride at a fixed stride,
so a consumer that knows the slot can read ONE route's hours exactly —
but a renderer's filter expression can slice a string at a computed
offset and cannot count the commas in `routes`, and route ids are not
fixed width. Publishing the slot costs two digits per route, a fifth of
what repeating the masks would, and it is what lets a client isolate a
single line honestly: "is the 3 running on this track" rather than "is
anything running on this track", which on shared trunk (2/3, A/C — most
of a big system) are different questions with different answers at 3am.
Absent on builds that predate it, and a consumer should fall back to the
per-segment union rather than drawing nothing.

The mid-edge short-turn residual is RESOLVED by terminal cuts
(stages.CutSegmentsAtTerminals, a post-FAIR pass): emitted segments are
cut at mid-segment pattern terminals — anchored at the pattern's
terminal STOP, since shapes overrun terminals with tail trackage — and
every piece recomputes each route's activity from actual pattern
coverage. No ORDER/FAIR decision changes; seams are same-offset
endpoint joins under round caps. This is what makes the M's weekend
map end at Delancey/Essex and its late-night map at Myrtle Av, matching
the MTA's own service maps (checked invariants in check:dynamic). A
pattern that merely tip-touches an edge's last metres no longer donates
its hours to the whole edge (the overnight shuttle at Myrtle).

**3. Render time = filter + re-center.** At timestamp T the client:
   - hides ribbons whose routes are all inactive at T,
   - re-centers the survivors within the fixed union order (formula
     above, grouped per corridor, pitch from the union build),
   - re-derives transition ramps between the dynamic offsets of adjacent
     corridors.

   No round-trip, no layout, no new geometry. The console already
   assembles FeatureCollections client-side from the content-addressed
   geometry cache (`/api/build-delta`), so offsets are rewritten at
   assembly and handed to the existing fork via `setData` — the fork's
   variable line-offset is a data-driven paint property, nothing new
   needed from the renderer. (At tile scale later, the same values move
   through `feature-state` instead of `setData`; the math is identical.)

**4. Server-side scenarios become derivation + QA.** The drawn-ink
equivalence pass still answers "which distinct maps exist this week" —
that is how the Service page paints its grid, and it is the *test
harness* for the dynamic path: for each scenario, compare the dynamic
render at its hours against the full per-scenario re-layout and score the
divergence. Prebuilt scenario files stop being the product and become
reference outputs.

## What dynamic rendering gives up, knowingly

Three deviations from a full per-timestamp re-layout, all bounded by the
measurements:

- **Inert junctions keep their union treatment.** FAIR's cuts and fillets
  are computed against the union junction set, so a junction whose
  diverging branch is asleep at 2am still draws as a junction (a cut and
  a fillet where a through-run could be seamless). This is the same
  blocker stable segmentation had (docs/SCENARIO-DELTA.md); fixing it
  properly means FAIR learning per-mask inertness, which stays on the
  roadmap. Visually it is a subtle over-articulation, never a wrong line.
- **The ~6.5% corridor-grouping cases** need corridor-level (not
  per-edge) grouping to re-center exactly; until then those bundles
  re-center slightly differently than a dedicated re-layout would.
- **Re-cut edges.** A dedicated re-layout re-segments around vanished
  junctions and re-smooths; dynamic rendering keeps the union's cuts.
  This is invisible at ribbon width except at the inert junctions above.

The QA harness in §4 exists precisely to keep these three honest — the
divergence is measurable per scenario, continuously.

## Staging

Stages 1–2 are BUILT (see below). 3 is partially standing (the
`check:dynamic` harness); 4–5 remain.

1. **Emit masks + union order.** ✅ FAIR already knows each ribbon's routes
   and slots; add per-route activity masks (hex-encoded, 42 chars) to
   segment properties, or as a sidecar `route → mask` table per region
   (smaller — masks repeat per route, not per segment).
2. **Client dynamic mode.** ✅ Timestamp → bit test → filter + re-center +
   ramp rewrite at FeatureCollection assembly. The time picker UI is
   unchanged; "not built yet" states disappear — every timestamp renders
   immediately.
3. **QA loop.** (first cut exists: `npm run check:dynamic` scores the render rule against the real Saturday re-layout — 96.9% offset match on 843 shared edges, zero sleeping ribbons surviving.) Score dynamic-at-T against the prebuilt scenario for each
   derived scenario; gate on divergence. Keep prebuilds only as test
   references.
4. **Inert junctions in FAIR** (the drawing-risk item, done with eyes on
   the map): through-run treatment where a mask says nothing diverges.
5. **Tiles.** Same data, `feature-state` instead of `setData`; per-region
   resolution stays local, so the world map is exactly the sum of its
   regions.

## Relationship to existing pieces

- `/api/build-delta` (content-addressed geometry) remains the transport:
  dynamic mode makes its cache hit rate *better* — geometry never changes
  with time at all.
- `?band=N` remains: bands are a zoom concern, orthogonal to time.
- The `?t=` timestamp UI remains as-is; it stops resolving to prebuilt
  files and starts driving the filter directly.
- `portolan scenarios` and the Service grid remain as the human-readable
  view of the week's structure.
