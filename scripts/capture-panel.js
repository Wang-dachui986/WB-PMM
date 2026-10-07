#!/usr/bin/env node
/* ============================================================================
 * 面板界面截图
 *
 * 用途：对**真实面板**（wbgui 内嵌的前端）逐页截图，用于视觉评审与回归对比。
 *
 * 为什么不去截原生 App 的窗口：
 *   · 窗口位置由系统决定，截图区域要跟着算，很容易拍到无关内容；
 *   · 一次只能截到当前路由，要换页得人工点击；
 *   · 依赖 GUI 会话，在自动化环境里不稳定。
 * 这里改为直接拉起 wbgui + wb2api，用 Playwright 控制浏览器逐页访问 ——
 * 覆盖全部路由、视口固定、可反复复跑。
 *
 * 用法：
 *   NODE_PATH=<playwright-core 的 node_modules> node scripts/capture-panel.js
 *   OUT=<输出目录> node scripts/capture-panel.js
 *   VIEWPORT=1180x720 node scripts/capture-panel.js
 *
 * 退出码：0 全部成功；1 存在失败
 *
 * 注意：使用临时数据目录与临时端口，不读取也不修改任何真实凭证。
 * ==========================================================================*/

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const net = require('net');
const { spawn } = require('child_process');

const REPO = path.resolve(__dirname, '..');
const HELPER_DIR = process.env.WB_HELPERS_SRC || path.join(os.homedir(), 'workbuddy2api-build', 'bin');
const WBGUI = path.join(HELPER_DIR, 'wbgui');
const WB2API = path.join(HELPER_DIR, 'wb2api');
const OUT = process.env.OUT || path.join(REPO, 'docs', 'panel-screenshots');

const [vw, vh] = (process.env.VIEWPORT || '1180x720').split('x').map(Number);

/** 页面清单：[路由, 截图名, 说明]。顺序即侧栏顺序。 */
const ROUTES = [
  ['/', 'dashboard', '仪表盘'],
  ['/accounts', 'accounts', '账号管理'],
  ['/stats', 'stats', '请求统计'],
  ['/login', 'login-wizard', '添加账号'],
  ['/playground', 'playground', '聊天测试'],
  ['/config', 'config', '网关配置'],
  ['/system', 'system', '系统'],
];

/* ── 工具 ──────────────────────────────────────────────────────────────── */

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** 找一个空闲端口。 */
function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const p = srv.address().port;
      srv.close(() => resolve(p));
    });
  });
}

function resolveChromium() {
  let playwright;
  try {
    playwright = require('playwright-core');
  } catch {
    console.error('找不到 playwright-core。请先安装：npm install playwright-core');
    console.error('并设置 NODE_PATH 指向其 node_modules 目录。');
    process.exit(1);
  }

  const candidates = [playwright.chromium.executablePath()];
  const cache = path.join(os.homedir(), 'Library', 'Caches', 'ms-playwright');
  if (fs.existsSync(cache)) {
    for (const d of fs.readdirSync(cache)) {
      if (!d.startsWith('chromium-')) continue;
      candidates.push(path.join(cache, d, 'chrome-mac-arm64',
        'Google Chrome for Testing.app', 'Contents', 'MacOS', 'Google Chrome for Testing'));
      candidates.push(path.join(cache, d, 'chrome-mac', 'Chromium.app', 'Contents', 'MacOS', 'Chromium'));
    }
  }
  for (const c of candidates) if (c && fs.existsSync(c)) return c;
  console.error('找不到可用的 Chromium。请先执行：npx playwright install chromium');
  process.exit(1);
}

/** 等待子进程在 stdout 上输出一行匹配 pattern 的内容。 */
function waitForLine(child, pattern, timeoutMs, label) {
  return new Promise((resolve, reject) => {
    let buf = '';
    const timer = setTimeout(() => {
      reject(new Error(`${label} 在 ${timeoutMs}ms 内没有就绪`));
    }, timeoutMs);

    const onData = (chunk) => {
      buf += chunk.toString('utf8');
      const m = buf.match(pattern);
      if (m) {
        clearTimeout(timer);
        child.stdout.off('data', onData);
        resolve(m);
      }
    };
    child.stdout.on('data', onData);
    child.once('exit', (code) => {
      clearTimeout(timer);
      reject(new Error(`${label} 提前退出（退出码 ${code}）`));
    });
  });
}

/* ── 主流程 ─────────────────────────────────────────────────────────────── */

const children = [];
let workDir = '';

function cleanup() {
  for (const c of children) {
    try { c.kill('SIGTERM'); } catch { /* 忽略 */ }
  }
  if (workDir) {
    try { fs.rmSync(workDir, { recursive: true, force: true }); } catch { /* 忽略 */ }
  }
}

