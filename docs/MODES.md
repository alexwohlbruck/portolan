# Modes and trunking

portolan currently draws metros. `chart` keeps `route_type` 0/1/2 and the
100-series ([pipeline.go:174](../internal/pipeline/pipeline.go)), `osm.Load`
keeps `railway ∈ {rail,subway,light_rail,tram}`, and `Order` trunks by
colour ("law 5: same-colour routes share one ribbon",
[order.go:13](../internal/stages/order.go)). That set of assumptions holds
for NYC and Chicago and breaks for everything else.

This document is the design for every other mode. **No pipeline code has
been written against it yet** — it is the plan, and the parts marked
*inferred* need the observation pass in the last section before they get
built.

## Why this is not a filter change

Three assumptions fail at once when the mode set widens:

1. **Infrastructure.** Buses ride streets, ferries ride water, gondolas
   ride cables. `osm.Load` only loads rails. `Match`'s own doc comment
   already promises "rails for trains, roads for buses, sea routes for
   ferries" ([match.go:127](../internal/stages/match.go)) — the matcher was
   designed for this; the loader never caught up.
2. **The trunk unit.** Colour works because subway lines have distinct
   colours. Every bus in a city shares one colour, so 40 routes on one
   street collapse into a single indistinguishable group; commuter rail
   frequently has no `route_color` at all, so everything colourless trunks
   together into `888888`.
3. **Density.** A metro has tens of patterns; a city bus network has
   thousands. Drawing them at the same zoom as the subway is what makes a
   global map unreadable — the exact "10+ route bundles" failure.

## Mode classes

GTFS route types (basic and the extended HVT ranges) collapse into eight
classes. Everything downstream keys off the class, never the raw number.

| class | route_type | infrastructure | typical |
|---|---|---|---|
| `metro` | 1, 400–404 | `railway=subway` | NYC subway, Métro, U-Bahn |
| `tram` | 0, 900–906 | `railway=tram\|light_rail` | streetcar, LRT, Chicago's own tram tags |
| `regional` | 2, 100–117, 200–209 | `railway=rail` | commuter, S-Bahn, intercity, high-speed, coach |
| `monorail` | 12, 405 | `railway=monorail` | Tokyo Haneda, Seattle |
| `funicular` | 7, 1400 | `railway=funicular` | Angels Flight |
| `cable` | 5, 1701 | `railway=tram` + `cable=yes` | SF cable cars |
| `aerial` | 6, 1300 | `aerialway=*` | Roosevelt Island Tram, Medellín |
| `ferry` | 4, 1000, 1200 | none — GTFS shape only | Staten Island, Thames Clipper |
| `bus` | 3, 11, 700–716, 800 | `highway=*` | everything else |

Two notes that matter:

- **GTFS has no BRT type.** Apple visibly promotes BRT to rail-like
  prominence, but nothing in the feed says "this is BRT". It has to be
  derived — headway from `stop_times` (the service-scenario code in
  `internal/gtfs/service.go` already computes exactly this), or agency
  route naming. Treat BRT as a *promotion rule* applied to `bus`, not a
  class of its own.
- **`ferry` needs no infrastructure layer.** There is nothing to match to;
  the GTFS shape is the geometry. That makes ferries the cheapest mode to
  add and a good first proof that the class machinery works.

## The trunk key

The single most important change. Today:

```go
colorOf := func(rid string) string { … }   // order.go:23
```

Becomes a mode-aware key. Same-key routes share one ribbon; the key *is*
the slot unit:

| class | trunk key | consequence |
|---|---|---|
| `metro`, `tram`, `monorail` | `color` (law 5, unchanged) | NYC/Chicago behaviour is bit-for-bit preserved |
| `regional` | `color`, falling back to `agency:class` | colourless commuter stops collapsing into one grey trunk |
| `bus` | **`corridor`** — the edge id | any number of bus routes on a street render as exactly one ribbon |
| `ferry`, `aerial`, `funicular`, `cable` | `route` | too few to bundle; never merge them |

Corridor trunking for buses is what caps complexity globally: a street's
ribbon count stops depending on how many routes ride it. Route identity
moves to the label and to selection, which is where Apple puts it.

The fallback rule matters as much as the key. `regional` keyed on colour
alone would put every colourless commuter operator in one trunk — the
`888888` bug at city scale.

## Zoom floors *(inferred — see observation pass)*

