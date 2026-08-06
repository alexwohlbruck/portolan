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
    printf '%-10s %-14s %-8s %-8s %-8s %s\n' "$f" "$(get "$f" name)" \
      "$(mark "$(get "$f" gtfs)")" "$(mark "$(get "$f" rail)")" \
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

  query="[out:json][timeout:600];
way[\"railway\"~\"^(rail|subway|light_rail|tram)\$\"]($s,$w,$n,$e);
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
  local feed=$1 gtfs rail out net
  gtfs=$(get "$feed" gtfs); rail=$(get "$feed" rail)
  out=$(get "$feed" out);   net=$(get "$feed" network)
  [ -s "$rail" ] || { echo "$feed: no rail extract at $rail — tools/city.sh rail $feed"; exit 2; }
  [ -s "$gtfs" ] || { echo "$feed: no GTFS at $gtfs — see docs/CITIES.md"; exit 2; }
  go run ./cmd/portolan chart --gtfs "$gtfs" --rail "$rail" --out "$out"
  if [ -s "$net" ]; then
    go run ./cmd/portolan sound --network "$net" --build "$out" || true
  else
    echo "$feed: no drawn network at $net — draw one in the atlas sketch editor to score"
  fi
}

case "${1:-list}" in
  list) list ;;
  gtfs)  known "${2:-}" gtfs;  gtfs "$2" "${3:-}" ;;
  rail)  known "${2:-}" rail;  rail "$2" ;;
  build) known "${2:-}" build; build "$2" ;;
  all)   known "${2:-}" all;   rail "$2"; build "$2" ;;
  *) echo "usage: $0 list|gtfs|rail|build|all [city] [transitland-feed-id]"; exit 2 ;;
esac
