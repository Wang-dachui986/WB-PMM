#!/usr/bin/env node
/* ============================================================================
 * 原型截图与自检
 *
 * 用途：对 prototype/index.html 做全量遍历 —— 生成 docs/screenshots/ 下的截图，
 *       同时检查控制台错误、最小窗口溢出、禁用文案。WB-01 的验证证据由本脚本产出。
 *
 * 依赖：playwright-core（不下载浏览器，复用本机已有的 Chromium 缓存）
 *   npm install playwright-core          # 装在任意工作区，用 NODE_PATH 指向它
 *
 * 用法：
 *   node scripts/capture-prototype.js
 *   OUT=<输出目录> node scripts/capture-prototype.js
 *
 * 退出码：0 全部检查通过；1 存在错误/溢出/禁用文案
 * ==========================================================================*/

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const ROOT = path.join(__dirname, '..');
const PROTOTYPE = path.join(ROOT, 'prototype', 'index.html');
const OUT = process.env.OUT || path.join(ROOT, 'docs', 'screenshots');

/* 视口取开发计划 §11.2 建议的初始主窗口 */
const VIEWPORT = { width: 1180, height: 780 };
/* 最小窗口，用于溢出检查（计划 §11.2） */
const MIN_VIEWPORT = { width: 960, height: 640 };

const NAV = ['overview', 'accounts', 'api', 'chat', 'stats', 'config', 'logs', 'settings'];
const FLOWS = ['normal', 'no-account', 'port-conflict', 'auth-expired', 'config-rollback', 'maintenance'];
const BANNED = ['永久免费', '无限额度', '节省金额', '永久无限', '100% 免费', '不限量'];

/* ── 定位 Chromium：优先用 playwright 注册表，回退到扫描 ms-playwright 缓存 ── */

