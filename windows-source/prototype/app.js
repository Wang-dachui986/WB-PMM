/* ============================================================================
 * WorkBuddy2API Desktop · 视觉原型交互
 * WB-01 交付物。设计规范见 docs/design.md。
 *
 * 重要：本文件内所有数据均为**模拟数据**，仅用于演示页面与状态，
 * 不代表任何真实账号、真实模型、真实用量或真实金额。
 * 界面上以「模拟数据」标记常驻提示（见 .mock-flag）。
 * ==========================================================================*/

(function () {
  'use strict';

  /* ── 工具 ───────────────────────────────────────────────────────────── */

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  const esc = (v) => String(v == null ? '' : v).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));

  const fmtNum = (n) => Number(n).toLocaleString('zh-CN');
  const fmtBytes = (n) => {
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
    if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
    return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
  };
  const fmtDur = (s) => {
    const d = Math.floor(s / 86400), hh = Math.floor(s % 86400 / 3600),
          mm = Math.floor(s % 3600 / 60), ss = Math.floor(s % 60);
    if (d) return `${d} 天 ${hh} 小时`;
    if (hh) return `${hh} 小时 ${mm} 分`;
    if (mm) return `${mm} 分 ${ss} 秒`;
    return `${ss} 秒`;
  };
  const plural = (n, unit) => `${fmtNum(n)} ${unit}`;

  /* ── 模拟数据（全部为虚构值）───────────────────────────────────────── */

  const BASE_URL = 'http://127.0.0.1:7863/v1';
  const API_KEY_MASK = 'wbd_sk_' + '•'.repeat(28);

  const ACCOUNTS = [
    {
      uid: 'u_8f3a2c91d4e7b056', name: '主账号（本人）', realm: 'cn',
      expires: '2026-10-18 09:12', useState: 'ready',
      credits: 1840, creditsAt: '2026-09-21 11:20', creditUnknown: false,
      lastError: '', loaded: true, cooldown: '', inFlight: false,
    },
    {
      uid: 'u_2b7e4d10a9c5f381', name: '备用 1', realm: 'cn',
      expires: '2026-10-02 22:40', useState: 'paused',
      credits: 620, creditsAt: '2026-09-21 10:05', creditUnknown: false,
      lastError: '', loaded: true, cooldown: '', inFlight: false,
    },
    {
      uid: 'u_c14a9e63b8d2075f', name: '备用 2', realm: 'cn',
      expires: '2026-09-25 08:31', useState: 'cooling',
      credits: 95, creditsAt: '2026-09-21 09:48', creditUnknown: false,
      lastError: '上游返回限流，软冷却中（预计 11:34 恢复）', loaded: true,
      cooldown: '软限流，约 14 分钟后恢复', inFlight: false,
    },
    {
      uid: 'u_5e08d7f2a1b6c943', name: '国际版（备用）', realm: 'global',
      expires: '2026-11-01 14:07', useState: 'ready',
      credits: null, creditsAt: '', creditUnknown: true,
      lastError: '', loaded: true, cooldown: '', inFlight: false,
    },
    {
      uid: 'u_9a3f6c05e2d84b17', name: '测试号', realm: 'cn',
      expires: '2026-09-22 03:15', useState: 'expired',
      credits: null, creditsAt: '', creditUnknown: true,
      lastError: '凭证已过期，需重新授权', loaded: false, cooldown: '', inFlight: false,
    },
  ];

  const MODELS = [
    { id: 'wb-chat-standard',  name: '标准对话', ctx: '128K', realm: 'cn',     at: '2026-09-21 11:15' },
    { id: 'wb-chat-pro',       name: '增强对话', ctx: '128K', realm: 'cn',     at: '2026-09-21 11:15' },
    { id: 'wb-reasoning',      name: '深度推理', ctx: '未提供', realm: 'cn',    at: '2026-09-21 11:15' },
    { id: 'global:wb-chat-standard', name: '标准对话（国际）', ctx: '128K', realm: 'global', at: '2026-09-21 11:15' },
    { id: 'global:wb-chat-pro',      name: '增强对话（国际）', ctx: '128K', realm: 'global', at: '2026-09-21 11:15' },
  ];

  const STATS = {
    since: '2026-09-21 11:20:14',
    uptime: 3 * 3600 + 42 * 60 + 18,
    total: { requests: 128, success: 121, failed: 7, prompt: 48620, completion: 31940, usageObserved: 118, avgLatency: 2840, avgTtfb: 940 },
    models: [
      { id: 'wb-chat-standard', requests: 74, success: 72, failed: 2, prompt: 24180, completion: 15230, observed: 74, avgLatency: 1920, credit: 4.42 },
      { id: 'wb-chat-pro',      requests: 38, success: 36, failed: 2, prompt: 18640, completion: 13420, observed: 34, avgLatency: 3980, credit: 6.87 },
      { id: 'global:wb-chat-standard', requests: 12, success: 12, failed: 0, prompt: 4380, completion: 2410, observed: 10, avgLatency: 4210, credit: 0.71 },
      { id: 'wb-reasoning',     requests: 4,  success: 1,  failed: 3, prompt: 1420, completion: 880, observed: 0, avgLatency: 9120, credit: 0 },
    ],
  };

  const LOG_LINES = [
    ['11:23:58', 'INFO',  '面板已就绪，监听 127.0.0.1:53412'],
    ['11:23:58', 'INFO',  '网关健康检查：OK（账号 3/5 可用）'],
    ['11:24:02', 'INFO',  '[watch] auths 目录监听已启用（每 5s 检查一次，新增账号自动加载）'],
    ['11:24:11', 'INFO',  'POST /v1/chat/completions 200 stream=true model=wb-chat-standard 1.9s'],
    ['11:24:19', 'INFO',  'POST /v1/chat/completions 200 stream=true model=wb-chat-pro 4.2s'],
    ['11:24:33', 'WARN',  '账号 u_c14a9e63b8d2075f 上游限流，进入软冷却（基数 600s）'],
    ['11:24:33', 'INFO',  'POST /v1/chat/completions 503 no_usable_account（其余账号在途已满）'],
    ['11:24:40', 'INFO',  'POST /v1/chat/completions 200 stream=true model=wb-chat-standard 2.1s'],
    ['11:24:52', 'ERROR', '账号 u_9a3f6c05e2d84b17 凭证已过期，需重新授权'],
    ['11:25:01', 'INFO',  '[watch] auths 目录变化：账号数保持 5（已热加载凭证更新）'],
    ['11:25:14', 'INFO',  'GET /v1/models 200（5 个模型）'],
    ['11:25:20', 'WARN',  '日志文件已达轮转阈值，准备滚动（保留 3 份）'],
  ];

  /* ── 演示场景：驱动状态机 + 数据 + 问题卡 ─────────────────────────── */

  const SCENARIOS = {
    'first-run': {
      label: '首次使用（向导）',
      gateway: 'stopped',
      accounts: [],
      wizard: true,
      problem: null,
    },
    normal: {
      label: '正常运行',
      gateway: 'running_ready',
      accounts: ACCOUNTS,
      problem: null,
    },
    'no-account': {
      label: '无可用账号',
      gateway: 'running_unavailable',
      accounts: ACCOUNTS.map((a) => (
        a.useState === 'ready' ? { ...a, useState: 'cooling', cooldown: '上游限流，冷却中' } : a
      )),
      problem: {
        tone: 'warn', icon: '⚠',
        title: '当前没有可调用的账号',
        text: '进程运行正常，但账号池里没有可用账号：1 个已暂停、3 个冷却中、1 个凭证过期。' +
              'API 此刻返回 503（NO_USABLE_ACCOUNT），这不是服务崩溃，也不会自动重启。',
        actions: [{ label: '去账号管理', route: 'accounts' }, { label: '重新授权过期账号', route: 'accounts' }],
      },
    },
    'port-conflict': {
      label: '端口冲突',
      gateway: 'failed',
      accounts: ACCOUNTS,
      problem: {
        tone: 'danger', icon: '✕',
        title: 'API 端口 7863 已被其他程序占用',
        text: '启动失败。占用端口的进程不属于本应用，因此不会结束它，也不会静默改用其他端口。' +
              '请在网关设置里换一个端口，或自行处理占用该端口的程序。',
        code: 'PORT_IN_USE',
        actions: [{ label: '修改端口', route: 'config' }, { label: '查看日志', route: 'logs' }],
      },
    },
    'auth-expired': {
      label: '面板会话过期',
      gateway: 'running_ready',
      accounts: ACCOUNTS,
      sessionExpired: true,
      problem: {
        tone: 'warn', icon: '⚠',
        title: '管理连接已过期',
        text: '与内置面板的会话已失效，服务本身仍在正常运行。点击下方按钮由应用重新建立内部连接，' +
              '不会影响正在进行的 API 请求。',
        code: 'AUTH_EXPIRED',
        actions: [{ label: '恢复管理连接', action: 'reconnect' }],
      },
    },
    'config-rollback': {
      label: '配置回滚',
      gateway: 'running_ready',
      accounts: ACCOUNTS,
      problem: {
        tone: 'danger', icon: '✕',
        title: '配置应用失败，已回滚到修改前的配置',
        text: '新配置无法生效（监听端口 8100 校验失败）。应用已恢复上一份有效配置，' +
              '当前服务运行在回滚后的配置上。你填写的草稿仍保留在设置页，可以修改后重试。',
        code: 'CONFIG_INVALID',
        actions: [{ label: '返回设置查看草稿', route: 'config' }, { label: '查看诊断', route: 'logs' }],
      },
    },
    maintenance: {
      label: '维护中（账号操作）',
      gateway: 'maintenance',
      accounts: ACCOUNTS,
      problem: {
        tone: 'accent', icon: '⟳',
        title: '正在维护账号，API 暂时不可用',
        text: '为避免面板与网关同时写入凭证导致令牌互相覆盖，维护期间会暂停 API 服务。' +
              '维护完成后会按操作前的状态自动恢复（操作前为运行中 → 恢复运行）。',
        actions: [{ label: '取消本次维护', action: 'cancelMaintenance' }],
      },
    },
  };

  const GW_STATES = {
    stopped:             { label: '已停止',           tone: 'idle',   icon: '■', sub: 'API 服务未运行' },
    starting:            { label: '正在启动',         tone: 'accent', icon: '⟳', sub: '正在拉起网关进程…' },
    running_ready:       { label: '运行中 · 可调用',   tone: 'ok',     icon: '●', sub: '服务可用，可以发送请求' },
    running_unavailable: { label: '运行中 · 暂不可调用', tone: 'warn',  icon: '▲', sub: '进程正常，但当前没有可用账号' },
    maintenance:         { label: '维护中',           tone: 'accent', icon: '⟳', sub: '账号维护进行中，API 已暂停' },
    stopping:            { label: '正在停止',         tone: 'idle',   icon: '⟳', sub: '正在等待在途请求结束…' },
    failed:              { label: '启动失败',         tone: 'danger', icon: '✕', sub: '进程未运行，需要处理' },
  };

  /* ── 应用状态 ───────────────────────────────────────────────────────── */

  const state = {
    route: 'overview',
    scenario: 'normal',
    theme: 'system',
    wizardStep: 0,
    drawer: null,          // 账号详情抽屉
    modal: null,           // 弹窗
    openMenu: null,        // 行内菜单
    selected: new Set(),   // 批量选择
    chat: [],              // 对话消息
    chatStreaming: false,
    chatError: null,
    addFlow: 'idle',       // 添加账号流程状态机
    addRegion: 'cn',
    configDirty: false,
    configResult: null,
    logLevel: 'ALL',
    logPaused: false,
    notices: [],
  };

  const sc = () => SCENARIOS[state.scenario];
  const gwState = () => GW_STATES[sc().gateway];

  /* 场景的原始定义快照。部分交互（取消维护、恢复管理连接）会就地修改场景对象，
     切换场景时按快照还原，保证原型可以被反复审阅而不会越用越脏。 */
  const SCENARIOS_PRISTINE = JSON.parse(JSON.stringify(SCENARIOS));
  const resetScenarios = () => {
    Object.keys(SCENARIOS).forEach((k) => {
      SCENARIOS[k] = JSON.parse(JSON.stringify(SCENARIOS_PRISTINE[k]));
    });
  };

  /* ── 通用片段 ───────────────────────────────────────────────────────── */

  const badge = (tone, text, dot = true) =>
    `<span class="badge badge--${tone}">${dot ? '<span class="badge__dot"></span>' : ''}${esc(text)}</span>`;

  const mockFlag = () => '<span class="mock-flag" title="本原型中的所有数据均为虚构，用于演示页面与状态">◈ 模拟数据</span>';

  /* 侧栏图标：统一一套 24×24 线性图标（stroke 1.6、currentColor、无填充）。
     早期版本用 ◉ ⌘ ⓘ ☰ 这类字符符号，来自不同字族、字重与视觉尺寸互不匹配，
     是侧栏「看起来像文档目录」的主因之一。 */
  const ICON = {
    overview: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/>'
      + '<rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
    accounts: '<path d="M19 20.5v-1.6a4.4 4.4 0 0 0-4.4-4.4H9.4A4.4 4.4 0 0 0 5 18.9v1.6"/>'
      + '<circle cx="12" cy="7.5" r="3.8"/>',
    api: '<path d="m5.5 8.5-4 3.5 4 3.5"/><path d="m18.5 8.5 4 3.5-4 3.5"/><path d="M13.6 4.5 10.4 19.5"/>',
    chat: '<path d="M20.5 14.8a2.6 2.6 0 0 1-2.6 2.6H8.2L4 20.6V6.1a2.6 2.6 0 0 1 2.6-2.6h11.3'
      + 'a2.6 2.6 0 0 1 2.6 2.6z"/>',
    stats: '<path d="M4 20.5V11"/><path d="M10 20.5V4"/><path d="M16 20.5v-6.6"/><path d="M2 20.5h20"/>',
    config: '<path d="M4 7.5h8.4"/><path d="M17.6 7.5H20"/><path d="M4 16.5h2.4"/><path d="M11.6 16.5H20"/>'
      + '<circle cx="15" cy="7.5" r="2.3"/><circle cx="9" cy="16.5" r="2.3"/>',
    logs: '<path d="M14 3H7.5A2.5 2.5 0 0 0 5 5.5v13A2.5 2.5 0 0 0 7.5 21h9a2.5 2.5 0 0 0 2.5-2.5V8z"/>'
      + '<path d="M13.8 3v5.2H19"/><path d="M9 13h6"/><path d="M9 16.8h4"/>',
    settings: '<circle cx="12" cy="12" r="3"/>'
      + '<path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3h.1a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8v.1a1.7 1.7 0 0 0 1.5 1h.2a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
    menubar: '<rect x="2.5" y="4" width="19" height="16" rx="2.5"/><path d="M2.5 9h19"/>',
  };

  const icon = (name) => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6"
    stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICON[name] || ''}</svg>`;

  /* 页面标题区：统一为「分类标签 / 大标题 / 说明 / 右侧操作」四段式。
     - 分类标签（eyebrow）承载页面归属，让 12 个页面不必靠同一句说明互相区分
     - 标题是页面名的唯一出处 —— 顶栏不再重复，消除「同一个词出现两次」
     - 右侧操作区让标题区具备功能，而不只是一块装饰性的说明文字
     标题按纯文本转义；说明允许含内联标记（部分页面需要在其中插入变量）。 */
  const pageHead = ({ eyebrow, title, desc = '', actions = '' }) => `
    <header class="page-head">
      <div class="page-head__text">
        ${eyebrow ? `<div class="page-head__eyebrow">${esc(eyebrow)}</div>` : ''}
        <h1 class="page-head__title">${esc(title)}</h1>
        ${desc ? `<p class="page-head__desc">${desc}</p>` : ''}
      </div>
      ${actions ? `<div class="page-head__actions">${actions}</div>` : ''}
    </header>`;

  const useStateBadge = (a) => {
    switch (a.useState) {
      case 'ready':   return badge('ok', '可用');
      case 'paused':  return badge('idle', '已暂停（手动）');
      case 'cooling': return badge('warn', '冷却中');
      case 'expired': return badge('danger', '凭证过期');
      case 'full':    return badge('warn', '并发已满');
      default:        return badge('idle', '未知');
    }
  };

  const creditCell = (a) => {
    if (a.creditUnknown) return '<span class="text-muted" style="color:var(--text-muted)">余额未知</span>';
    return `<span class="table__num">${fmtNum(a.credits)}</span>`;
  };

  /* 依赖面板会话的页面在会话过期时必须显式失败，
     不能继续渲染看起来正常的数据。API 服务本身不受影响。 */
  const sessionGuard = () => {
    if (!sc().sessionExpired) return null;
    return `
      <div class="page">
        ${pageHead({ eyebrow: '管理连接', title: TITLES[state.route] || '' })}
        <div class="state state--error">
          <div class="state__icon" aria-hidden="true">⚠</div>
          <div class="state__title">管理连接已过期，无法读取这一页</div>
          <div class="state__text">
            与内置面板的会话已失效，因此账号、配置与日志都无法读取。
            这不是数据丢失：API 服务仍在正常运行，正在进行的请求不受影响。
          </div>
          <button class="btn btn--primary" data-action="reconnect">恢复管理连接</button>
        </div>
      </div>`;
  };

  /* ── 页面：U02 总览 ─────────────────────────────────────────────────── */

  function pageOverview() {
    const s = sc();
    const g = gwState();
    const accts = s.accounts;
    const ready = accts.filter((a) => a.useState === 'ready').length;
    const cooling = accts.filter((a) => a.useState === 'cooling').length;
    const abnormal = accts.filter((a) => a.useState === 'expired' || a.useState === 'full').length;
    const paused = accts.filter((a) => a.useState === 'paused').length;
    const running = ['running_ready', 'running_unavailable', 'maintenance'].includes(s.gateway);
    const stats = s.gateway === 'running_ready' ? STATS : null;

    const problem = s.problem ? `
      <div class="notice notice--${s.problem.tone}" role="status">
        <div class="notice__icon" aria-hidden="true">${s.problem.icon}</div>
        <div class="notice__body">
          <div class="notice__title">${esc(s.problem.title)}
            ${s.problem.code ? `<span class="code--inline" style="margin-left:8px">${esc(s.problem.code)}</span>` : ''}
          </div>
          <div class="notice__text">${esc(s.problem.text)}</div>
          <div class="notice__actions">
            ${(s.problem.actions || []).map((a, i) => a.route
              ? `<button class="btn btn--sm ${i === 0 ? 'btn--primary' : ''}" data-nav="${esc(a.route)}">${esc(a.label)}</button>`
              : `<button class="btn btn--sm btn--primary" data-action="${esc(a.action)}">${esc(a.label)}</button>`
            ).join('')}
          </div>
        </div>
      </div>` : '';

    return `
      <div class="page page--wide">
        ${pageHead({
          eyebrow: '服务状态',
          title: '总览',
          desc: '一眼看清服务能不能用，以及下一步该点什么。',
        })}

        <div class="statusbar">
          <div class="statusbar__main">
            <div class="spinner" aria-hidden="true" style="${s.gateway === 'starting' || s.gateway === 'stopping' || s.gateway === 'maintenance' ? '' : 'display:none'}"></div>
            <span class="badge badge--${g.tone}" style="${s.gateway === 'starting' || s.gateway === 'stopping' || s.gateway === 'maintenance' ? 'display:none' : ''}"><span class="badge__dot"></span>${esc(g.label)}</span>
            <div class="statusbar__text">
              <div class="statusbar__title">API 服务</div>
              <div class="statusbar__sub">${esc(g.sub)} · 更新于 ${s.gateway === 'starting' ? '刚刚' : '2 秒前'}</div>
            </div>
          </div>
          <div class="statusbar__sep" aria-hidden="true"></div>
          <div class="metric">
            <span class="metric__label">监听地址</span>
            <span class="metric__value" style="font-family:var(--font-mono);font-size:var(--fs-base)">127.0.0.1:7863</span>
          </div>
          <div class="metric">
            <span class="metric__label">运行时长</span>
            <span class="metric__value">${running ? fmtDur(STATS.uptime) : '—'}</span>
          </div>
          <div class="statusbar__spacer"></div>
          <div class="btn-row">
            ${s.gateway === 'stopped' || s.gateway === 'failed'
              ? '<button class="btn btn--primary" data-action="gw-start">启动 API 服务</button>'
              : s.gateway === 'starting' || s.gateway === 'stopping'
                ? '<button class="btn" disabled><span class="spinner" aria-hidden="true"></span>处理中…</button>'
                : '<button class="btn" data-action="gw-restart">重启</button><button class="btn" data-action="gw-stop">停止</button>'}
          </div>
        </div>

        <div style="margin-top:var(--sp-4)">${problem}</div>

        <div class="metric-strip" style="margin-top:var(--sp-5)">
          <button class="metric-strip__item" data-nav="accounts">
            <span class="metric-strip__label">账号</span>
            <span class="metric-strip__value">${accts.length}<span class="metric-strip__unit">个</span></span>
            <span class="metric-strip__hint">可用 ${ready} · 冷却 ${cooling} · 异常 ${abnormal}${paused ? ` · 暂停 ${paused}` : ''}</span>
          </button>
          <button class="metric-strip__item" data-nav="api">
            <span class="metric-strip__label">接入信息</span>
            <span class="metric-strip__value" style="font-size:var(--fs-xl);font-family:var(--font-mono)">/v1</span>
            <span class="metric-strip__hint">复制地址与密钥 →</span>
          </button>
          <button class="metric-strip__item" data-nav="stats">
            <span class="metric-strip__label">本次运行请求</span>
            <span class="metric-strip__value">${stats ? fmtNum(stats.total.requests) : '—'}</span>
            <span class="metric-strip__hint">${stats ? '自 ' + esc(stats.since) : '服务未运行，暂无统计'}</span>
          </button>
          <button class="metric-strip__item" data-nav="stats">
            <span class="metric-strip__label">已观测 Token</span>
            <span class="metric-strip__value">${stats ? fmtNum(stats.total.prompt + stats.total.completion) : '—'}</span>
            <span class="metric-strip__hint">${stats ? `${stats.total.usageObserved}/${stats.total.requests} 次请求返回用量` : '暂无数据'}</span>
          </button>
        </div>

        <div class="grid grid--2" style="margin-top:var(--sp-5)">
          <div class="card">
            <div class="card__head"><h3>接入信息</h3><div class="spacer"></div></div>
            <dl class="kv">
              <dt>Base URL</dt>
              <dd><code>${esc(BASE_URL)}</code></dd>
              <dt>API 密钥</dt>
              <dd><code>${esc(API_KEY_MASK)}</code></dd>
            </dl>
            <div class="btn-row" style="margin-top:var(--sp-3)">
              <button class="btn btn--sm" data-action="copy-url">复制 API 地址</button>
              <button class="btn btn--sm" data-action="copy-key">复制密钥</button>
              <button class="btn btn--sm btn--ghost" data-nav="api">查看模型列表</button>
            </div>
          </div>

          <div class="card">
            <div class="card__head"><h3>最近事件</h3><div class="spacer"></div>
              <button class="btn btn--sm btn--ghost" data-nav="logs">全部日志</button></div>
            <div style="font-size:var(--fs-sm);color:var(--text-secondary);line-height:1.9">
              ${accts.filter((a) => a.lastError).slice(0, 3).map((a) => `
                <div style="display:flex;gap:var(--sp-2)">
                  <span style="color:var(--warn);flex:0 0 14px">▲</span>
                  <span>${esc(a.lastError)}</span>
                </div>`).join('') || '<div style="color:var(--text-muted)">本次运行暂无异常事件。</div>'}
            </div>
          </div>
        </div>
      </div>`;
  }

  /* ── 页面：U03 账号管理 ─────────────────────────────────────────────── */

  function pageAccounts() {
    const guard = sessionGuard(); if (guard) return guard;
    const s = sc();
    const accts = s.accounts;
    const sel = state.selected;

    const rows = accts.map((a) => `
      <tr>
        <td class="table__check">
          <input type="checkbox" ${sel.has(a.uid) ? 'checked' : ''} data-select="${esc(a.uid)}"
                 aria-label="选择账号 ${esc(a.name)}">
        </td>
        <td>
          <div style="font-weight:500">${esc(a.name)}</div>
          <div style="font-size:var(--fs-xs);color:var(--text-muted);font-family:var(--font-mono)">${esc(a.uid)}</div>
        </td>
        <td>${a.realm === 'cn' ? badge('accent', 'CN', false) : badge('accent', 'Global', false)}</td>
        <td>${esc(a.expires)}</td>
        <td>${useStateBadge(a)}</td>
        <td>${creditCell(a)}</td>
        <td style="font-size:var(--fs-xs);color:var(--text-muted)">${esc(a.creditsAt || '—')}</td>
        <td style="max-width:220px">
          ${a.lastError
            ? `<span style="font-size:var(--fs-xs);color:var(--warn)">${esc(a.lastError)}</span>`
            : '<span style="color:var(--text-muted)">—</span>'}
        </td>
        <td class="table__actions">
          <button class="btn btn--sm" data-detail="${esc(a.uid)}">详情</button>
          <span class="menu-wrap">
            <button class="btn btn--sm btn--ghost" data-menu="${esc(a.uid)}"
                    aria-haspopup="true" aria-expanded="${state.openMenu === a.uid}">⋯<span class="sr-only">更多操作</span></button>
            ${state.openMenu === a.uid ? `
              <div class="menu" role="menu">
                <button role="menuitem" data-account-action="reauth">重新授权</button>
                <button role="menuitem" data-account-action="refresh">刷新凭证</button>
                <button role="menuitem" data-account-action="credits">刷新余额</button>
                <div class="menu__sep"></div>
                <button role="menuitem" data-account-action="toggle-pause">
                  ${a.useState === 'paused' ? '恢复对话使用' : '暂停对话使用'}
                </button>
                <div class="menu__sep"></div>
                <button role="menuitem" class="is-danger" data-account-action="delete">删除本机账号</button>
              </div>` : ''}
          </span>
        </td>
      </tr>`).join('');

    const emptyState = `
      <div class="state">
        <div class="state__icon" aria-hidden="true">◫</div>
        <div class="state__title">还没有添加任何账号</div>
        <div class="state__text">API 服务可以启动，但在添加账号之前不会有可调用的模型。
          添加账号需要你在系统浏览器里完成一次上游授权，本应用不会要求你在这里输入密码。</div>
        <div class="btn-row" style="justify-content:center">
          <button class="btn btn--primary" data-action="add-account">添加账号</button>
          <button class="btn" data-action="import-auth">导入凭证文件</button>
        </div>
      </div>`;

    return `
      <div class="page page--wide">
        ${pageHead({
          eyebrow: '本地凭证',
          title: '账号管理',
          desc: '账号是本地凭证文件。显示名只是本机备注，不会改动上游账号资料。',
          actions: accts.length === 0 ? '' : `
            <button class="btn btn--primary" data-action="add-account">添加账号</button>
            <button class="btn" data-action="import-auth">导入</button>`,
        })}

        <div class="notice notice--accent">
          <div class="notice__icon" aria-hidden="true">i</div>
          <div class="notice__body">
            <div class="notice__title">账号维护会暂时停止 API</div>
            <div class="notice__text">授权、导入、刷新、删除都会写入凭证文件。为避免内置面板与网关同时写入导致令牌互相覆盖，
              这些操作期间 API 会暂停，完成后按操作前的状态自动恢复。</div>
          </div>
        </div>

        <div>
          ${accts.length === 0 ? emptyState : `
            <div class="filterbar">
              <input class="input" type="search" placeholder="搜索昵称或 UID" aria-label="搜索账号">
              <select class="select" aria-label="按区域筛选">
                <option>全部区域</option><option>CN</option><option>Global</option>
              </select>
              <select class="select" aria-label="按状态筛选">
                <option>全部状态</option><option>可用</option><option>已暂停</option>
                <option>冷却中</option><option>凭证过期</option>
              </select>
            </div>

            ${sel.size > 0 ? `
              <div class="batchbar">
                <span>已选择 <strong>${sel.size}</strong> 个账号</span>
                <span class="batchbar__spacer"></span>
                <button class="btn btn--sm" data-action="batch-refresh">刷新凭证</button>
                <button class="btn btn--sm" data-action="batch-credits">刷新余额</button>
                <button class="btn btn--sm btn--ghost" data-action="clear-selection">取消选择</button>
              </div>` : ''}

            <div class="table-wrap">
              <table class="table">
                <thead>
                  <tr>
                    <th class="table__check"><input type="checkbox" data-select-all
                        aria-label="全选当前列表"></th>
                    <th>显示名 / UID</th><th>区域</th><th>凭证有效期</th>
                    <th>对话使用</th><th class="table__num">余额 / 积分</th><th>更新时间</th>
                    <th>最近错误</th><th class="table__actions">操作</th>
                  </tr>
                </thead>
                <tbody>${rows}</tbody>
              </table>
            </div>
            <p class="card__hint" style="margin-top:var(--sp-2)">
              批量操作只处理你显式勾选的账号，最多 20 个，逐个串行执行并逐项显示结果。
              没有勾选时不会默认对全部账号操作。
            </p>`}
        </div>
      </div>`;
  }

  /* ── 抽屉：账号详情 ─────────────────────────────────────────────────── */

  function drawerAccount() {
    const a = sc().accounts.find((x) => x.uid === state.drawer);
    if (!a) return '';
    return `
      <div class="drawer-overlay" data-close-drawer>
        <aside class="drawer" role="dialog" aria-modal="true" aria-labelledby="drawer-title" data-drawer-panel>
          <div class="drawer__head">
            <h2 id="drawer-title">${esc(a.name)}</h2>
            <button class="btn btn--sm btn--ghost" data-close-drawer aria-label="关闭详情">✕</button>
          </div>
          <div class="drawer__body">
            <dl class="kv">
              <dt>UID</dt>
              <dd><code>${esc(a.uid)}</code>
                <button class="btn btn--sm btn--ghost" data-action="copy-uid">复制</button></dd>
              <dt>区域</dt><dd>${a.realm === 'cn' ? 'CN' : 'Global'}</dd>
              <dt>凭证</dt><dd>${a.loaded ? '存在' : '缺失'} · 到期 ${esc(a.expires)}</dd>
              <dt>网关已加载</dt><dd>${a.loaded ? '是' : '<span style="color:var(--warn)">否</span>'}</dd>
              <dt>对话使用</dt><dd>${useStateBadge(a)}</dd>
              <dt>冷却原因</dt><dd>${esc(a.cooldown || '—')}</dd>
              <dt>最近余额</dt><dd>${a.creditUnknown ? '余额未知（未成功观测）' : fmtNum(a.credits) + ' 积分'}</dd>
              <dt>最后更新</dt><dd>${esc(a.creditsAt || '—')}</dd>
            </dl>

            ${a.lastError ? `
              <div class="notice notice--warn" style="margin-top:var(--sp-4)">
                <div class="notice__icon" aria-hidden="true">▲</div>
                <div class="notice__body">
                  <div class="notice__title">局部错误</div>
                  <div class="notice__text">${esc(a.lastError)}</div>
                </div>
              </div>` : ''}

            <div class="notice" style="margin-top:var(--sp-4)">
              <div class="notice__icon" aria-hidden="true">i</div>
              <div class="notice__body">
                <div class="notice__text">此页默认只读，不会自动刷新凭证或余额。
                  需要刷新余额时请点击下方按钮——那会进入维护流程（API 短暂暂停）。</div>
              </div>
            </div>
          </div>
          <div class="drawer__foot">
            <button class="btn" data-account-action="refresh">刷新凭证</button>
            <button class="btn" data-account-action="credits">刷新余额</button>
            <button class="btn btn--primary" data-close-drawer>关闭</button>
          </div>
        </aside>
      </div>`;
  }

  /* ── 页面：U04 添加账号 / 导入向导 ──────────────────────────────────── */

  function pageAddAccount() {
    const step = state.addFlow;
    const steps = ['idle', 'opening', 'waiting', 'saving', 'loading', 'success'];
    const idx = steps.indexOf(step);
    const failed = step === 'failed' || step === 'cancelled' || step === 'expired';

    const stepList = steps.map((k, i) => {
      const st = failed ? 'pending' : (i < idx ? 'done' : i === idx ? 'current' : 'pending');
      const names = { idle: '准备', opening: '打开浏览器', waiting: '等待授权', saving: '保存凭证', loading: '加载到池', success: '完成' };
      return `<div class="steps__item" data-state="${st}">
        <span class="steps__num">${st === 'done' ? '✓' : i + 1}</span>${names[k]}
      </div>` + (i < steps.length - 1 ? '<span class="steps__sep" aria-hidden="true"></span>' : '');
    }).join('');

    let body = '';
    if (step === 'idle') {
      body = `
        <p style="color:var(--text-secondary);line-height:1.8">
          选择账号所属区域后，应用会在<b>系统默认浏览器</b>里打开上游授权页。
          请在那里登录并确认授权——本应用不会看到也不会保存你的密码。
        </p>
        <div class="field" style="margin-top:var(--sp-4)">
          <span class="field__label">区域</span>
          <div class="btn-row">
            <button class="btn ${state.addRegion === 'cn' ? 'btn--primary' : ''}" data-region="cn">CN（国内版）</button>
            <button class="btn ${state.addRegion === 'global' ? 'btn--primary' : ''}" data-region="global">Global（国际版）</button>
          </div>
          <span class="field__help">${state.addRegion === 'cn'
            ? 'CN 账号走国内版上游，通常延迟更低。'
            : 'Global 账号走国际版上游，模型列表与 CN 可能不同，统计中会带 global: 前缀。'}</span>
        </div>
        <div class="notice notice--warn" style="margin-top:var(--sp-4)">
          <div class="notice__icon" aria-hidden="true">▲</div>
          <div class="notice__body">
            <div class="notice__title">授权期间 API 会暂停</div>
            <div class="notice__text">授权成功后需要写入凭证文件。为避免与网关并发写入冲突，API 会短暂停止，完成后自动恢复。</div>
          </div>
        </div>
        <div class="btn-row btn-row--end" style="margin-top:var(--sp-5)">
          <button class="btn" data-nav="accounts">取消</button>
          <button class="btn btn--primary" data-action="add-start">在浏览器中授权</button>
        </div>`;
    } else if (step === 'opening' || step === 'waiting') {
      body = `
        <div class="state state--loading" style="padding:var(--sp-6) 0">
          <div class="spinner spinner--lg" aria-hidden="true"></div>
          <div class="state__title">${step === 'opening' ? '正在打开系统浏览器…' : '已打开系统浏览器，等待你完成授权'}</div>
          <div class="state__text">
            ${step === 'opening'
              ? '如果浏览器没有自动弹出，点击下方「重新打开授权页」。'
              : '在浏览器里完成上游登录并确认授权后，这里会自动继续。授权会话有效期约 10 分钟，过期后需要重新发起。'}
          </div>
          <div class="btn-row" style="justify-content:center">
            <button class="btn" data-action="add-reopen">重新打开授权页</button>
            <button class="btn btn--ghost" data-action="add-cancel">取消授权</button>
          </div>
          <p class="card__hint">区域：${state.addRegion === 'cn' ? 'CN' : 'Global'} ·
            轮询间隔遵从上游返回的会话参数，不会无间隔重复请求。</p>
        </div>`;
    } else if (step === 'saving' || step === 'loading') {
      body = `
        <div class="state state--loading" style="padding:var(--sp-6) 0">
          <div class="spinner spinner--lg" aria-hidden="true"></div>
          <div class="state__title">${step === 'saving' ? '授权成功，正在保存凭证…' : '凭证已保存，正在让网关加载…'}</div>
          <div class="state__text">
            ${step === 'loading'
              ? '网关每 5 秒检查一次账号目录，通常几秒内即可生效，无需重启服务。'
              : '正在原子写入凭证文件，不会产生半截文件。'}
          </div>
        </div>`;
    } else if (step === 'success') {
      body = `
        <div class="state" style="padding:var(--sp-6) 0">
          <div class="state__icon" style="color:var(--ok)" aria-hidden="true">✓</div>
          <div class="state__title">账号已添加并加载完成</div>
          <div class="state__text">
            凭证已写入本机，网关已确认加载（账号池内可见）。<br>
            这里只显示是否成功，不显示任何令牌内容。
          </div>
          <dl class="kv" style="text-align:left;margin-top:var(--sp-2)">
            <dt>显示名</dt><dd>新账号（可在列表里改本机备注）</dd>
            <dt>区域</dt><dd>${state.addRegion === 'cn' ? 'CN' : 'Global'}</dd>
            <dt>凭证有效期</dt><dd>2026-10-21 12:04</dd>
          </dl>
          <div class="btn-row" style="justify-content:center">
            <button class="btn btn--primary" data-nav="accounts">返回账号列表</button>
          </div>
        </div>`;
    } else if (failed) {
      const map = {
        failed: ['保存失败', '授权已成功，但写入凭证文件失败（磁盘空间不足）。为避免显示成「添加成功」，本次不会记入账号列表。你可以释放空间后重试，或改用手工导入。'],
        cancelled: ['已取消授权', '已停止轮询并释放本次授权会话，没有写入任何账号。'],
        expired: ['授权会话已过期', '上游授权会话超时失效，未写入任何账号。请重新发起一次授权。'],
      };
      body = `
        <div class="state state--error" style="padding:var(--sp-6) 0">
          <div class="state__icon" aria-hidden="true">✕</div>
          <div class="state__title">${esc(map[step][0])}</div>
          <div class="state__text">${esc(map[step][1])}</div>
          <div class="btn-row" style="justify-content:center">
            <button class="btn btn--primary" data-action="add-reset">重新开始</button>
            <button class="btn" data-nav="accounts">返回账号列表</button>
          </div>
        </div>`;
    }

    return `
      <div class="page">
        ${pageHead({
          eyebrow: '本地凭证 · 新增',
          title: '添加账号',
          desc: '授权在你的系统浏览器里完成，本应用不收集密码。',
        })}
        <div class="card">
          <div class="steps" style="margin-bottom:var(--sp-5)">${stepList}</div>
          ${body}
        </div>
      </div>`;
  }

  function pageImportAuth() {
    return `
      <div class="page">
        ${pageHead({
          eyebrow: '本地凭证 · 迁移',
          title: '导入凭证文件',
          desc: '用于把已有凭证迁移到本应用，或恢复备份。',
        })}
        <div class="card">
          <div class="notice">
            <div class="notice__icon" aria-hidden="true">i</div>
            <div class="notice__body">
              <div class="notice__title">只导入你自己有权使用的凭证</div>
              <div class="notice__text">本应用不会扫描你的磁盘。文件必须由你通过系统文件选择器指定，
                单个文件上限 1 MB，导入前会先预览非敏感信息。</div>
            </div>
          </div>
          <div class="field" style="margin-top:var(--sp-4)">
            <span class="field__label">凭证文件</span>
            <div class="input-group">
              <input class="input" value="（未选择文件）" readonly aria-label="已选择的凭证文件路径">
              <button class="btn" data-action="pick-file">选择文件…</button>
            </div>
            <span class="field__help">支持上游项目的凭证 JSON 格式。拒绝路径穿越与符号链接越界。</span>
          </div>
          <div class="btn-row btn-row--end" style="margin-top:var(--sp-5)">
            <button class="btn" data-nav="accounts">取消</button>
            <button class="btn" disabled title="请先选择文件">预览并导入</button>
          </div>
        </div>
      </div>`;
  }

  /* ── 页面：U05 API 与模型 ───────────────────────────────────────────── */

  function pageApi() {
    return `
      <div class="page page--wide">
        ${pageHead({
          eyebrow: '客户端接入',
          title: 'API 与模型',
          desc: '把这些信息填进支持 OpenAI Chat Completions 协议的客户端即可接入。',
        })}

        <div class="card">
          <div class="card__head"><h3>接入信息</h3><div class="spacer"></div>${mockFlag()}</div>
          <div class="field">
            <span class="field__label">Base URL</span>
            <div class="input-group">
              <input class="input input--mono" value="${esc(BASE_URL)}" readonly aria-label="Base URL">
              <button class="btn" data-action="copy-url">复制</button>
            </div>
            <span class="field__help">多数客户端填 <code class="code--inline">…/v1</code> 即可。</span>
          </div>
          <div class="field">
            <span class="field__label">完整聊天端点</span>
            <div class="input-group">
              <input class="input input--mono" value="${esc(BASE_URL)}/chat/completions" readonly aria-label="完整聊天端点">
              <button class="btn" data-action="copy-endpoint">复制</button>
            </div>
            <span class="field__help">只有当客户端要求「完整端点」时才填这一条。两条不要同时填，否则会拼成 <code class="code--inline">/v1/v1/…</code>。</span>
          </div>
          <div class="field">
            <span class="field__label">API 密钥</span>
            <div class="input-group">
              <input class="input input--mono" value="${esc(API_KEY_MASK)}" readonly aria-label="API 密钥（已遮罩）">
              <button class="btn" data-action="copy-key">复制</button>
              <button class="btn" data-action="rotate-key">轮换</button>
            </div>
            <span class="field__help">默认遮罩显示。轮换后旧密钥立即对新请求失效，已配置旧密钥的客户端需要同步更新。</span>
          </div>
          <div class="btn-row" style="margin-top:var(--sp-4)">
            <button class="btn" data-action="check-local">检查本地连通性</button>
            <button class="btn" data-action="check-chat">发送一次真实测试对话</button>
          </div>
          <p class="card__hint" style="margin-top:var(--sp-2)">
            本地连通性检查只访问本机服务，不产生上游请求。「真实测试对话」会调用上游并消耗额度，由你主动触发。
          </p>
        </div>

        <div class="card">
          <div class="card__head">
            <h3>可用模型</h3><div class="spacer"></div>
            <span class="card__hint">最近刷新 2026-09-21 11:15</span>
            <button class="btn btn--sm" data-action="refresh-models">刷新</button>
          </div>
          <div class="table-wrap">
            <table class="table">
              <thead><tr>
                <th>模型 ID</th><th>名称</th><th>上下文</th><th>区域</th><th>最近刷新</th>
                <th class="table__actions">操作</th>
              </tr></thead>
              <tbody>
                ${MODELS.map((m) => `
                  <tr>
                    <td class="model-id">${esc(m.id)}</td>
                    <td>${esc(m.name)}</td>
                    <td>${esc(m.ctx)}</td>
                    <td>${m.realm === 'cn' ? badge('accent', 'CN', false) : badge('accent', 'Global', false)}</td>
                    <td style="font-size:var(--fs-xs);color:var(--text-muted)">${esc(m.at)}</td>
                    <td class="table__actions">
                      <button class="btn btn--sm" data-action="copy-model" data-model="${esc(m.id)}">复制 ID</button>
                    </td>
                  </tr>`).join('')}
              </tbody>
            </table>
          </div>
          <p class="card__hint" style="margin-top:var(--sp-2)">
            模型 ID 按上游原样保留（含 <code class="code--inline">global:</code> 前缀），不重命名。
            接口未提供的字段显示「未提供」。<b>模型被列出不等于当前账号一定有权调用。</b>
          </p>
        </div>

        <div class="card">
          <div class="card__head"><h3>接入示例</h3><div class="spacer"></div>
            <button class="btn btn--sm" data-action="copy-curl">复制</button></div>
          <pre class="code">curl ${esc(BASE_URL)}/chat/completions \\
  -H "Authorization: Bearer &lt;你的密钥&gt;" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "wb-chat-standard",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'</pre>
          <p class="card__hint" style="margin-top:var(--sp-2)">
            示例中的密钥是占位符。本应用不会自动改写其他软件的配置文件。
          </p>
        </div>
      </div>`;
  }

  /* ── 页面：U06 对话测试 ─────────────────────────────────────────────── */

  function pageChat() {
    const canSend = sc().gateway === 'running_ready';
    const msgs = state.chat;

    let thread;
    if (!msgs.length) {
      thread = `
        <div class="state">
          <div class="state__icon" aria-hidden="true">◌</div>
          <div class="state__title">还没有对话</div>
          <div class="state__text">选择模型后发送一条消息试试。对话只保存在内存中，退出应用即清空。</div>
        </div>`;
    } else {
      thread = msgs.map((m) => `
        <div class="msg msg--${m.role}">
          <div class="msg__role" aria-hidden="true">${m.role === 'user' ? '我' : 'AI'}</div>
          <div class="msg__body">
            <div class="msg__name">${m.role === 'user' ? '你' : '助手'}</div>
            <div class="msg__text">${esc(m.text)}${m.streaming ? '<span class="caret" aria-hidden="true"></span>' : ''}</div>
            ${m.meta ? `<div class="msg__meta">${m.meta.map((x) => `<span>${esc(x)}</span>`).join('')}</div>` : ''}
            ${m.stopped ? '<div class="msg__meta"><span style="color:var(--warn)">▲ 已停止生成，以上为已接收内容</span></div>' : ''}
            ${m.interrupted ? '<div class="msg__meta"><span style="color:var(--danger)">✕ 连接中断，以上为已接收内容</span></div>' : ''}
          </div>
        </div>`).join('');
    }

    return `
      <div class="page page--wide" style="display:flex;flex-direction:column;height:100%">
        ${pageHead({
          eyebrow: '连通性验证',
          title: '对话测试',
          desc: '在应用内验证接入是否正常。对话仅存在于当前会话内存，退出即清空。',
        })}

        <div class="card" style="flex:1 1 auto;display:flex;flex-direction:column;min-height:0">
          <div class="card__head">
            <select class="select" style="width:auto;min-width:200px" aria-label="选择模型">
              ${MODELS.map((m) => `<option>${esc(m.id)}</option>`).join('')}
            </select>
            <button class="btn btn--sm btn--ghost" data-action="chat-clear">清空本页对话</button>
            <button class="btn btn--sm btn--ghost" data-action="chat-params" aria-expanded="false">参数 ▾</button>
            <div class="spacer"></div>
            ${mockFlag()}
          </div>

          <div style="flex:1 1 auto;overflow-y:auto;padding:var(--sp-2) 0;min-height:200px">
            <div class="chat">${thread}</div>
            ${state.chatError ? `
              <div class="notice notice--danger" style="margin-top:var(--sp-3)">
                <div class="notice__icon" aria-hidden="true">✕</div>
                <div class="notice__body">
                  <div class="notice__title">${esc(state.chatError.title)}</div>
                  <div class="notice__text">${esc(state.chatError.text)}</div>
                  <div class="notice__actions">
                    <button class="btn btn--sm" data-action="chat-retry">重试</button>
                    <button class="btn btn--sm btn--ghost" data-nav="accounts">去账号管理</button>
                  </div>
                </div>
              </div>` : ''}
          </div>

          <div style="border-top:1px solid var(--border);padding-top:var(--sp-3)">
            ${!canSend ? `
              <div class="notice notice--warn" style="margin-bottom:var(--sp-3)">
                <div class="notice__icon" aria-hidden="true">▲</div>
                <div class="notice__body"><div class="notice__text">
                  当前没有可用账号（503 NO_USABLE_ACCOUNT），发送会直接失败。请先处理账号状态。</div></div>
              </div>` : ''}
            <div class="field">
              <label class="field__label sr-only" for="chat-input">输入消息</label>
              <textarea class="textarea" id="chat-input" placeholder="输入消息，⌘Enter 发送 / Enter 换行"
                ${canSend ? '' : 'disabled'}>${esc(state.chatDraft || '')}</textarea>
            </div>
            <div class="btn-row" style="margin-top:var(--sp-2)">
              <button class="btn btn--primary" data-action="chat-send" ${canSend && !state.chatStreaming ? '' : 'disabled'}>
                ${state.chatStreaming ? '生成中…' : '发送'}</button>
              <button class="btn" data-action="chat-stop" ${state.chatStreaming ? '' : 'disabled'}>停止</button>
              <span class="card__hint">⌘Enter 发送 · 中文输入法候选状态下 Enter 不会触发发送</span>
            </div>
          </div>
        </div>
      </div>`;
  }

  /* ── 页面：U07 运行统计 ─────────────────────────────────────────────── */

  function pageStats() {
    const running = ['running_ready', 'running_unavailable', 'maintenance'].includes(sc().gateway);
    const s = STATS;

    if (!running) {
      return `
        <div class="page page--wide">
          ${pageHead({
            eyebrow: '本次运行',
            title: '本次运行统计',
            desc: '统计范围是网关本次进程运行期间，重启后重新开始。',
          })}
          <div class="card">
            <div class="state">
              <div class="state__icon" aria-hidden="true">◷</div>
              <div class="state__title">服务未运行，暂无统计</div>
              <div class="state__text">
                统计保存在网关进程内存中，停止后不保留。启动服务后这里会显示本次运行的请求数、Token 与延迟。
              </div>
              <div class="btn-row" style="justify-content:center">
                <button class="btn btn--primary" data-action="gw-start">启动 API 服务</button>
              </div>
            </div>
          </div>
        </div>`;
    }

    const pct = (a, b) => (b ? Math.round((a / b) * 100) : 0);

    return `
      <div class="page page--wide">
        ${pageHead({
          eyebrow: '本次运行',
          title: '本次运行统计',
          desc: `统计开始时间：<b>${esc(s.since)}</b> · 已运行 ${fmtDur(s.uptime)}`,
        })}

        <div class="notice notice--accent">
          <div class="notice__icon" aria-hidden="true">i</div>
          <div class="notice__body">
            <div class="notice__title">这是本次运行的累计，不是历史账单</div>
            <div class="notice__text">
              网关统计只存在于内存中，重启或重置都会清零，因此这里不提供日期范围筛选，也没有历史曲线。
              Token 与积分只在上游返回用量时计入；未返回用量的请求单独计数，不会按 0 计算。
            </div>
          </div>
        </div>

        <div class="metric-strip" style="margin-top:var(--sp-5)">
          <div class="metric-strip__item">
            <span class="metric-strip__label">请求数</span>
            <span class="metric-strip__value">${fmtNum(s.total.requests)}</span>
            <span class="metric-strip__hint">成功 ${s.total.success} · 失败 ${s.total.failed}</span>
          </div>
          <div class="metric-strip__item">
            <span class="metric-strip__label">输入 Token</span>
            <span class="metric-strip__value">${fmtNum(s.total.prompt)}</span>
            <span class="metric-strip__hint">仅统计有用量返回的请求</span>
          </div>
          <div class="metric-strip__item">
            <span class="metric-strip__label">输出 Token</span>
            <span class="metric-strip__value">${fmtNum(s.total.completion)}</span>
            <span class="metric-strip__hint">平均延迟 ${fmtNum(s.total.avgLatency)} ms</span>
          </div>
          <div class="metric-strip__item">
            <span class="metric-strip__label">上游积分（观测值）</span>
            <span class="metric-strip__value">${s.models.reduce((a, m) => a + m.credit, 0).toFixed(2)}</span>
            <span class="metric-strip__hint">上游实际返回，非估算</span>
          </div>
        </div>

        <div class="card">
          <div class="card__head"><h3>统计完整性</h3><div class="spacer"></div></div>
          <div class="metric-row">
            <div class="metric">
              <span class="metric__label">返回用量的请求</span>
              <span class="metric__value">${s.total.usageObserved} / ${s.total.requests}</span>
            </div>
            <div class="metric">
              <span class="metric__label">未返回用量的请求</span>
              <span class="metric__value">${s.total.requests - s.total.usageObserved}</span>
            </div>
            <div class="metric">
              <span class="metric__label">平均首 Token 延迟</span>
              <span class="metric__value">${fmtNum(s.total.avgTtfb)} ms</span>
            </div>
          </div>
          <p class="card__hint" style="margin-top:var(--sp-3)">
            ${s.total.requests - s.total.usageObserved} 个请求上游未返回用量，它们的 Token 计为「未知」而不是 0。
            首 Token 延迟按「首段内容到达时间 − 请求开始时间」计算。
          </p>
        </div>

        <div class="card">
          <div class="card__head"><h3>按模型</h3><div class="spacer"></div>
            <button class="btn btn--sm btn--danger" data-action="stats-reset">重置本次统计</button></div>
          <div class="table-wrap">
            <table class="table">
              <thead><tr>
                <th>模型</th><th class="table__num">请求</th><th class="table__num">成功/失败</th>
                <th class="table__num">输入</th><th class="table__num">输出</th>
                <th class="table__num">平均延迟</th><th class="table__num">积分</th>
              </tr></thead>
              <tbody>
                ${s.models.map((m) => `
                  <tr>
                    <td class="model-id">${esc(m.id)}</td>
                    <td class="table__num">${fmtNum(m.requests)}</td>
                    <td class="table__num">${m.success} / ${m.failed}</td>
                    <td class="table__num">${fmtNum(m.prompt)}</td>
                    <td class="table__num">${fmtNum(m.completion)}</td>
                    <td class="table__num">${fmtNum(m.avgLatency)} ms</td>
                    <td class="table__num">${m.credit ? m.credit.toFixed(2) : '—'}</td>
                  </tr>`).join('')}
              </tbody>
            </table>
          </div>
          <p class="card__hint" style="margin-top:var(--sp-2)">
            重置只清空本次运行的统计，<b>不会</b>删除账号、不会影响上游账单或额度。
          </p>
        </div>
      </div>`;
  }

  /* ── 页面：U08 网关设置 ─────────────────────────────────────────────── */

  function pageConfig() {
    const guard = sessionGuard(); if (guard) return guard;
    const res = state.configResult;
    return `
      <div class="page">
        ${pageHead({
          eyebrow: '服务配置',
          title: '网关设置',
          desc: '修改后会停止并重启 API 服务。保存失败会自动回滚到修改前的配置。',
        })}

        ${res === 'rollback' ? `
          <div class="notice notice--danger">
            <div class="notice__icon" aria-hidden="true">✕</div>
            <div class="notice__body">
              <div class="notice__title">应用失败，已回滚到修改前的配置
                <span class="code--inline" style="margin-left:8px">CONFIG_INVALID</span></div>
              <div class="notice__text">监听端口 8100 校验失败（超出允许范围）。
                当前服务运行在<b>回滚后的旧配置</b>上，你填写的草稿已保留，可以修改后重试。</div>
              <div class="notice__actions">
                <button class="btn btn--sm" data-action="config-retry">修改并重试</button>
                <button class="btn btn--sm btn--ghost" data-action="config-discard">放弃草稿</button>
              </div>
            </div>
          </div>` : ''}

        ${res === 'ok' ? `
          <div class="notice notice--ok">
            <div class="notice__icon" aria-hidden="true">✓</div>
            <div class="notice__body">
              <div class="notice__title">配置已生效</div>
              <div class="notice__text">服务已在新的监听地址 <code>127.0.0.1:7863</code> 上运行，重启耗时 2.4 秒。
                如果你在外部客户端里配置过地址，端口没变则无需改动。</div>
            </div>
          </div>` : ''}

        <div class="card">
          <div class="card__head"><h3>基础设置</h3><div class="spacer"></div>${mockFlag()}</div>

          <div class="setting-row">
            <div class="setting-row__label">API 端口</div>
            <div class="setting-row__control">
              <input class="input" value="7863" inputmode="numeric" aria-label="API 端口" style="max-width:140px">
              <div class="field__help">只监听本机回环地址。端口被其他程序占用时会明确提示，不会静默改用其他端口。</div>
            </div>
          </div>

          <div class="setting-row">
            <div class="setting-row__label">请求超时</div>
            <div class="setting-row__control">
              <div class="input-group" style="max-width:220px">
                <input class="input" value="120" inputmode="numeric" aria-label="请求超时秒数" style="max-width:100px">
                <span class="card__hint">秒</span>
              </div>
              <div class="field__help">单次上游请求的总时长上限，同时作为流式响应首字节前的等待上限。</div>
            </div>
          </div>

          <div class="setting-row">
            <div class="setting-row__label">账号并发上限</div>
            <div class="setting-row__control">
              <input class="input" value="3" inputmode="numeric" aria-label="账号并发上限" style="max-width:100px">
              <div class="field__help">单个账号同时处理的请求数。调高会提高吞吐，也更容易触发上游限流。</div>
            </div>
          </div>

          <div class="setting-row">
            <div class="setting-row__label">会话粘性</div>
            <div class="setting-row__control">
              <label class="switch">
                <input type="checkbox" checked><span class="switch__track"></span>
                <span class="switch__text">同一会话尽量复用同一账号</span>
              </label>
              <div class="field__help">开启后多轮对话更稳定；关闭后每次请求独立选号。</div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card__head"><h3>网络代理</h3><div class="spacer"></div></div>
          <div class="setting-row">
            <div class="setting-row__label">代理模式</div>
            <div class="setting-row__control">
              <select class="select" aria-label="代理模式">
                <option>直连</option><option>指定 HTTP 代理</option>
              </select>
              <div class="field__help">
                本机面板与网关之间的互访始终绕过代理，不受这里影响。
                这是本应用新增的设置，不是上游网关原有的配置项。
              </div>
            </div>
          </div>
          <div class="setting-row">
            <div class="setting-row__label">代理地址</div>
            <div class="setting-row__control">
              <input class="input input--mono" placeholder="http://127.0.0.1:7890" disabled
                     aria-label="代理地址" style="max-width:320px">
              <div class="field__help">仅支持 <code class="code--inline">http://主机:端口</code>，不接受带用户名密码的地址。</div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card__head"><h3>高级（只读摘要）</h3><div class="spacer"></div>
            <button class="btn btn--sm btn--ghost" data-action="toggle-advanced">展开</button></div>
          <div class="summary-grid">
            <div class="summary-grid__row"><span class="summary-grid__key">监听地址</span><span class="summary-grid__val">127.0.0.1:7863（固定回环）</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">数据目录</span><span class="summary-grid__val">~/Library/Application Support/…</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">账号目录</span><span class="summary-grid__val">…/auths</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">定时任务</span><span class="summary-grid__val">全部关闭（6 项）</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">Redis</span><span class="summary-grid__val">未配置（纯内存）</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">配置备份</span><span class="summary-grid__val">3 / 10 份 · 24 KB</span></div>
          </div>
          <p class="card__hint" style="margin-top:var(--sp-3)">
            监听地址、数据路径、管理密钥与桌面禁用项不允许在此修改。
          </p>
        </div>

        ${state.configDirty ? `
          <div class="notice notice--warn">
            <div class="notice__icon" aria-hidden="true">▲</div>
            <div class="notice__body">
              <div class="notice__text">有未保存的修改。保存会停止并重启 API 服务，进行中的请求会受影响。</div>
            </div>
          </div>` : ''}

        <div class="card" style="position:sticky;bottom:0">
          <div class="btn-row btn-row--end">
            <button class="btn" data-action="config-discard" ${state.configDirty ? '' : 'disabled'}>放弃修改</button>
            <button class="btn btn--primary" data-action="config-save" ${state.configDirty ? '' : 'disabled'}>保存并应用</button>
          </div>
          ${!state.configDirty ? '<p class="card__hint" style="text-align:right">改动任意字段后此按钮才可用</p>' : ''}
        </div>
      </div>`;
  }

  /* ── 页面：U09 日志与诊断 ───────────────────────────────────────────── */

  function pageLogs() {
    const levels = ['ALL', 'INFO', 'WARN', 'ERROR'];
    const lines = LOG_LINES.filter((l) => state.logLevel === 'ALL' || l[1] === state.logLevel);
    return `
      <div class="page page--wide">
        ${pageHead({
          eyebrow: '故障排查',
          title: '日志与诊断',
          desc: '日志按容量滚动（每个来源 10 MB × 最多 3 份），写入前已脱敏。',
        })}

        <div class="card">
          <div class="filterbar" style="margin-bottom:var(--sp-3)">
            <select class="select" aria-label="日志来源">
              <option>全部来源</option><option>应用</option><option>网关</option><option>面板</option>
            </select>
            <div class="segmented" role="group" aria-label="日志级别">
              ${levels.map((l) => `<button data-loglevel="${l}" aria-pressed="${state.logLevel === l}">${l}</button>`).join('')}
            </div>
            <input class="input" type="search" placeholder="关键词过滤" aria-label="关键词过滤">
            <div class="filterbar__spacer"></div>
            <button class="btn btn--sm" data-action="log-pause">${state.logPaused ? '继续滚动' : '暂停滚动'}</button>
            <button class="btn btn--sm" data-action="log-copy">复制选中</button>
            <button class="btn btn--sm btn--primary" data-action="diag-export">导出诊断包</button>
          </div>

          <div class="logview" role="log" aria-live="polite" aria-label="日志内容">
            ${lines.map(([ts, lv, msg]) => `
              <div class="logline">
                <span class="logline__ts">${esc(ts)}</span>
                <span class="logline__lv logline__lv--${lv}">${lv}</span>
                <span class="logline__msg">${esc(msg)}</span>
              </div>`).join('')}
          </div>
          <p class="card__hint" style="margin-top:var(--sp-2)">
            只读取日志尾部（单次最多 200 条），不会一次加载整个文件。页面最多保留 2,000 行。
          </p>
        </div>

        <div class="card">
          <div class="card__head"><h3>诊断包内容</h3><div class="spacer"></div></div>
          <p style="color:var(--text-secondary);line-height:1.8">导出前会先列出内容清单，由你用系统保存面板选择位置。不会自动上传到任何地方。</p>
          <div class="summary-grid" style="margin-top:var(--sp-3)">
            <div class="summary-grid__row"><span class="summary-grid__key">应用 / 网关 / 面板版本</span><span class="summary-grid__val">包含</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">系统与 CPU 架构</span><span class="summary-grid__val">包含</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">脱敏配置摘要</span><span class="summary-grid__val">包含</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">最近日志</span><span class="summary-grid__val">包含</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">账号凭证 / 令牌</span><span class="summary-grid__val" style="color:var(--danger)">不包含</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">完整 API 密钥 / Cookie</span><span class="summary-grid__val" style="color:var(--danger)">不包含</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">聊天正文</span><span class="summary-grid__val" style="color:var(--danger)">不包含</span></div>
            <div class="summary-grid__row"><span class="summary-grid__key">个人目录绝对路径</span><span class="summary-grid__val">导出时脱敏</span></div>
          </div>
        </div>
      </div>`;
  }

  /* ── 页面：U10 应用设置与关于 ───────────────────────────────────────── */

  function pageSettings() {
    return `
      <div class="page">
        ${pageHead({
          eyebrow: '应用偏好',
          title: '应用设置',
          desc: '这些是应用自身的偏好，与网关配置分开保存。',
        })}

        <div class="card">
          <div class="setting-row">
            <div class="setting-row__label">界面语言</div>
            <div class="setting-row__control">
              <select class="select" aria-label="界面语言" style="max-width:200px">
                <option>简体中文</option>
              </select>
              <div class="field__help">首版只提供简体中文。</div>
            </div>
          </div>
          <div class="setting-row">
            <div class="setting-row__label">外观</div>
            <div class="setting-row__control">
              <div class="segmented" role="group" aria-label="外观主题">
                <button data-theme-set="system" aria-pressed="${state.theme === 'system'}">跟随系统</button>
                <button data-theme-set="light" aria-pressed="${state.theme === 'light'}">浅色</button>
                <button data-theme-set="dark" aria-pressed="${state.theme === 'dark'}">深色</button>
              </div>
            </div>
          </div>
          <div class="setting-row">
            <div class="setting-row__label">打开应用时启动 API</div>
            <div class="setting-row__control">
              <label class="switch">
                <input type="checkbox" checked><span class="switch__track"></span>
                <span class="switch__text">应用启动后自动启动 API 服务</span>
              </label>
              <div class="field__help">
                这只影响「打开这个应用」的行为。<b>不是开机自启动</b>——首版不提供开机登录项。
              </div>
            </div>
          </div>
          <div class="setting-row">
            <div class="setting-row__label">关闭窗口时</div>
            <div class="setting-row__control">
              <div class="card__hint" style="line-height:1.7">
                保留在菜单栏继续运行，API 服务不中断。需要完全退出请使用 <kbd>⌘</kbd><kbd>Q</kbd> 或菜单栏的「完全退出」。
              </div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card__head"><h3>数据位置</h3><div class="spacer"></div></div>
          <div class="btn-row">
            <button class="btn" data-action="open-data-dir">打开数据目录</button>
            <button class="btn" data-action="open-log-dir">打开日志目录</button>
          </div>
          <dl class="kv" style="margin-top:var(--sp-4)">
            <dt>数据目录</dt><dd><code>~/Library/Application Support/WorkBuddy2APIDesktop/</code></dd>
            <dt>日志目录</dt><dd><code>~/Library/Logs/WorkBuddy2APIDesktop/</code></dd>
            <dt>占用</dt><dd>配置与凭证 312 KB · 日志 4.1 MB · 备份 24 KB</dd>
          </dl>
          <div class="notice" style="margin-top:var(--sp-4)">
            <div class="notice__icon" aria-hidden="true">i</div>
            <div class="notice__body">
              <div class="notice__title">清理是分开的，不会一键清空</div>
              <div class="notice__text">
                「清理日志」只删除滚动日志；账号与配置默认保留。本应用不会清理其他应用的文件，也不会清理整台电脑。
              </div>
              <div class="notice__actions">
                <button class="btn btn--sm" data-action="clean-logs">清理日志</button>
                <button class="btn btn--sm btn--danger" data-action="clean-data">清除账号与配置…</button>
              </div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card__head"><h3>关于</h3><div class="spacer"></div></div>
          <dl class="kv">
            <dt>应用版本</dt><dd>1.0.0（内部测试包，未公证）</dd>
            <dt>网关提交</dt><dd><code>d1023f37</code> · Sliverkiss/workbuddy2api</dd>
            <dt>面板提交</dt><dd><code>9413e70b</code> · linbeize/workbuddy2api-gui</dd>
            <dt>构建时间</dt><dd>2026-09-21 11:20:14</dd>
            <dt>许可</dt><dd>MIT（含上游组件）</dd>
          </dl>
          <p class="card__hint" style="margin-top:var(--sp-3);line-height:1.7">
            本应用不是任何上游厂商的官方产品，由第三方基于开源项目组合而成。
            更新方式：查看发布说明后手动下载新安装包，应用不会自动执行远程脚本。
          </p>
        </div>
      </div>`;
  }

  /* ── 页面：U01 首次使用向导 ─────────────────────────────────────────── */

  function pageWizard() {
    const step = state.wizardStep;
    const steps = ['欢迎', '本机准备', '添加账号', '启动服务', '接入测试'];
    const stepsHtml = steps.map((n, i) => `
      <div class="steps__item" data-state="${i < step ? 'done' : i === step ? 'current' : 'pending'}">
        <span class="steps__num">${i < step ? '✓' : i + 1}</span>${n}
      </div>` + (i < steps.length - 1 ? '<span class="steps__sep" aria-hidden="true"></span>' : '')).join('');

    let body = '';
    if (step === 0) {
      body = `
        <div class="wizard__hero">
          <div class="wizard__hero-icon" aria-hidden="true">◈</div>
          <h2>欢迎使用 WorkBuddy2API Desktop</h2>
          <p>把本机上的上游账号变成一个标准的 OpenAI 兼容 API 服务，供你本机的客户端调用。</p>
        </div>
        <div class="card">
          <dl class="kv">
            <dt>数据保存在</dt><dd><code>~/Library/Application Support/WorkBuddy2APIDesktop/</code></dd>
            <dt>日志保存在</dt><dd><code>~/Library/Logs/WorkBuddy2APIDesktop/</code></dd>
            <dt>服务地址</dt><dd><code>http://127.0.0.1:7863/v1</code>（仅本机可访问）</dd>
          </dl>
          <p class="card__hint" style="margin-top:var(--sp-3)">
            本应用不是上游厂商的官方产品。你需要使用自己有权使用的账号完成授权。
          </p>
        </div>
        <div class="btn-row btn-row--end" style="margin-top:var(--sp-5)">
          <button class="btn btn--ghost" data-action="wizard-skip">跳过设置</button>
          <button class="btn btn--primary" data-action="wizard-next">开始设置</button>
        </div>`;
    } else if (step === 1) {
      const checks = [
        ['✓', '内置服务程序', 'wb2api 与 wbgui 已就位（8.4 MB / 7.5 MB）', 'ok'],
        ['✓', '私有数据目录可写', '权限 0700，写入测试通过', 'ok'],
        ['✓', 'API 端口可用', '127.0.0.1:7863 未被占用', 'ok'],
        ['—', '上游网络可达', '尚未添加账号，添加后再验证', 'idle'],
      ];
      body = `
        <h2>本机准备</h2>
        <p style="color:var(--text-secondary);margin:var(--sp-2) 0 var(--sp-4)">
          以下检查在本机完成，不会联网，也不需要你在终端里执行任何命令。
        </p>
        <div class="checklist">
          ${checks.map(([icon, name, detail, tone]) => `
            <div class="checklist__item">
              <span class="checklist__icon" style="color:var(--${tone === 'ok' ? 'ok' : 'text-muted'})" aria-hidden="true">${icon}</span>
              <div class="checklist__body">
                <div class="checklist__name">${esc(name)}</div>
                <div class="checklist__detail">${esc(detail)}</div>
              </div>
            </div>`).join('')}
        </div>
        <div class="btn-row btn-row--end" style="margin-top:var(--sp-5)">
          <button class="btn" data-action="wizard-prev">上一步</button>
          <button class="btn btn--primary" data-action="wizard-next">下一步</button>
        </div>`;
    } else if (step === 2) {
      body = `
        <h2>添加账号</h2>
        <p style="color:var(--text-secondary);margin:var(--sp-2) 0 var(--sp-4)">
          授权会在你的系统浏览器里完成。本应用不会要求你在这里输入密码。
        </p>
        <div class="card">
          <div class="btn-row">
            <button class="btn btn--primary" data-action="wizard-add">在浏览器中授权 CN 账号</button>
            <button class="btn" data-action="wizard-add">授权 Global 账号</button>
          </div>
          <div class="notice" style="margin-top:var(--sp-3)">
            <div class="notice__icon" aria-hidden="true">i</div>
            <div class="notice__body"><div class="notice__text">
              现在也可以跳过。跳过的话主界面会提示「待添加账号」，API 服务能启动但暂时没有可调用的模型。</div></div>
          </div>
        </div>
        <div class="btn-row btn-row--end" style="margin-top:var(--sp-5)">
          <button class="btn" data-action="wizard-prev">上一步</button>
          <button class="btn" data-action="wizard-skip-step">稍后添加，继续</button>
          <button class="btn btn--primary" data-action="wizard-next">下一步</button>
        </div>`;
    } else if (step === 3) {
      body = `
        <h2>启动服务</h2>
        <p style="color:var(--text-secondary);margin:var(--sp-2) 0 var(--sp-4)">
          进程启动与账号就绪是两件事，这里分开显示。
        </p>
        <div class="card">
          <div class="checklist">
            <div class="checklist__item">
              <span class="checklist__icon" style="color:var(--ok)" aria-hidden="true">✓</span>
              <div class="checklist__body"><div class="checklist__name">API 进程已启动</div>
                <div class="checklist__detail">监听 127.0.0.1:7863，耗时 1.8 秒</div></div>
            </div>
            <div class="checklist__item">
              <span class="checklist__icon" style="color:var(--warn)" aria-hidden="true">▲</span>
              <div class="checklist__body"><div class="checklist__name">暂无可用账号</div>
                <div class="checklist__detail">还没有添加账号，因此模型暂时不可调用。这不是启动失败，服务不会反复重启。</div></div>
            </div>
          </div>
        </div>
        <div class="btn-row btn-row--end" style="margin-top:var(--sp-5)">
          <button class="btn" data-action="wizard-prev">上一步</button>
          <button class="btn btn--primary" data-action="wizard-next">下一步</button>
        </div>`;
    } else {
      body = `
        <h2>接入测试</h2>
        <p style="color:var(--text-secondary);margin:var(--sp-2) 0 var(--sp-4)">
          把下面两条复制到你的客户端，或在这里直接发一条测试消息。
        </p>
        <div class="card">
          <dl class="kv">
            <dt>Base URL</dt><dd><code>${esc(BASE_URL)}</code></dd>
            <dt>API 密钥</dt><dd><code>${esc(API_KEY_MASK)}</code></dd>
            <dt>模型 ID</dt><dd><code>wb-chat-standard</code></dd>
          </dl>
          <div class="btn-row" style="margin-top:var(--sp-3)">
            <button class="btn btn--sm" data-action="copy-url">复制地址</button>
            <button class="btn btn--sm" data-action="copy-key">复制密钥</button>
            <button class="btn btn--sm" data-action="copy-model" data-model="wb-chat-standard">复制模型 ID</button>
          </div>
          <div class="notice notice--warn" style="margin-top:var(--sp-4)">
            <div class="notice__icon" aria-hidden="true">▲</div>
            <div class="notice__body"><div class="notice__text">
              「发送测试消息」会调用上游并消耗额度，只有你点击时才会发出。</div></div>
          </div>
          <div class="btn-row" style="margin-top:var(--sp-3)">
            <button class="btn btn--primary" data-action="wizard-test">发送测试消息</button>
          </div>
        </div>
        <div class="card">
          <label class="switch">
            <input type="checkbox"><span class="switch__track"></span>
            <span class="switch__text">以后打开应用时启动 API</span>
          </label>
          <div class="field__help">初始关闭。这只是应用启动行为，不等于开机自启动。</div>
        </div>
        <div class="btn-row btn-row--end" style="margin-top:var(--sp-5)">
          <button class="btn" data-action="wizard-prev">上一步</button>
          <button class="btn btn--primary" data-action="wizard-finish">进入主界面</button>
        </div>`;
    }

    return `
      <div class="wizard">
        <div class="steps" style="justify-content:center;margin-bottom:var(--sp-6)">${stepsHtml}</div>
        ${body}
      </div>`;
  }

  /* ── 页面：菜单栏（示意图） ─────────────────────────────────────────── */

  function pageMenubar() {
    const g = gwState();
    return `
      <div class="page">
        ${pageHead({
          eyebrow: '系统集成',
          title: '菜单栏',
          desc: '关闭窗口后应用继续在菜单栏运行，API 服务不中断。这里展示菜单栏下拉的内容。',
        })}

        <div class="grid grid--2">
          <div class="card">
            <div class="card__head"><h3>下拉菜单</h3><div class="spacer"></div>${mockFlag()}</div>
            <div class="menu" style="position:static;box-shadow:none;min-width:0">
              <button><span style="display:flex;align-items:center;gap:8px">
                <span class="badge badge--${g.tone}" style="pointer-events:none"><span class="badge__dot"></span>${esc(g.label)}</span>
                <span style="font-size:var(--fs-xs);color:var(--text-muted)">127.0.0.1:7863</span>
              </span></button>
              <div class="menu__sep"></div>
              <button data-nav="overview">打开主窗口</button>
              <button>${sc().gateway === 'stopped' || sc().gateway === 'failed' ? '启动 API 服务' : '停止 API 服务'}</button>
              <button>复制 API 地址</button>
              <div class="menu__sep"></div>
              <button>打开日志目录</button>
              <div class="menu__sep"></div>
              <button class="is-danger">完全退出</button>
            </div>
            <p class="card__hint" style="margin-top:var(--sp-3)">
              状态徽标与文字同时表达状态，不只靠颜色。
            </p>
          </div>

          <div class="card">
            <div class="card__head"><h3>关闭窗口 vs 完全退出</h3><div class="spacer"></div></div>
            <div class="notice">
              <div class="notice__icon" aria-hidden="true">i</div>
              <div class="notice__body">
                <div class="notice__title">关闭窗口（<kbd>⌘</kbd><kbd>W</kbd>）</div>
                <div class="notice__text">应用留在菜单栏继续运行，API 服务不受影响。首次关闭时会给出一次说明。</div>
              </div>
            </div>
            <div class="notice notice--warn">
              <div class="notice__icon" aria-hidden="true">▲</div>
              <div class="notice__body">
                <div class="notice__title">完全退出（<kbd>⌘</kbd><kbd>Q</kbd>）</div>
                <div class="notice__text">停止接受新的本地测试请求，发出优雅关闭信号；等待最多 10 秒后
                  只终止本应用启动的子进程，然后退出。退出后端口一并释放。</div>
              </div>
            </div>
          </div>
        </div>
      </div>`;
  }

  /* ── 路由表 ─────────────────────────────────────────────────────────── */

  /* 导航条目。图标不再放在这里 —— 由 ICON 表按 id 取用（见文件上方），
     保证整套图标出自同一线性风格，而不是各页各挑一个字符符号。 */
  const ROUTES = [
    { id: 'overview',  label: '总览' },
    { id: 'accounts',  label: '账号管理' },
    { id: 'api',       label: 'API 与模型' },
    { id: 'chat',      label: '对话测试' },
    { id: 'stats',     label: '运行统计' },
    { id: 'config',    label: '网关设置' },
    { id: 'logs',      label: '日志与诊断' },
    { id: 'settings',  label: '应用设置' },
  ];

  const RENDER = {
    wizard: pageWizard,
    overview: pageOverview,
    accounts: pageAccounts,
    api: pageApi,
    chat: pageChat,
    stats: pageStats,
    config: pageConfig,
    logs: pageLogs,
    settings: pageSettings,
    'add-account': pageAddAccount,
    'import-auth': pageImportAuth,
    menubar: pageMenubar,
  };

  const TITLES = {
    overview: '总览', accounts: '账号管理', api: 'API 与模型', chat: '对话测试',
    stats: '运行统计', config: '网关设置', logs: '日志与诊断', settings: '应用设置',
    'add-account': '添加账号', 'import-auth': '导入凭证文件', menubar: '菜单栏',
  };

  /* ── 渲染 ───────────────────────────────────────────────────────────── */

  function render() {
    const s = sc();

    // 主题
    const theme = state.theme === 'system'
      ? (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
      : state.theme;
    document.documentElement.setAttribute('data-theme', theme);

    // 首次使用向导接管整个窗口，不显示侧栏
    const sidebar = $('.sidebar');
    if (sidebar) sidebar.hidden = state.route === 'wizard';

    // 侧栏：两组导航，各带明确的分组标签
    const navItem = (id, label) => `
      <button class="nav-item" data-nav="${id}" ${state.route === id ? 'aria-current="page"' : ''}>
        <span class="nav-item__icon">${icon(id)}</span>${esc(label)}
        ${id === 'accounts' && s.accounts.length === 0
          ? '<span class="badge badge--warn badge--sm nav-item__badge">待添加</span>' : ''}
      </button>`;

    $('#nav').innerHTML =
      '<div class="nav-group__label">工作区</div>'
      + ROUTES.map((r) => navItem(r.id, r.label)).join('')
      + '<div class="nav-group__label">系统</div>'
      + navItem('menubar', '菜单栏');

    // 顶栏是工具栏：只承载全局信息（网关状态 / 管理连接 / 接入地址），
    // 不再重复页面名 —— 页面身份由侧栏选中项与内容区标题共同表达。
    // 网关状态与管理连接分开呈现，对应「管理会话过期 ≠ API 不可用」这条设计约束。
    const g = gwState();
    $('#topbar').innerHTML = `
      <span class="badge badge--${g.tone}"><span class="badge__dot"></span>${esc(g.label)}</span>
      <span class="topbar__sep" aria-hidden="true"></span>
      <span class="topbar__meta">${s.sessionExpired ? '管理连接已过期' : '管理连接正常'}</span>
      <span class="topbar__spacer"></span>
      <span class="topbar__endpoint">127.0.0.1:7863</span>
      <span class="topbar__actions">${mockFlag()}</span>`;

    // 内容
    const renderer = RENDER[state.route] || pageOverview;
    $('#view').innerHTML = renderer();

    // 抽屉
    $('#layer').innerHTML = state.drawer ? drawerAccount() : '';

    // 弹窗
    $('#layer').innerHTML += state.modal ? modalHtml() : '';

    // 原型工具条状态同步
    $('#proto-scenario').value = state.scenario;
    $$('#proto-theme button').forEach((b) => {
      b.setAttribute('aria-pressed', String(b.dataset.themeSet === state.theme));
    });

    // 焦点管理：弹窗打开时把焦点移入
    if (state.modal) {
      const el = $('#layer .modal');
      if (el) (el.querySelector('button') || el).focus();
    }
  }

  function modalHtml() {
    const m = state.modal;
    return `
      <div class="overlay" data-close-modal>
        <div class="modal ${m.wide ? 'modal--wide' : ''}" role="dialog" aria-modal="true"
             aria-labelledby="modal-title" data-modal-panel>
          <div class="modal__head"><h2 class="modal__title" id="modal-title">${esc(m.title)}</h2></div>
          <div class="modal__body">${m.body}</div>
          <div class="modal__foot">
            <button class="btn" data-close-modal>${esc(m.cancelLabel || '取消')}</button>
            <button class="btn ${m.danger ? 'btn--danger' : 'btn--primary'}" data-modal-confirm>${esc(m.confirmLabel || '确定')}</button>
          </div>
        </div>
      </div>`;
  }

  /* ── 交互 ───────────────────────────────────────────────────────────── */

  const toast = (text, tone = 'ok') => {
    state.notices.push({ text, tone });
    const el = document.createElement('div');
    el.className = `notice notice--${tone}`;
    el.setAttribute('role', 'status');
    el.style.cssText = 'position:fixed;right:20px;top:64px;z-index:300;max-width:360px;box-shadow:var(--shadow-md)';
    el.innerHTML = `<div class="notice__icon" aria-hidden="true">${tone === 'ok' ? '✓' : '▲'}</div>
      <div class="notice__body"><div class="notice__text">${esc(text)}</div></div>`;
    document.body.appendChild(el);
    setTimeout(() => el.remove(), 2600);
  };

  const go = (route) => { state.route = route; state.openMenu = null; render(); $('#view').scrollTop = 0; };

  document.addEventListener('click', (e) => {
    const t = e.target;

    // 关闭菜单：点击空白处
    if (!t.closest('.menu-wrap') && state.openMenu) { state.openMenu = null; render(); }

    const nav = t.closest('[data-nav]');
    if (nav) { e.preventDefault(); go(nav.dataset.nav); return; }

    const themeBtn = t.closest('[data-theme-set]');
    if (themeBtn) { state.theme = themeBtn.dataset.themeSet; render(); return; }

    const logLv = t.closest('[data-loglevel]');
    if (logLv) { state.logLevel = logLv.dataset.loglevel; render(); return; }

    const menuBtnEl = t.closest('[data-menu]');
    if (menuBtnEl) {
      state.openMenu = state.openMenu === menuBtnEl.dataset.menu ? null : menuBtnEl.dataset.menu;
      render(); return;
    }

    const detail = t.closest('[data-detail]');
    if (detail) { state.drawer = detail.dataset.detail; render(); return; }

    const closeDrawer = t.closest('[data-close-drawer]');
    if (closeDrawer && (!t.closest('[data-drawer-panel]') || t.tagName === 'BUTTON')) {
      state.drawer = null; render(); return;
    }

    const closeModal = t.closest('[data-close-modal]');
    if (closeModal && (!t.closest('[data-modal-panel]') || t.tagName === 'BUTTON')) {
      state.modal = null; render(); return;
    }

    const confirm = t.closest('[data-modal-confirm]');
    if (confirm) { const fn = state.modal && state.modal.onConfirm; state.modal = null; if (fn) fn(); render(); return; }

    const region = t.closest('[data-region]');
    if (region) { state.addRegion = region.dataset.region; render(); return; }

    const selAll = t.closest('[data-select-all]');
    if (selAll) {
      const ids = sc().accounts.map((a) => a.uid).slice(0, 20);
      if (state.selected.size) state.selected.clear(); else ids.forEach((i) => state.selected.add(i));
      render(); return;
    }

    const action = t.closest('[data-action]');
    if (action) { handleAction(action.dataset.action, action); return; }
  });

  document.addEventListener('change', (e) => {
    const t = e.target;
    if (t.matches('[data-select]')) {
      const uid = t.dataset.select;
      if (t.checked) {
        if (state.selected.size >= 20) { toast('批量操作一次最多选择 20 个账号', 'warn'); t.checked = false; return; }
        state.selected.add(uid);
      } else state.selected.delete(uid);
      render();
    }
  });

  // 配置页任意改动 → 标记草稿
  document.addEventListener('input', (e) => {
    if (state.route === 'config' && e.target.matches('.input, .select, .textarea')) {
      if (!state.configDirty) { state.configDirty = true; render(); }
    }
    if (state.route === 'chat' && e.target.id === 'chat-input') {
      state.chatDraft = e.target.value;
    }
  });

  // 键盘：Escape 关闭弹窗/抽屉/菜单；⌘Enter 发送
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if (state.modal) { state.modal = null; render(); return; }
      if (state.drawer) { state.drawer = null; render(); return; }
      if (state.openMenu) { state.openMenu = null; render(); }
    }
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && state.route === 'chat') {
      e.preventDefault();
      handleAction('chat-send');
    }
  });

  /* ── 动作分发 ───────────────────────────────────────────────────────── */

  let streamTimer = null;

  function handleAction(name, el) {
    switch (name) {
      /* 复制类：原型只提示，不访问剪贴板 */
      case 'copy-url': case 'copy-key': case 'copy-endpoint': case 'copy-uid':
      case 'copy-curl': case 'copy-model':
        toast('原型演示：真实应用中此操作由原生剪贴板完成'); break;

      /* 网关控制 */
      case 'gw-start':
        sc().gateway = 'starting'; render();
        setTimeout(() => { sc().gateway = sc().accounts.some((a) => a.useState === 'ready') ? 'running_ready' : 'running_unavailable'; render(); toast('API 服务已启动'); }, 1400);
        break;
      case 'gw-stop':
        sc().gateway = 'stopping'; render();
        setTimeout(() => { sc().gateway = 'stopped'; render(); toast('API 服务已停止'); }, 900);
        break;
      case 'gw-restart':
        sc().gateway = 'stopping'; render();
        setTimeout(() => { sc().gateway = 'starting'; render(); }, 800);
        setTimeout(() => { sc().gateway = 'running_ready'; render(); toast('API 服务已重启（耗时 2.4 秒）'); }, 2000);
        break;

      /* 账号 */
      case 'add-account': state.addFlow = 'idle'; go('add-account'); break;
      case 'import-auth': go('import-auth'); break;
      case 'add-start':
        state.addFlow = 'opening'; render();
        setTimeout(() => { state.addFlow = 'waiting'; render(); }, 900);
        setTimeout(() => { state.addFlow = 'saving'; render(); }, 3200);
        setTimeout(() => { state.addFlow = 'loading'; render(); }, 4400);
        setTimeout(() => { state.addFlow = 'success'; render(); }, 5800);
        break;
      case 'add-reopen': toast('已复用当前授权会话，重新打开浏览器'); break;
      case 'add-cancel': state.addFlow = 'cancelled'; render(); break;
      case 'add-reset': state.addFlow = 'idle'; render(); break;
      case 'pick-file': toast('原型演示：真实应用中会打开系统文件选择器'); break;

      case 'reauth': case 'refresh': case 'credits':
        state.modal = {
          title: name === 'delete' ? '删除本机账号' : '将进入账号维护',
          body: name === 'reauth'
            ? '<p>重新授权会替换该账号的凭证，并保留本地显示名等非凭证信息。</p>'
            : name === 'refresh'
              ? '<p>刷新凭证需要写入凭证文件。为避免与网关并发写入冲突，<strong>API 服务会暂停</strong>，完成后自动恢复。</p>'
              : '<p>刷新余额会向上游发起一次查询，并写入最新凭证。<strong>API 服务会暂停</strong>，完成后自动恢复。</p>',
          confirmLabel: '开始维护', onConfirm: () => toast('已进入维护模式，完成后按原状态恢复服务'),
        };
        render(); break;

      case 'toggle-pause':
        state.modal = {
          title: '暂停对话使用',
          body: '<p>暂停后该账号不再参与聊天选号，但<strong>不等于</strong>撤销上游授权，也不影响凭证刷新等其他维护动作。</p>',
          confirmLabel: '暂停使用', onConfirm: () => toast('已暂停该账号的对话使用'),
        };
        render(); break;

      case 'delete':
        state.modal = {
          title: '删除本机账号', danger: true, confirmLabel: '删除',
          body: '<p>将删除本机的凭证文件及对应引用。<strong>这不等于注销上游账号。</strong></p>' +
                '<p>删除后不会保留可自动恢复的令牌备份。删除失败会显示原因，不会先从列表里假装删除。</p>',
          onConfirm: () => toast('已删除（原型演示，未真正改动数据）', 'warn'),
        };
        render(); break;

      case 'clear-selection': state.selected.clear(); render(); break;
      case 'batch-refresh': case 'batch-credits':
        state.modal = {
          title: '批量维护 ' + state.selected.size + ' 个账号',
          body: `<p>将对已选择的 ${state.selected.size} 个账号<strong>逐个串行</strong>执行，并逐项显示结果。</p>
                 <p>期间 API 服务会暂停，完成后按操作前的状态恢复。某一个失败不会影响其余账号，也不会把整体报告为全部失败。</p>`,
          confirmLabel: '开始', onConfirm: () => toast('已开始批量维护（原型演示）'),
        };
        render(); break;

      /* 对话 */
      case 'chat-send': {
        if (state.chatStreaming) break;
        const input = $('#chat-input');
        const text = (state.chatDraft || (input ? input.value : '') || '你好，请用一句话介绍你自己。').trim();
        if (!text) break;
        state.chatDraft = '';
        state.chatError = null;
        state.chat.push({ role: 'user', text });
        const reply = { role: 'assistant', text: '', streaming: true, meta: null };
        state.chat.push(reply);
        state.chatStreaming = true;
        render();

        const full = '你好，这是一条原型演示回复，用于展示流式输出的追加效果与首 Token 延迟显示。' +
                     '真实应用里，这里的内容来自你选择的上游模型，并且只按纯文本渲染，不会执行返回的 HTML 或脚本。';
        let i = 0;
        clearInterval(streamTimer);
        streamTimer = setInterval(() => {
          i += 3;
          reply.text = full.slice(0, i);
          if (i >= full.length) {
            clearInterval(streamTimer);
            reply.streaming = false;
            reply.meta = ['首 Token 940 ms', '总耗时 2.4 s', '输出 68 tokens'];
            state.chatStreaming = false;
          }
          render();
          const box = $('#view');
          if (box && !state.userScrolledUp) box.scrollTop = box.scrollHeight;
        }, 60);
        break;
      }
      case 'chat-stop':
        clearInterval(streamTimer);
        state.chatStreaming = false;
        state.chat.forEach((m) => { if (m.streaming) { m.streaming = false; m.stopped = true; } });
        render(); toast('已停止生成，已接收内容保留', 'warn');
        break;
      case 'chat-clear': state.chat = []; state.chatError = null; render(); break;
      case 'chat-params': toast('原型演示：参数区可折叠，含温度、最大输出等'); break;
      case 'chat-retry':
        state.chatError = null;
        handleAction('chat-send');
        break;

      /* 设置 */
      case 'config-save':
        state.modal = {
          title: '保存并应用配置',
          wide: true, confirmLabel: '保存并应用',
          body: `<p>以下变更会停止并重启 API 服务：</p>
                 <div class="summary-grid" style="margin-top:8px">
                   <div class="summary-grid__row"><span class="summary-grid__key">API 端口</span><span class="summary-grid__val">7863 → 7863（未变）</span></div>
                   <div class="summary-grid__row"><span class="summary-grid__key">请求超时</span><span class="summary-grid__val">120 → 180 秒</span></div>
                   <div class="summary-grid__row"><span class="summary-grid__key">账号并发上限</span><span class="summary-grid__val">3 → 4</span></div>
                 </div>
                 <p style="margin-top:10px">当前有 <strong>2 个进行中的请求</strong>，重启会中断它们。应用会先备份当前配置，失败时自动回滚。</p>`,
          onConfirm: () => {
            state.configDirty = false;
            state.configResult = 'ok';
            toast('配置已保存并生效');
          },
        };
        render(); break;
      case 'config-discard': state.configDirty = false; state.configResult = null; render(); toast('已放弃修改'); break;
      case 'config-retry': state.configResult = null; render(); break;
      case 'toggle-advanced': toast('原型演示：高级区只读摘要，受限字段可编辑'); break;

      /* 统计 */
      case 'stats-reset':
        state.modal = {
          title: '重置本次统计', danger: true, confirmLabel: '重置',
          body: `<p>将清空本次运行的请求数、Token 与延迟累计。</p>
                 <p><strong>不会</strong>删除账号、不会影响上游账单或额度，也不会清空日志。</p>`,
          onConfirm: () => toast('本次统计已重置（原型演示）'),
        };
        render(); break;

      /* 日志与诊断 */
      case 'log-pause': state.logPaused = !state.logPaused; render(); break;
      case 'log-copy': toast('原型演示：复制的是脱敏后的内容'); break;
      case 'diag-export':
        state.modal = {
          title: '导出诊断包', wide: true, confirmLabel: '选择位置并导出',
          body: `<p>将包含：应用 / 网关 / 面板版本、系统与 CPU 架构、脱敏配置摘要、状态与错误码、最近日志。</p>
                 <p><strong>不包含</strong>账号凭证、访问令牌、完整 API 密钥、Cookie、授权链接参数与聊天正文。
                 个人目录路径在导出时脱敏。</p>
                 <p>导出后使用系统保存面板选择位置，不会自动上传到任何第三方。</p>`,
          onConfirm: () => toast('诊断包已导出（原型演示）'),
        };
        render(); break;

      /* 其他 */
      case 'reconnect': sc().sessionExpired = false; sc().problem = null; render(); toast('已重新建立内部管理连接'); break;
      case 'cancelMaintenance': sc().gateway = 'running_ready'; sc().problem = null; render(); toast('已取消维护，服务已恢复'); break;
      case 'open-data-dir': case 'open-log-dir':
        toast('原型演示：真实应用中由原生层打开 Finder'); break;
      case 'clean-logs': toast('已清理滚动日志（原型演示）'); break;
      case 'clean-data':
        state.modal = {
          title: '清除账号与配置', danger: true, confirmLabel: '我确认清除',
          body: `<p>将删除本应用的账号凭证、网关配置与备份。</p>
                 <p>这<strong>不会</strong>注销你的上游账号，也不影响其他应用的数据。</p>
                 <p>建议先导出诊断包或备份配置。</p>`,
          onConfirm: () => toast('已清除（原型演示，未真正改动数据）', 'warn'),
        };
        render(); break;

      /* 向导 */
      case 'wizard-next': state.wizardStep = Math.min(4, state.wizardStep + 1); render(); break;
      case 'wizard-prev': state.wizardStep = Math.max(0, state.wizardStep - 1); render(); break;
      case 'wizard-skip-step': state.wizardStep = 3; render(); break;
      case 'wizard-add': toast('原型演示：真实应用中会打开系统浏览器完成授权'); break;
      case 'wizard-test': toast('原型演示：真实应用中会发起一次上游测试对话'); break;
      case 'wizard-skip': case 'wizard-finish': state.scenario = 'normal'; state.wizardStep = 0; go('overview'); toast('向导已完成'); break;

      case 'check-local': toast('本地连通性检查通过：127.0.0.1:7863 响应正常'); break;
      case 'check-chat': toast('已发起一次真实测试对话（原型演示）'); break;
      case 'refresh-models': toast('已从实际接口刷新模型列表'); break;
      case 'rotate-key':
        state.modal = {
          title: '轮换 API 密钥', danger: true, confirmLabel: '轮换密钥',
          body: `<p>将生成新的本地 API 密钥。</p>
                 <p>轮换成功后，<strong>旧密钥对新请求立即失效</strong>，所有使用旧密钥的客户端都需要更新。
                 若轮换失败，应用会回滚，旧密钥继续有效。</p>`,
          onConfirm: () => toast('密钥已轮换（原型演示）'),
        };
        render(); break;

      default: toast('原型演示：' + name);
    }
  }

  /* ── 初始化 ─────────────────────────────────────────────────────────── */

  function init() {
    // 原型工具条
    const bar = document.createElement('div');
    bar.className = 'proto-bar';
    bar.setAttribute('role', 'group');
    bar.setAttribute('aria-label', '原型演示控制');
    bar.innerHTML = `
      <span class="proto-bar__label">原型演示</span>
      <span class="proto-bar__sep" aria-hidden="true"></span>
      <label class="proto-bar__label" for="proto-scenario">场景</label>
      <select id="proto-scenario" aria-label="切换演示场景">
        ${Object.entries(SCENARIOS).map(([k, v]) => `<option value="${k}">${esc(v.label)}</option>`).join('')}
      </select>
      <span class="proto-bar__sep" aria-hidden="true"></span>
      <span class="proto-bar__label">主题</span>
      <span class="segmented" id="proto-theme" role="group" aria-label="切换主题">
        <button data-theme-set="system">跟随系统</button>
        <button data-theme-set="light">浅色</button>
        <button data-theme-set="dark">深色</button>
      </span>
      <span class="proto-bar__sep" aria-hidden="true"></span>
      <span class="proto-bar__note">页面内全部数据为模拟数据</span>`;
    document.body.appendChild(bar);

    $('#proto-scenario').addEventListener('change', (e) => {
      state.scenario = e.target.value;
      state.selected.clear();
      state.drawer = null; state.modal = null; state.openMenu = null;
      state.configDirty = false; state.configResult = null;
      state.chat = []; state.chatError = null; state.chatStreaming = false;
      state.wizardStep = 0;
      resetScenarios();
      // 切换场景后回到该场景的入口页，保证演示者一定看到场景的核心状态
      state.route = SCENARIOS[state.scenario].wizard ? 'wizard' : 'overview';
      render();
    });

    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (state.theme === 'system') render();
    });

    // 首次进入：默认场景为首次使用，直接展示向导
    state.route = 'wizard';
    render();
  }

  document.addEventListener('DOMContentLoaded', init);
  if (document.readyState !== 'loading') init();
})();