(async () => {
  const { chromium } = require('playwright-core');
  const executablePath = resolveChromium();

  for (const f of [WBGUI, WB2API]) {
    if (!fs.existsSync(f)) {
      console.error(`找不到 helper：${f}`);
      console.error('请先运行 scripts/build.sh，或设置 WB_HELPERS_SRC。');
      process.exit(1);
    }
  }

  workDir = fs.mkdtempSync(path.join(os.tmpdir(), 'wb-panel-shot-'));
  const authDir = path.join(workDir, 'auths');
  fs.mkdirSync(authDir, { recursive: true });

  const problems = [];

  /* ── 1) 起网关：让页面处于「服务在跑、暂无可用账号」的真实状态，
     比「网关不可达」更适合做视觉评审。 ─────────────────────────────── */
  const gwPort = await freePort();
  const gwConfig = path.join(workDir, 'gateway-config.json');
  fs.writeFileSync(gwConfig, JSON.stringify({
    listen: `127.0.0.1:${gwPort}`,
    api_key: 'wbd_sk_' + '0'.repeat(48),
    auth_dir: authDir,
    state_file: path.join(workDir, 'state.json'),
    schedule: {
      checkin_enabled: false, travel_enabled: false, activity_enabled: false,
      keepalive_enabled: false, school_enabled: false, cat_enabled: false,
    },
    admin: { enabled: false },
  }, null, 2));

  const gw = spawn(WB2API, ['-config', gwConfig], { stdio: ['ignore', 'pipe', 'pipe'] });
  children.push(gw);
  gw.stdout.on('data', () => {});
  gw.stderr.on('data', () => {});

  // 等网关开始监听
  const gwDeadline = Date.now() + 10_000;
  while (Date.now() < gwDeadline) {
    await sleep(200);
    try {
      const r = await fetch(`http://127.0.0.1:${gwPort}/healthz`);
      if (r.ok) break;
    } catch { /* 还没起来 */ }
  }
  console.log(`网关已就绪：127.0.0.1:${gwPort}`);

  /* ── 2) 起面板（桌面模式），从 stdin 下发启动参数 ──────────────────── */
  const password = require('crypto').randomBytes(24).toString('hex');
  const username = 'desktop-preview';
  const instanceId = require('crypto').randomBytes(8).toString('hex');

  const panel = spawn(WBGUI, ['--desktop'], { stdio: ['pipe', 'pipe', 'pipe'] });
  children.push(panel);
  panel.stderr.on('data', () => {});

  const ready = waitForLine(panel, /@@WBGUI-READY@@ (\{.*\})/, 10_000, '面板');
  panel.stdin.write(JSON.stringify({
    protocol: 1,
    instance_id: instanceId,
    gateway_url: `http://127.0.0.1:${gwPort}`,
    auth_dir: authDir,
    config_file: gwConfig,
    backup_dir: path.join(workDir, 'backups'),
    username,
    password,
  }) + '\n');

  const m = await ready;
  const panelPort = JSON.parse(m[1]).port;
  console.log(`面板已就绪：127.0.0.1:${panelPort}`);

  /* ── 3) 登录拿会话 Cookie ─────────────────────────────────────────── */
  const loginRes = await fetch(`http://127.0.0.1:${panelPort}/api/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Origin: `http://127.0.0.1:${panelPort}` },
    body: JSON.stringify({ username, password }),
  });
  if (!loginRes.ok) {
    throw new Error(`登录失败：HTTP ${loginRes.status}`);
  }
  const setCookie = loginRes.headers.getSetCookie
    ? loginRes.headers.getSetCookie().join('; ')
    : (loginRes.headers.get('set-cookie') || '');
  const cookieMatch = setCookie.match(/(wbgui_session=[^;]+)/);
  if (!cookieMatch) throw new Error('登录响应里没有会话 Cookie');
  const [cookieName, cookieValue] = cookieMatch[1].split('=');
  console.log('会话已建立');

  /* ── 4) 逐页截图 ──────────────────────────────────────────────────── */
  fs.mkdirSync(OUT, { recursive: true });

  const browser = await chromium.launch({ executablePath });
  const ctx = await browser.newContext({
    viewport: { width: vw, height: vh },
    deviceScaleFactor: 2,
  });
  await ctx.addCookies([{
    name: cookieName, value: cookieValue, domain: '127.0.0.1', path: '/',
  }]);

  const page = await ctx.newPage();
  page.on('console', (msg) => {
    if (msg.type() === 'error') problems.push(`[console.error] ${msg.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`[pageerror] ${e.message}`));
  page.on('requestfailed', (r) => {
    const u = r.url();
    // 上游会去探测外网（模型列表等），在离线环境必然失败，不算问题
    if (u.includes('127.0.0.1')) problems.push(`[requestfailed] ${u}`);
  });

  const shots = [];
  for (const [route, name, label] of ROUTES) {
    await page.goto(`http://127.0.0.1:${panelPort}${route}`, { waitUntil: 'networkidle' });
    await page.waitForTimeout(450);
    const file = path.join(OUT, `${name}.png`);
    await page.screenshot({ path: file });
    shots.push(`${name}.png`);
    console.log(`  ${label.padEnd(6, '　')} → ${name}.png`);
  }

  // 顺带截一张深色，确认深色下也成立
  await ctx.close();
  const darkCtx = await browser.newContext({
    viewport: { width: vw, height: vh },
    deviceScaleFactor: 2,
    colorScheme: 'dark',
  });
  await darkCtx.addCookies([{
    name: cookieName, value: cookieValue, domain: '127.0.0.1', path: '/',
  }]);
  const darkPage = await darkCtx.newPage();
  await darkPage.goto(`http://127.0.0.1:${panelPort}/`, { waitUntil: 'networkidle' });
  await darkPage.waitForTimeout(450);
  await darkPage.screenshot({ path: path.join(OUT, 'dashboard-dark.png') });
  shots.push('dashboard-dark.png');
  console.log('  深色   → dashboard-dark.png');

  await browser.close();

  /* ── 5) 报告 ──────────────────────────────────────────────────────── */
  const pass = problems.length === 0;
  console.log('');
  console.log(`截图：${shots.length} 张 → ${OUT}`);
  console.log(`控制台错误 / 页面异常 / 本地请求失败：${problems.length}`);
  problems.slice(0, 20).forEach((p) => console.log('  ' + p));
  console.log(pass ? '\n结果：全部通过' : '\n结果：存在问题，见上');

  cleanup();
  process.exit(pass ? 0 : 1);
})().catch((err) => {
  console.error('失败：' + err.message);
  cleanup();
  process.exit(1);
});
