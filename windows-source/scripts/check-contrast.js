#!/usr/bin/env node
/* ============================================================================
 * 设计令牌对比度校验
 *
 * 依据开发计划 §11.2：「普通文字对比度目标不低于 4.5:1」。
 * 直接解析 prototype/app.css 中的 CSS 变量，不硬编码颜色，避免令牌改了而校验没跟上。
 *
 * 用法：node scripts/check-contrast.js
 * 退出码：0 全部达标；1 存在不达标项
 * ==========================================================================*/

'use strict';

const fs = require('fs');
const path = require('path');

const CSS = path.join(__dirname, '..', 'prototype', 'app.css');

/* ── WCAG 2.1 相对亮度与对比度 ─────────────────────────────────────────── */

const toRgb = (h) => {
  h = h.replace('#', '').trim();
  if (h.length === 3) h = h.split('').map((c) => c + c).join('');
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255);
};

const luminance = (h) => {
  const [r, g, b] = toRgb(h).map((c) => (c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

const contrast = (a, b) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

/* ── 从 app.css 抽取某个选择器块里的自定义属性 ─────────────────────────── */

function parseTokens(css, selector) {
  const start = css.indexOf(selector);
  if (start < 0) throw new Error(`在 app.css 中找不到选择器：${selector}`);
  const open = css.indexOf('{', start);
  const close = css.indexOf('}', open);
  const body = css.slice(open + 1, close);

  const tokens = {};
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    tokens[m[1]] = m[2].trim();
  }
  return tokens;
}

/* ── 校验用例：[说明, 前景令牌, 背景令牌, 最低要求] ─────────────────────── */

const CASES = [
  ['正文 / 页面背景',        '--text',           '--bg',           4.5],
  ['正文 / 卡片',            '--text',           '--bg-elevated',  4.5],
  ['次要文字 / 卡片',        '--text-secondary', '--bg-elevated',  4.5],
  ['弱化文字 / 卡片',        '--text-muted',     '--bg-elevated',  4.5],
  ['弱化文字 / 页面背景',    '--text-muted',     '--bg',           4.5],
  ['主色文字 / 卡片',        '--accent',         '--bg-elevated',  4.5],
  ['主色文字 / 选中项',      '--accent',         '--bg-selected',  4.5],
  ['主色文字 / 主色底',      '--accent',         '--accent-soft',  4.5],
  ['按钮白字 / 主色填充',    '--on-accent',      '--accent-fill',  4.5],
  ['向导步骤编号 / 主色填充', '--on-accent',     '--accent-fill',  4.5],
  ['成功徽标文字 / 底色',    '--ok',             '--ok-soft',      4.5],
  ['警告徽标文字 / 底色',    '--warn',           '--warn-soft',    4.5],
  ['危险徽标文字 / 底色',    '--danger',         '--danger-soft',  4.5],
  ['中性徽标文字 / 底色',    '--idle',           '--idle-soft',    4.5],
  ['成功文字 / 卡片',        '--ok',             '--bg-elevated',  4.5],
  ['警告文字 / 卡片',        '--warn',           '--bg-elevated',  4.5],
  ['危险文字 / 卡片',        '--danger',         '--bg-elevated',  4.5],

  /* 侧栏与顶栏使用独立的 chrome 底色，必须单独校验 ——
     否则「文字在卡片上达标」不代表「文字在侧栏上也达标」。 */
  ['正文 / 侧栏背景',        '--text',           '--bg-chrome',    4.5],
  ['导航文字 / 侧栏背景',    '--text-secondary', '--bg-chrome',    4.5],
  ['分组标签 / 侧栏背景',    '--text-muted',     '--bg-chrome',    4.5],
  ['导航选中文字 / 选中底色', '--text',          '--nav-active',   4.5],

  /* 下沉底色用于表头、代码块、日志视图 —— 也是独立表面。 */
  ['正文 / 下沉底色',        '--text',           '--bg-sunken',    4.5],
  ['弱化文字 / 下沉底色',    '--text-muted',     '--bg-sunken',    4.5],
];

/* 令牌分布在三个块里：
   - `:root {`            基础令牌，两套主题共用（如 --on-accent）
   - `[data-theme="light"]` 浅色主题
   - `[data-theme="dark"]`  深色主题，未覆盖的令牌回退到基础/浅色 */
const css = fs.readFileSync(CSS, 'utf8');
const base = parseTokens(css, ':root {');
const light = { ...base, ...parseTokens(css, '[data-theme="light"]') };
const dark = { ...base, ...light, ...parseTokens(css, '[data-theme="dark"]') };

let failures = 0;

for (const [themeName, T] of [['浅色主题', light], ['深色主题', dark]]) {
  console.log(`\n${themeName}`);
  console.log('─'.repeat(72));
  for (const [label, fgKey, bgKey, need] of CASES) {
    const fg = T[fgKey], bg = T[bgKey];
    if (!fg || !bg) { console.log(`SKIP  缺少令牌 ${fgKey} 或 ${bgKey}（${label}）`); continue; }
    const r = contrast(fg, bg);
    const pass = r >= need;
    if (!pass) failures++;
    console.log(
      `${pass ? 'PASS' : 'FAIL'}  ${r.toFixed(2).padStart(6)}:1  需 ${need.toFixed(1)}  ${label}`
    );
  }
}

console.log('\n' + '─'.repeat(72));
if (failures === 0) {
  console.log('结果：全部达标（0 项不通过）');
  process.exit(0);
} else {
  console.log(`结果：${failures} 项未达 4.5:1，需要调整 prototype/app.css 中的令牌`);
  process.exit(1);
}
