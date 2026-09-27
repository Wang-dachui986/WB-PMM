// System.tsx 系统页：运行信息、容器状态与控制、任务历史、使用说明。
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, ApiError } from '../api'
import { desktopCommand, hasDesktopBridge } from '../desktopBridge'
import type { SessionInfo, SystemInfo, TaskListResponse, TaskView } from '../types'
import { Alert, Badge, ConfirmDialog, fmtDuration, fmtISO, gatewayStatusBadge, Modal, Spinner } from '../ui'

function panelStatusBadge(value: unknown): { cls: string; text: string } {
  switch (value) {
    case '已就绪': return { cls: 'badge-ok', text: '已就绪' }
    case '正在启动': return { cls: 'badge-warn', text: '正在启动' }
    case '未启动': return { cls: 'badge-danger', text: '未启动' }
    case '管理连接已过期': return { cls: 'badge-danger', text: '管理连接已过期' }
    case '启动失败': return { cls: 'badge-danger', text: '启动失败' }
    default: return { cls: 'badge-warn', text: '状态未读取' }
  }
}

function taskStatusBadge(task: TaskView): { cls: string; text: string } {
  if (task.running) return { cls: 'badge-warn', text: '执行中' }
  if (task.failed > 0 || task.error) return { cls: 'badge-danger', text: '已结束（有失败）' }
  return { cls: 'badge-ok', text: '已完成' }
}

export default function System(props: { session: SessionInfo; onSessionRefresh: () => Promise<SessionInfo | null> }) {
  return hasDesktopBridge() ? <DesktopSystem /> : <WebSystem {...props} />
}

function DesktopSystem() {
  const [status, setStatus] = useState<Record<string, unknown>>({})
  const [about, setAbout] = useState<Record<string, string>>({})
  const [commands, setCommands] = useState<string[]>([])
  const [tasks, setTasks] = useState<TaskListResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const active = useRef(false)
  const busy = useRef(false)
  const load = async () => {
    if (busy.current || !active.current) return
    busy.current = true; setLoading(true)
    try {
      const [state, bridge, preferences, history] = await Promise.allSettled([
        desktopCommand('desktop.getStatus'), desktopCommand('desktop.getBridgeInfo'), desktopCommand('preferences.get'), api.tasks(),
      ])
      if (!active.current) return
      if (state.status === 'fulfilled') setStatus((state.value.status ?? {}) as Record<string, unknown>)
      if (bridge.status === 'fulfilled') setCommands((bridge.value.commands ?? []) as string[])
      if (preferences.status === 'fulfilled') setAbout((preferences.value.about ?? {}) as Record<string, string>)
      if (history.status === 'fulfilled') setTasks(history.value)
      setError([state, bridge, preferences, history].some(value => value.status === 'rejected') ? '部分本机信息未能读取，请刷新重试' : null)
    } finally { busy.current = false; if (active.current) setLoading(false) }
  }
  useEffect(() => { active.current = true; void load(); return () => { active.current = false } }, [])
  const gatewayState = gatewayStatusBadge(status.gateway_state)
  const panelState = panelStatusBadge(status.panel_state)
  return <>
    <div className="page-head"><div><h1>系统</h1><p>本机内置服务状态与任务历史</p></div><button className="btn" disabled={loading} onClick={() => void load()}>刷新</button></div>
    {error && <Alert kind="error">{error}</Alert>}
    <div className="card"><div className="card-head"><h2>内置服务</h2></div><dl className="kv">
      <dt>API 状态</dt><dd><Badge cls={gatewayState.cls}>{gatewayState.text}</Badge></dd><dt>面板状态</dt><dd><Badge cls={panelState.cls}>{panelState.text}</Badge></dd>
      <dt>API 地址</dt><dd className="mono">{String(status.api_base_url ?? '未读取')}</dd><dt>本次进程运行时长</dt><dd>{typeof status.uptime_seconds === 'number' ? `${Math.floor(status.uptime_seconds)} 秒` : '未运行'}</dd>
      <dt>可用 / 总账号</dt><dd>{String(status.healthy_accounts ?? '未知')} / {String(status.total_accounts ?? '未知')}</dd><dt>状态观测时间</dt><dd>{fmtISO(status.updated_at as string)}</dd>
      <dt>原生能力</dt><dd>{commands.length} 个固定命令</dd></dl>
      <p>启停与重启请使用原生服务栏或菜单栏；<Link to="/config">网关配置</Link> · <Link to="/logs">日志与诊断</Link></p></div>
    <div className="card"><div className="card-head"><h2>应用信息</h2></div><dl className="kv"><dt>版本 / 构建</dt><dd>{about.app_version ?? '未读取'} / {about.app_build ?? '未读取'}</dd>
      <dt>构建时间</dt><dd>{about.build_time === 'unrecorded' ? '此构建未记录' : about.build_time ?? '未读取'}</dd><dt>网关固定提交</dt><dd className="mono">{about.gateway_commit ?? '未读取'}</dd><dt>面板固定提交</dt><dd className="mono">{about.panel_commit ?? '未读取'}</dd></dl></div>
    <div className="card"><div className="card-head"><h2>任务历史</h2><span className="hint">本次面板进程最近 20 条</span></div>
      {!tasks?.tasks?.length ? <div className="empty">暂无本机任务记录</div> : <table><thead><tr><th>任务</th><th>状态</th><th>成功 / 失败</th><th>开始</th></tr></thead><tbody>{tasks.tasks.map(task => { const state = taskStatusBadge(task); return <tr key={task.id}><td>{task.title}</td><td><Badge cls={state.cls}>{state.text}</Badge></td><td><span className="text-ok">{task.ok}</span> / <span className={task.failed > 0 ? 'text-danger' : 'text-dim'}>{task.failed}</span></td><td>{fmtISO(task.started_at)}</td></tr> })}</tbody></table>}</div>
  </>
}

