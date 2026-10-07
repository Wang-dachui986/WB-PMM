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
  stdin: { contents: `export {pollDashboard,problemAccounts,queryAllCredits} from './src/pages/Dashboard'; export {api,ApiError} from './src/api'`, resolveDir: web, loader: 'ts' },
  bundle: true, platform: 'node', format: 'cjs', outfile: path.join(out, 'dashboard.cjs'),
})
const { pollDashboard, problemAccounts, queryAllCredits, api, ApiError } = require(path.join(out, 'dashboard.cjs'))
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
let first, second, third, fourth, fifth
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
  const resumed = []
  fourth = pollDashboard((o) => resumed.push(o.marker), () => {}, () => {})
  check(calls.length === 20, 'new dashboard starts one visible read pair')
  fourth.pause(); finish(18, 'while-paused'); await flush()
  check(resumed.length === 0 && timers.size === 0, 'pause discards in-flight reads and clears timer')
  fourth.resume(); check(calls.length === 22, 'resume starts a fresh read pair')
  finish(20, 'resumed'); await flush(); fourth.stop()
  check(JSON.stringify(resumed) === JSON.stringify(['resumed']) && timers.size === 0, 'resumed dashboard only publishes post-maintenance data')
  const nativeWindow = new EventTarget()
  nativeWindow.webkit = { messageHandlers: { workbuddyNative: { postMessage() {} } } }
  nativeWindow.__workbuddyDocumentID = 'dashboard-doc'
  globalThis.window = nativeWindow
  const gatewayEvent = epoch => {
    const event = new Event('workbuddy:gateway-status')
    event.detail = { document_id: 'dashboard-doc', epoch: String(epoch) }
    nativeWindow.dispatchEvent(event)
  }
  const gatewayPages = []
  fifth = pollDashboard(o => gatewayPages.push(o.marker), () => {}, () => {})
  check(calls.length === 24, 'native dashboard starts one visible read pair')
  fifth.pause(); gatewayEvent(1); gatewayEvent(2); finish(22, 'paused-gateway'); await flush()
  check(gatewayPages.length === 0 && calls.length === 24, 'paused native status events do not poll during maintenance')
  fifth.resume(); check(calls.length === 26, 'resume drains paused native status events with one read pair')
  gatewayEvent(3); gatewayEvent(4); gatewayEvent(3)
  finish(24, 'gateway-first'); await flush()
  check(calls.length === 28, 'busy status events coalesce into one queued read pair')
  finish(26, 'gateway-latest'); await flush(); fifth.stop()
  check(JSON.stringify(gatewayPages) === JSON.stringify(['gateway-first', 'gateway-latest']) && timers.size === 0, 'queued native status refresh publishes latest state and unsubscribes')
  check(maximum.overview === 1 && maximum.accounts === 1 && active.overview === 0 && active.accounts === 0, 'each endpoint has at most one unfinished request across page lifetimes')
  console.log('DASHBOARD_POLLING_PASS')
} finally {
  first?.stop(); second?.stop(); third?.stop(); fourth?.stop(); fifth?.stop()
  globalThis.setTimeout = realTimeout; globalThis.clearTimeout = realClear
  delete globalThis.document
  delete globalThis.window
}

const uids = Array.from({ length: 100 }, (_, i) => `uid-${i}`)
const batches = [], ended = [], cancelled = [], progress = []
const run = { cancelled: false, flow: null }
const operations = {
  begin: (action, batch) => {
    assert.equal(action, 'credits')
    assert.ok(batch.length > 0 && batch.length <= 20)
    const id = batches.length
    batches.push([...batch])
    return { ready: Promise.resolve({ instance_id: `lease-${id}` }), end: async () => { ended.push(id) }, cancel: async () => { cancelled.push(id) } }
  },
  batch: async (batch, lease) => {
    assert.equal(lease.instance_id, `lease-${batches.length - 1}`)
    assert.deepEqual(batch, batches.at(-1))
    return { id: String(batches.length - 1), running: true }
  },
  task: async id => {
    const items = batches[Number(id)].map((uid, i) => ({ uid, ok: !(id === '2' && i === 3) }))
    return { running: false, done: items.length, items }
  },
  wait: async () => {},
}
const result = await queryAllCredits(uids, true, run, value => progress.push(value), operations)
assert.deepEqual(batches.map(batch => batch.length), [20, 20, 20, 20, 20])
assert.deepEqual(ended, [0, 1, 2, 3, 4])
assert.deepEqual(cancelled, [])
assert.equal(progress.at(-1).done, 100)
assert.equal(result.confirmed, 100)
assert.equal(result.okUIDs.size, 99)
assert.equal(result.failed, 1)
assert.equal(result.issue, undefined)
console.log('PASS 100 accounts query in five leased batches; failed account is excluded')

