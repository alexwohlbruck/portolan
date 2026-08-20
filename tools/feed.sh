#!/bin/bash
# feed.sh — bring one GTFS feed in portolan.json up: fetch its track
# extract, build it, see what is still missing. The FEED is the unit of
# everything — build, curation, refresh; there are no city bundles.
#
#   tools/feed.sh list                # every configured feed + which inputs it has
#   tools/feed.sh gtfs london         # Transitland feeds covering the bbox
#   tools/feed.sh gtfs london 1234    # …download one of them
#   tools/feed.sh rail london         # Overpass → the feed's rail geojson
#   tools/feed.sh shapesrail amtrak   # the feed's OWN shapes as the track
#   tools/feed.sh stops london        # Overpass → named transit stops
#   tools/feed.sh build london        # chart (+ sound when a sketch exists)
#   tools/feed.sh all london          # rail then build
#   tools/feed.sh refresh london      # rebuild ONLY if the feed content changed
#
# A feed needs two inputs: its GTFS zip and a track extract — Overpass
# where OSM is the truth, or `shapesrail` where the feed's own shapes
# are (intercity feeds with no city window). Everything else — bbox,
# paths, name, extra chart flags — comes from portolan.json.
set -euo pipefail
cd "$(dirname "$0")/.."

CFG=portolan.json
OVERPASS="${OVERPASS_URL:-https://overpass-api.de/api/interpreter}"

need() { command -v "$1" >/dev/null || { echo "need $1 on PATH"; exit 2; }; }
need jq
need python3

feeds() { jq -r '.feeds | keys[]' "$CFG"; }
get()   { jq -r --arg f "$1" --arg k "$2" '.feeds[$f][$k] // empty' "$CFG"; }

known() { # $1 = feed key, $2 = subcommand (for the usage line)
  [ -n "${1:-}" ] || { echo "usage: $0 $2 <feed> — one of: $(feeds | tr '\n' ' ')"; exit 2; }
  jq -e --arg f "$1" '.feeds | has($f)' "$CFG" >/dev/null 2>&1 || {
    echo "unknown feed '$1' — configured: $(feeds | tr '\n' ' ')"; exit 2; }
}

list() {
  printf '%-10s %-14s %-8s %-8s %-8s %s\n' feed name gtfs rail build sketch
  for f in $(feeds); do
    mark() { [ -s "$1" ] && echo yes || echo MISSING; }
    markList() { # gtfs may be a comma list — every part must exist
      local p; IFS=',' read -ra parts <<<"$1"
      for p in "${parts[@]}"; do [ -s "$p" ] || { echo MISSING; return; }; done
      echo yes
    }
    printf '%-10s %-14s %-8s %-8s %-8s %s\n' "$f" "$(get "$f" name)" \
      "$(markList "$(get "$f" gtfs)")" "$(mark "$(get "$f" rail)")" \
      "$(mark "$(get "$f" out)")" "$(mark "$(get "$f" network)")"
  done
}

bbox() { # $1 = feed key → sets w s e n
  read -r w s e n <<<"$(jq -r --arg f "$1" \
    '.feeds[$f].bbox // empty | @tsv' "$CFG" | tr '\t' ' ')"
  [ -n "${n:-}" ] || { echo "$1: no 'bbox' [w,s,e,n] in $CFG"; exit 2; }
}

