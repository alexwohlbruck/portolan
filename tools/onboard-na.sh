#!/bin/bash
# extract + gated build + tiles for every feed in /tmp/na-worklist.txt.
# The rail extract merges cuts from EVERY cached regional railall — a
# bbox outside a region yields nothing and costs nothing, so no
# region-mapping table can rot.
set -uo pipefail
cd "$(dirname "$0")/.."
while read -r f; do
  [ -n "$f" ] || continue
  bbox=$(jq -r --arg f "$f" '.feeds[$f].bbox | join(",")' portolan.json)
  parts=()
  for all in build/pbf/*-railall.osm.pbf; do
    r=$(basename "$all" -railall.osm.pbf)
    if osmium extract -b "$bbox" "$all" -o "build/pbf/x-$f-$r.osm.pbf" --overwrite >/dev/null 2>&1 \
       && osmium export "build/pbf/x-$f-$r.osm.pbf" -f geojsonseq -u type_id -o "build/pbf/x-$f-$r.geojsonseq" --overwrite >/dev/null 2>&1; then
      parts+=("build/pbf/x-$f-$r.geojsonseq")
    fi
  done
  if [ ${#parts[@]} -eq 0 ]; then echo "$f: NO EXTRACT (bbox outside all regions)"; continue; fi
  python3 tools/pbf2rail.py "${parts[@]}" > "build/$f-rail.geojson" 2>/dev/null
  rm -f build/pbf/x-$f-*
  ways=$(python3 -c "import json;print(len(json.load(open('build/$f-rail.geojson'))['features']))" 2>/dev/null || echo 0)
  if [ "$ways" -lt 2 ]; then echo "$f: EMPTY EXTRACT"; continue; fi
  tools/feedstops.sh "$f" | tail -1
  if GOMEMLIMIT=2GiB GOMAXPROCS=4 nice -n 10 tools/feed.sh build "$f" > "/tmp/na-$f.log" 2>&1; then
    ./portolan tiles --build "build/$f.geojson" --out "build/tiles/$f" --name "$f" | tail -1 | sed "s/^/$f: /"
  else
    echo "$f: GATE $(grep -oE 'MATCH: [0-9]+ rail pattern' /tmp/na-$f.log | head -1)$(grep -E 'portolan:' /tmp/na-$f.log | tail -1 | head -c 120)"
  fi
done < /tmp/na-worklist.txt
echo NA-BUILD-DONE