const interrupted = { cancelled: false, flow: null }
const cancelCalls = []
const interruptedResult = await queryAllCredits(uids, true, interrupted, () => {}, {
  begin: () => ({ ready: Promise.resolve({ instance_id: 'lease' }), end: async () => assert.fail('cancelled flow must not end normally'), cancel: async () => { cancelCalls.push('cancel') } }),
  batch: async () => ({ id: 'first', running: true }),
  task: async () => assert.fail('cancelled task must not be polled'),
  wait: async () => { interrupted.cancelled = true },
})
assert.deepEqual(cancelCalls, ['cancel'])
assert.equal(interruptedResult.confirmed, 0)
console.log('PASS cancellation stops before task poll and before the next batch')

let signalPendingCancel = () => {}, resolveLateBatch = () => {}
const pendingBatchRun = { cancelled: false, flow: null, cancelWait: new Promise(resolve => { signalPendingCancel = resolve }) }
let pendingBatchCalls = 0, pendingCancelCalls = 0, pendingTaskCalls = 0
const pendingBatch = new Promise(resolve => { resolveLateBatch = resolve })
const pendingResultPromise = queryAllCredits(uids, true, pendingBatchRun, () => {}, {
  begin: () => ({ ready: Promise.resolve({ instance_id: 'lease' }), end: async () => assert.fail('cancelled flow must not end normally'), cancel: async () => { pendingCancelCalls++ } }),
  batch: () => { pendingBatchCalls++; return pendingBatch },
  task: async () => { pendingTaskCalls++; return { running: false } },
  wait: async () => {},
})
await flush()
assert.equal(pendingBatchCalls, 1)
pendingBatchRun.cancelled = true
signalPendingCancel()
const pendingResult = await pendingResultPromise
resolveLateBatch({ id: 'late', running: false, done: 20, items: uids.slice(0, 20).map(uid => ({ uid, ok: true })) })
await flush()
assert.equal(pendingCancelCalls, 1)
assert.equal(pendingTaskCalls, 0)
assert.equal(pendingBatchCalls, 1)
assert.equal(pendingResult.confirmed, 0)
assert.equal(pendingBatchRun.flow, null)
console.log('PASS cancellation exits a hung task submission, confirms cleanup and ignores its late response')

const incomplete = { cancelled: false, flow: null }
let incompleteBatches = 0
const incompleteResult = await queryAllCredits(uids, true, incomplete, () => {}, {
  begin: () => ({ ready: Promise.resolve({ instance_id: 'lease' }), end: async () => {}, cancel: async () => {} }),
  batch: async () => { incompleteBatches++; return { id: 'partial', running: false, done: 1, items: [{ uid: uids[0], ok: true }] } },
  task: async () => assert.fail('already finished task must not be polled'),
  wait: async () => {},
})
assert.equal(incompleteBatches, 1)
assert.equal(incompleteResult.confirmed, 1)
assert.match(incompleteResult.issue, /未完整返回结果/)
console.log('PASS incomplete batch stops further batches and preserves unconfirmed count')

const cleanupFailed = { cancelled: false, flow: null }
const stuckFlow = { ready: Promise.resolve({ instance_id: 'lease' }), end: async () => { throw new Error('native cleanup failed') }, cancel: async () => {} }
let cleanupBatches = 0
const cleanupResult = await queryAllCredits(uids, true, cleanupFailed, () => {}, {
  begin: () => stuckFlow,
  batch: async batch => { cleanupBatches++; return { id: 'done', running: false, done: batch.length, items: batch.map(uid => ({ uid, ok: true })) } },
  task: async () => assert.fail('already finished task must not be polled'),
  wait: async () => {},
})
assert.equal(cleanupBatches, 1)
assert.equal(cleanupFailed.flow, stuckFlow)
assert.match(cleanupResult.issue, /native cleanup failed/)
console.log('PASS failed native cleanup retains flow and blocks the next batch')

const timedOut = { cancelled: false, flow: null }
const realNow = Date.now
let fakeNow = 1000, timeoutCancels = 0, timeoutBatches = 0
Date.now = () => fakeNow
try {
  const timeoutResult = await queryAllCredits(uids, true, timedOut, () => {}, {
    begin: () => ({ ready: Promise.resolve({ instance_id: 'lease' }), end: async () => assert.fail('timed out flow must not end normally'), cancel: async () => { timeoutCancels++ } }),
    batch: async () => { timeoutBatches++; return { id: 'running', running: true } },
    task: async () => assert.fail('expired task must not be polled'),
    wait: async () => { fakeNow += 13 * 60_000 + 1 },
  })
  assert.equal(timeoutBatches, 1)
  assert.equal(timeoutCancels, 1)
  assert.equal(timedOut.flow, null)
  assert.match(timeoutResult.issue, /超时/)
  console.log('PASS task deadline cancels maintenance and stops the next batch')
} finally { Date.now = realNow }
