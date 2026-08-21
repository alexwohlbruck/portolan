# SYNC — the reconciliation command

`portolan sync` keeps a fleet of feeds current: it notices which GTFS feeds
changed upstream, downloads them, rebuilds exactly the builds whose inputs
changed, retiles those pyramids, and re-exports corrected GTFS. It is the
single entry point an operator (or barrelman's ops worker) calls; everything
it does is also achievable by hand with `chart`/`tiles`/`tools/feed.sh` — sync
just automates the bookkeeping.

Three subcommands:

    portolan sync global  --config portolan.json [flags]
    portolan sync patch   --config portolan.json --feeds key1,key2 [flags]
    portolan sync check   --config portolan.json [flags]

Common flags:

    --config     portolan.json            feed registry (required)
    --data       data/gtfs                where GTFS zips live / are downloaded
    --build      build                    build fan output dir
    --tiles      build/tiles             tile pyramids + index.json
    --export-gtfs build/export           corrected GTFS zips (empty = skip export)
    --state      <build>/sync-state.json state manifest
    --style-dir  style                    curation documents
    --jobs       min(4, NumCPU)           parallel feed builds (charts are memory-heavy)
    --dry-run                             plan only, print what would rebuild
    --json                                final line of stdout is `RESULT {…}`

`TRANSITLAND_API_KEY` is read from the environment for `check` and for any
download.

## Why patch equals global

Every build is a pure function of its inputs: the GTFS zips named in its
config entry, the rail/stops/streets extracts, the window (bbox +
exclude-bbox), and the layered style documents. No build reads another
build's output (a corridor input is a committed file, not a live edge).
So the patch invariant is:

> Rebuild every build whose input set changed; touch nothing else.
> The resulting build/tiles/export trees are identical to a global run.

The work is finding the closure of "input set changed":

1. **Changed feeds** C — feeds whose zip content-hash differs from the state
   manifest.
2. **Interleaved neighbors** — feeds sharing steel with any feed in C. Shared
   steel is a measurement, not curation (same rules as `tools/groups.py`):
   sample rail-typed shapes every 30 m, bin into 60 m cells, a pair
   qualifies at ≥900 m of co-occupied run. A bbox prefilter keeps this
   cheap: only feeds whose window intersects a changed feed's window are
   measured — transitively through region-scale windows, so a whole
   member chain is measured together, but corridor-scale windows
   (>20 deg²: Amtrak, VIA) are measured when touched without propagating
   the frontier, or every patch would weld into a global run.
3. **Group closure** — group membership is re-derived over the affected
   component (the union-find component containing any feed in C, measured
   with the same rules `tools/groups.py` uses). If membership, windows, or
   the group's gtfs list changed, the group rebuilds. If a group appears or
   dissolves, the config is rewritten the same way `groups.py --write`
   would have.
4. **Overlay closure** — an overlay (wide feed, e.g. Amtrak) whose
   exclude-bbox set changed (because a group window moved) rebuilds its own
   background build; groups it rides through already rebuild via (3).

The rebuild set is then: standalone builds for affected non-member feeds,
group builds for affected groups, overlay backgrounds when their windows
changed. Members of surviving groups do not get standalone rebuilds (the
global index skips them), but their per-feed pyramids rebuild if their own
zip changed.

`sync global` is the same executor with C = every feed, and serves as the
oracle: `sync patch` after any single-feed change must produce byte-identical
tiles to `sync global`. That equivalence is a test, not a hope.

## State manifest

`sync-state.json` records, per feed key:

    {
      "version": 1,
      "last_check": "2026-08-20T04:00:00Z",
      "feeds": {
        "mta-subway": {
          "onestop": "f-dr5r-nyct",
          "sha1": "…",            // transitland feed_version sha at last download
          "content": "…",         // sha256 over sorted *.txt members of the zip
          "built": "…",           // input fingerprint at last successful build
          "tiled": "…",           // input fingerprint at last successful tiling
          "exported": "…"         // input fingerprint at last successful export
        }
      }
    }

The content hash is the identity that matters (transitland occasionally
republishes identical bytes under a new sha): `check` diffs it to decide
what changed. `built`/`tiled`/`exported` hold the executor's **input
fingerprint**: a hash over the assembled chart request (which folds in
the registry entry, its window, the ceded exclude windows and the style
layering) plus the content hash of every input zip. A build whose
current fingerprint equals its stamps is clean and skips — this is what
makes an overlay rebuild when only its ceded windows moved, and a group
(whose row keys the group, not any single zip) rebuild when its
membership changes. Extract and style-document contents are deliberately
outside the fingerprint: the manifest covers the feed, by design — a
curation change wants an explicit rebuild. Interrupted runs are safe:
the manifest is written after each feed completes, so a rerun resumes
where it stopped.

## check

For every feed entry with an `onestop` id, ask transitland for the current
`feed_state.feed_version.sha1` (one `feeds/{onestop}` request per feed,
sequential, honoring a 429 once via Retry-After — the API has no batch
lookup), diff against the manifest, download changed feeds into `--data` as
`<feedkey>.zip` (via `download_latest_feed_version`, falling back to
`urls.static_current`), then run the patch flow on the changed set:
check → changed set → plan → execute, one command. A new sha over
identical content records the sha and changes nothing on disk.
`--dry-run` stops after the diff, touching neither `--data` nor the
manifest. Feeds without an `onestop` id are reported and skipped. Exit 0
with `"changed": []` when nothing moved. A changed feed's zip is on disk
before planning by construction — check downloaded it — which matters:
the measurement reads it, and a patch planned over an absent zip would
read a partial data tree as the railway vanishing.

The `<feedkey>.zip` name is not a new convention: every onestop-bearing
entry in portolan.json already points its primary gtfs at
`data/gtfs/<feedkey>.zip`, so a download lands exactly where the feed's
build reads.

## Execution

The executor walks a Plan in this order:

1. **Registry.** If the plan rewrote it (groups created, dissolved,
   windows moved), the new portolan.json is written first, atomically —
   a crash after this point leaves a registry the next run re-plans
   from correctly. Pyramids of deleted groups are removed. (Feeds newly
   absorbed into a group keep their per-feed pyramids: the world index
   skips grouped members, but the atlas still serves them feed-scoped.)
2. **Builds**, under `--jobs` bounded parallelism. Every build in the
   plan — standalone, overlay background, member pyramid, group — is
   independent of every other (inputs are zips and extracts, never
   another build's output), so they share one worker pool. Each build
   runs as a `portolan chart` CHILD PROCESS with the argv
   `tools/feed.sh build` would have assembled (gtfs list, geometry
   source, window, ceded windows, style layering, `--onestop` from the
   registry, `chart_args` word-split — parsed by chart's own flag set,
   not a second parser); chart configuration is process state, so two
   in-process builds would read each other's dials, and a child per
   build also isolates its memory spike. Builds whose input fingerprint
   already matches their state stamps are skipped.
3. **Group preflight.** A group's rail extract that is missing or does
   not cover the window is MERGED from its members' (and overlays')
   extracts, first-wins on way id (the tools/mergefc.py rule — the same
   OSM way must not enter the bundler twice); likewise a missing stops
   extract, and `streets_from` into `streets`. sync never calls
   Overpass — where groupbuild.sh would fetch, sync merges what the
   members already have.
4. **Verify gate**, immediately after each group's build: every
   member's own band-15 non-transition centerlines, sampled at 25 m,
   must land ≥90% within 30 m of the group's ink (tools/groupverify.py,
   ported). A failing group is DELETED from the registry on the spot —
   its members go straight back into the world index — its pyramid is
   removed, the failure lands in `errors`, and any member without a
   current standalone build joins the queue.
