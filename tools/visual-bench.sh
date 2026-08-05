#!/bin/bash
# visual-bench.sh — capture Apple Maps + portolan renders at every named
# problem area (docs/VISUAL-BENCH.md). Outputs land in refs/ (gitignored:
# Apple imagery is copyrighted — local comparison only).
#
#   tools/visual-bench.sh apple      # drive Maps.app + screencapture (takes over the screen)
#   tools/visual-bench.sh apple KEY  # capture just one problem area
#   tools/visual-bench.sh portolan   # open the atlas snapall pages in your browser
#   tools/visual-bench.sh index      # regenerate refs/index.html side-by-side viewer
#   tools/visual-bench.sh all
#
# The portolan side needs the atlas running (make atlas / portolan atlas).
# The Apple side needs Screen Recording permission for your terminal.
set -euo pipefail
cd "$(dirname "$0")/.."

ATLAS="http://127.0.0.1:8765"

# problem spots come from locations.json (owner-editable: append a row
# [key, name, lat, lon, feed] (+ optional [w,s,e,n] bbox) and rerun) —
# "key lat lon zoom" per feed. Zoom follows the drawn bbox: an area box
# spanning a few blocks wants z17; a branch-scale box wants z14.
areas() { # $1 = feed id
  jq -r --arg f "$1" '.[] | select(.[4]==$f) |
    (if (.[5]? and (.[5]|length)==4) then
       ( [ (.[5][2]-.[5][0])*111.32*((.[2]*3.14159/180)|cos),
           (.[5][3]-.[5][1])*110.54 ] | max ) as $km |
       (if   $km <= 0.35 then 17 elif $km <= 0.8 then 16
        elif $km <= 1.6  then 15 elif $km <= 3.2 then 14
        elif $km <= 6.4  then 13 else 12 end)
     else 16 end) as $z |
    "\(.[0]) \(.[2]) \(.[3]) \($z)"' locations.json
}
AREAS_5="$(areas 5)"
AREAS_29="$(areas 29)"

apple() { # optional $1 = single area key
  local only="${1:-}"
  mkdir -p refs/apple
  echo "capturing Apple Maps transit views — keep the screen free…"
  open -a Maps; sleep 3
  for feed in 5 29; do
    areas=$([ "$feed" = 5 ] && echo "$AREAS_5" || echo "$AREAS_29")
    echo "$areas" | while read -r key lat lon zoom; do
      [ -z "$key" ] && continue
      [ -n "$only" ] && [ "$key" != "$only" ] && continue
      open "maps://?ll=${lat},${lon}&z=${zoom:-16}&t=r"
      sleep 6
      screencapture -x "refs/apple/${key}.png"
      echo "  refs/apple/${key}.png"
    done
  done
}

portolan() {
  mkdir -p refs/portolan
  echo "opening atlas snapall pages (each tab saves via /api/snap, then titles 'snap done')…"
  open "${ATLAS}/map?feed=5&bare=1&snapall=1"
  sleep 2
  open "${ATLAS}/map?feed=29&bare=1&snapall=1"
  echo "watch refs/portolan/ fill; close the tabs when their titles read 'snap done'"
}

index() {
  {
    echo '<!doctype html><meta charset="utf-8"><title>portolan · visual bench</title>'
    echo '<style>body{background:#141418;color:#ddd;font:14px system-ui;margin:20px}'
    echo 'h2{margin:28px 0 8px;font-size:15px}img{width:49%;border:1px solid #333;border-radius:8px;background:#fff}'
    echo '.pair{display:flex;gap:1%}</style>'
    echo "<h1 style='font-size:17px'>Apple (left) vs portolan (right) — $(date +%F)</h1>"
    for feed in 5 29; do
      areas=$([ "$feed" = 5 ] && echo "$AREAS_5" || echo "$AREAS_29")
      echo "$areas" | while read -r key lat lon zoom; do
        [ -z "$key" ] && continue
        echo "<h2>${key} · ${lat},${lon}</h2><div class=pair>"
        echo "<img src='apple/${key}.png' loading=lazy><img src='portolan/${key}.png' loading=lazy></div>"
      done
    done
  } > refs/index.html
  echo "refs/index.html"
}

case "${1:-all}" in
  apple) apple "${2:-}" ;;
  portolan) portolan ;;
  index) index ;;
  all) portolan; apple; index ;;
  *) echo "usage: $0 apple|portolan|index|all"; exit 2 ;;
esac
