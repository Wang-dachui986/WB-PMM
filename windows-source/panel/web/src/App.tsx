import { desktopCommand, hasDesktopBridge, reconnectDesktop, subscribeDesktopGatewayStatus } from './desktopBridge'
// App.tsx 应用外壳：登录门禁 + 侧边导航 + 路由。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { NavLink, Navigate, Route, Routes, useNavigate, useLocation } from 'react-router-dom'
import { api, setUnauthorizedHandler } from './api'
import type { SessionInfo } from './types'
import { Alert, Badge, gatewayStatusBadge, Spinner } from './ui'
import Login from './pages/Login'
import GuidePopover from './GuidePopover'
import Dashboard from './pages/Dashboard'
import StatsPage from './pages/StatsPage'
import TokenPage from './pages/TokenPage'
import Accounts from './pages/Accounts'
import LoginWizard from './pages/LoginWizard'
import Playground from './pages/Playground'
import ConfigPage from './pages/ConfigPage'
import ApiModelsPage from './pages/ApiModelsPage'
import System from './pages/System'
import LogsPage from './pages/LogsPage'
import SettingsPage, { type Preferences } from './pages/SettingsPage'

/**
 * 侧栏图标：一套自绘的 24×24 线性图标（stroke 1.6、currentColor、无填充）。
 *
 * 原实现用 emoji 当图标。emoji 来自不同字族，字重、视觉尺寸、基线各不相同，
 * 放在侧栏里参差不齐；而且彩色 emoji 会跟系统强调色的选中态打架。
 * 桌面应用侧栏采用统一的单色线性符号，这里照此对齐。
 */
const ICONS: Record<string, ReactNode> = {
  // 仪表盘：速度表
  dashboard: (
    <>
      <path d="M4 17.5a9 9 0 1 1 16 0" />
      <path d="m12 13.5 4.2-4.7" />
      <circle cx="12" cy="13.5" r="1.3" />
    </>
  ),
  // 账号管理：双人
  accounts: (
    <>
      <circle cx="9.2" cy="8" r="3.4" />
      <path d="M2.8 19.6c0-3.4 2.9-5.6 6.4-5.6s6.4 2.2 6.4 5.6" />
      <path d="M16.4 5.1a3.2 3.2 0 0 1 0 6.3" />
      <path d="M18.1 14.4c2.1.7 3.3 2.4 3.3 4.6" />
    </>
  ),
  // 请求统计：柱状图
  stats: (
    <>
      <path d="M4 20.5V11" />
      <path d="M10 20.5V4" />
      <path d="M16 20.5v-6.6" />
      <path d="M2 20.5h20" />
    </>
  ),
  // 添加账号：圆形加号
  add: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 8.2v7.6" />
      <path d="M8.2 12h7.6" />
    </>
  ),
  // 聊天测试：对话气泡
  chat: (
    <>
      <path d="M20.5 14.2a2.6 2.6 0 0 1-2.6 2.6H8.4L4 20.4V6.1a2.6 2.6 0 0 1 2.6-2.6h11.3a2.6 2.6 0 0 1 2.6 2.6z" />
      <path d="M8.2 8.6h8" />
      <path d="M8.2 12.2h5" />
    </>
  ),
  // 网关配置：滑块
  config: (
    <>
      <path d="M4 8.2h9.2" />
      <path d="M17.4 8.2H20" />
      <path d="M4 15.8h2.6" />
      <path d="M10.8 15.8H20" />
      <circle cx="15.2" cy="8.2" r="2.2" />
      <circle cx="8.6" cy="15.8" r="2.2" />
    </>
  ),
  // 系统：齿轮
  system: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5v.2a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3h.1a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8v.1a1.7 1.7 0 0 0 1.5 1h.2a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" />
    </>
  ),
}

type NavItem = { to: string; label: string; icon: string; end?: boolean; desktop?: boolean }

const NAV_GROUPS: { id: string; title: string; items: NavItem[] }[] = [
  {
    id: 'workspace', title: '工作区', items: [
      { to: '/', label: '仪表盘', icon: 'dashboard', end: true },
      { to: '/accounts', label: '账号管理', icon: 'accounts' },
      { to: '/login', label: '添加账号', icon: 'add' },
      { to: '/playground', label: '聊天测试', icon: 'chat' },
      { to: '/stats', label: '请求统计', icon: 'stats' },
      { to: '/tokens', label: 'Token 统计', icon: 'stats' },
    ],
  },
  {
    id: 'service', title: '服务', items: [
      { to: '/config', label: '网关配置', icon: 'config' },
      { to: '/api-models', label: 'API 与模型', icon: 'config', desktop: true },
    ],
  },
  {
    id: 'application', title: '应用', items: [
      { to: '/system', label: '系统', icon: 'system' },
      { to: '/logs', label: '日志与诊断', icon: 'stats', desktop: true },
      { to: '/settings', label: '应用设置', icon: 'system', desktop: true },
    ],
  },
]

