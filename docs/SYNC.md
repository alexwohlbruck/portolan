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
    --jobs       NumCPU                   parallel feed builds
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
   measured.
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
          "built": "…",           // content hash at last successful build
          "tiled": "…",           // content hash at last successful tiling
          "exported": "…"         // content hash at last successful export
        }
      }
    }

The content hash is the identity that matters (transitland occasionally
republishes identical bytes under a new sha). A feed whose `content` equals
`built`/`tiled`/`exported` is clean. Interrupted runs are safe: the manifest
is written after each feed completes, so a rerun resumes where it stopped.

## check

For every feed entry with an `onestop` id, ask transitland for the current
`feed_state.feed_version.sha1` (one `feeds/{onestop}` request per feed,
sequential, honoring a 429 once via Retry-After — the API has no batch
lookup), diff against the manifest, download changed feeds into `--data` as
`<feedkey>.zip` (via `download_latest_feed_version`, falling back to
`urls.static_current`), then run the patch flow on the changed set. A new
sha over identical content records the sha and changes nothing on disk.
`--dry-run` stops after the diff, touching neither `--data` nor the
manifest. Feeds without an `onestop` id are reported and skipped. Exit 0
with `"changed": []` when nothing moved; check's RESULT line carries
`{"changed":[…],"skipped":[…],"errors":[…]}`.

The `<feedkey>.zip` name is not a new convention: every onestop-bearing
entry in portolan.json already points its primary gtfs at
`data/gtfs/<feedkey>.zip`, so a download lands exactly where the feed's
build reads.

(Phasing: today check ends after download-and-record — the handoff into
the patch flow arrives with the patch executor itself.)

## Machine-readable result

With `--json`, the final stdout line is:

    RESULT {"changed":["a"],"affected":["a","b","dallas"],
            "rebuilt":["a","dallas"],"groups_rewritten":false,
            "tiles":{"written":1234,"unchanged":56789,"removed":12},
            "exported":["a.zip","amtrak.zip"],"errors":[]}

Barrelman parses this line; everything above it is human progress output.
A build failure for one feed does not abort the run — it lands in `errors`
and the exit code is 1, with everything else completed.

## What sync does not do

- **Onboarding.** New feeds (Overpass extracts, pfaedle, discovery sweeps)
  remain `tools/feed.sh` / `tools/onboard-*` territory. sync operates on
  feeds the registry already describes with existing extracts.
- **OSM refresh.** Rail/stops/streets extracts are inputs, not managed
  artifacts. If an extract changes on disk, the affected feeds rebuild on
  the next run (extract hashes participate in the input fingerprint).
- **Serving.** The atlas serves; sync writes files.
