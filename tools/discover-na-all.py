#!/usr/bin/env python3
"""Enumerate EVERY Transitland GTFS feed over North America.

    TRANSITLAND_API_KEY=... tools/discover-na-all.py [checkpoint] [output]

No route-type filter: portolan is the preprocessor for every feed —
the drawn map takes the rail, MOTIS takes the processed GTFS of all of
them. Sweeps 8x6-degree tiles and pages the /feeds endpoint per tile
(the reliable one; /routes is the endpoint that times out). Records the
feed's ONESTOP ID (Transitland's stable identity, the hook to wikidata
station associations), its operator names, and its static url.

Output: { onestop_id: {names: [...], url: str, spec: str} }
Resumable via the checkpoint; rerun until it prints COMPLETE.
"""
import json, os, sys, time, urllib.parse, urllib.request

KEY = os.environ['TRANSITLAND_API_KEY']
CKPT = sys.argv[1] if len(sys.argv) > 1 else '/tmp/na-alltiles.json'
OUT = sys.argv[2] if len(sys.argv) > 2 else '/tmp/na-allfeeds.json'
API = 'https://transit.land/api/v2/rest'

ck = {'done': [], 'feeds': {}}
if os.path.exists(CKPT):
    ck.update(json.load(open(CKPT)))
done = set(ck['done'])
feeds = ck['feeds']

def get(url, timeout=120, tries=3):
    last = None
    for i in range(tries):
        try:
            req = urllib.request.Request(url, headers={'User-Agent': 'portolan/0.3'})
            return json.load(urllib.request.urlopen(req, timeout=timeout))
        except Exception as e:
            last = e
            time.sleep(3 * (i + 1))
    raise last

def save():
    ck['done'] = sorted(done)
    json.dump(ck, open(CKPT, 'w'))

tiles = []
for lon in range(-168, -52, 8):
    for lat in range(16, 64, 6):
        tiles.append((lon, lat, lon + 8, lat + 6))

err = 0
for t in tiles:
    key = str(t)
    if key in done:
        continue
    bbox = f'{t[0]},{t[1]},{t[2]},{t[3]}'
    url = f'{API}/feeds?apikey={KEY}&bbox={bbox}&limit=100'
    try:
        n = 0
        pages = 0
        while url and pages < 60:
            d = get(url)
            for f in d.get('feeds', []):
                osid = f.get('onestop_id')
                if not osid:
                    continue
                spec = (f.get('spec') or '').lower()
                if spec and spec != 'gtfs':
                    continue  # realtime/gbfs/mds companions ride other ids
                e = feeds.setdefault(osid, {'names': [], 'url': '', 'spec': spec})
                if not e['url']:
                    e['url'] = ((f.get('urls') or {}).get('static_current')) or ''
                for op in (f.get('operators') or []):
                    nm = op.get('name')
                    if nm and nm not in e['names']:
                        e['names'].append(nm)
                n += 1
            nxt = (d.get('meta') or {}).get('next')
            url = nxt + f'&apikey={KEY}' if nxt and 'apikey' not in (nxt or '') else nxt
            pages += 1
        done.add(key)
        save()
        print(f'DONE {key} +{n} ({len(feeds)} feeds total)', flush=True)
    except Exception as e:
        print(f'ERR {key} {e}', flush=True)
        err += 1
    time.sleep(0.3)

save()
remaining = [t for t in tiles if str(t) not in done]
if not remaining:
    json.dump(feeds, open(OUT, 'w'), indent=1)
    print(f'COMPLETE {len(feeds)} feeds -> {OUT}', flush=True)
else:
    print(f'INCOMPLETE {len(remaining)} tiles remain, {err} errors — rerun to retry', flush=True)
