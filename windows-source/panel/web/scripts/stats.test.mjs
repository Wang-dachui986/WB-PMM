// Run: PLAYWRIGHT_MODULE=/path/to/playwright/index.js node scripts/stats.test.mjs
// Uses the existing esbuild and a supplied Playwright; serves synthetic data only.
import assert from 'node:assert/strict'
import http from 'node:http'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const webDir = fileURLToPath(new URL('../', import.meta.url))
const { outputFiles } = await build({
  stdin: {
    contents: `import React from 'react'; import {createRoot} from 'react-dom/client';
      import StatsPage from './src/pages/StatsPage';
      createRoot(document.getElementById('root')).render(<StatsPage session={{read_only:false}} />);`,
    loader: 'tsx', resolveDir: webDir,
  },
  bundle: true, write: false, format: 'iife', jsx: 'automatic',
})

const row = {
  model: 'synthetic-model', requests: 2, success: 1, failed: 1, streaming: 1,
  usage_observed_requests: 0, credit_observed_requests: 0,
  avg_ttfb_ms: 1, avg_latency_ms: 2, tokens_per_sec: 0,
  prompt_tokens: 0, completion_tokens: 0, total_tokens: 0,
  cache_hit_tokens: 0, cache_miss_tokens: 0, cache_write_tokens: 0, cache_hit_rate: 0,
  credit: 0, credit_per_req: 0,
}
let payload = {
  scope: 'current_run',
  stats: { enabled: true, since: '2026-09-26T00:00:00Z', now: '2026-09-26T00:00:30Z', uptime_sec: 30, total: row, models: [row] },
}
let unavailable = false
let resetCalls = 0
let holdNextRead
const server = http.createServer((req, res) => {
  if (req.url === '/stats.js') {
    res.setHeader('Content-Type', 'text/javascript')
    res.end(outputFiles[0].text)
  } else if (req.url === '/api/stats/reset') {
    assert.equal(req.method, 'POST')
    resetCalls++
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ ok: true, message: 'Synthetic reset completed' }))
  } else if (req.url.startsWith('/api/stats')) {
    if (holdNextRead) {
      const hold = holdNextRead
      holdNextRead = undefined
      const body = JSON.stringify(hold.error ? { error: 'Obsolete read error' } : payload)
      hold.arrived(() => {
        res.statusCode = hold.error ? 502 : 200
        res.setHeader('Content-Type', 'application/json')
        res.end(body)
      })
      return
    }
    res.statusCode = unavailable ? 502 : 200
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify(unavailable ? { error: 'Synthetic gateway unavailable' } : payload))
  } else {
    res.setHeader('Content-Type', 'text/html')
    res.end('<!doctype html><html lang="zh"><body><div id="root"></div><script src="/stats.js"></script></body></html>')
  }
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
let browser
try {
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage()
  const origin = `http://127.0.0.1:${server.address().port}`
  await page.route('**/*', route => route.request().url().startsWith(origin + '/') ? route.continue() : route.abort())
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(origin)
  await page.getByRole('heading', { name: '本次运行统计', exact: true }).waitFor()
  await page.getByText('未返回用量', { exact: true }).first().waitFor()
  assert.equal(await page.getByRole('heading', { name: '时间趋势', exact: true }).count(), 0)
  assert.equal(await page.getByRole('heading', { name: '官方 API 价格换算', exact: true }).count(), 0)
  const tokenCard = page.locator('.stat').filter({ has: page.getByText('输入 / 输出', { exact: true }) })
  assert.equal(await tokenCard.locator('.stat-value').textContent(), '— / —')

  const refresh = () => page.getByRole('button', { name: '刷新', exact: true }).click()
  row.usage_observed_requests = 2
  row.credit_observed_requests = 2
  await refresh()
  await page.getByText('用量已返回 2/2 次', { exact: true }).first().waitFor()
  assert.equal(await tokenCard.locator('.stat-value').textContent(), '0 / 0')

  row.usage_observed_requests = 1
  row.credit_observed_requests = 0
  row.prompt_tokens = 7
  payload.stats.since = '2026-09-26T00:00:20Z'
  await refresh()
  await page.getByText('用量已返回 1/2 次', { exact: true }).first().waitFor()
  await page.getByText('已开始新的统计周期；网关重启或重置会清空本次累计。', { exact: true }).waitFor()
  assert.equal(await tokenCard.locator('.stat-value').textContent(), '7 / 0')

  unavailable = true
  await refresh()
  await page.getByText(/统计暂不可用，保留最后一次成功快照/).waitFor()
  assert.equal(await tokenCard.locator('.stat-value').textContent(), '7 / 0')
  unavailable = false

  delete row.usage_observed_requests
  delete row.credit_observed_requests
  await refresh()
  await page.getByText('用量完整性未知', { exact: true }).first().waitFor()
  assert.equal(await tokenCard.locator('.stat-value').textContent(), '— / —')

  await page.getByRole('button', { name: '重置本次统计', exact: true }).click()
  await page.getByRole('button', { name: '取消', exact: true }).click()
  assert.equal(resetCalls, 0)
  await page.getByRole('button', { name: '重置本次统计', exact: true }).click()
  await page.getByRole('button', { name: '确认重置', exact: true }).click()
  await page.getByText('Synthetic reset completed', { exact: true }).waitFor()
  assert.equal(resetCalls, 1)

  // A pre-reset read may finish after the reset and fresh snapshot. Neither its
  // success nor its error may overwrite the current period or observation.
  for (const error of [false, true]) {
    row.usage_observed_requests = 1
    row.credit_observed_requests = 0
    row.prompt_tokens = 11
    payload.stats.since = '2026-09-26T01:00:00Z'
    await refresh()
    await tokenCard.locator('.stat-value').getByText('11 / 0', { exact: true }).waitFor()
    const delayedRead = new Promise(resolve => { holdNextRead = { error, arrived: resolve } })
    await refresh()
    const releaseRead = await delayedRead
    row.prompt_tokens = 22
    payload.stats.since = '2026-09-26T02:00:00Z'
    await page.getByRole('button', { name: '重置本次统计', exact: true }).click()
    await page.getByRole('button', { name: '确认重置', exact: true }).click()
    await tokenCard.locator('.stat-value').getByText('22 / 0', { exact: true }).waitFor()
    const oldResponse = page.waitForResponse(response => response.url().includes('/api/stats') && response.status() === (error ? 502 : 200))
    releaseRead()
    await oldResponse
    // Flush the completed fetch and subsequent React render without a fixed wait.
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    assert.equal(await tokenCard.locator('.stat-value').textContent(), '22 / 0')
    assert.equal(await page.getByText('Obsolete read error', { exact: true }).count(), 0)
  }

  payload = {
    stats: payload.stats, mode: 'peak', costs: {},
    total: { priced: true, total: 0, cached_input_cost: 0, miss_input_cost: 0, output_cost: 0 },
    pricing: { models: {}, editable: false }, priced: [], unpriced: [],
  }
  await page.reload()
  await page.getByRole('heading', { name: '请求统计', exact: true }).waitFor()
  await page.getByRole('heading', { name: '官方 API 价格换算', exact: true }).waitFor()
  await page.getByRole('heading', { name: '时间趋势', exact: true }).waitFor()
  assert.deepEqual(errors, [])
  console.log('PASS: desktop missing/zero/partial/unknown usage, new period, retained snapshot, reset cancel/confirm, delayed pre-reset success/error ignored, server compatibility; no page errors')
} finally {
  if (browser) await browser.close()
  await new Promise(resolve => server.close(resolve))
}
