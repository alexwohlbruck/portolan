#!/bin/bash
# fetchwindows.sh — fill an Overpass rail cache from a windows file
# (tools/railwindows.py output: one "w s e n" per line).
#
#   tools/fetchwindows.sh <feed> <windows.txt> <out-rail.geojson>
#
# Serial, ~6 s spacing (the public API rate-limits eager clients), cached
# per window in build/osmwin/, converging over up to 6 passes; the merge
# runs only when every window is present.
set -u
FEED=$1; WINDOWS=$2; OUT=$3
OVERPASS="${OVERPASS_URL:-https://overpass-api.de/api/interpreter}"
mkdir -p build/osmwin
pass=0; missing=1
while [ $pass -lt 6 ] && [ $missing -gt 0 ]; do
  pass=$((pass+1)); missing=0; i=0
  while read -r w s e n; do
    out="build/osmwin/$FEED-$i.geojson"
    if [ ! -s "$out" ]; then
      q="[out:json][timeout:300];way[\"railway\"=\"rail\"]($s,$w,$n,$e);out geom;"
      if curl -sS --fail --max-time 600 --data-urlencode "data=$q" "$OVERPASS" -o "$out.json" 2>>build/osmwin/errors.log \
         && head -c 200 "$out.json" | grep -q '"elements"'; then
        python3 tools/overpass2geojson.py < "$out.json" > "$out" 2>/dev/null && rm -f "$out.json"
        echo "pass$pass win $i ok ($(du -h "$out" 2>/dev/null | cut -f1))"
      else
        missing=$((missing+1)); rm -f "$out.json"
      fi
      sleep 6
    fi
    i=$((i+1))
  done < "$WINDOWS"
  echo "pass $pass done: $missing missing"
  [ $missing -gt 0 ] && sleep 30
done
if [ "$missing" = 0 ]; then
  python3 tools/mergefc.py build/osmwin/$FEED-*.geojson > "$OUT.tmp" && mv "$OUT.tmp" "$OUT"
  echo "MERGED: $(du -h "$OUT" | cut -f1) → $OUT"
else
  echo "STILL MISSING: $missing — re-run to converge"
  exit 1
fi