const isGatewayRunning = (status: Record<string, unknown>) =>
  status.gateway_state === 'running_ready' || status.gateway_state === 'running_unavailable'

export default function App() {
  const [session, setSession] = useState<SessionInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [banner, setBanner] = useState<string | null>(null)
  const navigate = useNavigate()

  const refreshSession = useCallback(async () => {
    try {
      let s = await api.session()
      if (!s.authenticated && hasDesktopBridge()) {
        await reconnectDesktop()
        s = await api.session()
      }
      setSession(s)
      return s
    } catch {
      // 会话接口失败（后端未起来）也要结束 loading，否则页面永久空白。
      setSession(null)
      return null
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refreshSession()
  }, [refreshSession])

  // 任何接口返回 401 时统一回到登录页，避免用户在坏会话里反复点。
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setSession((prev) => (prev ? { ...prev, authenticated: false } : prev))
      setBanner('会话已过期，请重新登录')
      navigate('/')
    })
  }, [navigate])

  const handleLoggedOut = useCallback(async () => {
    await api.logout().catch(() => undefined)
    setSession((s) => (s ? { ...s, authenticated: false, username: '' } : s))
  }, [])

  if (loading) {
    return (
      <div className="login-wrap">
        <Spinner label="正在连接服务…" />
      </div>
    )
  }

  if (!session?.authenticated) {
    return <Login info={session} banner={banner} onCloseBanner={() => setBanner(null)} onSuccess={refreshSession} />
  }

  return (
    <Shell session={session} onLogout={handleLoggedOut} onSessionRefresh={refreshSession} />
  )
}

