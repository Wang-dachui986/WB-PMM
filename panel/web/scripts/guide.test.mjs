// PLAYWRIGHT_MODULE=/path/to/playwright/index.js node scripts/guide.test.mjs
// In-memory bundle and synthetic native/API replies only. No credentials or chat requests.
import assert from 'node:assert/strict'
import http from 'node:http'
import { createRequire } from 'node:module'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const web = fileURLToPath(new URL('../', import.meta.url))
const require = createRequire(new URL('../package.json', import.meta.url))
const { outputFiles } = await require('esbuild').build({
  stdin: { contents: `import React from 'react'; import {createRoot} from 'react-dom/client'; import {BrowserRouter} from 'react-router-dom'; import App from './src/App'; createRoot(document.getElementById('root')).render(<React.StrictMode><BrowserRouter><App/></BrowserRouter></React.StrictMode>);`, resolveDir: web, loader: 'tsx' },
  bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent',
})
if (process.argv.includes('--bundle-only')) { console.log('PASS guide bundle'); process.exit(0) }
const css = await readFile(new URL('../src/styles.css', import.meta.url), 'utf8')
const logo = await readFile(new URL('../public/brand/wb-pmm.png', import.meta.url))
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const shots = process.env.GUIDE_SHOT_DIR
if (shots) assert.match(shots, /^\/private\/tmp\/[^/]+$/, 'screenshots must use a task directory in /private/tmp')
const server = http.createServer((req, res) => {
  const pathname = new URL(req.url, 'http://127.0.0.1').pathname
  if (req.method !== 'GET') { res.writeHead(405).end(); return }
  if (pathname === '/app.js') { res.writeHead(200, { 'Content-Type': 'text/javascript' }).end(outputFiles[0].text); return }
  if (pathname === '/app.css') { res.writeHead(200, { 'Content-Type': 'text/css' }).end(css); return }
  if (pathname === '/brand/wb-pmm.png') { res.writeHead(200, { 'Content-Type': 'image/png' }).end(logo); return }
  if (pathname === '/favicon.ico') { res.writeHead(204).end(); return }
  if (['/', '/login', '/config', '/api-models'].includes(pathname)) {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }).end('<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><link rel="stylesheet" href="/app.css"></head><body><div id="root"></div><script src="/app.js"></script></body></html>')
    return
  }
  res.writeHead(404).end('Unexpected path')
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = `http://127.0.0.1:${server.address().port}`
let browser
try {
  browser = await chromium.launch({ headless: true })
  const open = async (viewport, theme) => {
    const context = await browser.newContext({ viewport, colorScheme: theme, permissions: ['clipboard-read', 'clipboard-write'] })
    await context.addInitScript(() => {
      const defaultPrefs = { theme: 'system', autoStartGatewayOnLaunch: true, hasCompletedFirstRun: false,
        autoStartGatewayAtLogin: false, loginItemStatus: 'notRegistered', notes: {},
        about: { app_version: 'test', app_build: 'synthetic', build_time: 'unrecorded',
          gateway_commit: 'synthetic', panel_commit: 'synthetic', os_version: 'test', architecture: 'arm64' } }
      const state = { preferences: JSON.parse(sessionStorage.getItem('guide-prefs') || 'null') || defaultPrefs,
        gatewayState: sessionStorage.getItem('guide-gateway') || 'stopped', ready: true,
        lanEnabled: false, lanUrls: [], failSave: false, native: [], network: [], unexpected: [], copied: '',
        config: { revision: 'a'.repeat(64), editable: {
          port: 15432, upstream: { timeout_seconds: 120, header_timeout_seconds: 120, idle_timeout_seconds: 300 },
          pool: { max_in_flight: 3 }, session_sticky: { enabled: true, ttl: '30m' },
          desktop: { proxy_url: '' }, schedule: { checkin_enabled: false, checkin_hours: [9, 21] } },
          safe_summary: { listen_host: '127.0.0.1', schedules_disabled: false }, key_configured: true } }
      window.__guideFixture = state
      Object.defineProperty(window, '__workbuddyDocumentID', { value: 'guide-synthetic-document' })
      Object.defineProperty(window, 'webkit', { value: { messageHandlers: { workbuddyNative: { postMessage(request) {
        state.native.push(request)
        queueMicrotask(() => {
          const answer = data => window.__workbuddyBridgeReceive({ id: request.id, ok: true, data })
          const reject = message => window.__workbuddyBridgeReceive({ id: request.id, ok: false, error: { code: 'synthetic_failure', message } })
          switch (request.action) {
            case 'preferences.get': return answer(state.preferences)
            case 'preferences.update':
              if (state.failSave) { state.failSave = false; return reject('合成保存失败') }
              state.preferences = { ...state.preferences, ...request.payload }
              sessionStorage.setItem('guide-prefs', JSON.stringify(state.preferences))
              return answer(state.preferences)
            case 'desktop.getStatus': return answer({ protocol: 1, status: {
              gateway_state: state.gatewayState, panel_state: 'ready', api_base_url: state.apiBase ?? 'http://127.0.0.1:15432/v1',
              lan_enabled: state.lanEnabled, lan_api_base_urls: state.lanUrls,
              healthy_accounts: 0, total_accounts: 0, uptime_seconds: 7 } })
            case 'desktop.checkReadiness': return answer({ ready: state.ready, port: 15432,
              port_result: state.ready ? 'free' : 'occupied', config_result: 'ok',
              checks: [{ check: 'wbgui', result: 'ok' }] })
            case 'gateway.start':
              state.gatewayState = 'running_unavailable'
              sessionStorage.setItem('guide-gateway', state.gatewayState)
              return answer({})
            case 'api.copyKey': return answer({ copied: true })
            case 'gateway.applyConfig':
              state.config = { ...state.config, revision: 'b'.repeat(64),
                editable: { ...state.config.editable, allow_lan: request.payload.patch.allow_lan } }
              state.lanEnabled = state.config.editable.allow_lan === true
              return answer({ config: state.config })
            default: state.unexpected.push(`native ${request.action}`); return reject('Unexpected native action')
          }
        })
      } } } } })
      Object.defineProperty(navigator, 'clipboard', { configurable: true,
        value: { writeText: async text => { state.copied = text } } })
      const response = value => new Response(JSON.stringify(value), { status: 200,
        headers: { 'Content-Type': 'application/json' } })
      window.fetch = async (input, init = {}) => {
        const url = new URL(typeof input === 'string' ? input : input.url, location.href)
        const method = (init.method || (input instanceof Request ? input.method : 'GET')).toUpperCase()
        const key = `${method} ${url.pathname}`
        state.network.push(key)
        if (url.origin !== location.origin || method !== 'GET') {
          state.unexpected.push(`fetch ${key}`); throw Error(`Unexpected fetch ${key}`)
        }
        if (key === 'GET /api/session') return response({ authenticated: true, username: 'synthetic',
          gateway_url: 'http://127.0.0.1:15432', read_only: false, dangerous_ops: false,
          using_default_password: false })
        if (key === 'GET /api/overview') return response({ gateway_url: 'http://127.0.0.1:15432', gateway_ok: true,
          health: { healthy: 0, total: 0, service: 'workbuddy2api' }, total: 0, healthy: 0,
          cooling: 0, disabled: 0, in_flight_full: 0, in_flight: 0, sticky_sessions: 0,
          redis_mode: 'memory', file_count: 0, expired: 0, expiring: 0, warnings: [], file_issues: [],
          credits: { remain: 0, used: 0, size: 0, accounts: 0, ok: 0, failed: 0 },
          read_only: false, dangerous_ops: false, server_time: '2026-09-27T00:00:00Z' })
        if (key === 'GET /api/accounts') return response({ accounts: [], file_issues: [], gateway_ok: true,
          summary: { total: 0, healthy: 0, cooling: 0, disabled: 0, in_flight_full: 0,
            sticky_sessions: 0, redis_mode: 'memory' } })
        if (key === 'GET /api/stats') return response({ scope: 'current_run',
          stats: { enabled: true, total: { requests: 0, usage_observed_requests: 0, total_tokens: 0 } } })
        if (key === 'GET /api/config') return response(state.config)
        if (key === 'GET /api/models') return response({ data: [{ id: 'cn:synthetic-model', name: '合成模型',
          object: 'model', created: 0, owned_by: 'synthetic', context_length: 8192,
          max_output_tokens: 1024 }], count: 1 })
        state.unexpected.push(`fetch ${key}`); throw Error(`Unexpected fetch ${key}`)
      }
    })
    const page = await context.newPage()
    page.setDefaultTimeout(8000)
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => {
      const url = route.request().url()
      if (url.startsWith(`${origin}/`)) return route.continue()
      errors.push(`External request ${url}`)
      return route.abort('blockedbyclient')
    })
    await page.goto(origin)
    await page.getByRole('dialog', { name: '欢迎使用 WB-PMM' }).waitFor()
    const checkTraffic = async () => {
      const state = await page.evaluate(() => window.__guideFixture)
      assert.deepEqual(state.unexpected, [], 'unexpected native/API action')
      assert.equal(state.network.some(key => /chat|\/api\/login/.test(key)), false, 'no chat or login call')
      assert.deepEqual(errors, [], 'browser errors')
      return state
    }
    return { context, page, checkTraffic }
  }
  const dialog = page => page.getByRole('dialog')
  const button = (page, name) => dialog(page).getByRole('button', { name, exact: true })
  const geometry = async (page, target, label) => {
    await page.locator(target).waitFor({ state: 'visible' })
    await page.locator(target).scrollIntoViewIfNeeded()
    await page.waitForFunction(selector => {
      const t = document.querySelector(selector), h = document.querySelector('.guide-highlight')
      if (!t || !h) return false
      const a = t.getBoundingClientRect(), b = h.getBoundingClientRect()
      return Math.abs(a.left - b.left) < 3 && Math.abs(a.top - b.top) < 3 &&
        Math.abs(a.width - b.width) < 3 && Math.abs(a.height - b.height) < 3
    }, target)
    const result = await page.evaluate(selector => {
      const a = document.querySelector(selector).getBoundingClientRect()
      const b = document.querySelector('.guide-popover').getBoundingClientRect()
      const x = Math.max(a.left, b.left), y = Math.max(a.top, b.top)
      const overlap = Math.max(0, Math.min(a.right, b.right) - x) * Math.max(0, Math.min(a.bottom, b.bottom) - y)
      const hit = document.elementFromPoint((a.left + a.right) / 2, (a.top + a.bottom) / 2)
      return { overlap, hit: hit === document.querySelector(selector) || document.querySelector(selector).contains(hit),
        panel: { left: b.left, top: b.top, right: b.right, bottom: b.bottom }, viewport: [innerWidth, innerHeight],
        ariaModal: document.querySelector('.guide-popover').getAttribute('aria-modal') }
    }, target)
    assert.equal(result.ariaModal, 'false', `${label}: guide stays non-modal`)
    assert.ok(await button(page, '跳过使用引导').isVisible(), `${label}: skip remains available`)
    assert.ok(result.hit, `${label}: highlighted target is clickable ${JSON.stringify(result)}`)
    assert.equal(result.overlap, 0, `${label}: guide covers target ${JSON.stringify(result)}`)
    assert.ok(result.panel.left >= 0 && result.panel.top >= 0 &&
      result.panel.right <= result.viewport[0] + 1 && result.panel.bottom <= result.viewport[1] + 1,
      `${label}: guide leaves viewport ${JSON.stringify(result)}`)
  }
  const geometryMatrix = async (page, target, label) => {
    for (const viewport of [{ width: 900, height: 600 }, { width: 1280, height: 900 }]) {
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize(viewport)
        await page.emulateMedia({ colorScheme: theme })
        await geometry(page, target, `${label} ${viewport.width}x${viewport.height} ${theme}`)
      }
    }
  }

  {
    const { context, page, checkTraffic } = await open({ width: 1280, height: 900 }, 'light')
    try {
      await page.evaluate(() => { window.__guideFixture.failSave = true })
      await button(page, '跳过使用引导').click()
      await dialog(page).getByText('合成保存失败').waitFor()
      assert.equal((await checkTraffic()).preferences.hasCompletedFirstRun, false)
      await button(page, '跳过使用引导').click()
      await dialog(page).waitFor({ state: 'hidden' })
      if (shots) await page.screenshot({ path: `${shots}/dashboard-1280x900-light.png`, animations: 'disabled' })
      let state = await checkTraffic()
      assert.equal(state.preferences.hasCompletedFirstRun, true)
      assert.equal(state.preferences.autoStartGatewayOnLaunch, true)
      assert.deepEqual(state.native.filter(item => item.action === 'preferences.update').map(item => item.payload),
        [{ hasCompletedFirstRun: true }, { hasCompletedFirstRun: true }], 'skip never changes auto start')
      await page.reload()
      await dialog(page).waitFor({ state: 'hidden' })
      await page.getByRole('button', { name: '使用引导' }).click()
      await dialog(page).getByRole('heading', { name: '欢迎使用 WB-PMM' }).waitFor()
      assert.equal((await checkTraffic()).preferences.hasCompletedFirstRun, true, 'replay preserves saved completion')
    } finally { await context.close() }
  }

  {
    const { context, page, checkTraffic } = await open({ width: 1280, height: 900 }, 'light')
    try {
      if (shots) {
        await page.getByRole('button', { name: '查询全部积分' }).waitFor()
        await page.screenshot({ path: `${shots}/guide-welcome-1280x900-light.png`, animations: 'disabled' })
      }
      await geometryMatrix(page, '[data-guide="entry"]', 'welcome target')
      await button(page, '开始设置').click()
      await geometryMatrix(page, '[data-guide-nav="/config"]', 'readiness target')
      await button(page, '检查本机准备').click()
      await dialog(page).getByText('本机准备已通过').waitFor()
      await button(page, '下一步').click()
      await dialog(page).getByRole('heading', { name: '添加一个账号' }).waitFor()
      await geometryMatrix(page, '[data-guide="login-action"]', 'login target')
      await button(page, '跳过账号，继续').click()
      await dialog(page).getByRole('heading', { name: '启动 API 服务' }).waitFor()
      await geometryMatrix(page, '[data-guide="gateway"]', 'gateway target')
      await button(page, '启动 API 服务').click()
      await button(page, '继续接入说明').click()
      await dialog(page).getByRole('heading', { name: '复制客户端地址' }).waitFor()
      await geometryMatrix(page, '[data-guide="api-address"]', 'address target')
      await page.locator('[data-guide="api-address"]').click()
      assert.equal((await checkTraffic()).copied, 'http://127.0.0.1:15432/v1')
      await page.setViewportSize({ width: 900, height: 600 })
      await geometry(page, '[data-guide="api-address"]', 'resized address target')
      await page.evaluate(() => window.scrollBy(0, 60))
      await geometry(page, '[data-guide="api-address"]', 'scrolled address target')
      await page.emulateMedia({ colorScheme: 'dark' })
      await geometry(page, '[data-guide="api-address"]', 'dark address target')
      await button(page, '下一步').click()
      await dialog(page).getByRole('heading', { name: '复制 API 密钥' }).waitFor()
      await geometryMatrix(page, '[data-guide="api-key"]', 'key target')
      if (shots) {
        await page.setViewportSize({ width: 900, height: 600 })
        await page.emulateMedia({ colorScheme: 'dark' })
        await geometry(page, '[data-guide="api-key"]', 'key screenshot')
        await page.screenshot({ path: `${shots}/guide-copy-key-900x600-dark.png`, animations: 'disabled' })
      }
      await page.locator('[data-guide="api-key"]').click()
      assert.ok((await checkTraffic()).native.some(item => item.action === 'api.copyKey'))
      await button(page, '下一步').click()
      await dialog(page).getByRole('heading', { name: '选择模型，完成接入' }).waitFor()
      await geometryMatrix(page, '#api-model', 'model target')
      await page.evaluate(() => { window.__guideFixture.gatewayState = 'stopped' })
      await button(page, '完成首次设置').click()
      await dialog(page).getByText('API 服务未运行', { exact: false }).waitFor()
      assert.equal((await checkTraffic()).preferences.hasCompletedFirstRun, false,
        'latest stopped status prevents completion')
      await page.evaluate(() => { window.__guideFixture.gatewayState = 'running_unavailable' })
      await dialog(page).getByLabel('下次打开应用时启动 API').uncheck()
      await button(page, '完成首次设置').click()
      await dialog(page).waitFor({ state: 'hidden' })
      let state = await checkTraffic()
      assert.equal(state.preferences.hasCompletedFirstRun, true)
      assert.equal(state.preferences.autoStartGatewayOnLaunch, false)
      assert.equal(state.native.filter(item => item.action === 'preferences.update').length, 1,
        'failed finish does not save completion')
      await page.reload()
      await dialog(page).waitFor({ state: 'hidden' })
      assert.equal((await checkTraffic()).preferences.hasCompletedFirstRun, true)
      await page.locator('[data-guide-nav="/config"]').click()
      await page.locator('#config-allowLan').waitFor()
      assert.equal(await page.locator('#config-allowLan').isChecked(), false, 'legacy config defaults LAN off')
      await page.locator('#config-allowLan').check()
      await page.getByRole('button', { name: '保存并应用' }).click()
      await page.getByText('配置已应用。服务启动状态已由原生流程确认。').waitFor()
      state = await checkTraffic()
      assert.deepEqual(state.native.findLast(item => item.action === 'gateway.applyConfig').payload.patch,
        { allow_lan: true }, 'LAN toggle uses the existing native config transaction')
      await page.evaluate(() => { window.__guideFixture.lanUrls = [
        'http://192.168.31.20:15432/v1', 'http://10.123.234.200:15432/v1' ] })
      await page.locator('[data-guide-nav="/"]').click()
      await page.getByRole('button', { name: '复制局域网地址 2' }).waitFor()
      await page.setViewportSize({ width: 900, height: 600 })
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1),
        'dashboard LAN addresses fit 900px viewport')
      await page.getByRole('button', { name: '复制局域网地址 1' }).click()
      assert.equal((await checkTraffic()).copied, 'http://192.168.31.20:15432/v1')
      await page.clock.install()
      await page.locator('[data-guide-nav="/api-models"]').click()
      await page.getByRole('button', { name: '复制局域网地址 2' }).waitFor()
      await page.setViewportSize({ width: 900, height: 600 })
      const overflow = await page.evaluate(() => ({ width: innerWidth, scrollWidth: document.documentElement.scrollWidth,
        items: [...document.querySelectorAll('*')].filter(el => el.scrollWidth > el.clientWidth + 1)
          .slice(0, 12).map(el => [el.tagName, el.className, el.clientWidth, el.scrollWidth]) }))
      assert.ok(overflow.scrollWidth <= overflow.width + 1, `LAN addresses fit 900px viewport: ${JSON.stringify(overflow)}`)
      await page.getByRole('button', { name: '复制局域网地址 2' }).click()
      assert.equal((await checkTraffic()).copied, 'http://10.123.234.200:15432/v1')
      const beforeIPChange = await checkTraffic()
      const beforeStatusReads = beforeIPChange.native.filter(item => item.action === 'desktop.getStatus').length
      const beforeModelReads = beforeIPChange.network.filter(item => item === 'GET /api/models').length
      await page.evaluate(() => { window.__guideFixture.lanUrls = ['http://192.168.31.21:15432/v1'] })
      await page.clock.fastForward(15_001)
      await page.locator('.lan-address-row').getByText('http://192.168.31.21:15432/v1').waitFor()
      await page.getByRole('button', { name: '复制局域网地址 1' }).click()
      assert.equal((await checkTraffic()).copied, 'http://192.168.31.21:15432/v1', 'visible 15-second status refresh updates copied IP without an event')
      const afterPoll = await checkTraffic()
      assert.ok(afterPoll.native.filter(item => item.action === 'desktop.getStatus').length > beforeStatusReads)
      assert.equal(afterPoll.network.filter(item => item === 'GET /api/models').length, beforeModelReads, 'IP polling does not fetch models')
      await page.evaluate(() => {
        Object.defineProperty(document, 'hidden', { configurable: true, value: true })
        document.dispatchEvent(new Event('visibilitychange'))
        window.__guideFixture.lanUrls = ['http://10.123.234.201:15432/v1']
        Object.defineProperty(document, 'hidden', { configurable: true, value: false })
        document.dispatchEvent(new Event('visibilitychange'))
      })
      await page.locator('.lan-address-row').getByText('http://10.123.234.201:15432/v1').waitFor()
      await page.getByRole('button', { name: '复制局域网地址 1' }).click()
      assert.equal((await checkTraffic()).copied, 'http://10.123.234.201:15432/v1', 'becoming visible refreshes the copied IP immediately')
      assert.equal((await checkTraffic()).network.filter(item => item === 'GET /api/models').length, beforeModelReads,
        'visibility refresh does not fetch models')
      await page.evaluate(() => { window.__guideFixture.lanUrls = [] })
      await page.getByRole('button', { name: '重新读取' }).click()
      await page.getByText('未找到可用的局域网网卡，请连接 Wi-Fi 或以太网。').waitFor()
      await page.evaluate(() => { window.__guideFixture.gatewayState = 'stopped' })
      await page.getByRole('button', { name: '重新读取' }).click()
      await page.getByText('API 服务未运行；启动后显示局域网地址。').waitFor()
      assert.equal(await page.getByRole('button', { name: '复制局域网地址 1' }).count(), 0,
        'stopped service exposes no copyable LAN address')
      await page.evaluate(() => {
        window.__guideFixture.apiBase = 'http://127.0.0.1:15433/v1'
        window.__guideFixture.lanEnabled = false
        window.dispatchEvent(new CustomEvent('workbuddy:gateway-status', { detail: { document_id: window.__workbuddyDocumentID, epoch: '1' } }))
      })
      await page.getByTitle('http://127.0.0.1:15433/v1', { exact: true }).waitFor()
      await page.getByRole('button', { name: '复制 Base URL', exact: true }).click()
      assert.equal((await checkTraffic()).copied, 'http://127.0.0.1:15433/v1', 'native status replaces stale config address')
      assert.equal(await page.locator('.lan-access').count(), 0, 'native LAN off replaces stale config flag')
      await page.evaluate(() => {
        window.__guideFixture.apiBase = undefined
        window.__guideFixture.lanEnabled = true
      })
      await page.locator('[data-guide-nav="/"]').click()
      await page.locator('.dashboard-lan').getByText('API 服务未运行；启动后显示局域网地址。').waitFor()
      await page.locator('[data-guide-nav="/config"]').click()
      await page.locator('#config-allowLan').uncheck()
      await page.getByRole('button', { name: '保存并应用' }).click()
      await page.getByText('配置已应用。服务启动状态已由原生流程确认。').waitFor()
      assert.deepEqual((await checkTraffic()).native.findLast(item => item.action === 'gateway.applyConfig').payload.patch,
        { allow_lan: false }, 'disabling LAN uses the same config transaction')
      await page.locator('[data-guide-nav="/"]').click()
      await page.locator('.dashboard-connection').waitFor()
      assert.equal(await page.locator('.dashboard-lan').count(), 0, 'disabled LAN addresses are hidden')
    } finally { await context.close() }
  }
  console.log('PASS guide and LAN: flow, geometry, safe config, address copies, stopped status, synthetic traffic')
} finally {
  await browser?.close()
  await new Promise(resolve => server.close(resolve))
}