function WebSystem({
  session,
  onSessionRefresh,
}: {
  session: SessionInfo
  onSessionRefresh: () => Promise<SessionInfo | null>
}) {
  const [info, setInfo] = useState<SystemInfo | null>(null)
  const [tasks, setTasks] = useState<TaskListResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [confirmRestart, setConfirmRestart] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const [showPassword, setShowPassword] = useState(false)

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const [i, t] = await Promise.all([api.system(), api.tasks()])
      setInfo(i)
      setTasks(t)
      setError(null)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '加载系统信息失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const timer = setInterval(() => void load(true), 20_000)
    return () => clearInterval(timer)
  }, [load])

  const doRestart = async () => {
    setRestarting(true)
    setNotice(null)
    try {
      const res = await api.restart()
      setNotice(res.message + '。网关需数秒恢复，请稍后刷新页面查看账号加载情况。')
      setConfirmRestart(false)
      // 重启不改变面板自身配置，但同步一次会话信息以保证模式标志（只读/高危）最新。
      void onSessionRefresh()
      // 给容器一点启动时间再刷新状态。
      setTimeout(() => void load(true), 4000)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '重启失败')
    } finally {
      setRestarting(false)
    }
  }

  if (loading && !info) return <Spinner label="正在加载系统信息…" />

  const c = info?.container
  const gatewayServiceOK = info?.gateway_health?.service === 'workbuddy2api'
  const gatewayHealth = gatewayServiceOK
    ? typeof info.gateway_health?.healthy === 'number' && info.gateway_health.healthy > 0
      ? { cls: 'badge-ok', text: '运行中 · 可调用' }
      : { cls: 'badge-warn', text: '运行中 · 暂不可调用' }
    : info?.gateway_health_error
      ? { cls: 'badge-danger', text: '不可达' }
      : { cls: 'badge-warn', text: '状态未确认' }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>系统</h1>
          <p>面板与网关的运行状态、容器控制与任务历史</p>
        </div>
        <div className="page-actions">
          <button className="btn" onClick={() => void load()} disabled={loading}>
            {loading && <Spinner />} 刷新
          </button>
          <button
            className="btn btn-primary"
            onClick={() => setConfirmRestart(true)}
            disabled={!session.dangerous_ops || !c?.exists}
            title={
              !session.dangerous_ops
                ? '需在服务端开启 dangerous_ops 才能重启容器'
                : !c?.exists
                  ? '未检测到容器'
                  : '重启网关容器以加载新账号/新配置'
            }
          >
            重启网关
          </button>
        </div>
      </div>

      {notice && (
        <Alert kind="ok" onClose={() => setNotice(null)}>
          {notice}
        </Alert>
      )}
      {error && (
        <Alert kind="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      {session.using_default_password && (
        <Alert kind="warn">
          面板正在使用默认口令，请在服务端配置中修改 <span className="mono">ui.password</span>。
        </Alert>
      )}

      {/* 网关与容器状态 */}
      <div className="card">
        <div className="card-head">
          <h2>网关状态</h2>
          <Badge cls={gatewayHealth.cls}>{gatewayHealth.text}</Badge>
        </div>
        <dl className="kv">
          <dt>网关地址</dt>
          <dd className="mono">{info?.gateway_url}</dd>
          <dt>身份标识</dt>
          <dd>
            {gatewayServiceOK ? (
              <>WBPMM网关已确认</>
            ) : (
              <span className={info?.gateway_health_error ? 'text-danger' : 'text-warn'}>{info?.gateway_health_error || '无法确认身份'}</span>
            )}
          </dd>
          <dt>账号池</dt>
          <dd>
            {info?.gateway_health ? `${info.gateway_health.healthy} / ${info.gateway_health.total} 可用` : '—'}
          </dd>
          <dt>容器名</dt>
          <dd className="mono">{c?.name || '（未配置）'}</dd>
          <dt>容器状态</dt>
          <dd>
            {!c ? (
              <Badge cls="badge-warn">状态未读取</Badge>
            ) : c.disabled ? (
              <Badge cls="badge-danger">容器控制已禁用（未配置）</Badge>
            ) : c?.exists ? (
              <>
                <Badge cls={!c.status || c.status === 'unknown' ? 'badge-warn' : c.running ? 'badge-ok' : 'badge-danger'}>{c.status || '状态未知'}</Badge>
                {c.health && c.health !== '-' && (
                  <span style={{ marginLeft: 8 }}><Badge cls={c.health === 'healthy' ? 'badge-ok' : c.health === 'unhealthy' ? 'badge-danger' : 'badge-warn'}>健康检查：{c.health}</Badge></span>
                )}
              </>
            ) : (
              <span className="text-danger">{c?.error || '容器不存在'}</span>
            )}
          </dd>
          <dt>容器镜像</dt>
          <dd className="mono">{c?.image || '—'}</dd>
          <dt>容器启动</dt>
          <dd>{c?.started_at ? fmtISO(c.started_at) : '—'}</dd>
        </dl>
        {!session.dangerous_ops && (
          <div className="desc" style={{ marginTop: 12 }}>
            当前未开启高危操作（<span className="mono">dangerous_ops=false</span>），因此「重启网关」「删除账号」
            「恢复配置备份」均被禁用。如需启用，在面板服务端配置中设置：
            <div className="muted-box" style={{ marginTop: 7 }}>{`WBGUI_DANGEROUS_OPS=true  # 或配置文件里 "dangerous_ops": true`}</div>
          </div>
        )}
      </div>

      {/* 面板信息 */}
      <div className="card">
        <div className="card-head">
          <h2>面板信息</h2>
          <button
            className="btn btn-sm"
            onClick={() => setShowPassword(true)}
            disabled={!session.password_changeable}
            title={session.password_changeable ? '修改面板登录口令' : '服务端未配置凭据持久化，无法从网页改密码'}
          >
            修改密码
          </button>
        </div>
        <dl className="kv">
          <dt>面板版本</dt>
          <dd className="mono">{info?.version || 'dev'}</dd>
          <dt>运行时长</dt>
          <dd>{info ? fmtDuration(info.uptime_sec) : '—'}</dd>
          <dt>启动时间</dt>
          <dd>{info ? fmtISO(info.started_at) : '—'}</dd>
          <dt>凭证目录</dt>
          <dd className="mono">{info?.auth_dir}</dd>
          <dt>网关配置文件</dt>
          <dd className="mono">{info?.config_file}</dd>
          <dt>运行模式</dt>
          <dd>
            {!info ? <Badge cls="badge-warn">运行模式未读取</Badge> : info.read_only ? <Badge cls="badge-warn">只读</Badge> : <Badge cls="badge-ok">可写</Badge>}
            {!info ? <Badge cls="badge-warn">高危操作状态未读取</Badge> : info.dangerous_ops ? (
              <Badge cls="badge-warn">高危操作已解锁</Badge>
            ) : (
              <Badge cls="badge-dim">高危操作已锁定</Badge>
            )}
          </dd>
          <dt>Docker 可用</dt>
          <dd>{!info ? <Badge cls="badge-warn">未读取</Badge> : info.docker_available ? <Badge cls="badge-ok">可用</Badge> : <Badge cls="badge-danger">不可用（容器控制已降级）</Badge>}</dd>
        </dl>
      </div>

      {/* 任务历史 */}
      <div className="card">
        <div className="card-head">
          <h2>任务历史</h2>
          <span className="hint">保留最近 20 条（进程重启后清空）</span>
        </div>
        {tasks?.running && tasks.running.length > 0 && (
          <Alert kind="warn">
            当前有 {tasks.running.length} 个任务正在执行：{tasks.running.map((t) => t.title).join('、')}
          </Alert>
        )}
        {!tasks?.tasks || tasks.tasks.length === 0 ? (
          <div className="empty">
            还没有执行过批量任务。
            <div style={{ marginTop: 10 }}>
              <Link className="btn btn-sm" to="/accounts">
                去账号管理执行
              </Link>
            </div>
          </div>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>任务</th>
                  <th>状态</th>
                  <th className="num">成功 / 失败</th>
                  <th>开始时间</th>
                </tr>
              </thead>
              <tbody>
                {tasks.tasks.map((t) => { const state = taskStatusBadge(t); return <tr key={t.id}>
                    <td>{t.title}</td>
                    <td><Badge cls={state.cls}>{state.text}</Badge></td>
                    <td className="num">
                      <span className="text-ok">{t.ok}</span> /{' '}
                      {t.failed > 0 ? <span className="text-danger">{t.failed}</span> : <span className="text-dim">0</span>}
                    </td>
                    <td className="text-dim" style={{ fontSize: 12 }}>
                      {fmtISO(t.started_at)}
                    </td>
                  </tr> })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* 使用说明 */}
      <div className="card">
        <div className="card-head">
          <h2>客户端接入方式</h2>
        </div>
        <p className="text-dim" style={{ marginTop: 0, fontSize: 13 }}>
          网关对客户端暴露标准 OpenAI 兼容接口，任何支持自定义 base_url 的客户端都可直接接入：
        </p>
        <div className="muted-box">
          {`Base URL:  ${info?.gateway_url || 'http://127.0.0.1:7863'}/v1
API Key:   你在网关 config.json 中设置的 api_key
模型:      见「聊天测试」页的模型列表（如 deepseek-v4.1-flash、glm-5.3 等）`}
        </div>
        <div className="muted-box" style={{ marginTop: 10 }}>
          {`# 命令行验证
curl ${info?.gateway_url || 'http://127.0.0.1:7863'}/v1/chat/completions \\
  -H "Authorization: Bearer <你的 api_key>" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true}'`}
        </div>
        <div className="desc" style={{ marginTop: 10 }}>
          提示：本面板与网关是<strong>两个独立服务</strong>。面板只通过 HTTP API 管理网关，并直读磁盘上的账号凭证与
          配置文件；因此网关重启不会影响面板，反之亦然。
        </div>
      </div>

      {confirmRestart && (
        <ConfirmDialog
          title="重启网关容器"
          danger
          confirmText="确认重启"
          busy={restarting}
          onCancel={() => setConfirmRestart(false)}
          onConfirm={() => void doRestart()}
          message={
            <>
              <p style={{ marginTop: 0 }}>
                即将执行 <span className="mono">docker restart {c?.name}</span>。
              </p>
              <p>
                重启期间（约 2-5 秒）网关无法处理请求，正在进行的对话会中断。
                重启后新账号会被加载进账号池、新配置会生效。
              </p>
              <p style={{ marginBottom: 0 }}>账号池状态由 state.json 持久化，重启不会丢失冷却/熔断记录。</p>
            </>
          }
        />
      )}

      {showPassword && (
        <ChangePasswordDialog
          currentUser={session.username}
          onClose={() => setShowPassword(false)}
          onDone={async () => {
            setShowPassword(false)
            await onSessionRefresh()
          }}
        />
      )}
    </>
  )
}

