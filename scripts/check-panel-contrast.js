#!/usr/bin/env node
/* ============================================================================
 * 面板设计令牌对比度校验
 *
 * 依据：正文文字对比度不低于 4.5:1（WCAG 2.1 AA，普通字号）。
 * 直接解析 panel/web/src/styles.css 的 CSS 变量，不硬编码颜色 ——
 * 否则令牌改了而校验没跟上，检查会静默失效。
 *
 * 用法：node scripts/check-panel-contrast.js
 * 退出码：0 全部达标；1 存在不达标项
 * ==========================================================================*/

'use strict';

const fs = require('fs');
const path = require('path');

const CSS = path.join(__dirname, '..', 'panel', 'web', 'src', 'styles.css');

/* ── WCAG 2.1 相对亮度与对比度 ─────────────────────────────────────────── */

const toRgb = (h) => {
  h = h.replace('#', '').trim();
  if (h.length === 3) h = h.split('').map((c) => c + c).join('');
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255);
};

const luminance = (h) => {
  const [r, g, b] = toRgb(h).map((c) =>
    c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

const contrast = (a, b) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

/* ── 从 CSS 里抽取某个选择器块内的自定义属性 ───────────────────────────── */

/** 从 `from` 位置起，找到 selector 后的第一个 {...} 块，返回其内容。 */
function blockAfter(css, selector, from = 0) {
  const start = css.indexOf(selector, from);
  if (start < 0) throw new Error(`找不到选择器：${selector}`);
  const open = css.indexOf('{', start);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === '{') depth++;
    else if (css[i] === '}') {
      depth--;
      if (depth === 0) return css.slice(open + 1, i);
    }
  }
  throw new Error(`选择器 ${selector} 的块没有闭合`);
}

function parseTokens(body) {
  const tokens = {};
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    tokens[m[1]] = m[2].trim();
  }
  return tokens;
}

/**
 * 把令牌值解析成不透明颜色。
 *
 * 深色主题的浅底色（`--ok-soft` 等）是 `rgba(...)` 半透明叠加。直接跳过它们会让
 * 校验回退到浅色主题的值，得出「深色的成功文字在浅绿底上只有 1.79:1」这种
 * 假失败 —— 底色根本不是那个。这里按 `--bg` 做一次合成，得到真实的等效色。
 */
function resolveColor(value, baseBg) {
  if (!value) return null;
  if (/^#[0-9a-fA-F]{3,8}$/.test(value)) return value;

  const m = value.match(
    /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\s*\)$/i);
  if (!m) return null;

  const alpha = m[4] === undefined ? 1 : parseFloat(m[4]);
  const [br, bg, bb] = toRgb(baseBg).map((c) => c * 255);
  const ch = [parseFloat(m[1]), parseFloat(m[2]), parseFloat(m[3])].map((v, i) =>
    Math.round(v * alpha + [br, bg, bb][i] * (1 - alpha)));

  return '#' + ch.map((v) => Math.max(0, Math.min(255, v)).toString(16).padStart(2, '0')).join('');
}

/** 解析整套令牌：以 --bg 为底，把 rgba 合成为不透明色。 */
function resolveTheme(raw) {
  const bg = resolveColor(raw['--bg'], '#ffffff') || '#ffffff';
  const out = {};
  for (const [k, v] of Object.entries(raw)) {
    const c = resolveColor(v, bg);
    if (c) out[k] = c;
  }
  return out;
}

/* ── 校验用例：[说明, 前景令牌, 背景令牌] ───────────────────────────────── */

const CASES = [
  ['正文 / 内容区', '--text', '--bg'],
  ['正文 / 卡片', '--text', '--bg-elev'],
  ['正文 / 侧栏', '--text', '--bg-sidebar'],
  ['正文 / 平灰按钮', '--text', '--bg-elev2'],
  ['正文 / 下沉底', '--text', '--bg-sunken'],
  ['次要文字 / 内容区', '--text-dim', '--bg'],
  ['次要文字 / 下沉底', '--text-dim', '--bg-sunken'],
  ['弱化文字 / 内容区', '--text-faint', '--bg'],
  ['弱化文字 / 侧栏', '--text-faint', '--bg-sidebar'],
  ['弱化文字 / 下沉底', '--text-faint', '--bg-sunken'],
  ['主色文字 / 内容区', '--accent', '--bg'],
  ['主色文字 / 选中浅底', '--accent', '--accent-soft'],
  ['主按钮白字 / 主色实底', '--on-accent', '--accent-fill'],
  ['成功文字 / 内容区', '--ok', '--bg'],
  ['成功文字 / 成功浅底', '--ok', '--ok-soft'],
  ['警告文字 / 内容区', '--warn', '--bg'],
  ['警告文字 / 警告浅底', '--warn', '--warn-soft'],
  ['危险文字 / 内容区', '--danger', '--bg'],
  ['危险文字 / 危险浅底', '--danger', '--danger-soft'],
];

const NEED = 4.5;

/* ── 解析：浅色是基准，深色在 @media (prefers-color-scheme: dark) 里 ──── */

const css = fs.readFileSync(CSS, 'utf8');
const light = resolveTheme(parseTokens(blockAfter(css, ':root')));

const darkMediaIdx = css.indexOf('@media (prefers-color-scheme: dark)');
if (darkMediaIdx < 0) {
  console.error('找不到 @media (prefers-color-scheme: dark) 块。');
  process.exit(1);
}
const darkRaw = { ...parseTokens(blockAfter(css, ':root')), ...parseTokens(blockAfter(css, ':root', darkMediaIdx)) };
const dark = resolveTheme(darkRaw);

let failures = 0;

for (const [themeName, T] of [['浅色（默认）', light], ['深色（跟随系统）', dark]]) {
  console.log(`\n${themeName}`);
  console.log('─'.repeat(66));
  for (const [label, fgKey, bgKey] of CASES) {
    const fg = T[fgKey];
    const bg = T[bgKey];
    if (!fg || !bg) {
      console.log(`SKIP   缺少令牌 ${fgKey} 或 ${bgKey}（${label}）`);
      continue;
    }
    const r = contrast(fg, bg);
    const pass = r >= NEED;
    if (!pass) failures++;
    console.log(`${pass ? 'PASS' : 'FAIL'}  ${r.toFixed(2).padStart(6)}:1  ${label}`);
  }
}

console.log('\n' + '─'.repeat(66));
if (failures === 0) {
  console.log(`结果：全部达标（${CASES.length} 项 × 2 主题）`);
  process.exit(0);
}
console.log(`结果：${failures} 项未达 ${NEED}:1，需调整 panel/web/src/styles.css 的令牌`);
process.exit(1);
