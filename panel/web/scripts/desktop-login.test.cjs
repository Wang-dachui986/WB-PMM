// Exercise the settled unauthenticated view without a service or real account.
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const web = path.resolve(__dirname, '..')
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'wbpmm-login-'))
const React = require('react')
const { renderToString } = require('react-dom/server')
const { MemoryRouter } = require('react-router-dom')
const original = React.useState
try {
  require('esbuild').buildSync({entryPoints: [path.join(web, 'src/App.tsx')], bundle: true, platform: 'node', format: 'cjs', packages: 'external', outfile: path.join(dir, 'app.cjs')})
  // Resolve dependencies of the temporary bundle from this web project.
  const Module = require('node:module')
  const instance = new Module(path.join(dir, 'app.cjs'), module)
  instance.paths = module.paths
  instance._compile(fs.readFileSync(path.join(dir, 'app.cjs'), 'utf8'), instance.filename = path.join(dir, 'app.cjs'))
  const App = instance.exports.default
  React.useState = value => original(value === true ? false : value)
  for (const surface of [
    { webkit: { messageHandlers: { workbuddyNative: {} } } },
    { chrome: { webview: {} } },
  ]) {
    global.window = surface
    const html = renderToString(React.createElement(MemoryRouter, null, React.createElement(App)))
    assert.match(html, /重试连接/)
    assert.doesNotMatch(html, /type="password"/)
    assert.doesNotMatch(html, /退出登录/)
  }
  global.window = {}
  const html = renderToString(React.createElement(MemoryRouter, null, React.createElement(App)))
  assert.match(html, /type="password"/)
  console.log('PASS desktop bridge and WebView2 fallback show reconnect; standalone web authentication remains protected')
} finally {
  React.useState = original
  delete global.window
  fs.rmSync(dir, {recursive: true, force: true})
}
