// Dashboard.tsx 仪表盘：账号池健康度、积分总览、异常提示。
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { api, ApiError } from '../api'
import { beginMaintenance, desktopCommand, hasDesktopBridge, subscribeDesktopGatewayStatus, type MaintenanceFlow } from '../desktopBridge'
import type { Account, Overview, StatsResponse, TaskView } from '../types'
import { Alert, Badge, observedCredits, displayName, fmtDuration, fmtISO, fmtNum, gatewayStatusBadge, Spinner, creditStatusBadge, statusBadge } from '../ui'

// Keep both reads in one flight, including while a previous page is leaving.
let pendingDashboard: Promise<unknown> | undefined

export const problemAccounts = (accounts: Account[]) => accounts.filter(
  a => a.status === 'disabled' || a.status === 'token_expired' || a.status === 'cooling' || a.status === 'missing_credential' || !!a.credits_error,
)

export function pollDashboard(onPage: (overview: Overview, accounts: Account[]) => void,
  onError: (message: string) => void, onLoading: (loading: boolean) => void,
  onDesktop?: (status: Record<string, unknown>, stats: StatsResponse | null) => void) {
  let active = true
  let busy = false
  let paused = false
  let pendingVisibleRefresh = false
  let pendingGatewayRefresh = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const clearTimer = () => { if (timer !== undefined) clearTimeout(timer); timer = undefined }
  const load = async (silent = false) => {
    if (!active || paused || document.hidden || busy) return
    busy = true
    clearTimer()
    if (!silent) onLoading(true)
    try {
      while (pendingDashboard) {
        await pendingDashboard
        if (!active || paused || document.hidden) return
      }
      // A fast failure must not release the flight while the other read is pending.
      pendingVisibleRefresh = false
      pendingGatewayRefresh = false
      const request = Promise.allSettled([api.overview(), api.accounts(),
        onDesktop ? Promise.allSettled([desktopCommand('desktop.getStatus'), api.stats()]) : Promise.resolve(null)])
      pendingDashboard = request
      const [overview, accounts, desktop] = await request
      if (pendingDashboard === request) pendingDashboard = undefined
      if (!active || paused || document.hidden) return
      if (onDesktop && desktop.status === 'fulfilled' && desktop.value) {
        const [status, stats] = desktop.value
        onDesktop(status.status === 'fulfilled' ? (status.value.status ?? {}) as Record<string, unknown> : {}, stats.status === 'fulfilled' ? stats.value : null)
      }
      if (overview.status === 'rejected') throw overview.reason
      if (accounts.status === 'rejected') throw accounts.reason
      onPage(overview.value, accounts.value.accounts ?? [])
    } catch (err) {
      if (active && !paused && !document.hidden) onError(err instanceof ApiError ? err.message : '加载失败')
    } finally {
      busy = false
      if (active) onLoading(false)
      if (active && !paused && !document.hidden) {
        if (pendingVisibleRefresh || pendingGatewayRefresh) void load(true)
        else timer = setTimeout(() => void load(true), 15_000)
      }
    }
  }
  const visibility = () => {
    clearTimer()
    if (document.hidden) pendingVisibleRefresh = false
    else if (paused) return
    else if (busy) pendingVisibleRefresh = true
    else void load(true)
  }
  const gatewayStatus = () => {
    if (!active) return
    pendingGatewayRefresh = true
    if (!paused && !document.hidden && !busy) void load(true)
  }
  document.addEventListener('visibilitychange', visibility)
  const unsubscribeGateway = typeof window !== 'undefined' && hasDesktopBridge() ? subscribeDesktopGatewayStatus(gatewayStatus) : () => {}
  void load()
  return {
    refresh: () => load(),
    pause: () => { paused = true; pendingVisibleRefresh = false; clearTimer() },
    resume: () => {
      if (!active) return
      paused = false
      if (document.hidden) return
      if (busy) pendingVisibleRefresh = true
      else void load()
    },
    stop: () => { active = false; pendingVisibleRefresh = false; pendingGatewayRefresh = false; clearTimer(); document.removeEventListener('visibilitychange', visibility); unsubscribeGateway() },
  }
}

