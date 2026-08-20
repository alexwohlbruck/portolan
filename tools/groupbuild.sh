#!/bin/bash
# groupbuild.sh — bring every DERIVED group up: extract, build, verify, tile.
#
#   tools/groups.py --write     # first: derive the groups from the shapes
#   tools/groupbuild.sh         # then: make them real
#   tools/groupbuild.sh <key>…  # or just these
#
# A group REPLACES its members in the world index, so a group that fails
# to build would take its members off the map with it. Every group is
# therefore verified before it is allowed to stand — the members' drawn
# kilometres must survive the move — and a group that fails is DELETED
# from portolan.json, which puts its members straight back on the map.
set -uo pipefail
cd "$(dirname "$0")/.."
# ONE runner at a time. Two of these racing rewrite portolan.json from
# stale copies and delete each other's groups — which is how Washington,
# built and verified, vanished from the config at three in the morning.
exec 9>build/.groupbuild.lock
flock -n 9 || { echo "another groupbuild is running"; exit 3; }
export PATH=$PATH:/usr/local/go/bin
go build -o portolan ./cmd/portolan || exit 1

keys=("$@")
if [ ${#keys[@]} -eq 0 ]; then
  mapfile -t keys < <(jq -r '.feeds | to_entries[] | select(.value.derived) | .key' portolan.json)
fi
echo "== ${#keys[@]} derived group(s)"

drop() { # remove a failed group so its members go back to drawing themselves
  python3 - "$1" <<'PY'
import json,sys
k=sys.argv[1]
c=json.load(open('portolan.json',encoding='utf-8'))
c['feeds'].pop(k,None)
open('portolan.json','w',encoding='utf-8').write(json.dumps(c,indent=2))
PY
  echo "DROP $1 — members restored to standalone"
}

for k in "${keys[@]}"; do
  echo; echo "== $k"
  rail=$(jq -r --arg k "$k" '.feeds[$k].rail' portolan.json)
  stops=$(jq -r --arg k "$k" '.feeds[$k].stops' portolan.json)
  # the extract must COVER the window: a group whose members grew needs a
  # wider extract than the one its old hand-written entry left behind
  if ! python3 - "$k" "$rail" <<'PY'
import json,os,sys
k,rail=sys.argv[1],sys.argv[2]
b=json.load(open('portolan.json',encoding='utf-8'))['feeds'][k]['bbox']
if not os.path.exists(rail) or os.path.getsize(rail)<2048: sys.exit(1)
d=json.load(open(rail)); w=s=1e9; e=n=-1e9
for f in d['features']:
    g=f['geometry']; parts=g['coordinates'] if g['type']=='MultiLineString' else [g['coordinates']]
    for c in parts:
        for x,y in c: w=min(w,x); e=max(e,x); s=min(s,y); n=max(n,y)
sys.exit(0 if (w-0.05<=b[0] and s-0.05<=b[1] and e+0.05>=b[2] and n+0.05>=b[3]) else 1)
PY
  then
    echo "-- rail extract does not cover the window; fetching"
    tools/feed.sh rail "$k" 2>&1 | tail -1 || { drop "$k"; continue; }
  fi
  [ -s "$stops" ] || tools/feed.sh stops "$k" 2>&1 | tail -1
  # members with DIFFERENT street extracts need one merged extract, or the
  # group draws only the buses of whichever member's file it inherited
  srcs=$(jq -r --arg k "$k" '.feeds[$k].streets_from // [] | join(" ")' portolan.json)
  if [ -n "$srcs" ]; then
    dst=$(jq -r --arg k "$k" '.feeds[$k].streets' portolan.json)
    [ -s "$dst" ] || { echo "-- merging streets: $srcs"; python3 tools/mergefc.py $srcs > "$dst.tmp" && mv "$dst.tmp" "$dst"; }
  fi

  if ! GOMEMLIMIT=6000MiB nice -n 5 tools/feed.sh build "$k" > "/tmp/group-$k.log" 2>&1; then
    echo "BUILD FAILED — $(grep -m1 -e 'MATCH:' -e 'error' "/tmp/group-$k.log" | head -c 160)"
    drop "$k"; continue
  fi
  grep -E '^(match|split|fair|stations):' "/tmp/group-$k.log" | tail -3

  if ! python3 tools/groupverify.py "$k"
  then
    echo "VERIFY FAILED"; drop "$k"; continue
  fi
  ./portolan tiles --build "build/$k.geojson" --out "build/tiles/$k" --name "$k" 2>&1 | tail -1 \
    || { drop "$k"; continue; }
  echo "OK   $k"
done
