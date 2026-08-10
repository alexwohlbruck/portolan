#!/bin/bash
# city.sh — bring one of the cities in portolan.json up: fetch its OSM rail
# extract, build it, see what's still missing (docs/CITIES.md).
#
#   tools/city.sh list              # every configured city + which inputs it has
#   tools/city.sh gtfs london       # Transitland feeds covering the city bbox
#   tools/city.sh gtfs london 1234  # …download one of them
#   tools/city.sh rail london       # Overpass → the city's rail geojson
#   tools/city.sh build london      # chart (+ sound when a sketch exists)
#   tools/city.sh all london        # rail then build
#
# A city needs two inputs: a GTFS zip (Transitland, or any of the operator
# portals in docs/CITIES.md) and a rail extract (Overpass). Nothing here is
# city-specific: the bbox, paths and name all come from portolan.json.
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
  [ -n "${1:-}" ] || { echo "usage: $0 $2 <city> — one of: $(feeds | tr '\n' ' ')"; exit 2; }
  jq -e --arg f "$1" '.feeds | has($f)' "$CFG" >/dev/null 2>&1 || {
    echo "unknown city '$1' — configured: $(feeds | tr '\n' ' ')"; exit 2; }
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

# gtfs: Transitland's feed registry, queried by the city's own bbox.
#   tools/city.sh gtfs paris          — list the GTFS feeds covering Paris
#   tools/city.sh gtfs paris 1234     — download feed 1234 to the configured path
# Needs TRANSITLAND_API_KEY (free: transit.land/users/sign_up). The list step
# is deliberately separate: a metro bbox matches every bus operator in the
# region, and only you can say which feed is the city's rail network.
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

# rail: one Overpass query for the city's window, converted to the geojson
# shape internal/osm reads (way/<id> + railway/service/bridge/tunnel/layer
# tags). Every service value is kept — osm.Load does the yard/siding
# filtering itself, and it needs the crossovers.
rail() { # $1 = feed key
  local feed=$1 out w s e n query tmp
  out=$(get "$feed" rail)
  [ -n "$out" ] || { echo "$feed: no 'rail' path in $CFG"; exit 2; }
  bbox "$feed"

  # every drawable infrastructure class (docs/MODES.md): the rail family
  # plus cable-supported aerialways. Buses stay out — highways would grow
  # the extract 10-100x, and bus matching is not implemented yet.
  query="[out:json][timeout:600];
(
way[\"railway\"~\"^(rail|subway|light_rail|tram|monorail|funicular|narrow_gauge)\$\"]($s,$w,$n,$e);
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
  local feed=$1 gtfs rail out net streets bboxarg first
  gtfs=$(get "$feed" gtfs); rail=$(get "$feed" rail)
  out=$(get "$feed" out);   net=$(get "$feed" network)
  streets=$(get "$feed" streets)
  [ -s "$rail" ] || { echo "$feed: no rail extract at $rail — tools/city.sh rail $feed"; exit 2; }
  # gtfs may be a comma list (primary + overlay feeds) — every part must exist
  local IFS=','
  for first in $gtfs; do
    [ -s "$first" ] || { echo "$feed: no GTFS at $first — see docs/CITIES.md"; exit 2; }
  done
  unset IFS
  bboxarg=$(jq -r --arg f "$feed" '.feeds[$f].bbox // empty | join(",")' "$CFG")
  lineag=$(jq -r --arg f "$feed" '.feeds[$f].line_agencies // empty | join(",")' "$CFG")
  set -- --gtfs "$gtfs" --rail "$rail" --out "$out"
  [ -n "$bboxarg" ] && set -- "$@" --bbox "$bboxarg"
  [ -n "$lineag" ] && set -- "$@" --line-agencies "$lineag"
  # style: config-wide block with the city's own layered on top (deep merge
  # for modes so a city naming one field keeps the rest). The atlas does the
  # same merge in Go — both paths must agree or a CLI build looks different
  # from the same build launched from the dashboard.
  local stylefile
  stylefile=$(mktemp -t portolan-style)
  jq --arg f "$feed" '{
        modes:  ((.style.modes  // {}) * (.feeds[$f].modes  // {})),
        colors: ((.style.colors // {}) + (.feeds[$f].colors // {}))
      }
      + (if .feeds[$f].bullet_order != null then {bullet_order: .feeds[$f].bullet_order}
         elif .style.bullet_order != null then {bullet_order: .style.bullet_order} else {} end)
      + (if .feeds[$f].caterpillars != null then {caterpillars: .feeds[$f].caterpillars}
         elif .style.caterpillars != null then {caterpillars: .style.caterpillars} else {} end)' "$CFG" > "$stylefile"
  if [ -s "$stylefile" ] && [ "$(jq -r 'if (.modes|length)==0 and (.colors|length)==0 and (.bullet_order == null) and (.caterpillars == null) then "empty" else "set" end' "$stylefile")" = set ]; then
    set -- "$@" --style "$stylefile"
  fi
  if [ -n "$streets" ]; then
    if [ -s "$streets" ]; then set -- "$@" --streets "$streets"
    else echo "$feed: streets configured but missing at $streets — tools/city.sh streets $feed (building rail-only)"; fi
  fi
  go run ./cmd/portolan chart "$@"
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

case "${1:-list}" in
  list) list ;;
  gtfs)  known "${2:-}" gtfs;  gtfs "$2" "${3:-}" ;;
  rail)  known "${2:-}" rail;  rail "$2" ;;
  streets) known "${2:-}" streets; streets "$2" ;;
  shapes) known "${2:-}" shapes; shapes "$2" ;;
  build) known "${2:-}" build; build "$2" ;;
  all)   known "${2:-}" all;   rail "$2"; build "$2" ;;
  *) echo "usage: $0 list|gtfs|rail|shapes|build|all [city] [transitland-feed-id]"; exit 2 ;;
esac