5. **Tiles.** Each successful build is cut into `--tiles/<key>` and the
   build's resolved style manifest (`<out>.style.json`) is copied into
   the pyramid as `--tiles/<key>/style.json`. Differ stats aggregate
   into the RESULT line.
6. **Export.** With `--export-gtfs`, each feed-OWN build (standalone,
   overlay background, member pyramid) writes its corrected zips into
   the export dir. Group builds do not export: their member zips belong
   to the members' own builds, and two builds writing one zip would
   race — and which geometry won would depend on scheduling.
7. **Index.** After all tiling, a static `--tiles/index.json` is
   written: the same composer the atlas serves `/api/tiles/index.json`
   from (internal/tiles.Index), over the post-run registry, so the
   static file and the live endpoint cannot drift.
8. **State**, stamped and saved after each feed completes — kill the
   run anywhere and the rerun resumes, skipping what finished.

`sync global` is the same executor with C = every feed whose zip is on
disk. Registry entries whose zip was never downloaded (most of the
registry is discovery output) are reported in `skipped`, not failed.
Sound scoring (`portolan sound`) is not part of sync — it is advisory
and feed.sh territory.

## Machine-readable result

With `--json`, the final stdout line is:

    RESULT {"changed":["a"],"affected":["a","b","dallas"],
            "rebuilt":["a","dallas"],"groups_rewritten":false,
            "tiles":{"written":1234,"unchanged":56789,"removed":12},
            "exported":["a.zip","amtrak.zip"],"skipped":[],"errors":[]}

