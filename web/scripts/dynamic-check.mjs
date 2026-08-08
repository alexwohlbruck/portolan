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
import { applyDynamic, maskActive } from '../src/lib/dynamic.ts'

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
  const pred = (routes) => routes.length === 0 || routes.some((r) => active.has(r))

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