type CreditProgress = { done: number; total: number; batch: number; batches: number }
type CreditFailure = { uid: string; message: string }
type CreditResult = { failures: CreditFailure[]; okUIDs: Set<string>; failed: number; confirmed: number; issue?: string; batches: number; failureReasons: string[] }
type CreditRun = { cancelled: boolean; finished: boolean; flow: MaintenanceFlow | null; cleanupError?: string; cancelWait?: Promise<void>; signalCancel?: () => void }
type CreditOutcome = CreditResult & { total: number; target: number; skipped: number }

async function awaitCreditRequest<T>(request: Promise<T>, run: CreditRun, timeoutMs: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      request,
      new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error(message)), timeoutMs) }),
      ...(run.cancelWait ? [run.cancelWait.then(() => { throw new Error('积分查询已取消') })] : []),
    ])
  } finally { if (timer !== undefined) clearTimeout(timer) }
}

export async function queryAllCredits(
  uids: string[], desktop: boolean, run: CreditRun, progress: (value: CreditProgress) => void,
  operations: {
    batch: typeof api.batchCredits
    task: typeof api.task
    begin: typeof beginMaintenance
    wait: () => Promise<void>
  } = { batch: api.batchCredits, task: api.task, begin: beginMaintenance, wait: () => new Promise(resolve => setTimeout(resolve, 1000)) },
): Promise<CreditResult> {
  const batches = desktop ? Array.from({ length: Math.ceil(uids.length / 20) }, (_, i) => uids.slice(i * 20, i * 20 + 20)) : [uids]
  const okUIDs = new Set<string>()
  const failureReasons = new Set<string>()
  const failures: CreditFailure[] = []
  let failed = 0
  let confirmed = 0
  let issue: string | undefined
  for (let index = 0; index < batches.length && !run.cancelled; index++) {
    const batch = batches[index]
    let flow: MaintenanceFlow | null = null
    let settled = false
    progress({ done: confirmed, total: uids.length, batch: index + 1, batches: batches.length })
    try {
      if (desktop) {
        flow = operations.begin('credits', batch)
        run.flow = flow
      }
      const lease = await flow?.ready
      if (run.cancelled) break
      const submitted = await awaitCreditRequest(operations.batch(batch, lease), run, 30_000, '积分任务提交超时，正在取消当前维护')
      let task: TaskView = submitted
      const deadline = Date.now() + 13 * 60_000
      while (task.running && !run.cancelled) {
        if (Date.now() >= deadline) throw new Error('积分查询超时，正在取消当前维护')
        await operations.wait()
        if (!run.cancelled) {
          const remaining = deadline - Date.now()
          if (remaining <= 0) throw new Error('积分查询超时，正在取消当前维护')
          task = await awaitCreditRequest(operations.task(submitted.id), run, Math.min(remaining, 30_000), '积分任务状态读取超时，正在取消当前维护')
        }
        if (!run.cancelled) progress({ done: confirmed + task.done, total: uids.length, batch: index + 1, batches: batches.length })
      }
      if (run.cancelled) break
      settled = !task.running
      const items = task.items ?? []
      for (const item of items) {
        if (item.ok) okUIDs.add(item.uid)
        else {
          failed++
          failures.push({ uid: item.uid, message: item.message || '上游未返回失败详情' })
          if (failureReasons.size < 3) failureReasons.add(item.message || '上游未返回失败详情')
        }
      }
      confirmed += items.length
      progress({ done: confirmed, total: uids.length, batch: index + 1, batches: batches.length })
      if (task.error || !settled || task.done !== batch.length || items.length !== batch.length) {
        throw new Error(task.error || `第 ${index + 1} 批未完整返回结果`)
      }
    } catch (error) {
      if (!run.cancelled) issue = error instanceof Error ? error.message : '查询失败'
    } finally {
      if (flow) {
        try {
          if (run.cancelled || !settled) await flow.cancel()
          else await flow.end()
          if (run.flow === flow) run.flow = null
        } catch (error) {
          run.cleanupError = error instanceof Error ? error.message : '结束账号维护失败'
          issue = `账号维护结束未确认：${run.cleanupError}`
        }
      }
    }
    if (issue) break
  }
  return { failures, okUIDs, failed, confirmed, issue, batches: batches.length, failureReasons: [...failureReasons] }
}

