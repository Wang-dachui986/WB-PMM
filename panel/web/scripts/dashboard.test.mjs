// Runs the production Dashboard poller with deferred synthetic API reads and a
// controlled visibility/timer surface. No browser, service, account or network.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(path.join(web, 'package.json'))
const out = fs.realpathSync(process.argv[2])
assert.ok(out.startsWith('/private/tmp/'), 'use an isolated temporary output directory')
require('esbuild').buildSync({
  stdin: { contents: `export {pollDashboard,problemAccounts} from './src/pages/Dashboard'; export {api,ApiError} from './src/api'`, resolveDir: web, loader: 'ts' },
  bundle: true, platform: 'node', format: 'cjs', outfile: path.join(out, 'dashboard.cjs'),
})
const { pollDashboard, problemAccounts, api, ApiError } = require(path.join(out, 'dashboard.cjs'))
assert.deepEqual(problemAccounts(['healthy','disabled','token_expired','cooling','missing_credential'].map(status=>({uid:status,status}))).map(account=>account.uid),['disabled','token_expired','cooling','missing_credential'])
console.log('PASS dashboard problem count includes missing credential and excludes healthy')
if (process.argv.includes('--presentation-only')) process.exit(0)
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve() }
const events = new EventTarget(), listeners = new Set(), timers = new Map()
let hidden = true, timerID = 0
globalThis.document = {
  get hidden() { return hidden },
  addEventListener(name, callback) { assert.equal(name, 'visibilitychange'); listeners.add(callback); events.addEventListener(name, callback) },
  removeEventListener(name, callback) { listeners.delete(callback); events.removeEventListener(name, callback) },
}
const realTimeout = globalThis.setTimeout, realClear = globalThis.clearTimeout
globalThis.setTimeout = (callback, ms) => { assert.equal(ms, 15000); timers.set(++timerID, callback); return timerID }
globalThis.clearTimeout = id => timers.delete(id)
const visibility = value => { hidden = value; events.dispatchEvent(new Event('visibilitychange')) }
const calls = [], active = { overview: 0, accounts: 0 }, maximum = { overview: 0, accounts: 0 }
for (const name of ['overview', 'accounts']) api[name] = () => new Promise((resolve, reject) => {
  active[name]++; maximum[name] = Math.max(maximum[name], active[name])
  calls.push({ name, resolve: value => { active[name]--; resolve(value) }, reject: error => { active[name]--; reject(error) } })
})
const pages = [], errors = [], loading = []
const pair = index => [calls[index], calls[index + 1]]
const finish = (index, marker) => { const [overview, accounts] = pair(index); overview.resolve({ marker }); accounts.resolve({ accounts: [{ uid: marker }] }) }
const check = (condition, label) => { assert.ok(condition, label); console.log('PASS ' + label) }
let first, second, third
try {
  first = pollDashboard((o, a) => pages.push([o.marker, a[0].uid]), e => errors.push(e), v => loading.push(v))
  check(calls.length === 0 && timers.size === 0 && loading.length === 0, 'hidden mount performs no request and schedules no timer')
  visibility(false)
  check(calls.length === 2, 'becoming visible starts one overview/accounts pair immediately')
  void first.refresh(); void first.refresh(); await flush()
  check(calls.length === 2 && timers.size === 0, 'manual refresh shares the active request gate')
  pair(0)[0].reject(new ApiError(502, 'synthetic overview failure')); await flush(); void first.refresh(); await flush()
  check(calls.length === 2 && errors.length === 0 && timers.size === 0, 'early rejection keeps the gate until the other read settles')
  pair(0)[1].resolve({ accounts: [] }); await flush()
  check(errors[0] === 'synthetic overview failure' && timers.size === 1 && loading.at(-1) === false, 'settled failure publishes once and schedules one retry')
  const callback = [...timers.values()][0]; timers.clear(); callback(); await flush()
  check(calls.length === 4 && timers.size === 0, 'timer starts a single pair and waits for completion')
  finish(2, 'fresh'); await flush()
  check(JSON.stringify(pages) === JSON.stringify([['fresh', 'fresh']]) && timers.size === 1, 'successful reads publish together then schedule the next interval')
  visibility(true); void first.refresh(); await flush()
  check(timers.size === 0 && calls.length === 4, 'hidden page clears scheduled work and suppresses manual requests')
  visibility(false); check(calls.length === 6, 'visibility resumes immediately without waiting for an interval')
  visibility(true); finish(4, 'hidden-stale'); await flush()
  check(pages.length === 1 && timers.size === 0, 'hidden in-flight completion neither publishes nor schedules')
  visibility(false); check(calls.length === 8, 'next visible event starts fresh reads')
  first.stop(); const priorWrites = [pages.length, errors.length, loading.length]
  const nextPages = [], nextErrors = [], nextLoading = []
  second = pollDashboard((o, a) => nextPages.push([o.marker, a[0].uid]), e => nextErrors.push(e), v => nextLoading.push(v))
  void second.refresh(); await flush()
  check(calls.length === 8, 'remount waits for prior unfinished reads instead of overlapping')
  pair(6)[0].reject(new ApiError(500, 'old page failure')); pair(6)[1].resolve({ accounts: [] }); await flush()
  check(JSON.stringify(priorWrites) === JSON.stringify([pages.length, errors.length, loading.length]), 'stopped owner receives no late data, error or loading writes')
  check(calls.length === 10 && nextPages.length === 0 && nextErrors.length === 0, 'new owner starts its own fresh pair after old requests settle')
  finish(8, 'new-owner'); await flush()
  check(JSON.stringify(nextPages) === JSON.stringify([['new-owner', 'new-owner']]) && nextLoading.at(-1) === false, 'new owner only publishes its own result')
  void second.refresh(); second.stop()
  const stoppedWrites = [nextPages.length, nextErrors.length, nextLoading.length]
  finish(10, 'disposed-success'); await flush()
  check(JSON.stringify(stoppedWrites) === JSON.stringify([nextPages.length, nextErrors.length, nextLoading.length]), 'stopped owner also ignores a late successful pair')
  await second.refresh(); visibility(false); await flush()
  check(timers.size === 0 && listeners.size === 0 && calls.length === 12, 'stop removes timers/listeners and makes late refresh a no-op')
  let visibleWrites = 0
  third = pollDashboard(() => visibleWrites++, () => visibleWrites++, () => {})
  visibility(true); visibility(false); visibility(true); visibility(false)
  check(calls.length === 14 && timers.size === 0, 'hide/show during a flight records refresh without overlapping requests')
  pair(12)[0].resolve({ marker: 'before-visible' }); await flush()
  check(calls.length === 14, 'visible refresh waits for both old reads to settle')
  pair(12)[1].resolve({ accounts: [] }); await flush()
  check(calls.length === 16 && timers.size === 0, 'settled old pair immediately starts exactly one coalesced visible refresh')
  finish(14, 'after-visible'); await flush()
  check(visibleWrites === 2 && timers.size === 1, 'fresh visible pair returns to one normal polling timer')
  void third.refresh(); visibility(true); visibility(false); third.stop()
  finish(16, 'after-stop'); await flush()
  check(calls.length === 18 && visibleWrites === 2 && timers.size === 0 && listeners.size === 0, 'stop cancels a pending visible refresh and blocks old writes')
  check(maximum.overview === 1 && maximum.accounts === 1 && active.overview === 0 && active.accounts === 0, 'each endpoint has at most one unfinished request across page lifetimes')
  console.log('DASHBOARD_POLLING_PASS')
} finally {
  first?.stop(); second?.stop(); third?.stop()
  globalThis.setTimeout = realTimeout; globalThis.clearTimeout = realClear
  delete globalThis.document
}