function Shell({
  session,
  onLogout,
  onSessionRefresh,
}: {
  session: SessionInfo
  onLogout: () => void
  onSessionRefresh: () => Promise<SessionInfo | null>
}) {
  const [preferences, setPreferences] = useState<Preferences | null>(null)
  const [preferenceError, setPreferenceError] = useState<string | null>(null)
  const [firstRunBusy, setFirstRunBusy] = useState(false)
  const [firstRunAutoStart, setFirstRunAutoStart] = useState(false)
  const [firstRunStep, setFirstRunStep] = useState(0)
  const [guideReplay, setGuideReplay] = useState(false)
  const [readiness, setReadiness] = useState<Record<string, unknown> | null>(null)
  const [nativeStatus, setNativeStatus] = useState<Record<string, unknown>>({})
  const [firstRunNotice, setFirstRunNotice] = useState<string | null>(null)
  const firstRunActive = useRef(true)
  const firstRunFlight = useRef(false)
  const guideOpen = !!preferences && (!preferences.hasCompletedFirstRun || guideReplay)
  const location = useLocation()
  useEffect(() => { firstRunActive.current = true; return () => { firstRunActive.current = false } }, [])
  useEffect(() => {
    if (!hasDesktopBridge()) return
    let active = true
    let busy = false
    let pending = false
    const read = async () => {
      if (!active) return
      if (busy || document.hidden) { pending = true; return }
      busy = true; pending = false
      try {
        const value = await desktopCommand('desktop.getStatus')
        if (active) setNativeStatus((value.status ?? {}) as Record<string, unknown>)
      } catch { /* The action itself rechecks status before continuing. */ }
      finally { busy = false; if (active && pending && !document.hidden) void read() }
    }
    const unsubscribe = subscribeDesktopGatewayStatus(() => void read())
    const visible = () => { if (pending && !document.hidden) void read() }
    document.addEventListener('visibilitychange', visible)
    void read()
    return () => { active = false; unsubscribe(); document.removeEventListener('visibilitychange', visible) }
  }, [location.pathname])
  const navigate = useNavigate()
  const loadPreferences = useCallback(async () => {
    if (!hasDesktopBridge()) return
    try {
      const value = await desktopCommand('preferences.get') as unknown as Preferences
      setPreferences(value); setFirstRunAutoStart(value.autoStartGatewayOnLaunch); setPreferenceError(null)
    } catch { setPreferenceError('无法读取应用设置，请重试') }
  }, [])
  useEffect(() => { void loadPreferences() }, [loadPreferences])
  useEffect(() => { if (location.pathname === '/settings') void loadPreferences() }, [location.pathname, loadPreferences])
  useEffect(() => {
    const theme = preferences?.theme ?? 'system'
    document.documentElement.dataset.theme = theme
    document.documentElement.style.colorScheme = theme === 'system' ? 'light dark' : theme
  }, [preferences?.theme])
  const guideStep = (step: number) => {
    setFirstRunStep(step)
    if (step === 0 || step === 3) navigate('/')
    if (step === 2) navigate('/login')
    if (step >= 4) navigate('/api-models')
  }
  const startGuide = () => {
    setGuideReplay(true); setReadiness(null); setFirstRunNotice(null); setPreferenceError(null)
    setFirstRunAutoStart(preferences?.autoStartGatewayOnLaunch ?? false)
    guideStep(0)
  }
  const requireRunningGateway = async () => {
    const value = await desktopCommand('desktop.getStatus')
    const status = (value.status ?? {}) as Record<string, unknown>
    if (firstRunActive.current) setNativeStatus(status)
    if (!isGatewayRunning(status)) throw new Error('API 服务未运行，请先启动后再继续')
  }
  const finishFirstRun = async (skipped: boolean) => {
    if (firstRunFlight.current) return
    firstRunFlight.current = true; setFirstRunBusy(true); setPreferenceError(null)
    try {
      if (!skipped) await requireRunningGateway()
      const value = await desktopCommand('preferences.update', { hasCompletedFirstRun: true,
        ...(!skipped ? { autoStartGatewayOnLaunch: firstRunAutoStart } : {}) }) as unknown as Preferences
      if (!firstRunActive.current) return
      setPreferences(value); setGuideReplay(false)
      setFirstRunNotice(skipped ? '已跳过使用引导；本机准备、账号及聊天测试未全部验证。可从仪表盘右上角重新打开。' : '首次设置已完成。使用引导已收起至仪表盘右上角；聊天与上游可用性尚未测试。')
      if (!skipped) navigate('/')
    } catch (error) { if (firstRunActive.current) setPreferenceError(error instanceof Error ? error.message : '首次设置未保存，请重试') }
    finally { firstRunFlight.current = false; if (firstRunActive.current) setFirstRunBusy(false) }
  }
  const continueFirstRun = async () => {
    if (firstRunFlight.current) return
    firstRunFlight.current = true; setFirstRunBusy(true); setPreferenceError(null)
    try {
      await requireRunningGateway()
      if (firstRunActive.current) guideStep(4)
    } catch (error) { if (firstRunActive.current) setPreferenceError(error instanceof Error ? error.message : '无法读取 API 状态') }
    finally { firstRunFlight.current = false; if (firstRunActive.current) setFirstRunBusy(false) }
  }
  const firstRunCheck = async (start = false, advance = false) => {
    if (firstRunFlight.current) return
    firstRunFlight.current = true; setFirstRunBusy(true); setPreferenceError(null)
    try {
      if (start) {
        await desktopCommand('gateway.start')
        const value = await desktopCommand('desktop.getStatus')
        if (firstRunActive.current) setNativeStatus((value.status ?? {}) as Record<string, unknown>)
      } else {
        if (firstRunActive.current) setReadiness(null)
        const value = await desktopCommand('desktop.checkReadiness')
        if (firstRunActive.current) {
          setReadiness(value)
          if (advance && value.ready === true) guideStep(2)
        }
      }
    } catch (error) { if (firstRunActive.current) setPreferenceError(error instanceof Error ? error.message : '本机操作未完成') }
    finally { firstRunFlight.current = false; if (firstRunActive.current) setFirstRunBusy(false) }
  }
  const firstRunAccountDone = async () => {
    const value = await onSessionRefresh()
    if (firstRunActive.current && guideOpen && firstRunStep === 2) guideStep(3)
    return value
  }
  const warnings = useMemo(() => {
    const list: string[] = []
    if (session.using_default_password) {
      list.push('面板正在使用默认口令（admin / workbuddy），建议在服务端配置中修改 ui.password')
    }
    if (session.read_only) {
      list.push('服务端处于只读模式：登录、签到、改配置等写操作全部禁用')
    }
    return list
  }, [session])
  const firstRunGatewayStatus = gatewayStatusBadge(nativeStatus.gateway_state)

  return (
    <div className="app">
      <a className="skip-link" href="#main-content">跳过导航，前往主内容</a>
      <aside className="sidebar">
        <div className="brand">
          <img className="brand-dot" src="/brand/wb-pmm.png" alt="" />
          <div className="brand-text">
            <strong>WB-PMM控制台</strong>
            <span>WBPMM网关</span>
          </div>
        </div>
        <nav className="sidebar-nav" aria-label="主导航">
          {NAV_GROUPS.map((group) => (
            <section className="nav-group" aria-labelledby={`nav-group-${group.id}`} key={group.id}>
              <h2 className="nav-group-title" id={`nav-group-${group.id}`}>{group.title}</h2>
              {group.items.filter((item) => !item.desktop || hasDesktopBridge()).map((item) => (
                <NavLink
                  key={item.to}
                  data-guide-nav={item.to}
                  to={item.to}
                  end={item.end}
                  className={({ isActive }) => `nav-item ${isActive ? 'active' : ''}`}
                >
                  <span className="nav-icon" aria-hidden="true">
                    <svg
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth={1.6}
                      strokeLinecap="round"
                      strokeLinejoin="round"
                    >
                      {ICONS[item.icon]}
                    </svg>
                  </span>
                  {item.label}
                </NavLink>
              ))}
            </section>
          ))}
        </nav>
        <div className="nav-spacer" />
        <div className="nav-foot">
          <div style={{ marginBottom: 8, wordBreak: 'break-all' }}>网关：{hasDesktopBridge() ? String(nativeStatus.api_base_url ?? '等待本机状态') : session.gateway_url}</div>
          <div style={{ marginBottom: 8 }}>
            <span className="text-faint">登录身份：</span>
            {session.username}
            {session.read_only && <span className="badge badge-warn" style={{ marginLeft: 6 }}>只读</span>}
          </div>
          <button className="btn btn-sm" style={{ width: '100%' }} onClick={onLogout}>
            退出登录
          </button>
        </div>
      </aside>

      <main className="main" id="main-content" tabIndex={-1}>
        {preferenceError && <Alert kind="error">{preferenceError} <button className="btn btn-sm" onClick={() => void loadPreferences()}>重试</button></Alert>}
        {firstRunNotice && <Alert kind="info" onClose={() => setFirstRunNotice(null)}>{firstRunNotice}</Alert>}
        {warnings.map((w) => (
          <Alert key={w} kind="warn">
            {w}
          </Alert>
        ))}
        <Routes>
          <Route path="/" element={<Dashboard onStartGuide={preferences ? startGuide : undefined} guideOpen={guideOpen} />} />
          <Route path="/accounts" element={<Accounts session={session} />} />
          <Route path="/tokens" element={<TokenPage />} />
          <Route path="/stats" element={<StatsPage session={session} />} />
          <Route path="/login" element={<LoginWizard session={session} onDone={firstRunAccountDone} />} />
          <Route path="/playground" element={<Playground />} />
          <Route path="/config" element={<ConfigPage session={session} />} />
          <Route path="/api-models" element={hasDesktopBridge() ? <ApiModelsPage /> : <Navigate to="/" replace />} />
          <Route path="/system" element={<System session={session} onSessionRefresh={onSessionRefresh} />} />
          <Route path="/logs" element={<LogsPage />} />
          <Route path="/settings" element={<SettingsPage preferences={preferences} onSaved={setPreferences} />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
      {guideOpen && <GuidePopover
        step={firstRunStep} title={['欢迎使用 WB-PMM', '先检查本机准备', '添加一个账号', '启动 API 服务', '复制客户端地址', '复制 API 密钥', '选择模型，完成接入'][firstRunStep]}
        targets={[
          ['[data-guide="entry"]', '[data-guide-nav="/"]'],
          ['[data-guide-nav="/config"]'],
          ['[data-guide="login-action"]', '[data-guide-nav="/login"]'],
          ['[data-guide="gateway"]', '[data-guide-nav="/"]'],
          ['[data-guide="api-address"]', '[data-guide-nav="/api-models"]'],
          ['[data-guide="api-key"]', '[data-guide-nav="/api-models"]'],
          ['#api-model', '[data-guide-nav="/playground"]'],
        ][firstRunStep]}
        busy={firstRunBusy} onSkip={() => void finishFirstRun(true)}>
        {preferenceError && <Alert kind="error">{preferenceError}</Alert>}
        {firstRunStep === 0 && <><p>跟着高亮位置完成账号与 API 接入。引导不会自动发送聊天请求，任何一步都可以跳过。</p>
          <button className="btn btn-primary" onClick={() => guideStep(1)}>开始设置</button></>}
        {firstRunStep === 1 && <><p>先检查内置程序、数据目录和 API 端口。需要修改端口时，点击高亮的“网关配置”。</p>
          {readiness && <><p><Badge cls={readiness.ready ? 'badge-ok' : 'badge-warn'}>{readiness.ready ? '本机准备已通过' : '本机准备未通过'}</Badge></p>
            <p className="hint">端口 {String(readiness.port ?? '未知')}：{({ free: '可用', owned: '当前应用正在使用', occupied: '其他程序占用', unavailable: '系统检查失败' } as Record<string, string>)[String(readiness.port_result)] ?? '未检查'}</p>
            {!readiness.ready && <ul>{((readiness.checks ?? []) as { check: string; result: string }[]).filter(item => item.result !== 'ok').map(item => <li key={item.check}>{item.check}：{item.result}</li>)}
              {readiness.config_result !== 'ok' && <li>配置检查：{String(readiness.config_result)}</li>}</ul>}</>}
          <div className="page-actions"><button className="btn" disabled={firstRunBusy} onClick={() => void firstRunCheck()}>检查本机准备</button>
            <button className="btn btn-primary" disabled={firstRunBusy || readiness?.ready !== true} onClick={() => void firstRunCheck(false, true)}>下一步</button></div>
          <button className="btn btn-ghost btn-sm" onClick={() => navigate('/config')}>打开网关设置修改端口</button></>}
        {firstRunStep === 2 && <><p>选择账号区域后点击“发起授权”，再打开授权页面。浮层会跟随当前授权按钮；看到“凭证已保存”后自动进入下一步。</p>
          <p className="hint">已有账号可以直接继续。没有可用账号时，API 虽可启动，但不能调用模型。</p>
          <div className="page-actions"><button className="btn" onClick={() => navigate('/login')}>添加账号</button>
            <button className="btn btn-primary" onClick={() => guideStep(3)}>跳过账号，继续</button></div></>}
        {firstRunStep === 3 && <><p>这里显示 API 地址与运行状态。点击下方按钮启动，也可使用窗口顶部的“启动”。</p>
          <p><Badge cls={firstRunGatewayStatus.cls}>{firstRunGatewayStatus.text}</Badge></p>
          <p className="hint">{nativeStatus.gateway_state === 'running_unavailable' ? '进程正在运行，但当前没有可用账号；上游尚未测试。' : '进程启动成功不等于上游可用。'}</p>
          <div className="page-actions"><button className="btn btn-primary" disabled={firstRunBusy || isGatewayRunning(nativeStatus)} onClick={() => void firstRunCheck(true)}>启动 API 服务</button>
            <button className="btn" disabled={firstRunBusy || !isGatewayRunning(nativeStatus)} onClick={() => void continueFirstRun()}>继续接入说明</button></div></>}
        {firstRunStep === 4 && <><p>点击高亮按钮复制 Base URL，填入客户端。WorkBuddy 开启“自定义协议”时，请改用本页的“完整对话端点”。</p>
          <p className="hint">地址填写规则取决于客户端协议，不取决于账号区域。</p>
          <button className="btn btn-primary" onClick={() => guideStep(5)}>下一步</button></>}
        {firstRunStep === 5 && <><p>点击“复制密钥”，粘贴到客户端的 API Key 一栏。不要使用账号登录密码，也不需要点击“轮换密钥”。</p>
          <p className="hint">密钥通过原生剪贴板复制，不会显示在页面或教程里。</p>
          <button className="btn btn-primary" onClick={() => guideStep(6)}>下一步</button></>}
        {firstRunStep === 6 && <><p>选择模型并复制完整模型 ID，保留 cn: 或 global: 前缀。填好地址、密钥与模型后，就可以在客户端使用。</p>
          <p className="hint">“聊天测试”中的真实请求可能消耗积分，需要你手动发送。</p>
          <label className="checkbox"><input type="checkbox" checked={firstRunAutoStart} onChange={event => setFirstRunAutoStart(event.target.checked)} />下次打开应用时启动 API</label>
          <p className="hint">登录 Windows 后自动启动可在“应用设置”单独开启。</p>
          <div className="page-actions"><button className="btn" onClick={() => navigate('/playground')}>前往手动聊天测试</button>
            <button className="btn btn-primary" disabled={firstRunBusy} onClick={() => void finishFirstRun(false)}>完成首次设置</button></div></>}
        <div className="guide-footer">{firstRunStep > 0 && <button className="btn btn-ghost btn-sm" disabled={firstRunBusy} onClick={() => guideStep(firstRunStep - 1)}>上一步</button>}
          {firstRunBusy && <Spinner label="正在处理…" />}</div>
      </GuidePopover>}
    </div>
  )
}