One schema for check, patch and global (check's original `changed`/
`skipped`/`errors` keys are all present, so a parser built against it
keeps working). `rebuilt` is the builds that actually ran — clean skips
excluded; `exported` is exactly the `<feedkey>.zip` names present flat
under the `--export-gtfs` dir; `skipped` is feeds without an onestop id
(check) or without a zip on disk (global). `--dry-run` emits the same
line with the planned rebuild set, zero tiles and exit 0.

Barrelman parses this line; everything above it is human progress output.
A build failure for one feed does not abort the run — it lands in `errors`
and the exit code is 1, with everything else completed.

## Tile contract additions

Station label features (`ftype: "station"`) carry a `gtfs_ids` property:
semicolon-joined `<feed-onestop>:<stop_id>` pairs, deduped and sorted —
the GTFS stops merged into that station (served platforms plus the
parent-station record where the feed ships one). Stop ids are the source
feed's own, prefix-free. A stop whose source feed has no onestop id is
omitted; when nothing qualifies the property is absent, not empty. This
is how a clicked station tile feature opens a stop-detail panel keyed by
feed identity. Markers (`ftype: "marker"`) do not carry it — they share
the station's name, and the label feature is the identity anchor.

By hand the map arrives via `chart --onestop <zip-basename>=<onestop>,…`;
sync fills it from the registry's `onestop` fields automatically.

Each pyramid `--tiles/<feed>/` additionally carries:

- `style.json` — the build's resolved style manifest (`<out>.style.json`
  copied in), so the tile consumer fetches `<feed>/style.json` next to
  the tiles it draws;
- `tiles.json` — TileJSON with a single RELATIVE `{z}/{x}/{y}.mvt`
  template, no hosts.

Tiles are raw MVT, uncompressed. `--tiles/index.json` is the static
world index: `[{feed,name,bounds,maxzoom}]`, the identical schema (and
composer) the atlas serves at `/api/tiles/index.json`, carrying no URL
templates.

## What sync does not do

- **Onboarding.** New feeds (Overpass extracts, pfaedle, discovery sweeps)
  remain `tools/feed.sh` / `tools/onboard-*` territory. sync operates on
  feeds the registry already describes with existing extracts.
- **OSM refresh.** Rail/stops/streets extracts are inputs, not managed
  artifacts. If an extract changes on disk, the affected feeds rebuild on
  the next run (extract hashes participate in the input fingerprint).
- **Serving.** The atlas serves; sync writes files.
