# Visual benchmark — portolan vs Apple Maps transit

The founding bar (PAR-12) is Apple Maps' transit rendering. This bench
compares portolan's ribbons against Apple at the named problem areas,
side by side at matched zoom, and scores each area. Run it after any
FAIR/ORDER/bundling change — the sound score is blind to most of what
this catches.

## Adding a problem spot

**From the atlas UI (preferred):** hit **+ area** in the map's problem
areas panel, drag a box around the bad geometry, name it — the generated
id lands in your clipboard, ready to paste into chat. Every area renders
as a labeled dashed box on the map; **✎** shows draggable corner handles
for resizing (saved on release); **⟳** re-captures that area's portolan
screenshot; the **P**/**A** dots track the two screenshots per area
(portolan render / Apple Maps reference — click a green dot to view,
click a missing A to copy the one-line capture command).

Or append a row to `locations.json` in the repo root by hand:

    ["my_spot", "Short description", 40.12345, -73.12345, "5"]

with an optional 6th element `[w, s, e, n]` for a drawn bbox. That one
file drives everything: the atlas map's problem areas, the sketch
editor's camera bookmarks, and both sides of the capture script. Then:

    tools/visual-bench.sh apple      # capture the new Apple reference(s)
    tools/visual-bench.sh portolan   # re-render portolan refs
    tools/visual-bench.sh index      # refresh the side-by-side page

(`?bare=1&snapall=1` picks up new spots on the next run automatically —
zoom defaults to 15.4; add an entry to SNAP_ZOOM in map.html for a
non-default zoom.)

## Capture recipe

`tools/visual-bench.sh` automates both sides into `refs/` (gitignored —
Apple imagery is copyrighted, local comparison only), and
`tools/visual-bench.sh index` writes `refs/index.html`, a side-by-side
viewer of every pair.

**portolan side** (`tools/visual-bench.sh portolan`, or open manually):

    http://127.0.0.1:8765/map?feed=<feed>&bare=1&snapall=1

walks every named problem area for the feed, forces ribbons+basemap
layers, and saves each rendered frame to `refs/portolan/<key>.png` via
`POST /api/snap` (the map snapshots its own canvas —
`preserveDrawingBuffer` is enabled in bare mode). The tab's title flips
to "snap done" when finished. `?bare=1#<zoom>/<lat>/<lon>` alone gives a
clean single view.

Both sides read the city list from `portolan.json`, so a new city benches
itself as soon as it has areas in `locations.json` (docs/CITIES.md). The
portolan side skips cities with no build output yet; pass one feed —
`tools/visual-bench.sh portolan london` — to snap just that city.

**Apple side** (`tools/visual-bench.sh apple`): drives Maps.app through
each area (`open "maps://?ll=<lat>,<lon>&z=16&t=r"` — `z` only loosely
honored; the transit style reads best at the ~750 ft scale bar) and
saves `screencapture` frames to `refs/apple/<key>.png`. Requires the
terminal to have **Screen Recording** permission (System Settings →
Privacy & Security), and takes over the desktop for ~2 minutes.

**Renderer gotchas learned capturing these:** the viewer's transition
easing depends on the MapLibre fork's variable line-offset — after
touching the fork, `npm run build-dist` in `../maplibre-gl-js` or the
atlas silently serves a stale build (the offsets bind garbage; this
exact staleness shipped the first three weeks of attempt four). And all
casings must render below all colored lines per band, or one kind's
white casing wipes another's ribbon at junction overlaps.

What Apple does that is easy to miss:
- **No part-time patterns.** Weekend reroutes (R via Manhattan Bridge)
  simply are not drawn. One canonical pattern per service.
- **No terminal relay loops.** The 1 and 4/5 end cleanly at South
  Ferry / Bowling Green; the balloon loops are omitted. (The owner's
  sketch DOES draw the Battery loop — the sketch stays the ground truth;
  Apple's choice is context.)
- **One bundle per corridor, whatever the physical track layout.** The
  Chicago Loop's inner/outer tracks render as a single five-ribbon
  bundle per side.

## Review — 2026-08-04 (post ghost-route fixes; all 26 areas, fresh builds)

Ranked findings, verified against current side-by-side refs:

1. **Chicago Loop inner/outer split is still THE Chicago defect**
   (`chi_loop`): orange+pink ride a separate rectangle a track-width
   inside brown+purple around the whole Loop. The el's paired tracks sit
   ~8 m apart — above the 4 m cross-service co-merge gate. Needs the
   corridor-level co-location merge, not a gate loosening (12 m would
   re-weld South Ferry).
2. **Layer-blind bundling** (`chi_blue_multilevel`, owner-drawn): the
   Blue Line SUBWAY under Lake St braids into the ELEVATED Loop bundle
   as a sixth ribbon. Bundling has no notion of layer/tunnel/level —
   needs the OSM layer class carried through MATCH→SPLIT and respected
   as a merge gate.
3. **Terminal balloons** (`bwling_green_loop` owner-drawn, `gc149`,
   `chi_ohare`): South Ferry 4·5 loop is a wobbly kidney with the 1
   kinking into it (dup 15.9%); the 5 draws a lasso around E 146 St
   (relay/yard trackage kept as revenue); O'Hare terminal forks into a
   stray Y with a floating tail. All law-16 lollipop family.
4. **Junction furniture wads** (`sixth_53` fork blob at 51 St,
   `chi_loop_se` corner X-tangle, `dekalb` squared double-90 corners on
   the B/Q Lafayette bend): the sub-60 m consume-and-chain FAIR task.
5. **Seam braids/wiggles** (`flatbush_ext` north of Atlantic,
   `delancey` M×J braid at Essex, `eighth_53` minor braid,
   `myrtle_bway` M sags in a U below Myrtle before rejoining the el):
   merge-seam joints + one bad node meet.
6. **Small wobbles** (`rector` R S-wobble on Trinity Pl,
   `fulton_cityhall` yellow R S-hook at City Hall, `seventh_57` flat
   apex on the Q park curve, `schermerhorn` blue elbow at Livingston).

Fixed and verified this pass: `franklin` (2·3 red now clearly beside
4·5 green — old defect #6 gone), `park_place` (turn attaches the right
way), `canal_ace` (tight bend kept, clean), `bklyn_bridge` (J/Z × 4·5·6
weave crosses cleanly), `times_sq` (no red spur bleed), `borough_hall`
(near-Apple), `bway_junction` (acceptable), `grand_army`/`dekalb`
bundles hold, and no ghost geometry anywhere — zero gap bridges.

Capture notes: portolan snaps must wait on map.loaded() polling (idle
events starve under background-tab throttling — three empty refs
shipped that way before the fix); Apple zoom now derives from each
area's bbox span.

## Defect catalog — 2026-08-03 evening review (fixed renderer)

Reviewed all pairs in refs/index.html after the variable-offset fixes.
Per-defect: what's wrong → why → the correction.

**0. Grand Army Plaza / Flatbush run (owner-reported, FIXED).** B
dead-ended mid-plaza, Q ran alone through the park, 2·3 wove across
4·5. Two root causes, both structural: (a) through-member scoping broke
on megachain edges — the Q's 14.7 km edge required mates along 80% of
the WHOLE chain, so the 1.3 km tunnel tracks never qualified and
refinement froze there (fix: corridor-scale cap ~1.5 km in
bundle.throughMembers — also sped SPLIT 2× and improved fwd p90 to
~2.2 m); (b) the merge budget (60 rounds, one merge each) exhausted
before the B/Q pair's turn (fix: 600). Result: Brighton is one
[B|Q] corridor for 11 km and upper Flatbush a four-slot
[4·5|2·3|B|Q] bundle — matches Apple. Residual: merge-seam polish at
the plaza fork, Franklin S shuttle floating ends, orange gap-bridge
L-corner SW of the park (defect classes 2 and follow-ups of 1).

**1. Unbundled shared corridors (the systemic one — owner-reported).**
Schermerhorn (A/C+G), Jay St (A/C/F), Chicago Loop inner/outer, B/Q
Brighton: co-located corridors overlapped instead of slotting side by
side. Why: SPLIT only merged same-service edges; cross-service corridors
refine onto one median but stay separate edges, each slotted alone.
Correction (IN PROGRESS, validated at Hoyt): cross-service edge merge
when sustained co-located ≤4 m post-refinement (`split_co_merge_dist`).
Follow-ups: merge-seam kinks (new 72° spikes at Hoyt = merge boundary
joints), interval completeness (E Schermerhorn still collapses), and
re-verify South Ferry loops stay distinct.

**2. Gap-bridge chords (owner: "what are these fucking things") — RESOLVED
2026-08-04 with the ghost-route fixes.** NYC now builds with ZERO gap
bridges: the "honest data holes" were mostly weld failures (ways ending
on another way's segment between vertices — fixed by endpoint→segment
snapping) and the loader dropping service=crossover ways (the links
between running tracks). The R's SoHo diagonal was a third failure mode:
sparse shape vertices sitting on the wrong street matched the Lexington
because the true corridor was outside candidate reach — fixed by
per-sample confidence (emission weight + reach scaled by vertex
spacing). Original diagnosis below for history.

**2b. (historical) Gap-bridge chords.**
Diagnosed generically via TestGapConnectivity (walkable distance across
every bridge's anchors): two species. (a) REACHABLE-BUT-BLOCKED — the
graph walk to real track exceeded MaxWalk 350 m (M Chrystie needed
740 m, an E connector 391 m): fixed by `match_max_walk` = 1500. (b)
HONEST DATA HOLES — no nearby graph nodes at all (E's Queens Blvd trio:
the tunnel is missing from this OSM extract; R-reroute unreachable; FX
express, 5 Mott Haven detour 28–44 km): these bridges are law-8 correct.
Bridges now render EXACTLY like regular ribbons (no dasharray — which
also would break variable offsets via the lineSDF program path). Better
OSM extracts eliminate species (b); no pipeline hack should.

**3. Terminal balloon/V structures.** South Ferry 4·5 loop throat
(tuning-fork pinch), Coney Island terminal fan spike. Correction: law-16
lollipop pass in SPLIT (ring detection → merged throat + one closed
pass).

**4. Junction fan swing + furniture.** 6 Av/53 trident and 8 Av/53
curves swing wide of the physical junction (60 m cuts + blends); Tower 18
micro-edge stacks. Correction: free-area sized to slot count instead of
fixed CutBase; consume sub-60 m edges and chain transitions through
(spec law).

**5. Small kinks (jag gate).** 5's S-jog N of 149 GC junction; Bway/Canal
R-divergence 47°. Mostly merge/meet geometry at nodes — same family as
#1 follow-ups and #4.

**6. Missing second color on shared trunk.** Franklin Av: Eastern Pkwy
shows green only — 2·3 red renders under 4·5 green (same slot) or is
absent; investigate ORDER slots on the merged [2,3,4,5] corridor.

**7. Non-issues (verified good).** 7 Av/57 St Q curve; Hoyt–Schermerhorn
A/C S-curve (the named "kink" is gone); Myrtle-Broadway M/J·Z after the
renderer fixes; Times Sq red spur absent (no attribution bleed); Canal
A·C·E bend kept tight; DeKalb Flatbush bundle through the S-curve.

## Scores — 2026-08-03 build (pre-renderer-fix; rescore pending)

Scale: 10 = indistinguishable from Apple at a glance.

### Canal St / Manhattan Bridge approach (feed 5) — 6.5/10
Matches: 1/6/A·C·E/N·Q·R·W trunk alignments, J·Z Kenmare→Centre sweep
(genuinely Apple-quality curve), orange Chrystie connection, bridge
approach geometry.
Defects, ranked:
1. **R weekend-reroute bridge** (`kind:bridge`, R, 1.6 km): dashed
   diagonal slicing through SoHo blocks + a dotted shadow along
   Broadway. Apple doesn't draw reroutes at all. Fix is two-part:
   MATCH should land this pattern on the N·Q bridge tracks it really
   uses (they're ridden and present), and coverage-pruning arguably
   shouldn't keep one-off reroute shapes at all (`cover` dial).
2. Minor: yellow bridge-divergence at Broadway/Canal turns tighter than
   Apple's rounded corner (post-arc-fix it is ~40°, acceptable).

### DeKalb Av / Flatbush trunk (feed 5) — 7/10
Matches: the Manhattan Bridge→Flatbush multi-color bundle holds parallel
spacing through the whole S-curve; 4th Av fan; Eastern Pkwy greens;
A/C/G Schermerhorn→Lafayette sweep.
Defects:
1. White casing wedge / fan tangle at the bridge-landing junction
   (north of DeKalb) — junction fan-out artifact class.
2. B and Q appear on split alignments south of DeKalb where Apple shows
   one orange+yellow Brighton pair — same-corridor pairing (check
   whether the Brighton edges merged).

### South Ferry / Bowling Green (feed 5) — 6/10
Matches: 2/3 Clark St sweep, R Montague crossing, 4/5 Joralemon
crossing, J·Z Broad St terminal stub.
Defects:
1. The 4·5 Battery loop throat is a pinched two-leg "tuning fork" with
   a red/green tangle — needs the law-16 lollipop (merged throat, one
   closed pass). Apple omits loops entirely; the owner draws them, so we
   render the balloon but must render it cleanly.
2. The 1's terminal hook overlaps the loop area.

### Chicago Loop (feed 29) — 6/10
Matches: Loop rectangle with parallel ribbons, Red/State and Blue/
Dearborn subways, three of four corners rounded cleanly, Lake St branch.
Defects:
1. **Inner/outer Loop tracks not visually bundled**: orange+pink ride a
   separate parallel alignment from brown+purple around the rectangle
   (opposite-direction track pairs matched to different physical tracks;
   cross-color co-location merge is deferred). Apple: one five-ribbon
   bundle per side. This is the single biggest gap to the benchmark in
   Chicago.
2. Tower 18 (Lake/Wells): white-casing tangle where the fans meet — the
   4-way junction fan-out class.
3. SE corner exit toward Roosevelt: casing overlap tangle.

## Cross-cutting defect ranking (what to fix, in order of visual payoff)

1. Cross-color corridor bundling for opposite-direction track pairs
   (Chicago Loop inner/outer; likely also B/Q Brighton) — extends the
   same-service edge merge to tight co-location across services.
2. Part-time reroute patterns: fix the R-via-bridge match (tracks exist,
   are ridden by N/Q) and/or drop sub-threshold reroute shapes at CHART.
3. Junction fan-out casing tangles (bridge landing, Tower 18, Loop SE) —
   FAIR's free-area blend handles 2-way pairs; multi-pair node fronts
   need coordinated per-color reconnection.
4. Law-16 balloon lollipop (South Ferry throat).
