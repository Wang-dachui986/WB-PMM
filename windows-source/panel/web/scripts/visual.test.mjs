// PLAYWRIGHT_MODULE=/path/to/playwright/index.js node scripts/visual.test.mjs /private/tmp/existing-output-dir
// All data and native replies are synthetic. The bundle stays in memory; only screenshots use the caller's directory.
import assert from 'node:assert/strict'
import http from 'node:http'
import path from 'node:path'
import { createRequire } from 'node:module'
import { readFile, realpath, stat } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const web = fileURLToPath(new URL('../', import.meta.url))
const require = createRequire(new URL('../package.json', import.meta.url))
const { build } = require('esbuild')
const { outputFiles } = await build({
  stdin: {
    contents: `import React from 'react'; import {createRoot} from 'react-dom/client'; import {BrowserRouter} from 'react-router-dom'; import App from './src/App'; createRoot(document.getElementById('root')).render(<React.StrictMode><BrowserRouter><App/></BrowserRouter></React.StrictMode>);`,
    resolveDir: web,
    loader: 'tsx',
  },
  bundle: true,
  write: false,
  format: 'iife',
  platform: 'browser',
  jsx: 'automatic',
  logLevel: 'silent',
})
if (process.argv.includes('--bundle-only')) {
  console.log('PASS: App bundled in memory')
  process.exit(0)
}

