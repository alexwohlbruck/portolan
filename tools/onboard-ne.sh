#!/bin/bash
# one-shot northeast onboarding: extracts from Geofabrik, builds via feed.sh
set -uo pipefail
cd "$(dirname "$0")/.."
FAMILY=rail,subway,light_rail,tram,monorail,funicular,narrow_gauge,disused,construction
for region in us-northeast us-south; do
  all="build/pbf/$region-railall.osm.pbf"
  if [ ! -s "$all" ]; then
    pbf="build/pbf/$region.osm.pbf"
    echo "== $region: download"
    curl -sSL --fail -C - -o "$pbf" "https://download.geofabrik.de/north-america/$region-latest.osm.pbf"
    echo "== $region: filter full rail family"
    osmium tags-filter "$pbf" w/railway=$FAMILY -o "$all" --overwrite
    rm -f "$pbf"
  fi
done
region_for() { case "$1" in marc|vre|baltimore-*) echo us-south;; *) echo us-northeast;; esac; }
for f in septa-rail septa patco path nj-transit-rail marc baltimore-metro baltimore-light-rail vre hartford-line; do
  bbox=$(jq -r --arg f "$f" '.feeds[$f].bbox | join(",")' portolan.json)
  region=$(region_for "$f")
  echo "== $f: extract ($region, $bbox)"
  osmium extract -b "$bbox" "build/pbf/$region-railall.osm.pbf" -o "build/pbf/$f.osm.pbf" --overwrite \
    && osmium export "build/pbf/$f.osm.pbf" -f geojsonseq -u type_id -o "build/pbf/$f.geojsonseq" --overwrite \
    && python3 tools/pbf2rail.py "build/pbf/$f.geojsonseq" > "build/$f-rail.geojson" \
    && rm -f "build/pbf/$f.osm.pbf" "build/pbf/$f.geojsonseq"
  echo "== $f: build"
  if GOMEMLIMIT=2GiB GOMAXPROCS=4 nice -n 10 tools/feed.sh build $f > /tmp/onb-$f.log 2>&1; then
    ./portolan tiles --build "build/$f.geojson" --out "build/tiles/$f" --name "$f" | tail -1
  else
    grep -E "MATCH:|portolan:" /tmp/onb-$f.log | tail -1
  fi
done
echo ONBOARD-DONE
