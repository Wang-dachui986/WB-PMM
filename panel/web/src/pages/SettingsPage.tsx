import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { desktopCommand, hasDesktopBridge } from '../desktopBridge'
import { Alert, Badge, Spinner } from '../ui'

export type Preferences = {
  theme: 'system' | 'light' | 'dark'; autoStartGatewayOnLaunch: boolean; hasCompletedFirstRun: boolean
  autoStartGatewayAtLogin: boolean; loginItemStatus: 'enabled' | 'requiresApproval' | 'notRegistered' | 'failed'
  notes: Record<string, string>; about: Record<string, string>
}
const loginStartEnabled = (value: Preferences) => value.autoStartGatewayAtLogin &&
  (value.loginItemStatus === 'enabled' || value.loginItemStatus === 'requiresApproval')

export default function SettingsPage({ preferences, onSaved }: { preferences: Preferences | null; onSaved: (value: Preferences) => void }) {
  const [theme, setTheme] = useState<Preferences['theme']>('system')
  const [autoStart, setAutoStart] = useState(false)
  const [loginStart, setLoginStart] = useState(false)
  const [uid, setUID] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  useEffect(() => { if (preferences) { setTheme(preferences.theme); setAutoStart(preferences.autoStartGatewayOnLaunch); setLoginStart(loginStartEnabled(preferences)) } }, [preferences])
  const save = async (payload: Record<string, unknown>) => {
    setBusy(true); setError(null); setNotice(null)
    const changesLoginItem = payload.autoStartGatewayAtLogin !== undefined
    try {
      const value = await desktopCommand('preferences.update', payload) as unknown as Preferences
      onSaved(value); setNotice(changesLoginItem && value.loginItemStatus === 'requiresApproval' ? '登录项已登记，等待 macOS 批准' : '设置已保存')
    } catch {
      if (changesLoginItem && preferences) setLoginStart(loginStartEnabled(preferences))
      try { onSaved(await desktopCommand('preferences.get') as unknown as Preferences) } catch { /* 保留原错误 */ }
      setError(changesLoginItem ? '登录项设置失败；请检查 macOS 登录项设置并刷新状态后重试' : '保存失败，请检查输入或原生连接')
    }
    finally { setBusy(false) }
  }
  const savePreferences = () => {
    if (!preferences) return
    const payload: Record<string, unknown> = { theme, autoStartGatewayOnLaunch: autoStart }
    if (loginStart !== loginStartEnabled(preferences)) payload.autoStartGatewayAtLogin = loginStart
    void save(payload)
  }
  const refreshLoginItem = async () => {
    setError(null)
    try { onSaved(await desktopCommand('preferences.get') as unknown as Preferences) }
    catch { setError('无法读取 macOS 登录项状态') }
  }
  const openLoginItems = async () => {
    setError(null)
    try { await desktopCommand('preferences.update', { openLoginItems: true }) }
    catch { setError('无法打开系统登录项设置') }
  }
  const openDirectory = async (kind: 'data' | 'logs') => {
    setError(null)
    try {
      const result = await desktopCommand('desktop.openDirectory', { kind })
      if (!result.opened) setError('暂时无法打开目录')
    } catch { setError('暂时无法打开目录') }
  }
  if (!hasDesktopBridge()) return <Alert kind="info">应用设置由 macOS 桌面应用提供。</Alert>
  if (!preferences) return <Spinner label="正在读取应用设置…" />
  return <>
    <div className="page-head"><div><h1>应用设置与关于</h1><p>应用偏好与本地备注。</p></div></div>
    {error && <Alert kind="error" onClose={() => setError(null)}>{error}</Alert>}
    {notice && <Alert kind="ok" onClose={() => setNotice(null)}>{notice}</Alert>}
    <div className="card"><div className="card-head"><h2>应用偏好</h2></div>
      <div className="field"><label htmlFor="preference-theme">主题</label><select id="preference-theme" value={theme} onChange={(e) => setTheme(e.target.value as Preferences['theme'])}>
        <option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select></div>
      <div className="field"><label className="checkbox"><input type="checkbox" checked={autoStart} onChange={(e) => setAutoStart(e.target.checked)} />手动打开应用时启动 API</label>
        <div className="desc">下次手动打开本应用时生效。</div></div>
      <div className="field"><label className="checkbox"><input type="checkbox" checked={loginStart} onChange={(e) => setLoginStart(e.target.checked)} />登录 macOS 时自动打开应用并启动 API</label>
        <div className="desc">macOS 登录项：<Badge cls={preferences.loginItemStatus === 'enabled' ? 'badge-ok' : preferences.loginItemStatus === 'failed' ? 'badge-danger' : 'badge-warn'}>{({ enabled: '已启用', requiresApproval: '等待系统批准', notRegistered: '未启用', failed: '失败' } as const)[preferences.loginItemStatus]}</Badge>。{preferences.loginItemStatus === 'enabled' && !preferences.autoStartGatewayAtLogin ? '系统已启用登录项，当前未设置登录时启动 API。' : '关闭窗口不会停止已运行的 API；应用留在菜单栏。'}</div>
        {preferences.loginItemStatus === 'requiresApproval' && <><div className="desc">请在“系统设置 → 通用 → 登录项”中允许本应用；批准前不会自动启动。</div>
          <button className="btn btn-sm" disabled={busy} onClick={() => void openLoginItems()}>打开系统登录项设置</button></>}
        {preferences.loginItemStatus === 'failed' && <div className="desc">系统无法识别当前应用的登录项，请从已安装的应用运行后重试。</div>}
        <button className="btn btn-sm" disabled={busy} onClick={() => void refreshLoginItem()}>刷新登录项状态</button></div>
      <p>语言：简体中文</p><button className="btn btn-primary" disabled={busy} onClick={savePreferences}>{busy && <Spinner />}保存偏好</button>
      <div className="page-actions"><button className="btn" onClick={() => void openDirectory('data')}>打开数据目录</button>
        <button className="btn" onClick={() => void openDirectory('logs')}>打开日志目录</button></div>
    </div>
    <div className="card"><div className="card-head"><h2>本地账号备注</h2><span className="hint">最多 200 个账号，每条最多 256 字节</span></div>
      <div className="field"><label htmlFor="note-uid">账号 UID</label><input id="note-uid" maxLength={128} value={uid} onChange={(e) => { const next = e.target.value; setUID(next); setNote(preferences.notes[next] ?? '') }} /></div>
      <div className="field"><label htmlFor="note-value">备注</label><input id="note-value" maxLength={256} value={note} onChange={(e) => setNote(e.target.value)} /><div className="desc">仅保存在本机；留空保存可移除。不会写入日志或诊断文件。</div></div>
      <button className="btn" disabled={busy || !uid || new TextEncoder().encode(note).length > 256} onClick={() => void save({ note: { uid, value: note } })}>保存备注</button>
      {Object.keys(preferences.notes).length > 0 && <table><thead><tr><th>UID</th><th>备注</th><th>操作</th></tr></thead><tbody>{Object.entries(preferences.notes).map(([id, value]) =>
        <tr key={id}><td className="mono">{id}</td><td>{value}</td><td><button className="btn btn-sm" onClick={() => { setUID(id); setNote(value) }}>编辑</button></td></tr>)}</tbody></table>}
    </div>
    <div className="card"><div className="card-head"><h2>关于</h2></div>
      <dl><dt>应用版本 / 构建</dt><dd>{preferences.about.app_version} / {preferences.about.app_build}</dd>
        <dt>构建时间</dt><dd>{preferences.about.build_time === 'unrecorded' ? '此构建未记录' : preferences.about.build_time}</dd>
        <dt>网关固定提交</dt><dd className="mono">{preferences.about.gateway_commit}</dd><dt>面板固定提交</dt><dd className="mono">{preferences.about.panel_commit}</dd>
        <dt>系统 / 架构</dt><dd>macOS {preferences.about.os_version} / {preferences.about.architecture}</dd><dt>许可</dt><dd>MIT；基于开源项目构建，非上游厂商官方产品。</dd></dl>
      <p>项目来源：Sliverkiss/workbuddy2api；linbeize/workbuddy2api-gui。</p>
      <p>更新方式：查看项目发布说明后，手动下载并安装新版本。</p>
      <Link className="btn" to="/logs">日志与诊断</Link>
    </div>
  </>
}