# gtfs: Transitland's feed registry, queried by the feed's own bbox.
#   tools/feed.sh gtfs paris          — list the GTFS feeds covering Paris
#   tools/feed.sh gtfs paris 1234     — download feed 1234 to the configured path
# Needs TRANSITLAND_API_KEY (free: transit.land/users/sign_up). The list step
# is deliberately separate: a metro bbox matches every bus operator in the
# region, and only you can say which one is the rail network you mean.
gtfs() { # $1 = feed key, $2 = optional Transitland feed id
  local feed=$1 id="${2:-}" w s e n out url
  [ -n "${TRANSITLAND_API_KEY:-}" ] || {
    echo "TRANSITLAND_API_KEY unset — free key at https://transit.land/users/sign_up"
    echo "(your barrelman checkout already has one in its .env)"; exit 2; }
  bbox "$feed"

  if [ -z "$id" ]; then
    echo "$feed: GTFS feeds covering $w,$s,$e,$n —"
    curl -sS --fail -G "https://transit.land/api/v2/rest/feeds" \
      --data-urlencode "apikey=$TRANSITLAND_API_KEY" \
      --data-urlencode "spec=gtfs" \
      --data-urlencode "bbox=$w,$s,$e,$n" \
      --data-urlencode "limit=100" \
      | jq -r '.feeds[] | "  \(.id)\t\(.onestop_id)\t\(.name // "-")\t\(if .urls.static_current then "downloadable" else "no url" end)"'
    echo "then: $0 gtfs $feed <id>"
    return
  fi

  out=$(get "$feed" gtfs)
  [ -n "$out" ] || { echo "$feed: no 'gtfs' path in $CFG"; exit 2; }
  case "$out" in /*) echo "$feed: gtfs path is the shared mirror ($out) — not overwriting"; exit 2 ;; esac
  mkdir -p "$(dirname "$out")"
  # download_latest_feed_version serves the zip when the feed's licence
  # allows redistribution; otherwise fall back to the operator's own URL.
  echo "$feed: Transitland feed $id → $out"
  if ! curl -sS --fail -L -G "https://transit.land/api/v2/rest/feeds/$id/download_latest_feed_version" \
       --data-urlencode "apikey=$TRANSITLAND_API_KEY" -o "$out.tmp"; then
    url=$(curl -sS --fail -G "https://transit.land/api/v2/rest/feeds" \
      --data-urlencode "apikey=$TRANSITLAND_API_KEY" --data-urlencode "id=$id" \
      | jq -r '.feeds[0].urls.static_current // empty')
    [ -n "$url" ] || { echo "$feed: no downloadable URL for feed $id (licence?)"; rm -f "$out.tmp"; exit 1; }
    echo "$feed: redistribution blocked — fetching source $url"
    curl -sS --fail -L "$url" -o "$out.tmp"
  fi
  unzip -l "$out.tmp" > /dev/null 2>&1 || { echo "$feed: not a zip — check the feed id"; rm -f "$out.tmp"; exit 1; }
  mv "$out.tmp" "$out"
  echo "$feed: $(du -h "$out" | cut -f1), $(unzip -p "$out" routes.txt | tail -n +2 | wc -l | tr -d ' ') routes"
}

# rail: one Overpass query for the feed's window, converted to the geojson
# shape internal/osm reads (way/<id> + railway/service/bridge/tunnel/layer
# tags). Every service value is kept — osm.Load does the yard/siding
# filtering itself, and it needs the crossovers.
rail() { # $1 = feed key
  local feed=$1 out w s e n query tmp
  out=$(get "$feed" rail)
  [ -n "$out" ] || { echo "$feed: no 'rail' path in $CFG"; exit 2; }
  bbox "$feed"

  # every drawable infrastructure class (docs/MODES.md): the rail family
  # plus cable-supported aerialways. disused|construction ride along
  # because a feed outlives the map: OSM retags a suspended line's steel
  # (DC Streetcar) or a rebuild-closure (Toronto's 50x branches) while
  # the agency still publishes service — the rails are physically there,
  # and the map draws what the feed says runs. overpass2geojson resolves
  # their underlying class from disused:railway/construction:railway. Buses stay out — highways would grow
  # the extract 10-100x, and bus matching is not implemented yet.
  query="[out:json][timeout:600];
(
way[\"railway\"~\"^(rail|subway|light_rail|tram|monorail|funicular|narrow_gauge|disused|construction)\$\"]($s,$w,$n,$e);
way[\"aerialway\"~\"^(cable_car|gondola|mixed_lift)\$\"]($s,$w,$n,$e);
way[\"route\"=\"ferry\"]($s,$w,$n,$e);
);
out geom;"

  mkdir -p "$(dirname "$out")"
  tmp="$out.tmp"
  echo "$feed: Overpass $w,$s,$e,$n → $out (minutes for a big window)"
  # download and convert in two steps: piped straight into python, an
  # Overpass error page (it answers 429/502/504 under load, and does so
  # often — the same query 504s and then succeeds a minute later) surfaced
  # as a JSON traceback that read like a broken script.
  local try ok=0
  for try in 1 2 3; do
    if curl -sS --fail --max-time 1800 --data-urlencode "data=$query" \
       "$OVERPASS" -o "$tmp.json"; then ok=1; break; fi
    echo "$feed: attempt $try failed — public Overpass under load; waiting…"
    sleep $((try * 20))
  done
  if [ "$ok" = 0 ]; then
    echo "$feed: Overpass gave up after 3 tries. Retry later, or point"
    echo "       OVERPASS_URL at another endpoint (e.g. overpass.kumi.systems)."
    head -c 300 "$tmp.json" 2>/dev/null; rm -f "$tmp.json"; exit 1
  fi
  if ! head -c 200 "$tmp.json" | grep -q '"elements"\|"version"'; then
    echo "$feed: Overpass returned something that is not its JSON:"
    head -c 300 "$tmp.json"; rm -f "$tmp.json"; exit 1
  fi
  python3 tools/overpass2geojson.py < "$tmp.json" > "$tmp"
  rm -f "$tmp.json"
  mv "$tmp" "$out"
  echo "$feed: $(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))["features"]),"ways")' "$out"), $(du -h "$out" | cut -f1)"
}

build() { # $1 = feed key
  local feed=$1 gtfs rail out net streets stops bboxarg first
  gtfs=$(get "$feed" gtfs); rail=$(get "$feed" rail)
  out=$(get "$feed" out);   net=$(get "$feed" network)
  streets=$(get "$feed" streets); stops=$(get "$feed" stops)
  local corr
  corr=$(get "$feed" corridors)
  if [ -n "$corr" ]; then
    [ -s "$corr" ] || { echo "$feed: no corridor graph at $corr — tools/feed.sh shapescorr $feed"; exit 2; }
  else
    [ -s "$rail" ] || { echo "$feed: no rail extract at $rail — tools/feed.sh rail $feed"; exit 2; }
  fi
  # gtfs may be a comma list (primary + overlay feeds) — every part must exist
  local IFS=','
  for first in $gtfs; do
    [ -s "$first" ] || { echo "$feed: no GTFS at $first — see docs/CITIES.md"; exit 2; }
  done
  unset IFS
  bboxarg=$(jq -r --arg f "$feed" '.feeds[$f].bbox // empty | join(",")' "$CFG")
  if [ -n "$corr" ]; then
    set -- --gtfs "$gtfs" --corridors "$corr" --out "$out"
  else
    set -- --gtfs "$gtfs" --rail "$rail" --out "$out"
  fi
  [ -n "$bboxarg" ] && set -- "$@" --bbox "$bboxarg"
  # a feed that also rides in GROUP builds as an overlay cedes those
  # windows: the group draws it there, this build draws everywhere else,
  # and the two meet at the group's clip line — the world draws the
  # railroad exactly once. Derived from the groups' gtfs lists, so it
  # cannot rot. Absorbed members (listed in `members`) keep their full
  # standalone builds — those are hidden from the global index instead.
  local exarg primary
  primary=${gtfs%%,*}
  exarg=$(jq -r --arg f "$feed" --arg p "$primary" '
    [ .feeds | to_entries[]
      | select(.key != $f
          and ((.value.members // []) | length > 0)
          and ((.value.members // []) | index($f) | not)
          and ((.value.gtfs // "" | split(",") | map(gsub("^\\s+|\\s+$";""))) | index($p)))
      | .value.bbox | join(",") ] | join(";")' "$CFG")
  [ -n "$exarg" ] && set -- "$@" --exclude-bbox "$exarg"
  # style: ONE loader, in Go. This used to be a jq merge here plus a
  # second merge in the atlas, and the two drifted — CLI builds silently
  # dropped bullet_order for weeks. chart now reads style/_default.json
  # and style/<city>.json itself, so both paths cannot disagree.
  # A GROUP entry (members: [...]) layers each member's document under
  # its own, so member curation rides into the group build.
  # A GROUP layers its members' documents AND its overlays' under its own:
  # a corridor feed charted into the window must arrive with the same
  # trunk and colours its own build gives it, or the railroad changes
  # colour at the seam (Amtrak went light blue in St Louis and Buffalo).
  local members stylefeed
  members=$(jq -r --arg f "$feed" '(((.feeds[$f].members // []) + (.feeds[$f].overlays // [])) | join(","))' "$CFG")
  stylefeed="$feed"
  [ -n "$members" ] && stylefeed="$members,$feed"
  set -- "$@" --style-dir style --feed "$stylefeed"
  if [ -n "$streets" ]; then
    if [ -s "$streets" ]; then set -- "$@" --streets "$streets"
    else echo "$feed: streets configured but missing at $streets — tools/feed.sh streets $feed (building rail-only)"; fi
  fi
  # stops are opt-in and silent when absent: the extract's presence IS the
  # switch for OSM station naming, so a city without one is unchanged
  if [ -n "$stops" ] && [ -s "$stops" ]; then set -- "$@" --stops "$stops"; fi
  # EXPORT_GTFS=<dir> also writes the source feeds back out with matched
  # shapes.txt (docs/REGIONS.md) — env rather than a flag so `all` and
  # feed.sh reads them for every build
  if [ -n "${EXPORT_GTFS:-}" ]; then set -- "$@" --export-gtfs "$EXPORT_GTFS"; fi
  # chart_args: per-feed extra chart flags (e.g. "--set match_gap_cost=150"
  # for us-intercity) — how a region's tuned dials survive a refresh
  local extra
  extra=$(get "$feed" chart_args)
  # shellcheck disable=SC2086 — word splitting is the point
  go run ./cmd/portolan chart "$@" $extra
  if [ -s "$net" ]; then
    go run ./cmd/portolan sound --network "$net" --build "$out" || true
  else
    echo "$feed: no drawn network at $net — draw one in the atlas sketch editor to score"
  fi
}

# streets: the highway layer for bus matching — separate from the rail
# extract, opt-in per city ('streets' path in portolan.json), and big: a
# whole-city street grid is 10-50x the rail ways.
streets() { # $1 = feed key
  local feed=$1 out w s e n query tmp
  out=$(get "$feed" streets)
  [ -n "$out" ] || { echo "$feed: no 'streets' path in $CFG"; exit 2; }
  bbox "$feed"
  query="[out:json][timeout:900];
way[\"highway\"~\"^(motorway|trunk|primary|secondary|tertiary|unclassified|residential|living_street|busway|bus_guideway|motorway_link|trunk_link|primary_link|secondary_link|tertiary_link)\$\"]($s,$w,$n,$e);
out geom;"
  mkdir -p "$(dirname "$out")"
  tmp="$out.tmp"
  echo "$feed: Overpass streets $w,$s,$e,$n → $out (this one is BIG — minutes)"
  local try ok=0
  for try in 1 2 3; do
    if curl -sS --fail --max-time 1800 --data-urlencode "data=$query" \
       "$OVERPASS" -o "$tmp.json"; then ok=1; break; fi
    echo "$feed: attempt $try failed — public Overpass under load; waiting…"
    sleep $((try * 30))
  done
  if [ "$ok" = 0 ]; then
    echo "$feed: Overpass gave up after 3 tries."; rm -f "$tmp.json"; exit 1
  fi
  head -c 200 "$tmp.json" | grep -q '"elements"\|"version"' || {
    echo "$feed: not Overpass JSON:"; head -c 300 "$tmp.json"; rm -f "$tmp.json"; exit 1; }
  python3 tools/overpass2geojson.py --streets < "$tmp.json" > "$tmp"
  rm -f "$tmp.json"
  mv "$tmp" "$out"
  echo "$feed: $(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))["features"]),"ways")' "$out"), $(du -h "$out" | cut -f1)"
}

# stops: the named transit STOPS of the window — what the station-name
# matcher scores GTFS stops against. Small next to the rail extract (a few
# thousand points), and opt-in per city via a 'stops' path.
stops() { # $1 = feed key
  local feed=$1 out w s e n query tmp
  out=$(get "$feed" stops)
  [ -n "$out" ] || { echo "$feed: no 'stops' path in $CFG"; exit 2; }
  bbox "$feed"
  # every element that can carry a station name: nodes for stop positions
  # and tram stops, ways/relations for station buildings and complexes.
  # 'out center' gives ways and relations a representative point.
  query="[out:json][timeout:600];
(
node[\"railway\"~\"^(station|halt|tram_stop)\$\"]($s,$w,$n,$e);
way[\"railway\"~\"^(station|halt)\$\"]($s,$w,$n,$e);
relation[\"railway\"~\"^(station|halt)\$\"]($s,$w,$n,$e);
node[\"public_transport\"~\"^(station|stop_position)\$\"]($s,$w,$n,$e);
way[\"public_transport\"=\"station\"]($s,$w,$n,$e);
relation[\"public_transport\"=\"station\"]($s,$w,$n,$e);
node[\"aerialway\"=\"station\"]($s,$w,$n,$e);
node[\"amenity\"=\"ferry_terminal\"]($s,$w,$n,$e);
way[\"amenity\"=\"ferry_terminal\"]($s,$w,$n,$e);
);
out center;"

  mkdir -p "$(dirname "$out")"
  tmp="$out.tmp"
  echo "$feed: Overpass stops $w,$s,$e,$n → $out"
  local try ok=0
  for try in 1 2 3; do
    if curl -sS --fail --max-time 1800 --data-urlencode "data=$query" \
       "$OVERPASS" -o "$tmp.json"; then ok=1; break; fi
    echo "$feed: attempt $try failed — public Overpass under load; waiting…"
    sleep $((try * 20))
  done
  [ "$ok" = 1 ] || { echo "$feed: Overpass gave up after 3 tries"; rm -f "$tmp.json"; exit 1; }
  head -c 200 "$tmp.json" | grep -q '"elements"\|"version"' || {
    echo "$feed: not Overpass JSON:"; head -c 300 "$tmp.json"; rm -f "$tmp.json"; exit 1; }
  python3 tools/overpass2geojson.py --stops < "$tmp.json" > "$tmp"
  rm -f "$tmp.json"
  mv "$tmp" "$out"
  echo "$feed: $(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))["features"]),"named stops")' "$out"), $(du -h "$out" | cut -f1)"
}

# shapes: regenerate the feed's shapes.txt with pfaedle (docs/CITIES.md).
# Two feeds need this: ones with NO shapes.txt (Berlin, Tokyo, GTFS-JP in
# general) and ones whose shapes are SKELETAL — a couple of points per
# route, station-to-station chords. London ships two such shapes per line
# ("bakerloo-inbound"…), coarser than the spacing between neighbouring
# tube bores, and chart's Viterbi follows them onto the wrong tracks.
# Fetches an OSM XML window (the geojson rail extract won't do — pfaedle
# reads OSM), map-matches every trip, and zips the result back over the
# feed. Modes come from routes.txt route_type (ferries are left alone).
shapes() { # $1 = feed key
  local feed=$1 gtfs w s e n xml modes tmp
  need docker
  gtfs=$(get "$feed" gtfs)
  [ -s "$gtfs" ] || { echo "$feed: no GTFS at $gtfs — see docs/CITIES.md"; exit 2; }
  case "$gtfs" in /*) echo "$feed: gtfs is the shared mirror ($gtfs) — copy it under data/gtfs first"; exit 2 ;; esac
  bbox "$feed"
  # pad the window: routes run to the bbox edge, and a shape matched
  # against a clipped window goes straight-line outside it
  w=$(python3 -c "print($w-0.1)"); s=$(python3 -c "print($s-0.05)")
  e=$(python3 -c "print($e+0.1)"); n=$(python3 -c "print($n+0.05)")

  modes=$(unzip -p "$gtfs" routes.txt | python3 -c '
import csv,sys
m={"0":"tram","1":"subway","2":"rail","4":"ferry","5":"funicular","6":"gondola","7":"funicular"}
t={m[r["route_type"]] for r in csv.DictReader(sys.stdin) if r.get("route_type") in m}
print(",".join(sorted(t)))')
  [ -n "$modes" ] || { echo "$feed: no pfaedle-matchable route types"; exit 2; }

  xml="data/osm/$feed.osm"
  mkdir -p data/osm
  if [ ! -s "$xml" ]; then
    echo "$feed: Overpass XML window $w,$s,$e,$n → $xml (minutes for a big window)"
    local query="[out:xml][timeout:900];
(
way[\"railway\"]($s,$w,$n,$e);
way[\"route\"=\"ferry\"]($s,$w,$n,$e);
way[\"aerialway\"]($s,$w,$n,$e);
);
(._;>;);
out;"
    local try ok=0
    for try in 1 2 3; do
      if curl -sS --fail --max-time 1800 --data-urlencode "data=$query" \
         "$OVERPASS" -o "$xml.tmp"; then ok=1; break; fi
      echo "$feed: attempt $try failed — public Overpass under load; waiting…"
      sleep $((try * 20))
    done
    [ "$ok" = 1 ] || { echo "$feed: Overpass gave up after 3 tries"; rm -f "$xml.tmp"; exit 1; }
    grep -q '<osm' "$xml.tmp" || { echo "$feed: not OSM XML:"; head -c 300 "$xml.tmp"; rm -f "$xml.tmp"; exit 1; }
    mv "$xml.tmp" "$xml"
  fi
  echo "$feed: $(du -h "$xml" | cut -f1) OSM window, matching modes: $modes"

  tmp=$(mktemp -d)
  cp "$gtfs" "$tmp/feed.zip"
  # -D drops the feed's existing shapes first: without it pfaedle keeps
  # whatever is there, and a feed with SKELETAL shapes comes back unchanged
  docker run -i --rm \
    -v "$PWD/data/osm:/osm" -v "$tmp:/gtfs" -v "$tmp/out:/gtfs-out" \
    ghcr.io/ad-freiburg/pfaedle:latest -x "/osm/$feed.osm" -i /gtfs/feed.zip -m "$modes" -D
  [ -s "$tmp/out/shapes.txt" ] || { echo "$feed: pfaedle produced no shapes"; rm -rf "$tmp"; exit 1; }
  cp "$gtfs" "$gtfs.bak"
  (cd "$tmp/out" && zip -q -r feed-shaped.zip ./*.txt)
  mv "$tmp/out/feed-shaped.zip" "$gtfs"
  rm -rf "$tmp"
  echo "$feed: shapes.txt now $(unzip -p "$gtfs" shapes.txt | wc -l | tr -d ' ') rows (was $(unzip -p "$gtfs.bak" shapes.txt 2>/dev/null | wc -l | tr -d ' ')); original at $gtfs.bak"
}

# shapescorr: the feed's OWN shapes become an AUTHORED corridor graph
# (tools/gtfscorridors.py, docs/CORRIDORS.md) — chart --corridors then
# skips osm/bundle/MATCH/SPLIT entirely. THE path for intercity feeds:
# matching shapes against themselves is circular and produced every
# pathology from forced-GAP chords to piece oscillation; the graph is
# what the shapes already say.
shapescorr() { # $1 = feed key
  local feed=$1 out gtfs
  out=$(get "$feed" corridors); gtfs=$(get "$feed" gtfs)
  [ -n "$out" ] || { echo "$feed: no 'corridors' path in $CFG"; exit 2; }
  [ -s "$gtfs" ] || { echo "$feed: no GTFS at $gtfs"; exit 2; }
  mkdir -p "$(dirname "$out")"
  python3 tools/gtfscorridors.py "$gtfs" > "$out.tmp" && mv "$out.tmp" "$out"
  echo "$feed: corridors → $out ($(du -h "$out" | cut -f1))"
}

# shapesrail: the feed's OWN shapes become the track extract
# (tools/gtfsrail.py) — for intercity/no-city-window feeds where the
# shapes are denser truth than any Overpass query could be.
shapesrail() { # $1 = feed key
  local feed=$1 out gtfs
  out=$(get "$feed" rail); gtfs=$(get "$feed" gtfs)
  [ -n "$out" ] || { echo "$feed: no 'rail' path in $CFG"; exit 2; }
  [ -s "$gtfs" ] || { echo "$feed: no GTFS at $gtfs"; exit 2; }
  mkdir -p "$(dirname "$out")"
  python3 tools/gtfsrail.py "$gtfs" > "$out.tmp" && mv "$out.tmp" "$out"
  echo "$feed: rail → $out ($(du -h "$out" | cut -f1))"
}

# fingerprint: content hash of the feed's tables. Zip BYTES are useless —
# a re-download re-stamps member mtimes while the tables are identical.
fingerprint() { # $1 = feed key — hashes EVERY member of a comma list.
  # Passing the whole list to unzip as one path hashed empty input: every
  # multi-feed entry got the SAME fingerprint, so refresh never rebuilt.
  local gtfs part
  gtfs=$(get "$1" gtfs)
  printf '%s ' "$gtfs"
  (IFS=','; for part in $gtfs; do unzip -p "$part" '*.txt' 2>/dev/null; done) \
    | shasum -a 256 | cut -d' ' -f1
}

# refresh: rebuild ONLY when the feed content actually changed, then cut
# the tile pyramid. GTFS updates daily or faster; this is the cron entry
# point — a no-op refresh costs a hash pass, and a real one rewrites only
# the tiles that differ (the tiler prunes and skips identical files).
# Style/curation changes need an explicit build: the manifest covers the
# FEED only, by design.
refresh() { # $1 = feed key
  local feed=$1 manifest="build/$1.manifest" now
  now=$(fingerprint "$feed")
  if [ -s "$manifest" ] && [ "$now" = "$(cat "$manifest")" ]; then
    echo "$feed: feed unchanged — nothing to do"
    return 0
  fi
  echo "$feed: feed content changed — rebuilding"
  build "$feed"
  go run ./cmd/portolan tiles --build "$(get "$feed" out)" \
    --out "build/tiles/$feed" --name "$feed"
  fingerprint "$feed" > "$manifest"
}

case "${1:-list}" in
  list) list ;;
  gtfs)  known "${2:-}" gtfs;  gtfs "$2" "${3:-}" ;;
  rail)  known "${2:-}" rail;  rail "$2" ;;
  streets) known "${2:-}" streets; streets "$2" ;;
  stops) known "${2:-}" stops; stops "$2" ;;
  shapes) known "${2:-}" shapes; shapes "$2" ;;
  build) known "${2:-}" build; build "$2" ;;
  shapesrail) known "${2:-}" shapesrail; shapesrail "$2" ;;
  shapescorr) known "${2:-}" shapescorr; shapescorr "$2" ;;
  refresh) known "${2:-}" refresh; refresh "$2" ;;
  railwindows) shift; exec tools/fetchwindows.sh "$@" ;;
  all)   known "${2:-}" all;   rail "$2"; build "$2" ;;
  *) echo "usage: $0 list|gtfs|rail|shapesrail|stops|streets|shapes|build|all|refresh [feed] [transitland-feed-id]"; exit 2 ;;
esac
