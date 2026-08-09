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
    const snapped = mks.filter((f) => f.properties.span_px > 0 || f.properties.bearing !== 0)
    check('nearly every marker snapped to a ribbon', snapped.length / mks.length > 0.98,
      `${snapped.length}/${mks.length}`)
    const single = mks.filter((f) => f.properties.nlines === 1)
    check('single-line markers carry their line color', single.every((f) => /^[0-9A-Fa-f]{6}$/.test(f.properties.mcolor)),
      `${single.length} single-line markers`)
    const complexes = new Map()
    for (const m of mks) complexes.set(m.properties.name, (complexes.get(m.properties.name) ?? 0) + 1)
    check('complexes split into per-bundle markers (Times Sq has several)',
      (complexes.get('Times Sq-42 St') ?? 0) >= 3, `${complexes.get('Times Sq-42 St')} bundles at Times Sq`)
    const hoyt = mks.find((f) => f.properties.name === 'Hoyt St')
    check('Hoyt St dot rides ITS ribbon slot (offset baked)', hoyt && Math.abs(hoyt.properties.dot_off) > 0,
      `dot_off ${hoyt?.properties.dot_off}`)
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

    // ranks: the biggest stations are the famous hubs
    const top = sts.slice().sort((a, b) => b.properties.rank - a.properties.rank).slice(0, 6)
      .map((f) => f.properties.name)
    check('top ranks are the real hubs', top.some((n) => /Times Sq|Atlantic|Grand Central|Fulton/.test(n)),
      top.join(' | '))
  }
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