export default function Dashboard({ onStartGuide, guideOpen = false }: { onStartGuide?: () => void; guideOpen?: boolean }) {
  const [ov, setOv] = useState<Overview | null>(null)
  const [accounts, setAccounts] = useState<Account[]>([])
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshedAt, setRefreshedAt] = useState<Date | null>(null)
  const refresh = useRef<() => void>(() => {})
  const pollingRef = useRef<ReturnType<typeof pollDashboard> | null>(null)
  const creditRun = useRef<CreditRun | null>(null)
  const pendingCreditOutcome = useRef<CreditOutcome | null>(null)
  const mounted = useRef(true)
  const [creditBusy, setCreditBusy] = useState(false)
  const [creditProgress, setCreditProgress] = useState<CreditProgress | null>(null)
  const [creditFailures, setCreditFailures] = useState<(CreditFailure & { name: string })[]>([])
  const [creditOutcome, setCreditOutcome] = useState<CreditOutcome | null>(null)
  const [creditNotice, setCreditNotice] = useState<{ kind: 'ok' | 'warn' | 'error'; text: string } | null>(null)
  const desktop = hasDesktopBridge()
  const [nativeStatus, setNativeStatus] = useState<Record<string, unknown>>({})
  const [stats, setStats] = useState<StatsResponse | null>(null)
  const [copyNotice, setCopyNotice] = useState<string | null>(null)
  const copy = async (key = false, address?: string) => {
    try {
      if (key) await desktopCommand('api.copyKey')
      else if (typeof (address ?? nativeStatus.api_base_url) === 'string') await navigator.clipboard.writeText((address ?? nativeStatus.api_base_url) as string)
      else throw new Error()
      setCopyNotice(key ? 'API Key 已由原生应用复制' : address ? '局域网地址已复制' : 'API 地址已复制')
    } catch { setCopyNotice('复制未完成，请检查本机状态') }
  }

  useEffect(() => {
    mounted.current = true
    const polling = pollDashboard((o, a) => {
      setOv(o)
      setAccounts(a)
      setError(null)
      setRefreshedAt(new Date())
      if (pendingCreditOutcome.current) {
        setCreditOutcome(pendingCreditOutcome.current)
        pendingCreditOutcome.current = null
        setCreditNotice(current => current ? { ...current, text: current.text.replace('正在刷新仪表盘', '仪表盘已刷新') } : current)
      }
    }, setError, setLoading, desktop ? (status, statistics) => { setNativeStatus(status); setStats(statistics) } : undefined)
    pollingRef.current = polling
    refresh.current = () => { void polling.refresh() }
    return () => {
      mounted.current = false
      const run = creditRun.current
      if (run) { run.cancelled = true; run.signalCancel?.(); void run.flow?.cancel().catch(() => undefined) }
      refresh.current = () => {}
      pollingRef.current = null
      polling.stop()
    }
  }, [])

  const finishCreditRun = (run: CreditRun) => {
    if (creditRun.current !== run || !run.finished || run.flow) return
    creditRun.current = null
    if (mounted.current) {
      setCreditBusy(false)
      pollingRef.current?.resume()
    }
  }

  const queryCredits = async () => {
    if (creditRun.current) {
      const run = creditRun.current
      run.cancelled = true
      run.signalCancel?.()
      setCreditNotice({ kind: 'warn', text: '正在取消积分查询…' })
      const flow = run.flow
      if (flow) {
        try {
          await flow.cancel()
          if (run.flow === flow) run.flow = null
          run.cleanupError = undefined
          if (run.finished) setCreditNotice({ kind: 'warn', text: '积分查询已取消；仪表盘将刷新已完成的账号结果' })
          finishCreditRun(run)
        } catch (error) {
          if (run.flow === flow && mounted.current) {
            run.cleanupError = error instanceof Error ? error.message : '原生维护取消失败'
            setCreditNotice({ kind: 'error', text: `账号维护结束未确认：${run.cleanupError}。请重试结束维护` })
          }
        }
      }
      return
    }
    let signalCancel = () => {}
    const cancelWait = new Promise<void>(resolve => { signalCancel = resolve })
    const run: CreditRun = { cancelled: false, finished: false, flow: null, cancelWait, signalCancel }
    creditRun.current = run
    pollingRef.current?.pause()
    setCreditBusy(true)
    setCreditProgress(null)
    setCreditOutcome(null)
    setCreditFailures([])
    pendingCreditOutcome.current = null
    setCreditNotice(null)
    try {
      const snapshot = await awaitCreditRequest(api.accounts(), run, 30_000, '账号列表读取超时')
      if (run.cancelled) return
      const all = snapshot.accounts ?? []
      const uids = [...new Set(all.filter(a => a.has_file).map(a => a.uid))]
      const skipped = all.filter(a => !a.has_file).length
      if (uids.length === 0) {
        setCreditNotice({ kind: 'warn', text: all.length ? `没有可查询的凭证账号；${skipped} 个账号缺少本地凭证` : '尚未添加账号' })
        return
      }
      const result = await queryAllCredits(uids, desktop, run, value => { if (mounted.current && !run.cancelled) setCreditProgress(value) })
      if (!mounted.current) return
      setCreditFailures(result.failures.map(item => ({ ...item, name: all.find(a => a.uid === item.uid) ? displayName(all.find(a => a.uid === item.uid)!) : item.uid })))
      if (run.cancelled) {
        setCreditNotice({ kind: 'warn', text: '积分查询已取消；已完成的账号结果会随仪表盘刷新' })
        return
      }
      pendingCreditOutcome.current = { ...result, total: all.length, target: uids.length, skipped }
      const unconfirmed = uids.length - result.confirmed
      const detail = `已完成 ${result.confirmed}/${uids.length}，成功 ${result.okUIDs.size}，失败 ${result.failed}${unconfirmed ? `，未确认 ${unconfirmed}` : ''}${skipped ? `，无凭证 ${skipped}` : ''}（${result.batches} 批）`
      setCreditNotice({ kind: result.issue ? 'error' : result.failed || unconfirmed || skipped ? 'warn' : 'ok', text: `积分查询${result.issue ? `中断：${result.issue}；` : '完成：'}${detail}${result.failureReasons.length ? `。失败原因：${result.failureReasons.join('；')}` : ''}。正在刷新仪表盘` })
    } catch (error) {
      if (mounted.current && !run.cancelled) setCreditNotice({ kind: 'error', text: error instanceof Error ? error.message : '查询积分失败' })
    } finally {
      run.finished = true
      if (run.flow && mounted.current) {
        setCreditNotice({ kind: 'error', text: `账号维护结束未确认：${run.cleanupError ?? '原生应用未确认清理完成'}。请重试结束维护` })
      } else if (run.cancelled && mounted.current) {
        setCreditNotice({ kind: 'warn', text: '积分查询已取消；仪表盘将刷新已完成的账号结果' })
      }
      finishCreditRun(run)
    }
  }

  const problems = problemAccounts(accounts)
  const creditRejected = accounts.filter(a => a.credits_status === 'auth_rejected').length
  const creditChecked = accounts.filter(a => a.credits_status === 'ok').length
  const topAccounts = [...accounts]
    .sort((a, b) => desktop ? (observedCredits(b) ?? -Infinity) - (observedCredits(a) ?? -Infinity) : (b.live_credits ?? b.credits) - (a.live_credits ?? a.credits))
    .slice(0, 6)
  const gatewayState = gatewayStatusBadge(nativeStatus.gateway_state)
  const gatewayRunning = nativeStatus.gateway_state === 'running_ready' || nativeStatus.gateway_state === 'running_unavailable'
  const lanUrls = Array.isArray(nativeStatus.lan_api_base_urls)
    ? [...new Set((nativeStatus.lan_api_base_urls as unknown[]).filter((url): url is string => typeof url === 'string' && url.length > 0))]
    : []
  const observed = accounts.filter(a => observedCredits(a) !== undefined)
  const freshObserved = creditOutcome ? accounts.filter(a => creditOutcome.okUIDs.has(a.uid) && observedCredits(a) !== undefined) : []
  const creditValue = creditOutcome
    ? freshObserved.length ? fmtNum(freshObserved.reduce((sum, a) => sum + observedCredits(a)!, 0)) : '未知'
    : desktop ? observed.length ? fmtNum(observed.reduce((sum, a) => sum + observedCredits(a)!, 0)) : '未知' : fmtNum(ov?.credits.remain ?? 0)
  const creditSub = creditBusy
    ? `已完成 ${creditProgress?.done ?? 0}/${creditProgress?.total ?? '…'}${creditProgress ? ` · 第 ${creditProgress.batch}/${creditProgress.batches} 批` : ''}；当前数字为上次观测`
    : creditOutcome
      ? `本次成功 ${creditOutcome.okUIDs.size}/${creditOutcome.total}，仅合计成功且有余额的账号${creditOutcome.okUIDs.size > freshObserved.length ? `；余额未显示 ${creditOutcome.okUIDs.size - freshObserved.length}` : ''}${creditOutcome.failed ? `；失败 ${creditOutcome.failed}` : ''}${creditOutcome.target > creditOutcome.confirmed ? `；未确认 ${creditOutcome.target - creditOutcome.confirmed}` : ''}${creditOutcome.skipped ? `；无凭证 ${creditOutcome.skipped}` : ''}`
      : desktop ? `已观测 ${observed.length}/${accounts.length} 个账号，未知未计入` : ov && ov.credits.size > 0 ? `总量 ${fmtNum(ov.credits.size)}` : '未查询到配额'

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
          {onStartGuide && <button className="btn" data-guide="entry" onClick={onStartGuide} disabled={guideOpen}>使用引导</button>}
          <button className="btn" onClick={() => refresh.current()} disabled={loading || creditBusy}>
            {loading && <Spinner />} 立即刷新
          </button>
        </div>
      </div>

      {desktop && (
        <section className="card dashboard-connection" data-guide="gateway" aria-label="本机 API">
          <div className="dashboard-connection-main">
            <h2>本机 API</h2>
            <span className="mono dashboard-connection-url">{String(nativeStatus.api_base_url ?? '未读取')}</span>
            <Badge cls={gatewayState.cls}>{gatewayState.text}</Badge>
          </div>
          <div className="page-actions dashboard-connection-actions">
            <button className="btn" data-guide="copy-address" disabled={!nativeStatus.api_base_url} onClick={() => void copy()}>复制 API 地址</button>
            <button className="btn" data-guide="copy-key" onClick={() => void copy(true)}>复制 API Key</button>
          </div>
          {nativeStatus.lan_enabled === true && <div className="dashboard-lan">
            <strong>局域网地址</strong>
            {!gatewayRunning ? <p className="desc">API 服务未运行；启动后显示局域网地址。</p>
              : lanUrls.length ? lanUrls.map((url, index) => <div className="lan-address-row" key={url}>
                <span className="mono">{url}</span><button className="btn btn-sm" aria-label={`复制局域网地址 ${index + 1}`} onClick={() => void copy(false, url)}>复制地址</button>
              </div>) : <p className="desc">未找到可用的局域网网卡，请连接 Wi-Fi 或以太网。</p>}
            <p className="desc">其他设备使用上述地址和相同的 API Key；系统防火墙或访客网络隔离可能阻止连接。</p>
          </div>}
        </section>
      )}

      {loading && !ov && <Spinner label="正在加载仪表盘…" />}
      {error && <Alert kind="error">{error}</Alert>}
      {copyNotice && <Alert kind="info" onClose={() => setCopyNotice(null)}>{copyNotice}</Alert>}
      {creditNotice && <Alert kind={creditNotice.kind} onClose={() => setCreditNotice(null)}>{creditNotice.text}</Alert>}
      {creditFailures.length > 0 && (
        <section className="card" aria-label="积分查询失败账号">
          <div className="card-head"><h2>本次积分查询失败的账号</h2><span className="hint">{creditFailures.length} 个</span></div>
          <div className="table-wrap" style={{ maxHeight: 280, overflowY: 'auto' }}>
            <table>
              <thead><tr><th>账号</th><th>失败原因</th></tr></thead>
              <tbody>{creditFailures.map(item => (
                <tr key={item.uid}>
                  <td><div>{item.name}</div><div className="mono text-faint" style={{ fontSize: 11 }}>{item.uid}</div></td>
                  <td style={{ whiteSpace: 'normal', overflowWrap: 'anywhere' }}>{item.message}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
          <p className="hint">仅列出本次已确认失败的账号；取消或中断时未返回结果的账号不计为失败。下次查询时清空此列表。</p>
        </section>
      )}
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
              sub={ov.total === 0 ? '尚未添加任何账号' : `候选 / 已载入；${creditRejected ? `积分认证失败 ${creditRejected} 个` : '不代表上游认证通过'}`}
              tone={ov.healthy === 0 ? 'danger' : creditRejected || ov.healthy < ov.total ? 'warn' : undefined}
            />
            <Stat
              label="剩余积分"
              value={creditValue}
              sub={creditSub}
              buttonSlot={<button className="btn btn-sm" onClick={() => void queryCredits()} disabled={(!creditBusy && (ov.read_only || guideOpen)) || (creditBusy && !creditRun.current)}>
                {creditBusy ? creditRun.current?.finished && creditRun.current.flow ? '重试结束维护' : '取消查询' : '查询全部积分'}
              </button>}
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
              sub={creditRejected ? `积分认证失败 ${creditRejected} 个` : ov.expired > 0 ? `${ov.expired} 个本地时间已过期` : `本地时间未过期；积分已验证 ${creditChecked} 个`}
              tone={creditRejected || ov.expired ? 'danger' : creditChecked < ov.file_count ? 'warn' : 'ok'}
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
                              <div style={{ marginTop: 4 }} title={a.credits_error}><Badge cls={creditStatusBadge(a.credits_status).cls}>{creditStatusBadge(a.credits_status).text}</Badge></div>
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
                <div className="empty">未发现已记录的问题；未查询的积分认证仍待验证</div>
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
                              <div style={{ marginTop: 4 }} title={a.credits_error}><Badge cls={creditStatusBadge(a.credits_status).cls}>{creditStatusBadge(a.credits_status).text}</Badge></div>
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
                              {a.cool_remaining_sec ? fmtDuration(a.cool_remaining_sec) : fmtISO(a.credits_error ? a.credits_at : a.last_err)}
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
  tone, to, buttonSlot,
}: {
  label: string
  to?: string
  value: string
  sub?: string
  tone?: 'ok' | 'warn' | 'danger'
  buttonSlot?: ReactNode
}) {
  const cls = tone === 'ok' ? 'text-ok' : tone === 'warn' ? 'text-warn' : tone === 'danger' ? 'text-danger' : ''
  return (
    <div className="stat">
      <div className="stat-label">{to ? <Link to={to}>{label}</Link> : label}</div>
      <div className={`stat-value ${cls}`}>{value}</div>
      {sub && <div className="stat-sub">{sub}</div>}
      {buttonSlot && <div>{buttonSlot}</div>}
    </div>
  )
}
