import { desktopCommand, hasDesktopBridge, reconnectDesktop } from './desktopBridge'
// App.tsx 应用外壳：登录门禁 + 侧边导航 + 路由。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { NavLink, Navigate, Route, Routes, useNavigate, useLocation } from 'react-router-dom'
import { api, setUnauthorizedHandler } from './api'
import type { SessionInfo } from './types'
import { Alert, Badge, gatewayStatusBadge, Spinner } from './ui'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import StatsPage from './pages/StatsPage'
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
 * macOS 系统应用的侧栏一律是统一的单色线性符号，这里照此对齐。
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
  const [readiness, setReadiness] = useState<Record<string, unknown> | null>(null)
  const [nativeStatus, setNativeStatus] = useState<Record<string, unknown>>({})
  const [firstRunNotice, setFirstRunNotice] = useState<string | null>(null)
  const firstRunActive = useRef(true)
  const firstRunFlight = useRef(false)
  const location = useLocation()
  useEffect(() => { firstRunActive.current = true; return () => { firstRunActive.current = false } }, [])
  useEffect(() => {
    if (!hasDesktopBridge()) return
    let active = true
    void desktopCommand('desktop.getStatus').then(value => { if (active) setNativeStatus((value.status ?? {}) as Record<string, unknown>) }).catch(() => {})
    return () => { active = false }
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
        autoStartGatewayOnLaunch: firstRunAutoStart }) as unknown as Preferences
      if (!firstRunActive.current) return
      setPreferences(value)
      setFirstRunNotice(skipped ? '已跳过剩余首次设置；本机准备、账号及聊天测试未全部验证，可随时从侧栏继续。' : '首次设置已完成。聊天与上游可用性尚未测试，请按需手动测试。')
    } catch (error) { if (firstRunActive.current) setPreferenceError(error instanceof Error ? error.message : '首次设置未保存，请重试') }
    finally { firstRunFlight.current = false; if (firstRunActive.current) setFirstRunBusy(false) }
  }
  const continueFirstRun = async () => {
    if (firstRunFlight.current) return
    firstRunFlight.current = true; setFirstRunBusy(true); setPreferenceError(null)
    try {
      await requireRunningGateway()
      if (firstRunActive.current) setFirstRunStep(4)
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
          if (advance && value.ready === true) setFirstRunStep(2)
        }
      }
    } catch (error) { if (firstRunActive.current) setPreferenceError(error instanceof Error ? error.message : '本机操作未完成') }
    finally { firstRunFlight.current = false; if (firstRunActive.current) setFirstRunBusy(false) }
  }
  const firstRunAccountDone = async () => {
    const value = await onSessionRefresh()
    if (firstRunActive.current && firstRunStep === 2) setFirstRunStep(3)
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
        {preferences && !preferences.hasCompletedFirstRun && <div className="card">
          <div className="card-head"><h2>首次使用 · {firstRunStep + 1}/5</h2><span className="hint">{['欢迎', '本机准备', '添加账号', '启动服务', '客户端接入'][firstRunStep]}</span></div>
          {firstRunStep === 0 && <><p>本应用在本机运行管理面板和 API 网关。以下设置不会自动发送模型请求。</p><button className="btn btn-primary" onClick={() => setFirstRunStep(1)}>开始设置</button></>}
          {firstRunStep === 1 && <><p>检查内置程序、私有目录和已提交的 API 端口。</p>
            {readiness && <><p>检查时间：{String(readiness.checked_at)}；端口 {String(readiness.port ?? '未知')}：{({ free: '可用', owned: '当前应用正在使用', occupied: '其他程序占用', unavailable: '系统检查失败' } as Record<string, string>)[String(readiness.port_result)] ?? '未检查'}</p>
              <p>{readiness.ready ? '本机准备已通过' : '本机准备未通过，请调整配置或重试'}</p>
              <ul>{((readiness.checks ?? []) as { check: string; result: string }[]).map(item => <li key={item.check}>{item.check}：{item.result === 'ok' ? '已通过' : item.result}</li>)}</ul>
              {readiness.config_result !== 'ok' && <p>配置检查：{String(readiness.config_result)}</p>}</>}
            <div className="page-actions"><button className="btn" disabled={firstRunBusy} onClick={() => void firstRunCheck()}>检查本机准备</button><button className="btn" onClick={() => navigate('/config')}>打开网关设置修改端口</button>
              <button className="btn btn-primary" disabled={firstRunBusy || readiness?.ready !== true} onClick={() => void firstRunCheck(false, true)}>下一步</button></div></>}
          {firstRunStep === 2 && <><p>可以添加账号，或先完成本机设置。跳过账号后 API 进程可运行，但没有可用账号时不能调用模型。</p><div className="page-actions"><button className="btn btn-primary" onClick={() => navigate('/login')}>添加账号</button><button className="btn" onClick={() => setFirstRunStep(3)}>跳过账号，继续</button></div></>}
          {firstRunStep === 3 && <><p>当前 API 状态：<Badge cls={firstRunGatewayStatus.cls}>{firstRunGatewayStatus.text}</Badge>。{nativeStatus.gateway_state === 'running_unavailable' ? '进程正在运行，但当前没有可用账号；上游尚未测试。' : '进程启动成功不等于上游可用。'}</p>
            <div className="page-actions"><button className="btn btn-primary" disabled={firstRunBusy || isGatewayRunning(nativeStatus)} onClick={() => void firstRunCheck(true)}>启动 API 服务</button>
              <button className="btn" onClick={() => navigate('/config')}>查看网关设置</button><button className="btn" disabled={firstRunBusy || !isGatewayRunning(nativeStatus)} onClick={() => void continueFirstRun()}>继续接入说明</button></div></>}
          {firstRunStep === 4 && <><p>API 地址：<span className="mono">{String(nativeStatus.api_base_url ?? '请先检查本机状态')}</span>。在 API 与模型页复制 Key；在聊天测试页手动发送请求。当前未执行聊天测试。</p>
            <div className="page-actions"><button className="btn" onClick={() => navigate('/api-models')}>API 与模型</button><button className="btn" onClick={() => navigate('/playground')}>前往手动聊天测试</button></div>
            <label className="checkbox"><input type="checkbox" checked={firstRunAutoStart} onChange={event => setFirstRunAutoStart(event.target.checked)} />下次打开应用时启动 API</label><p className="hint">不会启用 macOS 开机启动。</p>
            <button className="btn btn-primary" disabled={firstRunBusy || !isGatewayRunning(nativeStatus)} onClick={() => void finishFirstRun(false)}>完成首次设置</button></>}
          <div className="page-actions">{firstRunStep > 0 && <button className="btn" disabled={firstRunBusy} onClick={() => setFirstRunStep(value => value - 1)}>上一步</button>}
            <button className="btn" disabled={firstRunBusy} onClick={() => void finishFirstRun(true)}>跳过剩余设置（未测试）</button></div>
        </div>}
        {warnings.map((w) => (
          <Alert key={w} kind="warn">
            {w}
          </Alert>
        ))}
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/accounts" element={<Accounts session={session} />} />
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
    </div>
  )
}
