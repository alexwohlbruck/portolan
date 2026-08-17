#!/usr/bin/env python3
"""Onboard every discovered North American GTFS feed.

    TRANSITLAND_API_KEY=... tools/onboard-na.py /tmp/na-allfeeds.json

Reads the all-feeds discovery output (onestop id -> operator names +
static url; NO route-type filter — portolan preprocesses every feed:
the drawn map takes the rail, MOTIS takes the processed GTFS of all of
them). Skips feeds already configured, downloads the rest, derives a
bbox from each feed's stops, writes portolan.json entries carrying the
ONESTOP ID (Transitland's stable identity — the wikidata hook), and
prints a work list for the extract/build loop (tools/onboard-na.sh).
Download failures (licence walls, dead urls) are reported, not fatal.
"""
import json, csv, io, os, re, sys, time, urllib.request, urllib.parse, zipfile

key = os.environ['TRANSITLAND_API_KEY']
discovered = json.load(open(sys.argv[1]))
cfg = json.load(open('portolan.json'))

# feeds already represented (by onestop id); legacy entries listed by hand
SKIP = {
 'f-dr5r-nyctsubway','f-dr5-mtanyclirr','f-dr7-mtanyc~metro~north',
 'f-dr5r-mtanyctbusbrooklyn','f-dr5r-mtanyctbusmanhattan','f-dr5r-mtanyctbusstatenisland','f-dr5x-mtanyctbusqueens',
 'f-drt-mbta','f-dp3-cta~rail','f-dp3-cta','f-dp3-metra','f-9-amtrak~amtrakcalifornia~amtrakcharteredvehicle',
 'f-dqc-wmata~rail','f-dqc-wmata','f-dqcjr-wmata','f-dr4-septa~rail','f-dr4-septa~bus','f-dr4e-patco',
 'f-dr5r-path~nj~us','f-dr5-nj~transit~rail','f-dq-mtamaryland~marc','f-dq-mtamaryland~subwaylink',
 'f-dq-mtamaryland~raillink','f-dqc-virginiarailwayexpress','f-cttransit~hartford~line',
 'f-9q5-metro~losangeles','f-dpz8-ttc','f-dnq-marta','f-dn-cats',
 'f-f-viarail~traindecharlevoix',
}
def slug(osid):
    base = osid.split('-')[-1]
    base = re.sub(r'[^a-z0-9]+','-', base.lower()).strip('-')
    return base[:40] or 'feed'

def get(url, to=120):
    return json.load(urllib.request.urlopen(url, timeout=to))

# an existing entry already carrying this onestop id also counts as
# configured — reruns must not duplicate
for k, f in cfg['feeds'].items():
    if f.get('onestop'):
        SKIP.add(f['onestop'])

work=[]
for osid, info in sorted(discovered.items()):
    if osid in SKIP: continue
    k = slug(osid)
    if k in cfg['feeds']:  # name collision with existing entry -> suffix
        k = (k + '-' + re.sub(r'[^a-z0-9]','',osid)[-4:])[:40]
    dest = f'data/gtfs/{k}.zip'
    if not (os.path.exists(dest) and os.path.getsize(dest) > 10000):
        try:
            fs = get('https://transit.land/api/v2/rest/feeds?'+urllib.parse.urlencode({'apikey':key,'onestop_id':osid})).get('feeds',[])
            if not fs: print(f'SKIP {osid}: not found'); continue
            fid = fs[0]['id']
            static = (fs[0].get('urls') or {}).get('static_current') or info.get('url') or ''
            ok=False
            try:
                urllib.request.urlretrieve(f'https://transit.land/api/v2/rest/feeds/{fid}/download_latest_feed_version?apikey={key}', dest)
                ok = os.path.getsize(dest)>10000 and open(dest,'rb').read(2)==b'PK'
            except Exception: pass
            if not ok and static:
                try:
                    req=urllib.request.Request(static, headers={'User-Agent':'portolan/0.3'})
                    import shutil
                    with urllib.request.urlopen(req, timeout=180) as r, open(dest,'wb') as f: shutil.copyfileobj(r,f)
                    ok = os.path.getsize(dest)>10000 and open(dest,'rb').read(2)==b'PK'
                except Exception as e: pass
            if not ok:
                print(f'FAIL-DL {osid} ({k})');
                if os.path.exists(dest): os.remove(dest)
                continue
            time.sleep(1.0)
        except Exception as e:
            print(f'FAIL {osid}: {e}'); continue
    # bbox from stops
    try:
        z=zipfile.ZipFile(dest)
        lats=[];lons=[]
        for r in csv.DictReader(io.TextIOWrapper(z.open('stops.txt'),encoding='utf-8-sig')):
            try:
                la=float(r['stop_lat']); lo=float(r['stop_lon'])
                if la and lo and -90<la<90 and -180<lo<180: lats.append(la); lons.append(lo)
            except Exception: pass
        if not lats: print(f'SKIP {osid}: no stops'); continue
        m=0.05
        bbox=[round(min(lons)-m,3),round(min(lats)-m,3),round(max(lons)+m,3),round(max(lats)+m,3)]
        name=(info.get('names') or [k])[0][:48] or k
        cfg['feeds'][k]={'name':name,'gtfs':dest,'rail':f'build/{k}-rail.geojson',
            'stops':f'build/{k}-stops.geojson',
            'out':f'build/{k}.geojson','network':f'sketches/network-{k}.json','bbox':bbox,
            'onestop':osid}
        work.append(k)
        print(f'OK {k} <- {osid} {bbox} {name}')
    except Exception as e:
        print(f'FAIL-ZIP {osid}: {e}')
json.dump(cfg, open('portolan.json','w'), indent=2)
open('/tmp/na-worklist.txt','w').write('\n'.join(work)+'\n')
print(f'{len(work)} feeds ready for extract+build', file=sys.stderr)
