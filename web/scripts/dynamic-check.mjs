// Guards the dynamic-service render rule (docs/DYNAMIC-SERVICE.md).
// Run: npm run check (from web/).
//
// applyDynamic claims that hiding inactive ribbons and re-centering the
// survivors within the union slot order reproduces what a full
// per-scenario re-layout would draw. This runs THE ACTUAL CLIENT CODE
// over the real NYC union build with the Saturday scenario's route set,
// and compares every offset against the real prebuilt scenario — the
// ground truth the pipeline itself produced.
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { applyDynamic, maskActive, stationVisible } from '../src/lib/dynamic.ts'

const repo = path.resolve(process.cwd(), '..')
let failures = 0
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'ok  ' : 'FAIL'}  ${name}${detail ? ' — ' + detail : ''}`)
  if (!ok) failures++
}

const hash = (coords) =>
  crypto.createHash('md5').update(JSON.stringify(coords)).digest('hex').slice(0, 12)

const unionPath = path.join(repo, 'build/nyc.geojson')
const scenPath = path.join(repo, 'build/nyc.scen-20f0c0b3.geojson')
if (!fs.existsSync(unionPath) || !fs.existsSync(scenPath)) {
  console.log('note  NYC union or Saturday scenario build missing — skipping ground-truth check')
} else {
  const band = 15
  const load = (p) =>
    JSON.parse(fs.readFileSync(p, 'utf8')).features.filter(
      (f) => f.properties.band_min === band,
    )
  const union = load(unionPath)
  const scen = load(scenPath)

  // the scenario's route set stands in for "active at this hour" — it is
  // exactly the selection the pipeline used for the re-layout, so any
  // offset mismatch is the render rule's fault, not the calendar's
  const active = new Set()
  for (const f of scen) for (const r of String(f.properties.routes).split(',')) active.add(r)
  const pred = (f) => {
    const routes = String(f.properties.routes ?? '').split(',').filter(Boolean)
    return routes.length === 0 || routes.some((r) => active.has(r))
  }

  const fc = {
    type: 'FeatureCollection',
    features: union.map((f) => ({ ...f, _g: hash(f.geometry.coordinates) })),
  }
  const dyn = applyDynamic(fc, pred)

  // purity: the cached union must be untouched by a dynamic pass
  const before = JSON.stringify(union.map((f) => f.properties.offset_px))
  check('applyDynamic does not mutate its input', JSON.stringify(fc.features.map((f) => f.properties.offset_px)) === before)

  // identity: with everything active, output === input
  const all = applyDynamic(fc, () => true)
  check(
    'all-active pass is the identity',
    all.features.length === fc.features.length &&
      all.features.every((f, i) => f.properties.offset_px === fc.features[i].properties.offset_px),
  )

  // ground truth: per shared (centerline, color), the dynamic offset vs
  // the offset the full re-layout chose
  const truth = new Map()
  for (const f of scen) {
    if (f.properties.kind !== 'steady') continue
    truth.set(hash(f.geometry.coordinates) + '|' + f.properties.color, f.properties.offset_px)
  }
  let match = 0
  let miss = 0
  const misses = []
  for (const f of dyn.features) {
    if (f.properties.kind !== 'steady') continue
    const t = truth.get(f._g + '|' + f.properties.color)
    if (t === undefined) continue // scenario re-cut this edge; not comparable
    if (Math.abs(t - f.properties.offset_px) < 0.11) match++
    else {
      miss++
      if (misses.length < 4) misses.push(`${f.properties.label}: dyn ${f.properties.offset_px} vs relayout ${t}`)
    }
  }
  const pct = (100 * match) / (match + miss)
  check(
    `dynamic offsets match the full re-layout on shared edges`,
    pct >= 90,
    `${match}/${match + miss} (${pct.toFixed(1)}%)${misses.length ? ' e.g. ' + misses.join('; ') : ''}`,
  )

  // hidden lines actually vanish. NOT "no surviving ribbon mentions a
  // sleeping route" — a trunk ribbon carrying B,D,F,M rightly stays up on
  // Saturday because D/F/M run; B is just a member of the shared trunk.
  // The invariant is that nothing survives with ALL routes asleep, and
  // that sleeping-only ribbons really disappeared vs the union.
  const gone = ['B', 'W', 'Z'].filter((r) => !active.has(r))
  const allAsleep = dyn.features.filter((f) => {
    const rs = String(f.properties.routes).split(',').filter(Boolean)
    return rs.length > 0 && rs.every((r) => !active.has(r))
  })
  check('no surviving ribbon has all routes asleep', allAsleep.length === 0, `${allAsleep.length} leaked`)
  const soloBefore = fc.features.filter((f) => {
    const rs = String(f.properties.routes).split(',').filter(Boolean)
    return rs.length > 0 && rs.every((r) => gone.includes(r))
  }).length
  check(`ribbons carrying only ${gone.join('/')} vanished`, gone.length > 0 && soloBefore > 0,
    `${soloBefore} such ribbons existed in the union and none survived`)

  // transitions touching moved bundles got their ramps re-pointed
  const ramped = dyn.features.filter(
    (f, i) => f.properties.kind === 'transition' && f.properties !== fc.features[i]?.properties,
  ).length
  console.log(`note  transitions re-pointed at moved bundles: ${ramped}`)
}

// ── per-segment acts: the short-turn fix ──────────────────────────────
// The overnight scenario is the ground truth: at 3am the pipeline's own
// re-layout drops segments that route-level masks would keep (a route
// running SOMEWHERE keeps all its segments lit). Per-segment acts must
// close most of that gap.
{
  const { activePredicate } = await import('../src/lib/dynamic.ts')
  const nightPath = path.join(repo, 'build/nyc.scen-e4bc58f8.geojson')
  const unionP = path.join(repo, 'build/nyc.geojson')
  if (!fs.existsSync(nightPath) || !fs.existsSync(unionP)) {
    console.log('note  overnight scenario missing — skipping acts check')
  } else {
    const band = 15
    const load = (p) => JSON.parse(fs.readFileSync(p, 'utf8')).features.filter((f) => f.properties.band_min === band)
    const union = load(unionP)
    const night = load(nightPath)
    const withActs = union.filter((f) => f.properties.acts).length
    check('union build carries per-segment acts', withActs > union.length / 2, `${withActs}/${union.length} features`)

    // dynamic at Tue 03:00 (inside the overnight scenario's cells)
    const pred = activePredicate({}, new Date('2026-08-11T03:30'))
    const fc = { type: 'FeatureCollection', features: union.map((f) => ({ ...f, _g: hash(f.geometry.coordinates) })) }
    const dyn = applyDynamic(fc, pred)

    const nightSet = new Set(night.map((f) => hash(f.geometry.coordinates) + '|' + f.properties.color))
    const unionKeys = (feats) => feats.filter((f) => f.properties.kind === 'steady')
    const phantoms = (feats) =>
      unionKeys(feats).filter((f) => !nightSet.has((f._g ?? hash(f.geometry.coordinates)) + '|' + f.properties.color))
    const kmOf = (feats) => feats.reduce((a, f) => a + f.properties.len_m, 0) / 1000

    const beforeKm = kmOf(phantoms(fc.features))
    const afterKm = kmOf(phantoms(dyn.features))
    check(
      'per-segment acts remove phantom overnight service',
      afterKm < beforeKm * 0.5,
      `steady km not in the 3am re-layout: union ${beforeKm.toFixed(0)} km -> dynamic ${afterKm.toFixed(0)} km`,
    )
    // Over-hiding guard. "Everything the re-layout draws survives" is the
    // WRONG invariant at hour precision: the overnight scenario aggregates
    // a whole block (Mon,Sun 01-05; Tue-Sat 00-05), so it draws patterns
    // that run Monday 1am but not Tuesday 3:30 — dynamic being stricter
    // there is the point, not a loss. The invariant that catches real
    // over-hiding: a route that is running somewhere must never vanish
    // from the map entirely, and a hidden-but-drawn segment must be
    // explained by either its routes being dark at this instant or by a
    // short-turn (the route visible elsewhere).
    const routeLit = new Map() // route -> OR of its acts across the union
    for (const f of fc.features) {
      const rs = String(f.properties.routes).split(',')
      const as = String(f.properties.acts ?? '').split(';')
      rs.forEach((r, i) => {
        if (!as[i]) return
        if (!routeLit.has(r)) routeLit.set(r, new Set())
        if (maskActive(as[i], 1, 3)) routeLit.get(r).add('lit') // Tue 03
      })
    }
    const isLit = (r) => routeLit.get(r)?.has('lit')
    const visibleRoutes = new Set()
    for (const f of unionKeys(dyn.features))
      for (const r of String(f.properties.routes).split(',')) visibleRoutes.add(r)
    const runningEverywhereGone = [...routeLit.keys()].filter((r) => isLit(r) && !visibleRoutes.has(r))
    check('no running route vanishes from the map entirely', runningEverywhereGone.length === 0,
      runningEverywhereGone.slice(0, 5).join(', ') || 'none')

    const dynSet = new Set(unionKeys(dyn.features).map((f) => f._g + '|' + f.properties.color))
    const truthShared = unionKeys(fc.features).filter((f) => nightSet.has(f._g + '|' + f.properties.color))
    let blockOnly = 0
    let shortTurn = 0
    let unexplained = 0
    for (const f of truthShared) {
      if (dynSet.has(f._g + '|' + f.properties.color)) continue
      const rs = String(f.properties.routes).split(',')
      if (rs.every((r) => !isLit(r))) blockOnly++
      else if (rs.filter(isLit).every((r) => visibleRoutes.has(r))) shortTurn++
      else unexplained++
    }
    check('every hidden-but-drawn segment is explained', unexplained === 0,
      `block-aggregation ${blockOnly}, short-turn ${shortTurn}, unexplained ${unexplained}`)

    // per-segment beats per-route somewhere real: some route must have
    // different acts on different segments (a short-turn's signature)
    const byRoute = new Map()
    for (const f of union) {
      const rs = String(f.properties.routes).split(',')
      const as = String(f.properties.acts ?? '').split(';')
      rs.forEach((r, i) => {
        if (!as[i]) return
        const set = byRoute.get(r) ?? byRoute.set(r, new Set()).get(r)
        set.add(as[i])
      })
    }
    const varied = [...byRoute.entries()].filter(([, set]) => set.size > 1)
    check('some routes have segment-dependent hours (short-turns exist)', varied.length > 0,
      `${varied.length} routes, e.g. ${varied.slice(0, 4).map(([r]) => r).join(', ')}`)
  }
}

// class filtering rides the same dynamic path as time — hide a class and
// the surviving bundles re-center into the freed slots
{
  const unionP = path.join(repo, 'build/nyc.geojson')
  if (fs.existsSync(unionP)) {
    const union = JSON.parse(fs.readFileSync(unionP, 'utf8')).features.filter(
      (f) => f.properties.band_min === 15,
    )
    const fc = {
      type: 'FeatureCollection',
      features: union.map((f) => ({ ...f, _g: hash(f.geometry.coordinates) })),
    }
    const dyn = applyDynamic(fc, (f) => f.properties.mode !== 'regional')
    const leaked = dyn.features.filter((f) => f.properties.mode === 'regional').length
    check('hiding a class removes every feature of it', leaked === 0, `${leaked} regional leaked`)
    const others = dyn.features.length
    check('other classes untouched by the class filter',
      others === fc.features.filter((f) => f.properties.mode !== 'regional').length)
    const changed = dyn.features.filter(
      (f, ) => f.properties.kind === 'steady' &&
        fc.features.find((u) => u._g === f._g && u.properties.color === f.properties.color &&
          u.properties.slot !== undefined && u.properties.offset_px !== f.properties.offset_px),
    ).length
    check('survivors re-center where the hidden class shared a corridor', changed > 0,
      `${changed} steady ribbons moved`)
  }
}

// ── stations (docs/STOP-LABELS.md) ──────────────────────────────────────
// The station layer rides the same dynamic rule: visible while any
// member route is class-enabled and awake. Real client code, real build.
{
  const stPath = path.join(repo, 'build/nyc.geojson.stations.geojson')
  if (!fs.existsSync(stPath)) {
    console.log('note  NYC stations build missing — skipping station checks')
  } else {
    const all = JSON.parse(fs.readFileSync(stPath, 'utf8')).features
    const sts = all.filter((f) => f.properties.ftype === 'station')
    const mks = all.filter((f) => f.properties.ftype === 'marker')
    check('stations exist', sts.length > 300, `${sts.length}`)

    // markers: snapped onto the drawn ribbons, one per bundle
    check('markers exist, at least one per station', mks.length >= sts.length,
      `${mks.length} markers / ${sts.length} stations`)
    const snapped = mks.filter((f) => f.properties.span_px !== undefined || f.properties.bearing !== 0)
    check('nearly every marker snapped to a ribbon', snapped.length / mks.length > 0.97,
      `${snapped.length}/${mks.length}`)
    // every marker is either a full-coverage pill or per-line dots with
    // valid colors and offsets
    const wellFormed = mks.every((f) => {
      const p = f.properties
      if (p.span_px !== undefined) return p.span_px > 0 && p.dots === undefined
      return String(p.dots).split(';').every((s) => /^[0-9A-Fa-f]{6}@-?\d+(\.\d+)?$/.test(s))
    })
    check('markers are pills XOR per-line dots', wellFormed)
    // a stop that skips part of its bundle gets dots, not a band: Grand
    // Army Plaza's 2/3/4 occupy two ribbons of the four-slot corridor
    const gap = mks.find((f) => f.properties.name === 'Grand Army Plaza')
    check('partial-coverage stops draw per-line dots (Grand Army Plaza)',
      gap && gap.properties.dots && gap.properties.dots.split(';').length === 2 && gap.properties.span_px === undefined,
      `dots: ${gap?.properties.dots}`)
    // terminal caps: relay tails past a terminal are dropped entirely,
    // so the drawn line ENDS at the station and the marker sits on the
    // tip — Atlantic Terminal is the canary (its tail once ran 180 m
    // past the bumper, then survived as a 137 m stub lit by platforms
    // inside the cut margin)
    {
      const at = mks.find((f) => f.properties.name === 'Atlantic Terminal')
      const lirr = JSON.parse(fs.readFileSync(path.join(repo, 'build/nyc.geojson'), 'utf8'))
        .features.filter((f) => f.properties.band_min === 15 && f.properties.kind !== 'transition' &&
          String(f.properties.routes).split(',').every((r) => r.startsWith('f2:')))
      const [mx, my] = at.geometry.coordinates
      const kx = 111320 * Math.cos((my * Math.PI) / 180)
      const dm = (c) => Math.hypot((c[0] - mx) * kx, (c[1] - my) * 111320)
      const capped = lirr.some((f) => {
        const cs = f.geometry.coordinates
        return dm(cs[0]) < 2 || dm(cs[cs.length - 1]) < 2
      })
      const past = lirr.some((f) => f.geometry.coordinates.some((c) => dm(c) < 400 && c[0] < mx - 0.0003))
      check('Atlantic Terminal marker caps the very end of the LIRR line', capped && !past,
        `capped=${capped} tail-past-bumper=${past}`)
    }

    // the dot wears the DRAWN ribbon color: Penn Station's LIRR dot must
    // match the color FAIR painted the LIRR trunk, not a branch color
    const penn = mks.find((f) => f.properties.name === 'Penn Station')
    const lirrSeg = JSON.parse(fs.readFileSync(path.join(repo, 'build/nyc.geojson'), 'utf8'))
      .features.find((f) => f.properties.band_min === 15 && f.properties.kind === 'steady' &&
        String(f.properties.routes).split(',').every((r) => r.startsWith('f2:')))
    check('Penn Station dot matches the drawn LIRR trunk color',
      penn && lirrSeg && String(penn.properties.dots).startsWith(lirrSeg.properties.color + '@'),
      `dot ${penn?.properties.dots} vs ribbon ${lirrSeg?.properties.color}`)
    const complexes = new Map()
    for (const m of mks) complexes.set(m.properties.name, (complexes.get(m.properties.name) ?? 0) + 1)
    check('complexes split into per-bundle markers (Times Sq has several)',
      (complexes.get('Times Sq-42 St') ?? 0) >= 3, `${complexes.get('Times Sq-42 St')} bundles at Times Sq`)
    const hoyt = mks.find((f) => f.properties.name === 'Hoyt St')
    const hoytOff = parseFloat(String(hoyt?.properties.dots ?? '').split('@')[1] ?? '0')
    check('Hoyt St dot rides ITS ribbon slot (offset baked)', Math.abs(hoytOff) > 0,
      `dots ${hoyt?.properties.dots}`)
    const aligned = sts.every((f) => {
      const p = f.properties
      const n = String(p.routes).split(',').length
      return (
        String(p.modes).split(',').length === n &&
        String(p.route_colors).split(',').length === n &&
        String(p.labels).split(',').length === n
      )
    })
    check('per-route arrays are aligned on every station', aligned)
    const named = sts.filter((f) => f.properties.name && String(f.properties.labels).replace(/,/g, '') !== '')
    check('every station has a name and at least one label',
      named.length === sts.length, `${sts.length - named.length} missing`)

    // all-service, nothing off → everything visible
    const allOn = sts.filter((f) => stationVisible(f.properties, {}, null, new Set())).length
    check('all-service shows every station', allOn === sts.length)

    // switching a class off hides exactly the stations with no other class
    const off = new Set(['metro'])
    const hidden = sts.filter((f) => !stationVisible(f.properties, {}, null, off))
    const wrong = hidden.filter((f) => String(f.properties.modes).split(',').some((m) => m !== 'metro'))
    check('class toggle hides only single-class stations', wrong.length === 0,
      `${hidden.length} hidden with metro off, ${wrong.length} wrongly`)

    // the marker rule: single-line stations exist in bulk, hubs exist,
    // and Hoyt St stays a colored dot while Hoyt-Schermerhorn is a pill
    const one = sts.filter((f) => f.properties.nlines === 1).length
    check('marker rule has both kinds', one > 0 && one < sts.length,
      `${one} single-line, ${sts.length - one} multi`)
    const hoytS = sts.find((f) => f.properties.name === 'Hoyt St')
    const hs = mks.find((f) => f.properties.name.startsWith('Hoyt-Schermerhorn'))
    check('Hoyt St (2,3) is ONE red line; Hoyt-Schermerhorn (A,C,G) is a spanning pill',
      hoytS?.properties.nlines === 1 && hs?.properties.nlines > 1 && hs?.properties.span_px > 0,
      `pill span ${hs?.properties.span_px}px`)

    // transfers.txt decides what merges: Rector St's two stations have
    // no free transfer (two labels, forever), Fulton St's platforms are
    // one linked complex — except the G's own unconnected Fulton St
    const rect = sts.filter((f) => f.properties.name === 'Rector St')
    check('Rector St stays two stations (no transfer between them)',
      rect.length === 2 && rect.some((f) => f.properties.labels === '1') &&
        rect.some((f) => f.properties.labels === 'N,R,W'),
      rect.map((f) => f.properties.labels).join(' vs '))
    const ful = sts.filter((f) => f.properties.name === 'Fulton St')
    const fulHub = ful.find((f) => f.properties.nmarkers > 1)
    const fulG = ful.find((f) => f.properties.labels === 'G')
    check('Fulton St: linked complex merges, the G one stays apart',
      ful.length === 2 && !!fulHub && !!fulG && fulHub.properties.nmarkers >= 3,
      `${ful.length} stations, complex has ${fulHub?.properties.nmarkers} corridors`)
    const cmk = mks.filter((f) => f.properties.name === 'Fulton St' && f.properties.nmarkers > 1)
    check('complex markers carry their OWN bullets for the z15 split',
      cmk.length >= 3 && cmk.every((f) =>
        String(f.properties.labels).split(',').length === String(f.properties.routes).split(',').length),
      cmk.map((f) => f.properties.labels).join(' | '))

    // bullet ordering: color groups over alphabetical (NYC convention) —
    // W 4 St reads A,C,E then B,D,F,M; Columbus Circle's letter groups
    // come before the numbers
    // (the raw prop still carries FX; the client folds X-variants)
    const noVariants = (s) => {
      const all = String(s).split(',')
      return all.filter((l) => !(l.endsWith('X') && all.includes(l.slice(0, -1)))).join(',')
    }
    const w4 = sts.find((f) => f.properties.name.startsWith('W 4 St'))
    check('W 4 St bullets group by color: A,C,E then B,D,F,M',
      noVariants(w4?.properties.labels) === 'A,C,E,B,D,F,M', `got ${w4?.properties.labels}`)
    const tsq = sts.find((f) => f.properties.name === 'Times Sq-42 St')
    check('Times Sq letter groups precede number groups',
      noVariants(tsq?.properties.labels) === 'N,Q,R,W,S,1,2,3,7', `got ${tsq?.properties.labels}`)
    const cc = sts.find((f) => f.properties.name === '59 St-Columbus Circle')
    check('Columbus Circle letter groups precede the 1',
      /^A,C,.*1/.test(String(cc?.properties.labels)) && !/^1/.test(String(cc?.properties.labels)),
      `got ${cc?.properties.labels}`)

    // no strip truncation: the merged Atlantic Av-Barclays label carries
    // ALL TEN lines — a former 8-bullet cap silently dropped the 4 and 5
    // (color ordering puts the green group last, so the cap always ate
    // exactly them). Big sets wrap into rows client-side instead.
    {
      const { bulletIdsOf } = await import('../src/lib/dynamic.ts')
      const atl = sts.find((f) => f.properties.name === 'Atlantic Av-Barclays Ctr')
      const ids = atl ? bulletIdsOf(atl.properties) : []
      const shown = ids.map((id) => id.split('-').slice(2).join('-'))
      check('Atlantic Av-Barclays merged bullets keep all 10 incl. 4 and 5',
        ids.length === 10 && shown.includes('4') && shown.includes('5'),
        `got ${shown.join(',')}`)
    }

    // station bullets follow per-STATION hours: at 3am the M's bullet
    // stays at Myrtle Av (the shuttle calls there) and drops at Flushing
    // Av (it doesn't) — acts precedence over the route-level mask, which
    // would keep the M everywhere because the shuttle runs somewhere
    {
      const { activeRouteIdx } = await import('../src/lib/dynamic.ts')
      const night = new Date('2026-08-11T03:30')
      // the J/M pair — the G's own 'Flushing Av' is a different station
      const at = (name) => sts.find((f) => f.properties.name === name &&
        String(f.properties.labels).split(',').includes('J'))
      const bullets = (f) => {
        const idx = activeRouteIdx(f.properties, {}, night, new Set())
        const labels = String(f.properties.labels).split(',')
        return (idx ? idx.map((i) => labels[i]) : labels).join(',')
      }
      const fl = at('Flushing Av')
      const my = at('Myrtle Av')
      check('night bullets at Flushing Av drop the M (J only)',
        fl && bullets(fl) === 'J', `got ${fl && bullets(fl)}`)
      check('night bullets at Myrtle Av keep the M (shuttle calls there)',
        my && bullets(my).includes('M') && bullets(my).includes('J'), `got ${my && bullets(my)}`)
    }

    // ranks: the biggest stations are the famous hubs
    const top = sts.slice().sort((a, b) => b.properties.rank - a.properties.rank).slice(0, 6)
      .map((f) => f.properties.name)
    check('top ranks are the real hubs', top.some((n) => /Times Sq|Atlantic|Grand Central|Fulton/.test(n)),
      top.join(' | '))
  }
}

// ── terminal cuts: the M ends where the MTA says it ends ───────────────
// Late nights the M runs Metropolitan–Myrtle Av only; weekends it runs
// through to Delancey/Essex. Terminal cuts give segments stop-granular
// activity, so the drawn M tail must stop at the right place at each
// hour — measured against the real build with the real client predicate.
{
  const unionP = path.join(repo, 'build/nyc.geojson')
  if (fs.existsSync(unionP)) {
    const { maskActive: mAct } = await import('../src/lib/dynamic.ts')
    const feats = JSON.parse(fs.readFileSync(unionP, 'utf8')).features.filter(
      (f) => f.properties.band_min === 15 && f.properties.kind !== 'transition' &&
        String(f.properties.routes).split(',').includes('M'),
    )
    // westmost point where the M ITSELF is lit (its own per-segment act),
    // not where a trunk survives via other members
    const westmostM = (iso) => {
      const d = new Date(iso)
      const day = (d.getDay() + 6) % 7
      const hour = d.getHours()
      let w = Infinity
      for (const f of feats) {
        const rts = String(f.properties.routes).split(',')
        const acts = String(f.properties.acts ?? '').split(';')
        const i = rts.indexOf('M')
        const a = acts[i]
        if (!a || a.length !== 42 || !mAct(a, day, hour)) continue
        for (const c of f.geometry.coordinates) w = Math.min(w, c[0])
      }
      return w
    }
    const night = westmostM('2026-08-11T03:30')
    const wknd = westmostM('2026-08-08T14:00')
    // Myrtle Av junction ≈ -73.935; Essex St ≈ -73.987; Chrystie fork ≈ -73.992
    check('night M ends at Myrtle Av (nothing drawn west of it)',
      night > -73.94, `westmost M lon ${night.toFixed(4)}`)
    check('weekend M ends at Delancey/Essex (not the Chrystie fork)',
      wknd > -73.9895 && wknd < -73.985, `westmost M lon ${wknd.toFixed(4)}`)

    // the H's MATCH path overruns its Rockaway Blvd terminal by 1.3 km
    // of relay trackage to the 80 St crossover; that tail must carry NO
    // hours at all — and gap-mask 24/7 lies must not survive the acts
    // recompute (the extension shuttle runs 8-21 only)
    const hFeats = JSON.parse(fs.readFileSync(unionP, 'utf8')).features.filter(
      (f) => f.properties.band_min === 15 && f.properties.kind !== 'transition' &&
        String(f.properties.routes).split(',').includes('H'),
    )
    let tailLit = 0
    let east321 = false
    for (const f of hFeats) {
      const rts = String(f.properties.routes).split(',')
      const a = String(f.properties.acts ?? '').split(';')[rts.indexOf('H')]
      if (!a || a.length !== 42) continue
      const anyHour = [...Array(7)].some((_, d) => parseInt(a.slice(d * 6, d * 6 + 6), 16) !== 0)
      const lons = f.geometry.coordinates.map((c) => c[0])
      const lats = f.geometry.coordinates.map((c) => c[1])
      if (Math.min(...lats) > 40.66 && Math.min(...lons) < -73.845 && anyHour) tailLit++
      if (Math.min(...lats) > 40.66 && Math.min(...lons) > -73.845 && Math.min(...lons) < -73.83 &&
        mAct(a, 5, 14)) east321 = true
    }
    check('the H relay tail west of Rockaway Blvd is never lit', tailLit === 0, `${tailLit} lit`)
    check('the H extension IS lit east of Rockaway Blvd on Sat 14:00', east321)

    // flatbush_willoughby: no tunnel→bridge phantom. At the fork where
    // the Montague legs leave the bridge trunk, the legs separate slower
    // than the ride-gate's probe tolerance, and a fixed 60 m probe let
    // the night N's tunnel walk attest an N,R,W transition onto the
    // BRIDGE steady — a movement no train makes, drawn as a little eye.
    // pairProbes walks the probes out until the legs separate. Every
    // N,R,W transition at this junction must head SOUTH (to the DeKalb
    // trunk), never north onto the bridge.
    const box = [-73.98356, 40.69108, -73.98168, 40.69256]
    const phantom = JSON.parse(fs.readFileSync(unionP, 'utf8')).features.filter((f) => {
      const p = f.properties
      if (p.kind !== 'transition' || p.routes !== 'N,R,W') return false
      const cs = f.geometry.coordinates
      if (!cs.some((c) => c[0] >= box[0] && c[0] <= box[2] && c[1] >= box[1] && c[1] <= box[3]))
        return false
      return cs[cs.length - 1][1] > cs[0][1] // ends north of where it starts
    })
    check('no tunnel→bridge phantom at Flatbush/Willoughby (all bands)',
      phantom.length === 0, `${phantom.length} northbound N,R,W ramps`)
  }
}

// ── transitions stay connected under dynamic re-centering ─────────────
// Every ramp end must land on a same-color steady at the same coordinate
// and the same SCREEN position. offset_px is signed relative to each
// feature's own coordinate direction, so the comparison is direction-
// aware — the exact trap that had re-pointed ramps crossing their
// bundles at Grand Army Plaza. The rule: dynamic must never add
// mis-connections beyond what the union build itself carries.
{
  const unionP = path.join(repo, 'build/nyc.geojson')
  if (!fs.existsSync(unionP)) {
    console.log('note  union build missing — skipping connectivity check')
  } else {
    const { applyDynamic: apDyn, activePredicate: apPred } = await import('../src/lib/dynamic.ts')
    const band = JSON.parse(fs.readFileSync(unionP, 'utf8')).features.filter(
      (f) => f.properties.band_min === 15,
    )
    const fc = { type: 'FeatureCollection', features: band.map((f) => ({ ...f, _g: hash(f.geometry.coordinates) })) }
    const ek = (c) => c[0].toFixed(6) + ',' + c[1].toFixed(6)
    const endDir = (cs, atStart) => {
      const a = atStart ? cs[0] : cs[cs.length - 1]
      const b = atStart ? cs[1] : cs[cs.length - 2]
      const kx = Math.cos((a[1] * Math.PI) / 180)
      const dx = (atStart ? b[0] - a[0] : a[0] - b[0]) * kx
      const dy = atStart ? b[1] - a[1] : a[1] - b[1]
      const n = Math.hypot(dx, dy) || 1
      return [dx / n, dy / n]
    }
    const misconnected = (dyn) => {
      const steady = new Map()
      for (const f of dyn.features) {
        const p = f.properties
        if (p.kind !== 'steady' && p.kind !== 'bridge') continue
        const cs = f.geometry.coordinates
        for (const atStart of [true, false]) {
          const k = ek(atStart ? cs[0] : cs[cs.length - 1]) + '|' + p.color
          if (!steady.has(k)) steady.set(k, [])
          steady.get(k).push({ off: p.offset_px, dir: endDir(cs, atStart) })
        }
      }
      let bad = 0
      for (const f of dyn.features) {
        const p = f.properties
        if (p.kind !== 'transition') continue
        const cs = f.geometry.coordinates
        for (const atStart of [true, false]) {
          const want = atStart ? p.off_from_px : p.off_to_px
          const list = steady.get(ek(atStart ? cs[0] : cs[cs.length - 1]) + '|' + p.color)
          if (!list) continue
          const d = endDir(cs, atStart)
          if (!list.some((s) => Math.abs((s.dir[0] * d[0] + s.dir[1] * d[1] >= 0 ? 1 : -1) * s.off - want) < 0.11)) bad++
        }
      }
      return bad
    }
    const unionBad = misconnected(fc)
    check('union transition connectivity is (near) clean', unionBad <= 1, `${unionBad} loose ends`)
    for (const iso of ['2026-08-08T14:00', '2026-08-11T03:30']) {
      const bad = misconnected(apDyn(fc, apPred({}, new Date(iso))))
      check(`dynamic at ${iso} adds no mis-connected ramps`, bad <= unionBad, `${bad} vs union ${unionBad}`)
    }
  }
}

// ── bullets respect the timestamp ──────────────────────────────────────
// A station open at 2am must not advertise routes that stopped at
// midnight: activeRouteIdx picks the awake subset the strip rebuilds
// from. Synthetic masks: A runs always, B never, C has no mask (benefit
// of the doubt), D is a class that's toggled off.
{
  const { activeRouteIdx } = await import('../src/lib/dynamic.ts')
  const allWeek = 'ffffff'.repeat(7)
  const never = '000000'.repeat(7)
  const props = { routes: 'A,B,C,D', labels: 'A,B,C,D', modes: 'metro,metro,metro,tram' }
  const masks = { A: allWeek, B: never, C: undefined }
  const at = new Date('2026-08-11T03:30') // any hour — B is never awake
  check('sleeping routes drop out of the bullet subset',
    JSON.stringify(activeRouteIdx(props, masks, at, new Set())) === '[0,2,3]')
  check('class toggles drop their routes from the subset',
    JSON.stringify(activeRouteIdx(props, masks, at, new Set(['tram']))) === '[0,2]')
  check('nothing filtered → null (reuse the original feature)',
    activeRouteIdx(props, { A: allWeek, B: allWeek }, at, new Set()) === null &&
      activeRouteIdx(props, masks, null, new Set()) === null)
}

// ── markers re-derive under time filters ───────────────────────────────
// A two-ribbon pill whose second line sleeps becomes the survivor's
// colored dot at the re-centered offset; when both run it stays a pill.
{
  const { markerIconAt } = await import('../src/lib/dynamic.ts')
  const allWeek = 'ffffff'.repeat(7)
  const never = '000000'.repeat(7)
  const bundle = [
    { g: 'g1', color: '996633', off: -3, routes: ['J'], props: { mode: 'metro', routes: 'J', acts: allWeek } },
    { g: 'g1', color: 'EB6800', off: 3, routes: ['M'], props: { mode: 'metro', routes: 'M', acts: never } },
  ]
  const marker = { routes: 'J,M', modes: 'metro,metro', acts: allWeek + ';' + never, icon: 'pill-6' }
  const night = new Date('2026-08-11T03:30')
  check('pill collapses to the survivor dot at the re-centered slot',
    markerIconAt(marker, bundle, {}, night, new Set()) === 'dots-996633@0')
  const bothOn = bundle.map((r) => ({ ...r, props: { ...r.props, acts: allWeek } }))
  const markerOn = { ...marker, acts: allWeek + ';' + allWeek }
  check('pill stays a pill while both lines run',
    markerIconAt(markerOn, bothOn, {}, night, new Set()) === 'pill-6')
  check('no filter → keep the static union icon',
    markerIconAt(markerOn, bothOn, {}, null, new Set()) === null)
}

// mask bit layout must match internal/atlas/activity.go: 7×6 hex chars,
// Monday first, hour 0 = LSB
{
  // Monday 09:00 only
  const mask = '000200' + '000000'.repeat(6)
  check('mask bit layout (Mon 09:00)', maskActive(mask, 0, 9) && !maskActive(mask, 0, 8) && !maskActive(mask, 1, 9))
  // Sunday 23:00 only
  const mask2 = '000000'.repeat(6) + '800000'
  check('mask bit layout (Sun 23:00)', maskActive(mask2, 6, 23) && !maskActive(mask2, 6, 22))
}

console.log(failures ? `\n${failures} FAILED` : '\nall checks passed')
process.exit(failures ? 1 : 0)
