const assert = require('node:assert/strict')
const fs = require('node:fs'), path = require('node:path'), os = require('node:os')
const Module = require('node:module')
const React = require('react')
const { renderToStaticMarkup } = require('react-dom/server')
const { MemoryRouter } = require('react-router-dom')
const web = path.resolve(__dirname, '..'), dir = fs.mkdtempSync(path.join(os.tmpdir(), 'wbpmm-credit-view-'))
const saved = { useState: React.useState, useRef: React.useRef, useEffect: React.useEffect }
try {
  const file = path.join(dir, 'dashboard.cjs')
  require('esbuild').buildSync({ entryPoints: [path.join(web, 'src/pages/Dashboard.tsx')], bundle: true, platform: 'node', format: 'cjs', packages: 'external', outfile: file })
  const mod = new Module(file, module); mod.paths = module.paths; mod.filename = file; mod._compile(fs.readFileSync(file, 'utf8'), file)
  global.window = { webkit: { messageHandlers: { workbuddyNative: {} } } }
  for (const queried of [true, false]) {
    const accounts = Array.from({length: 20}, (_, i) => ({uid: `account-${i}`, nickname: `账号 ${i}`, status: 'healthy', credits_status: queried ? i === 0 ? 'ok' : 'auth_rejected' : undefined, credits_error: queried && i > 0 ? 'HTTP 401' : undefined, has_file: true}))
    const ov = {gateway_ok: true,total:20,healthy:20,cooling:0,disabled:0,file_count:20,expired:0,expiring:0,in_flight:0,in_flight_full:0,sticky_sessions:0,credits:{remain:0}}
    const state = [ov, accounts, null, false, null, false, null, [], null, null, {}, null, null]
    let index = 0
    React.useState = () => [state[index++], () => {}]
    React.useRef = value => ({ current: value })
    React.useEffect = () => {}
    const element = mod.exports.default({})
    Object.assign(React, saved)
    const html = renderToStaticMarkup(React.createElement(MemoryRouter, null, element))
    assert.doesNotMatch(html, /全部有效|可用 \/ 总数|所有账号状态正常/)
    assert.match(html, /候选 \/ 已载入/)
    if (queried) {
      assert.match(html, /积分认证失败 19 个/)
      assert.match(html, /badge-danger[^>]*><span class="badge-dot"><\/span>积分认证失败/)
      assert.match(html, /stat-value text-danger[^>]*>20</)
    } else assert.match(html, /积分已验证 0 个/)
  }
  console.log('PASS 1 success plus 19 rejected and unqueried dashboards do not claim all credentials valid')
} finally { Object.assign(React, saved); delete global.window; fs.rmSync(dir, { recursive: true, force: true }) }
