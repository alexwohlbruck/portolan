# Test cities

Every city portolan builds is one row in `portolan.json` — a GTFS zip, an
OSM rail extract, an output path, a drawn network to score against, and the
Overpass window the extract came from. There is **no city-specific code**:
that's the claim the extra cities exist to keep honest. NYC and Chicago are
the two the algorithm was tuned on; the seven below widen the shapes it has
to survive (LA's street-running light rail, London's flat junctions, Tokyo's
JR bundles under private-railway viaducts, Paris and Berlin's mixed
metro/S-Bahn/RER stacks, and two small US systems where a single wrong
bundle is the whole map).

| feed key | city | GTFS source | builds | notes |
|---|---|---|---|---|
| `5` | NYC | mirror `5.zip` | ✅ 15 lines | the tuning city, scored |
| `29` | Chicago | mirror `29.zip` | ✅ | Loop topology correct |
| `atlanta` | Atlanta (MARTA) | mirror `17.zip` | ✅ 60 seg | 4 lines + streetcar |
| `charlotte` | Charlotte (CATS) | mirror `886.zip` | ✅ 8 seg | Blue Line + Gold streetcar |
| `la` | LA Metro | Transitland `734` | ✅ 112 seg | rail-only feed; 6 lines |
| `london` | London (TfL) | Transitland `9788` + pfaedle | ✅ 2898 seg | Interline's conversion ships SKELETAL shapes (2 per line, station-to-station chords) — `tools/city.sh shapes london` re-matches them; see below |
| `paris` | Paris (RATP) | Transitland `762` | ⚠️ 4690 seg | Metro/tram correct; RER + Transilien + TER stray outside the window (below) |
| `berlin` | Berlin (BVG) | Transitland `1268`, subset + pfaedle | ✅ 1358 seg | U1–U9 + 89 tram labels; no S-Bahn (below) |
| `tokyo` | Tokyo Metro | Transitland `8923` + pfaedle | ✅ 132 seg | all 9 lines, official colours |

## Feeds without shapes

