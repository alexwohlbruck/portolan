#!/bin/bash
# railall.sh — cache a region's FULL drawable-infrastructure PBF:
# the rail family + lifecycle tags + ferry lanes + cable aerialways.
# A second pass keeps the stop family — station/halt/stop nodes AND the
# station ways/relations the way-only track filter cannot see — so one
# download serves both track matching and stop-name matching.
#   tools/railall.sh north-america/us-midwest ...
set -uo pipefail
cd "$(dirname "$0")/.."
mkdir -p build/pbf
for region in "$@"; do
  name=$(basename "$region")
  all="build/pbf/$name-railall.osm.pbf"
  stops="build/pbf/$name-stopsall.osm.pbf"
  [ -s "$all" ] && [ -s "$stops" ] && { echo "== $region cached"; continue; }
  pbf="build/pbf/$name.osm.pbf"
  echo "== $region: download"
  curl -sSL --fail -C - -o "$pbf" "https://download.geofabrik.de/$region-latest.osm.pbf" || { echo "$region DOWNLOAD FAILED"; continue; }
  echo "== $region: filter ($(du -h "$pbf" | cut -f1))"
  osmium tags-filter "$pbf" \
    w/railway=rail,subway,light_rail,tram,monorail,funicular,narrow_gauge,disused,construction \
    w/route=ferry w/aerialway=cable_car,gondola,mixed_lift \
    -o "$all" --overwrite || { echo "$region FILTER FAILED"; continue; }
  osmium tags-filter "$pbf" \
    n/railway=station,halt,tram_stop,stop \
    nwr/public_transport=station,stop_position \
    nwr/railway=station,halt \
    n/aerialway=station nwr/amenity=ferry_terminal \
    -o "$stops" --overwrite && rm -f "$pbf"
done
echo RAILALL-DONE
