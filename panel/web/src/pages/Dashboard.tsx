// Dashboard.tsx 仪表盘：账号池健康度、积分总览、异常提示。
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, ApiError } from '../api'
import { desktopCommand, hasDesktopBridge } from '../desktopBridge'
import type { Account, Overview, StatsResponse } from '../types'
import { Alert, Badge, observedCredits, displayName, fmtDuration, fmtISO, fmtNum, gatewayStatusBadge, Spinner, statusBadge } from '../ui'

// Keep both reads in one flight, including while a previous page is leaving.
let pendingDashboard: Promise<unknown> | undefined

export const problemAccounts = (accounts: Account[]) => accounts.filter(
  a => a.status === 'disabled' || a.status === 'token_expired' || a.status === 'cooling' || a.status === 'missing_credential',
)

export function pollDashboard(onPage: (overview: Overview, accounts: Account[]) => void,
  onError: (message: string) => void, onLoading: (loading: boolean) => void,
  onDesktop?: (status: Record<string, unknown>, stats: StatsResponse | null) => void) {
  let active = true
  let busy = false
  let pendingVisibleRefresh = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const clearTimer = () => { if (timer !== undefined) clearTimeout(timer); timer = undefined }
  const load = async (silent = false) => {
    if (!active || document.hidden || busy) return
    busy = true
    clearTimer()
    if (!silent) onLoading(true)
    try {
      while (pendingDashboard) {
        await pendingDashboard
        if (!active || document.hidden) return
      }
      // A fast failure must not release the flight while the other read is pending.
      pendingVisibleRefresh = false
      const request = Promise.allSettled([api.overview(), api.accounts(),
        onDesktop ? Promise.allSettled([desktopCommand('desktop.getStatus'), api.stats()]) : Promise.resolve(null)])
      pendingDashboard = request
      const [overview, accounts, desktop] = await request
      if (pendingDashboard === request) pendingDashboard = undefined
      if (!active || document.hidden) return
      if (onDesktop && desktop.status === 'fulfilled' && desktop.value) {
        const [status, stats] = desktop.value
        onDesktop(status.status === 'fulfilled' ? (status.value.status ?? {}) as Record<string, unknown> : {}, stats.status === 'fulfilled' ? stats.value : null)
      }
      if (overview.status === 'rejected') throw overview.reason
      if (accounts.status === 'rejected') throw accounts.reason
      onPage(overview.value, accounts.value.accounts ?? [])
    } catch (err) {
      if (active && !document.hidden) onError(err instanceof ApiError ? err.message : '加载失败')
    } finally {
      busy = false
      if (active) onLoading(false)
      if (active && !document.hidden) {
        if (pendingVisibleRefresh) void load(true)
        else timer = setTimeout(() => void load(true), 15_000)
      }
    }
  }
  const visibility = () => {
    clearTimer()
    if (document.hidden) pendingVisibleRefresh = false
    else if (busy) pendingVisibleRefresh = true
    else void load(true)
  }
  document.addEventListener('visibilitychange', visibility)
  void load()
  return {
    refresh: () => load(),
    stop: () => { active = false; pendingVisibleRefresh = false; clearTimer(); document.removeEventListener('visibilitychange', visibility) },
  }
}

