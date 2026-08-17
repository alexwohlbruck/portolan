#!/bin/bash
# feedstops.sh — build a feed's OSM stops GeoJSON from the cached
# regional stopsall PBFs (no Overpass). Cuts the feed's bbox from EVERY
# region cache — a bbox outside a region yields nothing and costs
# nothing — and merges the cuts with id-level dedup.
#   tools/feedstops.sh <feed> [more feeds ...]
set -uo pipefail
cd "$(dirname "$0")/.."
for f in "$@"; do
  bbox=$(jq -r --arg f "$f" '.feeds[$f].bbox | select(.) | join(",")' portolan.json)
  [ -n "$bbox" ] || { echo "$f: no bbox in portolan.json"; continue; }
  out=$(jq -r --arg f "$f" '.feeds[$f].stops // empty' portolan.json)
  [ -n "$out" ] || out="build/$f-stops.geojson"
  parts=()
  for all in build/pbf/*-stopsall.osm.pbf; do
    [ -s "$all" ] || continue
    r=$(basename "$all" -stopsall.osm.pbf)
    if osmium extract -b "$bbox" "$all" -o "build/pbf/s-$f-$r.osm.pbf" --overwrite >/dev/null 2>&1 \
       && osmium export "build/pbf/s-$f-$r.osm.pbf" -f geojsonseq -u type_id -o "build/pbf/s-$f-$r.geojsonseq" --overwrite >/dev/null 2>&1; then
      parts+=("build/pbf/s-$f-$r.geojsonseq")
    fi
  done
  if [ ${#parts[@]} -eq 0 ]; then echo "$f: NO STOPS EXTRACT (bbox outside all regions)"; continue; fi
  python3 tools/pbf2stops.py "${parts[@]}" > "$out" 2>/tmp/feedstops-$f.err
  n=$(cat /tmp/feedstops-$f.err)
  rm -f build/pbf/s-$f-* /tmp/feedstops-$f.err
  echo "$f: $n -> $out"
done