assert.equal(process.argv.length, 3, 'usage: node scripts/visual.test.mjs /private/tmp/existing-output-dir')
const out = await realpath(process.argv[2])
assert.ok(out.startsWith('/private/tmp/'), 'screenshots must use an existing /private/tmp subdirectory')
assert.ok((await stat(out)).isDirectory(), 'screenshot output must be a directory')
const css = await readFile(new URL('../src/styles.css', import.meta.url), 'utf8')
const logo = await readFile(new URL('../public/brand/wb-pmm.png', import.meta.url))
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const routes = [
  ['/', 'dashboard', '仪表盘'],
  ['/accounts', 'accounts', '账号管理'],
  ['/playground', 'playground', '聊天测试'],
  ['/config', 'config', '网关配置'],
  ['/settings', 'settings', '应用设置与关于'],
]
const extraRoutes = [['/api-models', 'api-models', 'API 与模型'], ['/login', 'login', '添加账号']]
const servedPaths = new Set([...routes, ...extraRoutes].map(([url]) => url))
const unexpectedHTTP = []
const server = http.createServer((req, res) => {
  const pathname = new URL(req.url, 'http://127.0.0.1').pathname
  if (req.method !== 'GET') { unexpectedHTTP.push(`${req.method} ${pathname}`); res.writeHead(405).end(); return }
  if (pathname === '/app.js') { res.writeHead(200, { 'Content-Type': 'text/javascript; charset=utf-8' }).end(outputFiles[0].text); return }
  if (pathname === '/app.css') { res.writeHead(200, { 'Content-Type': 'text/css; charset=utf-8' }).end(css); return }
  if (pathname === '/brand/wb-pmm.png') { res.writeHead(200, { 'Content-Type': 'image/png' }).end(logo); return }
  if (pathname === '/favicon.ico') { res.writeHead(204).end(); return }
  if (servedPaths.has(pathname)) {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }).end('<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><link rel="stylesheet" href="/app.css"></head><body><div id="root"></div><script src="/app.js"></script></body></html>')
    return
  }
  unexpectedHTTP.push(`${req.method} ${pathname}`)
  res.writeHead(404).end('unexpected path')
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = `http://127.0.0.1:${server.address().port}`
let browser
try {
  browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 })
  await context.addInitScript(() => {
    const query = new URLSearchParams(location.search)
    const scene = query.get('scene') || 'normal'
    const theme = query.get('theme') || 'system'
    const longWord = 'SYNTHETIC_LONG_UNBROKEN_VALUE_'.repeat(18)
    const account = (uid, nickname, status, credits) => ({
      uid, nickname, enterprise_id: 'synthetic-enterprise', domain: 'codebuddy.cn',
      has_file: true, file_name: 'synthetic.json', expires_at: 1790000000, expired: false,
      needs_refresh: false, has_refresh_token: true, in_gateway: true, status,
      cooling: status === 'cooling', cool_kind: status === 'cooling' ? 'soft_rate' : '',
      cool_remaining_sec: status === 'cooling' ? 90 : 0, disabled: false, manual_disabled: false,
      in_flight: 0, breaker_fails: 0, success_count: 12, err_total: status === 'cooling' ? 1 : 0,
      credits, live_credits: credits, credits_at: '2026-09-26T10:00:00Z',
      last_success: '2026-09-26T09:00:00Z',
    })
    const accounts = scene === 'empty' ? [] : [
      account('synthetic-cn-0001', scene === 'long' ? longWord : '合成账号一', 'healthy', 1680),
      account('synthetic-cn-0002', '合成账号二', 'cooling', 320),
    ]
    let preferences = {
      theme, autoStartGatewayOnLaunch: false, hasCompletedFirstRun: true,
      notes: scene === 'long' ? { 'synthetic-cn-0001': longWord } : {},
      about: { app_version: '0.0.0-test', app_build: 'synthetic', build_time: 'unrecorded',
        gateway_commit: 'synthetic', panel_commit: 'synthetic', os_version: 'test', architecture: 'arm64' },
    }
    const nativeStatus = { gateway_state: scene === 'empty' ? 'stopped' : 'running_ready',
      api_base_url: 'http://127.0.0.1:7863/v1', healthy_accounts: scene === 'empty' ? 0 : 1,
      total_accounts: accounts.length }
    const config = { revision: 'a'.repeat(64), editable: {
      port: 7863, upstream: { timeout_seconds: 120, header_timeout_seconds: 120, idle_timeout_seconds: 300 },
      pool: { max_in_flight: 3 }, session_sticky: { enabled: true, ttl: '30m' },
      desktop: { proxy_url: '' }, schedule: { checkin_enabled: false, checkin_hours: [9, 21] },
    }, safe_summary: { listen_host: '127.0.0.1', schedules_disabled: false }, key_configured: true }
    const total = { remain: accounts.reduce((sum, item) => sum + item.credits, 0), used: 500,
      size: 2500, accounts: accounts.length, ok: accounts.length, failed: 0 }
    const overview = { gateway_url: 'http://127.0.0.1:7863', gateway_ok: true,
      health: { healthy: nativeStatus.healthy_accounts, total: accounts.length, service: 'workbuddy2api' },
      total: accounts.length, healthy: nativeStatus.healthy_accounts, cooling: accounts.filter(item => item.cooling).length,
      disabled: 0, in_flight_full: 0, in_flight: 0, sticky_sessions: 1, redis_mode: 'memory',
      file_count: accounts.length, expired: 0, expiring: 0,
      warnings: scene === 'long' ? [`合成布局提示 ${longWord}`] : [], file_issues: [],
      credits: total, read_only: false, dangerous_ops: false, server_time: '2026-09-26T10:00:00Z' }
    const model = { id: 'cn:synthetic-model', name: scene === 'long' ? longWord : '合成测试模型',
      object: 'model', created: 0, owned_by: 'synthetic', context_length: 8192,
      max_output_tokens: 1024, reasoning_supported_efforts: ['low', 'high'], reasoning_default_effort: 'low' }
    const response = (value, status = 200, headers = {}) => new Response(JSON.stringify(value), {
      status, headers: { 'Content-Type': 'application/json', ...headers },
    })
    const failure = () => response({ error: '合成读取失败' }, 503)
    const unexpected = []
    const network = []
    const native = []
    Object.defineProperty(window, '__workbuddyDocumentID', { value: 'visual-synthetic-document', configurable: false })
    Object.defineProperty(window, '__visualTest', { value: { unexpected, network, native, scene, preferences } })
    Object.defineProperty(window, 'webkit', { configurable: true, value: { messageHandlers: {
      workbuddyNative: { postMessage(request) {
        native.push(request.action)
        let data
        switch (request.action) {
          case 'preferences.get': data = preferences; break
          case 'preferences.update':
            if (request.payload.note && query.get('holdNote') === '1') {
              window.__visualTest.releaseNote = () => {
                preferences = { ...preferences, notes: { ...preferences.notes,
                  [request.payload.note.uid]: request.payload.note.value } }
                window.__workbuddyBridgeReceive({ id: request.id, ok: true, data: preferences })
              }
              return
            }
            preferences = { ...preferences,
              ...(request.payload.theme ? { theme: request.payload.theme } : {}),
              ...(typeof request.payload.autoStartGatewayOnLaunch === 'boolean'
                ? { autoStartGatewayOnLaunch: request.payload.autoStartGatewayOnLaunch } : {}),
              notes: request.payload.note
                ? { ...preferences.notes, [request.payload.note.uid]: request.payload.note.value }
                : preferences.notes }
            data = preferences; break
          case 'desktop.getStatus': data = { protocol: 1, status: nativeStatus }; break
          default:
            unexpected.push(`native ${request.action}`)
            queueMicrotask(() => window.__workbuddyBridgeReceive({ id: request.id, ok: false,
              error: { code: 'synthetic_unexpected_action', message: 'Unexpected native action' } }))
            return
        }
        queueMicrotask(() => window.__workbuddyBridgeReceive({ id: request.id, ok: true, data }))
      } },
    } } })
    window.fetch = async (input, init = {}) => {
      const url = new URL(typeof input === 'string' ? input : input.url, location.href)
      const method = (init.method || (input instanceof Request ? input.method : 'GET')).toUpperCase()
      network.push(`${method} ${url.pathname}`)
      if (url.origin !== location.origin || !url.pathname.startsWith('/api/')) {
        unexpected.push(`fetch ${method} ${url.href}`)
        throw new Error(`Unexpected fetch ${method} ${url.pathname}`)
      }
      const key = `${method} ${url.pathname}`
      if (key === 'GET /api/session') return response({ authenticated: true, username: 'synthetic-user',
        read_only: false, dangerous_ops: false, using_default_password: false,
        gateway_url: 'http://127.0.0.1:7863', password_changeable: false })
      if (key === 'GET /api/overview') return scene === 'error' ? failure() : response(overview)
      if (key === 'GET /api/accounts') return scene === 'error' ? failure() : response({ accounts, file_issues: [], gateway_ok: true,
        summary: { total: accounts.length, healthy: nativeStatus.healthy_accounts, cooling: overview.cooling,
          disabled: 0, in_flight_full: 0, sticky_sessions: 1, redis_mode: 'memory' } })
      if (method === 'GET' && /^\/api\/accounts\/[^/]+$/.test(url.pathname)) {
        const item = accounts.find(value => value.uid === decodeURIComponent(url.pathname.split('/').at(-1)))
        return item ? response({ account: item, credits: { remain: item.credits, used: 0, size: item.credits, packages: 1 } }) : response({ error: '合成账号不存在' }, 404)
      }
      if (key === 'GET /api/stats') return response({ scope: 'current_run', stats: { enabled: true,
        total: { requests: 20, usage_observed_requests: 20, total_tokens: 1200 } } })
      if (key === 'GET /api/models') return response({ data: scene === 'empty' ? [] : [model], count: scene === 'empty' ? 0 : 1 })
      if (key === 'GET /api/config') return scene === 'error' ? failure() : response(config)
      if (key === 'POST /api/chat/stream') {
        const content = scene === 'long' ? `合成长内容：${'这是一段只用于视觉布局验收的虚构回复。'.repeat(36)}${longWord}` : '合成回复'
        const body = `data: ${JSON.stringify({ content })}\n\ndata: ${JSON.stringify({ done: true, finish_reason: 'stop' })}\n\n`
        return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (key === 'POST /api/chat') return response({ result: { content: '合成回复', model: model.id,
        finish_reason: 'stop' }, elapsed_ms: 1 })
      unexpected.push(`fetch ${key}`)
      throw new Error(`Unexpected fetch ${key}`)
    }
  })
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', error => pageErrors.push(error.message))
  await page.route('**/*', route => {
    const url = route.request().url()
    if (url.startsWith(`${origin}/`)) return route.continue()
    unexpectedHTTP.push(`external ${url}`)
    return route.abort('blockedbyclient')
  })
  const open = async (route, scene = 'normal', theme = 'system') => {
    await page.goto(`${origin}${route}?scene=${scene}&theme=${theme}`)
    await page.waitForLoadState('load')
    await page.waitForFunction(expected => document.documentElement.style.colorScheme === expected,
      theme === 'system' ? 'light dark' : theme)
  }
  const check = async label => {
    const result = await page.evaluate(() => ({ width: innerWidth,
      document: document.documentElement.scrollWidth, body: document.body.scrollWidth,
      unexpected: window.__visualTest?.unexpected ?? ['fixture missing'],
      background: getComputedStyle(document.documentElement).getPropertyValue('--bg').trim(),
      preference: document.documentElement.style.colorScheme }))
    assert.ok(result.document <= result.width + 1 && result.body <= result.width + 1,
      `${label}: horizontal document overflow ${JSON.stringify(result)}`)
    assert.deepEqual(result.unexpected, [], `${label}: unexpected API/native action`)
    assert.deepEqual(pageErrors, [], `${label}: uncaught page error`)
    assert.deepEqual(unexpectedHTTP, [], `${label}: unexpected HTTP request`)
    return result
  }
  const shot = async label => page.screenshot({ path: path.join(out, `${label}.png`), fullPage: true,
    animations: 'disabled' })
  const waitHeading = name => page.getByRole('heading', { name, exact: true, level: 1 }).waitFor()
  let lightBackground
  for (const [width, height] of [[960, 640], [1280, 900], [1728, 1000]]) {
    await page.setViewportSize({ width, height })
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
    for (const [route, slug, heading] of routes) {
      await open(route)
      await waitHeading(heading)
      if (route === '/') await page.getByText('合成账号一').first().waitFor()
      if (route === '/accounts') await page.getByRole('button', { name: '合成账号一' }).waitFor()
      if (route === '/playground') await page.locator('#chat-model option[value="cn:synthetic-model"]').waitFor({ state: 'attached' })
      const result = await check(`light-${slug}-${width}`)
      if (route === '/') {
        await page.locator('.brand-text strong').filter({hasText:'WB-PMM控制台'}).waitFor()
        assert.equal(await page.locator('.brand-text span').textContent(), 'WBPMM网关')
        assert.ok(await page.locator('.brand-dot').evaluate(img => img.complete && img.naturalWidth > 0), 'brand logo loads')
        for (const name of ['复制 API 地址', '复制 API Key']) {
          const bounds = await page.getByRole('button', {name, exact:true}).boundingBox()
          assert.ok(bounds && bounds.y >= 0 && bounds.y + bounds.height <= height, `${name} must appear without scrolling at ${width}`)
        }
        const panel = await page.locator('.dashboard-page').boundingBox()
        if (width >= 1280) assert.ok(panel && panel.y + panel.height <= height, 'normal dashboard fits the common viewport')
        assert.match(await page.locator('h1').evaluate(el => getComputedStyle(el).fontFamily), /Songti/)
      }
      if (route === '/' && width === 1280) lightBackground = result.background
      await shot(`light-${slug}-${width}`)
    }
  }
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
  let darkBackground
  for (const [route, slug, heading] of routes) {
    await open(route)
    await waitHeading(heading)
    const result = await check(`dark-${slug}`)
    if (route === '/') darkBackground = result.background
    await shot(`dark-${slug}`)
  }
  assert.ok(lightBackground && darkBackground && lightBackground !== darkBackground,
    'system light and dark must resolve different workspace backgrounds')
  for (const [system, override, expected] of [
    ['light', 'dark', darkBackground], ['dark', 'light', lightBackground],
  ]) {
    await page.emulateMedia({ colorScheme: system, reducedMotion: 'reduce' })
    await open('/settings', 'normal', override)
    await waitHeading('应用设置与关于')
    const result = await check(`${override}-override-on-${system}`)
    assert.equal(result.background, expected, `explicit ${override} theme must override OS ${system}`)
    assert.equal(result.preference, override)
    await shot(`${override}-override-on-${system}`)
  }
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await open('/settings')
  await waitHeading('应用设置与关于')
  await page.locator('#preference-theme').selectOption('dark')
  await page.getByRole('button', { name: '保存偏好' }).click()
  await page.getByText('设置已保存').waitFor()
  assert.equal((await check('theme-save')).background, darkBackground, 'saved theme must apply immediately')
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  for (const [route, slug, heading] of routes) {
    await open(route, 'empty')
    await waitHeading(heading)
    await check(`empty-${slug}`)
    await shot(`empty-${slug}`)
  }
  for (const [route, slug] of [['/', 'dashboard'], ['/accounts', 'accounts'], ['/config', 'config']]) {
    await open(route, 'error')
    await page.getByText('合成读取失败').waitFor()
    await check(`error-${slug}`)
    await shot(`error-${slug}`)
  }
  for (const [route, slug, heading] of routes) {
    await open(route, 'long')
    await waitHeading(heading)
    if (route === '/playground') {
      await page.locator('#chat-model option[value="cn:synthetic-model"]').waitFor({ state: 'attached' })
      await page.getByRole('button', { name: '发送', exact: true }).click()
      await page.getByText('已完成', { exact: true }).waitFor()
    }
    await check(`long-${slug}`)
    await shot(`long-${slug}`)
  }
  for (const [route, slug, heading] of extraRoutes) {
    await open(route)
    await waitHeading(heading)
    await check(`route-${slug}`)
    await shot(`route-${slug}`)
  }
  await open('/accounts')
  await waitHeading('账号管理')
  const noteButton = page.getByRole('button', { name: '备注', exact: true }).first()
  await noteButton.click()
  const dialog = page.getByRole('dialog', { name: '本地账号备注' })
  await dialog.waitFor()
  await page.waitForFunction(() => !!document.querySelector('dialog[open]')?.contains(document.activeElement))
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press(i === 7 ? 'Shift+Tab' : 'Tab')
    const focus = await page.evaluate(() => ({ inside: !!document.querySelector('dialog[open]')?.contains(document.activeElement),
      body: document.activeElement === document.body }))
    // Chromium can put focus on the document body between native-dialog tab cycles.
    // Background controls must remain inert, and the next Tab must re-enter the dialog.
    assert.ok(focus.inside || focus.body, `Tab reached a background control at step ${i}`)
    if (focus.body) {
      await page.keyboard.press('Tab')
      assert.equal(await page.evaluate(() => !!document.querySelector('dialog[open]')?.contains(document.activeElement)), true,
        `Tab must re-enter native dialog after body at step ${i}`)
    }
  }
  await shot('accounts-dialog')
  await page.keyboard.press('Escape')
  await dialog.waitFor({ state: 'hidden' })
  const trigger = await noteButton.elementHandle()
  await page.waitForFunction(element => document.activeElement === element, trigger, { timeout: 1000 })
  assert.notEqual(await page.evaluate(() => getComputedStyle(document.documentElement).overflow), 'hidden',
    'closing the dialog must release document scroll lock')
  await page.goto(`${origin}/accounts?scene=normal&theme=system&holdNote=1`)
  await waitHeading('账号管理')
  await page.getByRole('button', { name: '备注', exact: true }).first().click()
  const busyDialog = page.getByRole('dialog', { name: '本地账号备注' })
  await busyDialog.waitFor()
  await busyDialog.locator('#account-note').fill('合成备注')
  await busyDialog.getByRole('button', { name: '保存备注' }).click()
  await page.waitForFunction(() => typeof window.__visualTest?.releaseNote === 'function')
  assert.equal(await busyDialog.getByRole('button', { name: '关闭' }).isDisabled(), true,
    'busy dialog disables its close button')
  assert.equal(await page.evaluate(() => getComputedStyle(document.documentElement).overflow), 'hidden',
    'open native dialog must lock document scrolling')
  await page.keyboard.press('Escape')
  assert.equal(await busyDialog.isVisible(), true, 'Escape must not dismiss a busy dialog')
  await page.mouse.click(5, 5)
  assert.equal(await busyDialog.isVisible(), true, 'backdrop must not dismiss a busy dialog')
  await check('busy-dialog')
  await shot('accounts-dialog-busy')
  await page.evaluate(() => window.__visualTest.releaseNote())
  await busyDialog.waitFor({ state: 'hidden' })
  await page.waitForFunction(() => getComputedStyle(document.documentElement).overflow !== 'hidden')
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce', contrast: 'more' })
  await open('/')
  await waitHeading('仪表盘')
  assert.equal(await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches &&
    matchMedia('(prefers-contrast: more)').matches), true, 'reduced motion and high contrast media must be active')
  await check('dashboard-high-contrast-reduced-motion')
  await shot('dashboard-high-contrast-reduced-motion')
  await page.emulateMedia({ contrast: 'no-preference' })
  await open('/settings')
  await waitHeading('应用设置与关于')
  await page.keyboard.press('Tab')
  assert.equal(await page.evaluate(() => {
    const active = document.activeElement
    if (!active?.matches(':focus-visible')) return false
    const style = getComputedStyle(active)
    return (style.outlineStyle !== 'none' && style.outlineWidth !== '0px') || style.boxShadow !== 'none'
  }), true, 'keyboard focus must have a visible outline or ring')
  await check('keyboard-focus')
  console.log(`PASS visual routes=${routes.length + extraRoutes.length}, widths=960/1280/1728, system+override themes, empty/error/long, dialog focus, screenshots=${out}`)
} finally {
  await browser?.close()
  server.closeAllConnections()
  await new Promise(resolve => server.close(resolve))
}