export default function Dashboard() {
  const [ov, setOv] = useState<Overview | null>(null)
  const [accounts, setAccounts] = useState<Account[]>([])
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshedAt, setRefreshedAt] = useState<Date | null>(null)
  const refresh = useRef<() => void>(() => {})
  const desktop = hasDesktopBridge()
  const [nativeStatus, setNativeStatus] = useState<Record<string, unknown>>({})
  const [stats, setStats] = useState<StatsResponse | null>(null)
  const [copyNotice, setCopyNotice] = useState<string | null>(null)
  const copy = async (key = false) => {
    try {
      if (key) await desktopCommand('api.copyKey')
      else if (typeof nativeStatus.api_base_url === 'string') await navigator.clipboard.writeText(nativeStatus.api_base_url)
      else throw new Error()
      setCopyNotice(key ? 'API Key 已由原生应用复制' : 'API 地址已复制')
    } catch { setCopyNotice('复制未完成，请检查本机状态') }
  }

  useEffect(() => {
    const polling = pollDashboard((o, a) => {
      setOv(o)
      setAccounts(a)
      setError(null)
      setRefreshedAt(new Date())
    }, setError, setLoading, desktop ? (status, statistics) => { setNativeStatus(status); setStats(statistics) } : undefined)
    refresh.current = () => { void polling.refresh() }
    return () => { refresh.current = () => {}; polling.stop() }
  }, [])

  const problems = problemAccounts(accounts)
  const topAccounts = [...accounts]
    .sort((a, b) => desktop ? (observedCredits(b) ?? -Infinity) - (observedCredits(a) ?? -Infinity) : (b.live_credits ?? b.credits) - (a.live_credits ?? a.credits))
    .slice(0, 6)
  const gatewayState = gatewayStatusBadge(nativeStatus.gateway_state)

  return (
    <div className="dashboard-page">
      <div className="page-head">
        <div>
          <h1>仪表盘</h1>
          <p>
            账号池实时健康度与积分总览
            {refreshedAt && <span className="text-faint"> · 更新于 {refreshedAt.toLocaleTimeString('zh-CN', { hour12: false })}</span>}
            <span className="text-faint"> · 页面可见时每 15 秒自动刷新</span>
          </p>
        </div>
        <div className="page-actions">
          <button className="btn" onClick={() => refresh.current()} disabled={loading}>
            {loading && <Spinner />} 立即刷新
          </button>
        </div>
      </div>

      {desktop && (
        <section className="card dashboard-connection" aria-label="本机 API">
          <div className="dashboard-connection-main">
            <h2>本机 API</h2>
            <span className="mono dashboard-connection-url">{String(nativeStatus.api_base_url ?? '未读取')}</span>
            <Badge cls={gatewayState.cls}>{gatewayState.text}</Badge>
          </div>
          <div className="page-actions dashboard-connection-actions">
            <button className="btn" disabled={!nativeStatus.api_base_url} onClick={() => void copy()}>复制 API 地址</button>
            <button className="btn" onClick={() => void copy(true)}>复制 API Key</button>
          </div>
        </section>
      )}

      {loading && !ov && <Spinner label="正在加载仪表盘…" />}
      {error && <Alert kind="error">{error}</Alert>}
      {copyNotice && <Alert kind="info" onClose={() => setCopyNotice(null)}>{copyNotice}</Alert>}
      {ov && !ov.gateway_ok && (
        <Alert kind="error">
          <strong>无法连接网关（{ov.gateway_url}）</strong>
          <div style={{ marginTop: 4 }}>{ov.gateway_error}</div>
          <div style={{ marginTop: 6, fontSize: 12.5 }} className="text-dim">
            {hasDesktopBridge() ? <>请查看原生窗口的服务状态，并前往<Link to="/config">配置页</Link>检查本机网关配置。</> : <>
              请确认网关容器正在运行（<span className="mono">docker ps</span>），且本面板的
              <span className="mono"> gateway_url </span>配置正确。
            </>}
          </div>
        </Alert>
      )}

      {ov?.warnings?.map((w) => (
        <Alert key={w} kind="warn">
          {w}
        </Alert>
      ))}

      {ov && (ov.expired > 0 || ov.expiring > 0) && (
        <Alert kind="warn">
          有 <strong>{ov.expired}</strong> 个账号 token 已过期、<strong>{ov.expiring}</strong> 个即将过期。
          <Link to="/accounts" style={{ marginLeft: 6 }}>
            去账号管理批量刷新 →
          </Link>
        </Alert>
      )}

      {ov && ov.file_issues && ov.file_issues.length > 0 && (
        <Alert kind="warn">
          <strong>凭证目录有问题文件：</strong>
          <ul style={{ margin: '5px 0 0', paddingLeft: 18 }}>
            {ov.file_issues.map((i) => (
              <li key={i} className="mono" style={{ fontSize: 12 }}>
                {i}
              </li>
            ))}
          </ul>
        </Alert>
      )}

      {ov && (
        <>
          <div className="dashboard-primary">
            <Stat
              to="/accounts?status=healthy" label="网关账号池"
              value={`${ov.healthy}/${ov.total}`}
              sub={ov.total === 0 ? '尚未添加任何账号' : `可用 / 总数`}
              tone={ov.healthy === 0 ? 'danger' : ov.healthy < ov.total ? 'warn' : 'ok'}
            />
            <Stat
              label="剩余积分"
              value={desktop ? (accounts.some(a => observedCredits(a) !== undefined) ? fmtNum(accounts.reduce((sum, a) => sum + (observedCredits(a) ?? 0), 0)) : '未知') : fmtNum(ov.credits.remain)}
              sub={desktop ? `已观测 ${accounts.filter(a => observedCredits(a) !== undefined).length}/${accounts.length} 个账号，未知未计入` : ov.credits.size > 0 ? `总量 ${fmtNum(ov.credits.size)}` : '未查询到配额'}
            />
          </div>

          <div className="dashboard-secondary">
            <Stat to="/accounts?status=cooling" label="冷却中" value={String(ov.cooling)} sub="限流 / 熔断冷却" tone={ov.cooling > 0 ? 'warn' : undefined} />
            <Stat to="/accounts?status=problem" label="已禁用" value={String(ov.disabled)} sub="需重新登录" tone={ov.disabled > 0 ? 'danger' : undefined} />
            <Stat label="在途请求" value={String(ov.in_flight)} sub={`满载账号 ${ov.in_flight_full} 个`} />
            <Stat label="粘性会话" value={String(ov.sticky_sessions)} sub={`Redis：${ov.redis_mode}`} />
            <Stat
              label="凭证文件"
              value={String(ov.file_count)}
              sub={ov.expired > 0 ? `${ov.expired} 个已过期` : '全部有效'}
              tone={ov.expired > 0 ? 'warn' : undefined}
            />
          </div>

          <div className="grid grid-2 dashboard-panels">
            <div className="card">
              <div className="card-head">
                <h2>积分最高的账号</h2>
                <Link to="/accounts" className="hint">
                  查看全部 →
                </Link>
              </div>
              {topAccounts.length === 0 ? (
                <div className="empty">
                  还没有账号。
                  <div style={{ marginTop: 10 }}>
                    <Link className="btn btn-primary btn-sm" to="/login">
                      添加第一个账号
                    </Link>
                  </div>
                </div>
              ) : (
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>账号</th>
                        <th>状态</th>
                        <th className="num">积分</th>
                        <th className="num">成功</th>
                      </tr>
                    </thead>
                    <tbody>
                      {topAccounts.map((a) => {
                        const b = statusBadge(a.status)
                        return (
                          <tr key={a.uid}>
                            <td>
                              <div>{displayName(a)}</div>
                              <div className="mono text-faint" style={{ fontSize: 11 }}>
                                {a.uid.slice(0, 8)}
                              </div>
                            </td>
                            <td>
                              <Badge cls={b.cls}>{b.text}</Badge>
                            </td>
                            <td className="num">{desktop ? (observedCredits(a) === undefined ? '未知' : fmtNum(observedCredits(a))) : fmtNum(a.live_credits ?? a.credits)}</td>
                            <td className="num text-dim">{fmtNum(a.success_count)}</td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </div>

            <div className="card">
              <div className="card-head">
                <h2>需要处理的账号</h2>
                <span className="hint">{problems.length} 个</span>
              </div>
              {problems.length === 0 ? (
                <div className="empty">所有账号状态正常</div>
              ) : (
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>账号</th>
                        <th>问题</th>
                        <th>剩余</th>
                      </tr>
                    </thead>
                    <tbody>
                      {problems.map((a) => {
                        const b = statusBadge(a.status)
                        return (
                          <tr key={a.uid}>
                            <td>
                              <div>{displayName(a)}</div>
                              <div className="mono text-faint" style={{ fontSize: 11 }}>
                                {a.uid.slice(0, 8)}
                              </div>
                            </td>
                            <td>
                              <Badge cls={b.cls}>{b.text}</Badge>
                              {a.reason && (
                                <div className="text-faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                                  {a.reason}
                                </div>
                              )}
                              {a.breaker_fails > 0 && (
                                <div className="text-faint" style={{ fontSize: 11.5 }}>
                                  熔断计数 {a.breaker_fails}
                                </div>
                              )}
                            </td>
                            <td className="text-dim" style={{ fontSize: 12 }}>
                              {a.cool_remaining_sec ? fmtDuration(a.cool_remaining_sec) : fmtISO(a.last_err)}
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </div>

        </>
      )}

      {(ov || desktop) && (
        <div className="card dashboard-runtime">
          <div className="card-head"><h2>运行信息</h2></div>
          <dl className="kv">
            {ov && (!desktop || typeof nativeStatus.api_base_url !== 'string' || ov.gateway_url !== nativeStatus.api_base_url) && <>
              <dt>{desktop ? '面板连接地址' : '网关地址'}</dt><dd className="mono">{ov.gateway_url}</dd>
            </>}
            {ov && <>
              <dt>身份标识</dt>
              <dd>{ov.health?.service === 'workbuddy2api' ? <Badge cls="badge-ok">WBPMM网关已确认</Badge> : <Badge cls="badge-warn">未确认</Badge>}</dd>
              <dt>服务端时间</dt><dd>{new Date(ov.server_time).toLocaleString('zh-CN', { hour12: false })}</dd>
            </>}
            {desktop && <>
              <dt>本次进程运行时长</dt><dd>{typeof nativeStatus.uptime_seconds === 'number' ? `${Math.floor(nativeStatus.uptime_seconds)} 秒` : '未运行或未读取'}</dd>
              <dt>本次运行请求数</dt><dd>{stats?.scope === 'current_run' && stats.stats.enabled ? fmtNum(stats.stats.total.requests) : '未知'}</dd>
              <dt>已观测 Token</dt><dd>{stats?.scope === 'current_run' && stats.stats.enabled && (stats.stats.total.usage_observed_requests ?? 0) > 0 ? fmtNum(stats.stats.total.total_tokens) : '未知'}（未返回用量的请求不计入）</dd>
            </>}
          </dl>
        </div>
      )}
    </div>
  )
}

function Stat({
  label,
  value,
  sub,
  tone, to,
}: {
  label: string
  to?: string
  value: string
  sub?: string
  tone?: 'ok' | 'warn' | 'danger'
}) {
  const cls = tone === 'ok' ? 'text-ok' : tone === 'warn' ? 'text-warn' : tone === 'danger' ? 'text-danger' : ''
  return (
    <div className="stat">
      <div className="stat-label">{to ? <Link to={to}>{label}</Link> : label}</div>
      <div className={`stat-value ${cls}`}>{value}</div>
      {sub && <div className="stat-sub">{sub}</div>}
    </div>
  )
}