`chart` map-matches route shapes onto the bundle graph, so a feed with no
`shapes.txt` cannot be charted at all. Both Berlin and Tokyo shipped that
way, and both are fixed the same way: **[pfaedle](https://github.com/ad-freiburg/pfaedle)
map-matches the feed against OSM and writes the shapes** (same research
group as LOOM). Docker is the least painful install:

    # 1. an OSM window for the city, as XML with the referenced nodes
    #    [out:xml][timeout:900];
    #    way["railway"](s,w,n,e); (._;>;); out;
    # 2. match — -m takes tram,bus,coach,rail,subway,ferry,funicular,gondola
    docker run -i --rm -v $PWD/osm:/osm -v $PWD/gtfs:/gtfs -v $PWD/out:/gtfs-out \
      ghcr.io/ad-freiburg/pfaedle:latest -x /osm/tokyo.osm -i /gtfs/tokyo.zip -m subway
    # 3. zip the output back over data/gtfs/<city>.zip

Tokyo: 9539 trips matched in about a second against a 12 MB window. This is
the general answer for GTFS-JP, which routinely omits shapes — neither
Tokyo Metro (`8923`) nor Toei (`8922`) has them.

All of the above is now one command — it fetches the OSM XML window from
the city's bbox, derives `-m` from `routes.txt`, and zips the result back
over the feed (original kept at `<gtfs>.bak`):

    tools/city.sh shapes london

**Skeletal shapes are worse than none.** London's Interline conversion HAS
a `shapes.txt` — 965 rows for the whole network, two shapes per line
("bakerloo-inbound"), consecutive points 1.7 km apart: station-to-station
chords, one polyline shared by every branch. chart's Viterbi followed
those chords onto whichever tracks they grazed — Bakerloo rode the Met's
Allsop Place curve at Baker St, the Central tangled at North Acton, the
Northern lost its entire Bank branch (one shape can only trace one
branch), and the tube pairs braided at Waterloo. pfaedle's `-D` flag
(part of `tools/city.sh shapes`) drops the feed's own shapes first —
without it pfaedle keeps whatever is there and returns the feed
unchanged. After re-matching: 62,423 trips, 168,886 shape rows, 344
patterns, and every branch draws on its own steel.

**National feeds must be subset first.** `1268 f-germany~urban~transport` is
240 MB and 21k routes covering all of Germany; pfaedle bills per trip, and
`chart` would try to match every German bus against a Berlin-only rail
extract. `tools/gtfssubset.py` cuts one operator out, cascading agencies →
routes → trips → stop_times → stops → calendars → shapes:

    tools/gtfssubset.py --agency 353 data/gtfs/berlin.zip berlin-bvg.zip

Agency `353` is Berliner Verkehrsbetriebe: 250 routes, 139k trips, U-Bahn +
tram + 6 ferries. It streams `stop_times.txt` rather than loading it —
the national copy is multiple GB uncompressed, and the dict-per-row version
of this script did not finish in ten minutes.

**Berlin has no S-Bahn.** Feed `1268` carries none as rail: every `S1`/`S41`
in it is a `route_type` 3 replacement bus. The S-Bahn lives in
`1267 f-germany~regional~rail`, and merging two feeds into one city is not
something `portolan.json` can express today — one `gtfs` path per city.
Transitland has no VBB or BVG-named feed at all (searched; the search
endpoint works, `marta` → `17`).

**Paris strays.** Feed `762` bundles RER (A–E), Transilien (H/J/L/N/P) and
TER in with the Metro and trams. Those run hundreds of km past any
Île-de-France window, so 1373 of 4690 segments fall outside
`build/paris-rail.geojson` and render as raw shape chords rather than
track-following ribbons. The Metro/tram core — the part that matters for
PAR-12 — is clean. Widening the bbox to cover TER is not practical (see
Tokyo below); a Metro-only feed would be the fix.

**Overpass windows have a ceiling.** Tokyo's original
`139.55,35.5,139.92,35.83` window 504'd on every endpoint and every retry —
too much rail. It is narrowed to the core (`139.65,35.6,139.85,35.78`),
which covers all nine Tokyo Metro lines and returns in one request. Expect
to do the same for any dense metro. The cost is small strays of its own:
the Tozai Line runs east to 139.96, past the window's edge.

"local mirror" is `~/Documents/code/barrelman/data/gtfs/<id>.zip`. Atlanta
and Charlotte are already there (MARTA's 4 heavy-rail routes + the
streetcar; CATS' Lynx Blue Line + Gold Line) — the other five are not, and
the mirror holds no non-US feeds at all.

    tools/city.sh list          # the table above, live: which inputs exist

## Bringing a city up

**1. GTFS.** Drop the zip where `portolan.json` expects it —
`data/gtfs/<city>.zip` (gitignored) for the five downloads. Feeds may be
full multimodal city feeds: `chart` keeps only `route_type` 0/1/2 and the
100-series extended rail types, so the bus half costs a few seconds of
parse and nothing else.

Transitland indexes most of them, searchable by the bbox already in
`portolan.json`:

    export TRANSITLAND_API_KEY=tlk_…      # free: transit.land/users/sign_up
    tools/city.sh gtfs paris              # feeds covering the Paris window
    tools/city.sh gtfs paris 1234         # download one to data/gtfs/paris.zip

Listing and downloading are separate on purpose: a metro bbox matches every
bus operator in the region, and only you can say which feed is the city's
rail network. The download uses
`/api/v2/rest/feeds/<id>/download_latest_feed_version`, falling back to the
operator's own `urls.static_current` when the licence bars redistribution —
which is exactly what happens for the feeds marked ⚠ below.

This is the same registry the barrelman mirror was built from, which is why
its zips are named by Transitland's numeric feed id (`5` = MTA NYCT, `29` =
CTA, `17` = MARTA, `886` = CATS). `city.sh gtfs` refuses to write into that
shared mirror — it only fills repo-local `data/gtfs/` paths.

**2. Rail extract.**

    tools/city.sh rail london   # or: make rail CITY=london

One Overpass query for that city's `bbox`, converted to the geojson shape
`internal/osm` reads. Every `service` value is kept — `osm.Load` drops
yards/sidings/spurs itself and *needs* the crossovers. Big windows (Tokyo,
London, Paris) take minutes and run into the public Overpass instance's
rate limits; set `OVERPASS_URL` to another endpoint if it stalls, or narrow
the `bbox` in `portolan.json`.

**3. Build.**

    tools/city.sh build london  # or: make city CITY=london
    tools/city.sh all london    # rail + build

**4. Draw the ground truth.** Open `portolan atlas`, pick the city in the
feed dropdown, switch to **sketch**, and draw the network — it saves to
`sketches/network-<feed>.json` and `sound` starts scoring against it. Until
then `build` charts the city and says so instead of scoring.

Each city ships with two seed problem areas in `locations.json` (junctions
where interlining is hardest — Five Points, 7th St/Metro Center, King's
Cross, Châtelet, Alexanderplatz, Ōtemachi…). They drive the atlas place
list, the sketch bookmarks and both sides of `tools/visual-bench.sh`, which
now enumerates whatever is in `portolan.json` rather than a hardcoded pair.

## Where the GTFS comes from

Try `tools/city.sh gtfs <city>` first — it covers most of these. Direct
sources, for when Transitland's copy is stale or licence-restricted (verify
before trusting them; operator portals move, and the two marked ⚠ need an
account):

- **LA Metro** — developer.metro.net publishes a rail-only bundle
  (`gtfs_rail.zip`) alongside the bus feed; the rail one is what you want.
- **Paris** — Île-de-France Mobilités open data (`prim.iledefrance-
  mobilites.fr`, also mirrored on `transport.data.gouv.fr`). The full
  regional feed is large (hundreds of MB) and covers Metro + RER + tram +
  Transilien.
- **Berlin** — VBB open data (`vbb.de` → API & open data → GTFS), or the
  gtfs.de regional extracts. Covers U-Bahn, S-Bahn, tram, regional.
- **London** ⚠ — TfL publishes no official GTFS, only TransXChange/journey-
  planner data. Practical options: a third-party conversion listed in the
  Mobility Database (`mobilitydatabase.org`), or converting the TfL feed
  yourself. Registration is on TfL's side.
- **Tokyo** ⚠ — the Public Transportation Open Data Center (`odpt.org`) and
  `gtfs-data.jp` carry GTFS-JP for Toei, Tokyo Metro and the JR/private
  railways; free registration and an API key are required, and licence
  terms vary per operator.
- **Atlanta / Charlotte** — already mirrored locally; the upstream sources
  are MARTA's developer page and CATS/City of Charlotte open data.

## Adding another city

Append a feed to `portolan.json` — `name`, `gtfs`, `rail`, `out`,
`network`, `bbox` — and it appears in the atlas dropdown, `city.sh`, the
Makefile targets and the visual bench with no further wiring. Give it a
couple of `locations.json` rows so the bench has somewhere to look.
