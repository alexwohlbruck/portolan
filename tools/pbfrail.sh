#!/bin/bash
# pbfrail.sh — continental rail extract from Geofabrik PBFs, no Overpass.
#
#   tools/pbfrail.sh <out-rail.geojson> <geofabrik-path>...
#   tools/pbfrail.sh build/amtrak-rail.geojson north-america/us-northeast north-america/us-south ...
#
# The public Overpass API cannot serve a continent (it rate-limits and
# connection-throttles eager clients, and every mirror has its own
# weather); Geofabrik serves the same planet as static files. Each region
# downloads (resumable), filters to railway=rail with osmium (streaming,
# small memory), and the PBF is deleted before the next download so peak
# disk stays one region. Conversion to the extract shape osm.Load reads
# happens in tools/pbf2rail.py.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=$1; shift
command -v osmium >/dev/null || { echo "need osmium (brew install osmium-tool)"; exit 2; }
mkdir -p build/pbf
parts=()
for region in "$@"; do
  name=$(basename "$region")
  pbf="build/pbf/$name.osm.pbf"
  rail="build/pbf/$name-rail.osm.pbf"
  if [ ! -s "$rail" ]; then
    echo "== $region: download"
    curl -sSL --fail -C - -o "$pbf" "https://download.geofabrik.de/$region-latest.osm.pbf"
    echo "== $region: filter railway=rail ($(du -h "$pbf" | cut -f1))"
    osmium tags-filter "$pbf" w/railway=rail -o "$rail" --overwrite
    rm -f "$pbf"
  else
    echo "== $region: filtered cache hit"
  fi
  parts+=("$rail")
done
echo "== export + convert"
for rail in "${parts[@]}"; do
  gj="${rail%.osm.pbf}.geojsonseq"
  [ -s "$gj" ] || osmium export "$rail" -f geojsonseq -u type_id -o "$gj" --overwrite
done
python3 tools/pbf2rail.py "${parts[@]/%.osm.pbf/.geojsonseq}" > "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
echo "rail → $OUT ($(du -h "$OUT" | cut -f1))"