/** ChangePasswordDialog 修改面板登录口令。 */
function ChangePasswordDialog({
  currentUser,
  onClose,
  onDone,
}: {
  currentUser: string
  onClose: () => void
  onDone: () => Promise<void>
}) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [newUsername, setNewUsername] = useState(currentUser)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)

  const submit = async () => {
    setError(null)
    if (!current) {
      setError('请输入当前口令')
      return
    }
    if (next.length < 6) {
      setError('新口令至少 6 位')
      return
    }
    if (next !== confirm) {
      setError('两次输入的新口令不一致')
      return
    }
    setBusy(true)
    try {
      const res = await api.changePassword(current, next, newUsername)
      setDone(res.message || '口令已修改')
      // 改成功后可无缝继续（后端已重发会话 Cookie）。
      await onDone()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '修改失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="修改登录口令"
      onClose={onClose}
      dismissDisabled={busy}
      footer={
        done ? (
          <button className="btn btn-primary" onClick={onClose}>
            完成
          </button>
        ) : (
          <>
            <button className="btn" onClick={onClose} disabled={busy}>
              取消
            </button>
            <button className="btn btn-primary" onClick={() => void submit()} disabled={busy}>
              {busy ? <Spinner /> : null}
              保存修改
            </button>
          </>
        )
      }
    >
      {done ? (
        <Alert kind="ok">{done}</Alert>
      ) : (
        <>
          {error && <Alert kind="error">{error}</Alert>}
          <div className="field">
            <label>用户名</label>
            <input type="text" value={newUsername} onChange={(e) => setNewUsername(e.target.value)} autoComplete="username" />
            <div className="desc">可同时修改登录用户名，留空则沿用当前用户名。</div>
          </div>
          <div className="field">
            <label>当前口令</label>
            <input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" autoFocus />
          </div>
          <div className="field">
            <label>新口令（至少 6 位）</label>
            <input type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
          </div>
          <div className="field" style={{ marginBottom: 0 }}>
            <label>确认新口令</label>
            <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" />
          </div>
          <div className="desc" style={{ marginTop: 12 }}>
            修改后所有已登录会话立即失效，需用新口令重新登录（本页面会自动续期）。
          </div>
        </>
      )}
    </Modal>
  )
}