`fairBands` ([fair.go:47](../internal/stages/fair.go)) already emits one
copy per band: `{15–24, 14–15, 13–14, 0–13}`. A per-class floor drops a
mode's segments below its band rather than adding new machinery:

| class | floor | rationale |
|---|---|---|
| `metro`, `regional` | band 0 | the skeleton of the city; visible at every zoom |
| `tram`, `monorail` | band 13 | too dense to read at metro scale |
| `ferry` | band 13 | few routes, long geometry, reads fine when zoomed out |
| `bus` corridor | band 15 | top band only — this is the density valve |
| `aerial`, `funicular`, `cable` | band 15 | short, local, invisible at range |

Every row is an inference from general cartographic practice plus the one
documented Apple behaviour (BRT gets rail-like prominence). None of it is
measured yet.

## Render treatment

`Segment` gains a `Mode` field so the style layer stops inferring intent
from `route_type`:

- `metro`/`regional` — current ribbon treatment, full width and casing
- `tram`/`monorail` — thinner, same casing
- `ferry` — dashed, no casing (the existing `Gap` dash pattern generalises)
- `aerial`/`funicular`/`cable` — thin, dotted
- `bus` corridor — thinnest, neutral colour, no per-route colouring

## Where the code changes

| what | where |
|---|---|
| taxonomy + class helper | new `internal/mode`, consumed by gtfs and stages |
| mode filter | [pipeline.go:174](../internal/pipeline/pipeline.go) |
| infrastructure layers | [osm.go:21](../internal/osm/osm.go) `railValues` → per-class tag sets |
| mode↔way compatibility | [match.go:108](../internal/stages/match.go) `classCompat` |
| trunk key | [order.go:23](../internal/stages/order.go) `colorOf` → `trunkKey` |
| band floor, width class | [fair.go:47](../internal/stages/fair.go), [fair.go:125](../internal/stages/fair.go) |
| `Mode` on the segment | [stages.go:61](../internal/stages/stages.go) |
| per-mode styling | `internal/atlas/map.html` |

## Order of work

1. **Ferry** — no infrastructure, no bundling. Proves the class machinery
   end to end against NYC (Staten Island) and London (Thames Clipper).
2. **Rail family** — `tram`, `monorail`, `funicular`, `cable` ride the
   extract that already exists; mostly `classCompat` and band floors.
3. **`regional` trunk-key fallback** — the colourless-commuter fix. Testable
   today: London's Overground and Paris's Transilien are both in feeds we
   already build.
4. **Aerial** — needs `aerialway` in the extract; small and self-contained.
5. **Bus** — the big one, deliberately last: highways in the extract
   (10–100× the ways), road-aware matching, corridor trunking, and the BRT
   promotion rule. Nothing before it depends on it.

Steps 1–4 are the "rail family + ferry/gondola" scope; step 5 is the fork.

## The observation pass

Apple publishes no per-mode trunking rules. Searching turned up exactly two
attested facts: BRT and frequent trunkline bus get rail-like prominence
(unlike Google), and information density changes at every zoom —
[Trillium's WWDC 2016 writeup](https://trilliumtransit.com/2016/06/20/wwdc-2016/),
[Apple's WWDC talk](https://developer.apple.com/videos/play/wwdc2016/241/).
Transit's [engineering post](https://blog.transitapp.com/how-we-built-the-worlds-prettiest-auto-generated-transit-maps-12d0c6fa502f/)
is the closest published account of this problem class, and it bundles by
raster skeletonization — which `docs/LESSONS.md` rules out here.

So the zoom floors above must be measured, not reasoned. `locations.json`
now carries Apple-reference areas on feeds we already build, all chosen
because several modes overlap there:

| key | feed | what it should settle |
|---|---|---|
| `ref_whitehall_ferry` | 5 | is the ferry drawn? dashed? at what zoom does it appear |
| `ref_penn_station` | 5 | commuter rail vs subway weight; colourless-operator handling |
| `ref_fordham_bus` | 5 | bus corridor: one line or many; the zoom it switches on |
| `ref_gline_brt` | la | the documented BRT promotion, in the wild |
| `ref_canary_wharf` | london | DLR + river bus + Underground in one frame |

Capture takes over the desktop for a couple of minutes and needs Screen
Recording permission, so it is a hands-on step:

    tools/visual-bench.sh apple ref_whitehall_ferry

Zoom in and out on each and record where lines appear and disappear. Every
row in the zoom-floor table is a hypothesis until that is done.