function resolveChromium() {
  let playwright;
  try {
    playwright = require('playwright-core');
  } catch (e) {
    console.error('找不到 playwright-core。请先安装：npm install playwright-core');
    console.error('并设置 NODE_PATH 指向其 node_modules 目录。');
    process.exit(1);
  }

  // 注册表里的版本可能未下载，逐个探测实际存在的可执行文件
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

/* ── 主流程 ─────────────────────────────────────────────────────────────── */

(async () => {
  const { chromium } = require('playwright-core');
  const executablePath = resolveChromium();

  fs.mkdirSync(OUT, { recursive: true });

  const fileUrl = 'file://' + PROTOTYPE.split('/')
    .map((seg, i) => (i === 0 ? '' : encodeURIComponent(seg))).join('/');

  const browser = await chromium.launch({ executablePath });
  const ctx = await browser.newContext({ viewport: VIEWPORT, deviceScaleFactor: 2 });
  const page = await ctx.newPage();

  const problems = [];
  page.on('console', (m) => {
    if (m.type() === 'error') problems.push('[console.error] ' + m.text());
    if (m.type() === 'warning') problems.push('[console.warn] ' + m.text());
  });
  page.on('pageerror', (e) => problems.push('[pageerror] ' + e.message));
  page.on('requestfailed', (r) => problems.push('[requestfailed] ' + r.url()));

  const shots = [];
  const shot = async (name) => {
    await page.screenshot({ path: path.join(OUT, name + '.png') });
    shots.push(name + '.png');
  };
  const setScenario = (k) => page.evaluate((key) => {
    const sel = document.querySelector('#proto-scenario');
    sel.value = key;
    sel.dispatchEvent(new Event('change', { bubbles: true }));
  }, k);

  await page.goto(fileUrl, { waitUntil: 'load' });
  await page.waitForTimeout(400);

  /* 1) U01 首次使用向导 5 步 */
  for (let step = 0; step < 5; step++) {
    await setScenario('first-run');
    await page.waitForTimeout(80);
    for (let i = 0; i < step; i++) {
      await page.click('[data-action="wizard-next"]').catch(() => {});
      await page.waitForTimeout(50);
    }
    await shot(`u01-wizard-step${step + 1}`);
  }

  /* 2) 浅色主题：8 个功能页 + 菜单栏（菜单栏不在主导航数组里，单独截） */
  await setScenario('normal');
  await page.waitForTimeout(150);
  for (const r of NAV) {
    await page.click(`[data-nav="${r}"]`);
    await page.waitForTimeout(180);
    await shot(`light-${r}`);
  }
  await page.click('[data-nav="menubar"]');
  await page.waitForTimeout(180);
  await shot('light-menubar');

  /* 3) 账号详情抽屉 */
  await page.click('[data-nav="accounts"]');
  await page.waitForTimeout(150);
  const row = await page.$('tbody tr');
  if (row) {
    await row.click().catch(() => {});
    await page.waitForTimeout(250);
    await shot('light-accounts-drawer');
    await page.keyboard.press('Escape');
    await page.waitForTimeout(150);
  }

  /* 4) 添加账号 */
  const addBtn = await page.$('[data-action="add-account"]');
  if (addBtn) {
    await addBtn.click().catch(() => {});
    await page.waitForTimeout(250);
    await shot('light-add-account');
  }

  /* 5) 六条重点流程（各自回到场景入口页） */
  for (const k of FLOWS) {
    await setScenario(k);
    await page.waitForTimeout(200);
    await shot(`flow-${k}`);
  }

  /* 6) 会话过期：依赖会话的管理页必须显式失败 */
  await setScenario('auth-expired');
  await page.waitForTimeout(150);
  for (const r of ['accounts', 'config']) {
    await page.click(`[data-nav="${r}"]`);
    await page.waitForTimeout(180);
    await shot(`flow-auth-expired-${r}`);
  }

  /* 7) 维护中 / 无可用账号 的页面级表现 */
  await setScenario('maintenance');
  await page.waitForTimeout(150);
  await page.click('[data-nav="accounts"]');
  await page.waitForTimeout(180);
  await shot('flow-maintenance-accounts');

  await setScenario('no-account');
  await page.waitForTimeout(150);
  await page.click('[data-nav="chat"]');
  await page.waitForTimeout(180);
  await shot('flow-no-account-chat');

  /* 8) 深色主题 */
  await setScenario('normal');
  await page.evaluate(() => document.querySelector('[data-theme-set="dark"]').click());
  await page.waitForTimeout(200);
  for (const r of ['overview', 'accounts']) {
    await page.click(`[data-nav="${r}"]`);
    await page.waitForTimeout(180);
    await shot(`dark-${r}`);
  }

  /* 9) 最小窗口溢出检查 */
  await page.evaluate(() => document.querySelector('[data-theme-set="light"]').click());
  await page.waitForTimeout(120);
  await page.setViewportSize(MIN_VIEWPORT);

  /* 取证前隐藏原型工具条：它是原型自身的 chrome，会盖住下半屏，
     使最小窗口截图无法用于人工复核。（它是 fixed 覆盖层，不影响溢出判定。） */
  await page.evaluate(() => {
    const bar = document.querySelector('.proto-bar');
    if (bar) bar.style.display = 'none';
  });

  const overflow = [];
  for (const r of NAV) {
    await page.click(`[data-nav="${r}"]`);
    await page.waitForTimeout(180);
    const m = await page.evaluate(() => {
      const doc = document.documentElement;
      const view = document.querySelector('#view');
      return {
        docW: doc.scrollWidth > doc.clientWidth + 1,
        docH: doc.scrollHeight > doc.clientHeight + 1,
        viewW: view ? view.scrollWidth > view.clientWidth + 1 : false,
      };
    });
    if (m.docW || m.docH || m.viewW) {
      overflow.push(`${r}: 文档横向=${m.docW} 文档纵向=${m.docH} 内容横向=${m.viewW}`);
    }
  }
  await shot('min-window-960x640');

  // 恢复工具条，后续步骤（禁用文案扫描）仍需页面处于完整状态
  await page.evaluate(() => {
    const bar = document.querySelector('.proto-bar');
    if (bar) bar.style.display = '';
  });

  /* 10) 禁用文案扫描 */
  const text = await page.evaluate(() => document.body.innerText);
  const bannedHits = BANNED.filter((b) => text.includes(b));

  await browser.close();

  /* ── 报告 ── */
  const pass = problems.length === 0 && overflow.length === 0 && bannedHits.length === 0;
  console.log(`截图：${shots.length} 张 → ${OUT}`);
  console.log(`控制台错误 / 页面异常 / 资源失败：${problems.length}`);
  problems.forEach((p) => console.log('  ' + p));
  console.log(`最小窗口 ${MIN_VIEWPORT.width}×${MIN_VIEWPORT.height} 溢出页面数：${overflow.length}`);
  overflow.forEach((o) => console.log('  ' + o));
  console.log(`禁用文案命中：${bannedHits.length}${bannedHits.length ? ' → ' + bannedHits.join('、') : ''}`);
  console.log(pass ? '\n结果：全部通过' : '\n结果：存在问题，见上');

  process.exit(pass ? 0 : 1);
})().catch((e) => { console.error('执行失败：', e); process.exit(1); });
