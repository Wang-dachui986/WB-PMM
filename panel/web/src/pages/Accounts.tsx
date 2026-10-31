// Accounts.tsx 账号管理：列表 / 筛选 / 单账号操作 / 批量任务 / 详情 / 导入导出。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, ApiError } from '../api'
import { beginMaintenance, desktopCommand, hasDesktopBridge, previewAuthFile, confirmAuthFile, cancelAuthPreview, type MaintenanceFlow, type MaintenanceAction, type ImportPreview, type CancelableOperation } from '../desktopBridge'
import type { Account, AccountProfile, SessionInfo } from '../types'
import {
  Alert,
  Badge,
  ConfirmDialog,
  coolKindText,
  displayName,
  accountRegion,
  observedCredits,
  Empty,
  fmtDuration,
  fmtISO,
  fmtNum,
  fmtTime,
  Modal,
  Spinner,
  creditStatusBadge, statusBadge,
} from '../ui'
import TaskProgress from './TaskProgress'

type Filter = 'all' | 'healthy' | 'cooling' | 'problem' | 'expiring'
type AccountTask = { id: string; flow: MaintenanceFlow | null }

export default function Accounts({ session }: { session: SessionInfo }) {
  const [accounts, setAccounts] = useState<Account[]>([])
  const [fileIssues, setFileIssues] = useState<string[]>([])
  const [gatewayOK, setGatewayOK] = useState(true)
  const [gatewayError, setGatewayError] = useState<string | undefined>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<{ kind: 'ok' | 'error' | 'warn'; text: string } | null>(null)

  const [searchParams] = useSearchParams()
  const [filter, setFilter] = useState<Filter>('all')
  const [region, setRegion] = useState('all')
  const [notes, setNotes] = useState<Record<string, string>>({})
  const [noteTarget, setNoteTarget] = useState<string | null>(null)
  const [noteDraft, setNoteDraft] = useState('')
  const [noteBusy, setNoteBusy] = useState(false)
  const [observedAt, setObservedAt] = useState<string>()
  useEffect(() => {
    const value = searchParams.get('status')
    setFilter(['healthy', 'cooling', 'problem', 'expiring'].includes(value ?? '') ? value as Filter : 'all')
  }, [searchParams])
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busyUid, setBusyUid] = useState<string | null>(null)

  const [detailUid, setDetailUid] = useState<string | null>(null)
  const [task, setTask] = useState<AccountTask | null>(null)
  const [showImport, setShowImport] = useState(false)
  const [deleteTargets, setDeleteTargets] = useState<Account[]>([])
  const [deleting, setDeleting] = useState(false)
  const desktop = hasDesktopBridge()
  const maintenance = useRef<MaintenanceFlow | null>(null)
  const mounted = useRef(true)
  const loadingRef = useRef(false)
  const [maintaining, setMaintaining] = useState(false)
  const ownsFlow = (flow: MaintenanceFlow | null) => mounted.current && maintenance.current === flow
  const finishMaintenance = useCallback(async (flow: MaintenanceFlow | null, cancel = false) => {
    if (!flow || maintenance.current !== flow) return
    if (cancel) await flow.cancel()
    else {
      try { await flow.end() }
      catch { await flow.cancel() } // Cancelled admission has no lease to end; wait for native cleanup.
    }
    if (maintenance.current === flow) {
      maintenance.current = null
      if (mounted.current) { setMaintaining(false); setBusyUid(null); setDeleting(false) }
    }
  }, [])
  const startMaintenance = (action: MaintenanceAction, uids: string[]): MaintenanceFlow | null => {
    if (!desktop) return null
    if (maintenance.current) throw new Error('账号维护仍在进行，请先完成或取消')
    const flow = beginMaintenance(action, uids)
    maintenance.current = flow
    setMaintaining(true)
    return flow
  }
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false; void finishMaintenance(maintenance.current, true).catch(() => undefined) }
  }, [finishMaintenance])


  const load = useCallback(async (silent = false) => {
    if (loadingRef.current) return
    loadingRef.current = true
    const loadingFlow = maintenance.current
    if (!silent) setLoading(true)
    try {
      const res = await api.accounts()
      if (!mounted.current || maintenance.current !== loadingFlow) return
      setAccounts(res.accounts ?? [])
      setObservedAt(new Date().toISOString())
      setFileIssues(res.file_issues ?? [])
      setGatewayOK(res.gateway_ok)
      setGatewayError(res.gateway_error)
      setError(null)
    } catch (err) {
      if (mounted.current && maintenance.current === loadingFlow) setError(err instanceof ApiError ? err.message : '加载账号失败')
    } finally {
      loadingRef.current = false
      if (mounted.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    const timer = setInterval(() => void load(true), 20_000)
    return () => clearInterval(timer)
  }, [load])

  useEffect(() => {
    if (!desktop) return
    let active = true
    void desktopCommand('preferences.get').then(value => { if (active) setNotes((value.notes ?? {}) as Record<string, string>) }).catch(() => { if (active) setError('本地备注读取失败，可重新进入此页重试') })
    return () => { active = false }
  }, [desktop])
  const copyUID = async (uid: string) => {
    try { await navigator.clipboard.writeText(uid); if (mounted.current) setNotice({ kind: 'ok', text: '完整 UID 已复制' }) }
    catch { if (mounted.current) setError('复制失败，请在详情中选择完整 UID') }
  }
  const saveNote = async () => {
    const uid = noteTarget
    if (!uid || noteBusy) return
    setNoteBusy(true)
    try {
      const value = await desktopCommand('preferences.update', { note: { uid, value: noteDraft } })
      if (mounted.current) { setNotes((value.notes ?? {}) as Record<string, string>); setNoteTarget(null) }
    } catch { if (mounted.current) setError('备注保存失败，请检查长度或原生连接') }
    finally { if (mounted.current) setNoteBusy(false) }
  }

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    return accounts.filter((a) => {
      if (desktop && region !== 'all' && accountRegion(a) !== region) return false
      if (q && !a.uid.toLowerCase().includes(q) && !a.nickname.toLowerCase().includes(q) && !(notes[a.uid] ?? '').toLowerCase().includes(q)) return false
      switch (filter) {
        case 'healthy':
          return a.status === 'healthy'
        case 'cooling':
          return a.status === 'cooling'
        case 'problem':
          return a.status === 'disabled' || a.status === 'token_expired' || a.status === 'missing_credential' || !!a.credits_error
        case 'expiring':
          return a.expired || a.needs_refresh
        default:
          return true
      }
    })
  }, [accounts, filter, query, region, notes, desktop])

  const allSelected = filtered.length > 0 && filtered.every((a) => selected.has(a.uid))
  const targetUIDs = useMemo(
    () => (selected.size > 0 ? [...selected] : []),
    [selected],
  )

  const toggleAll = () => {
    if (allSelected) {
      setSelected(new Set())
    } else {
      setSelected(new Set(filtered.map((a) => a.uid)))
    }
  }

  const toggleOne = (uid: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(uid)) next.delete(uid)
      else next.add(uid)
      return next
    })
  }

  /** Each operation keeps its own flow across HTTP, list reload, and cleanup. */
  const runSingle = async (a: Account, action: 'checkin' | 'refresh' | 'travel' | 'credits') => {
    if (desktop && (action === 'checkin' || action === 'travel')) return
    let flow: MaintenanceFlow | null = null
    try {
      flow = action === 'refresh' || action === 'credits' ? startMaintenance(action, [a.uid]) : null
      setBusyUid(a.uid)
      setNotice(null)
      const lease = await flow?.ready
      if (!ownsFlow(flow)) return
      if (action === 'checkin' || action === 'refresh' || action === 'travel') {
        const res = action === 'checkin' ? await api.accountCheckin(a.uid)
          : action === 'refresh' ? await api.accountRefresh(a.uid, lease) : await api.accountTravel(a.uid)
        if (!ownsFlow(flow)) return
        setNotice({ kind: res.ok ? 'ok' : 'error', text: `${displayName(a)}：${res.message}${res.reward ? `（+${res.reward}）` : ''}` })
      } else {
        const c = await api.accountCredits(a.uid, lease)
        if (!ownsFlow(flow)) return
        setNotice({ kind: 'ok', text: `${displayName(a)}：剩余 ${fmtNum(c.remain)} / 总量 ${fmtNum(c.size)}（${c.packages} 个套餐）` })
      }
      await load(true)
    } catch (err) {
      if (ownsFlow(flow)) setNotice({ kind: 'error', text: err instanceof Error ? err.message : '操作失败' })
    } finally {
      await finishMaintenance(flow).catch((err) => { if (ownsFlow(flow)) setNotice({ kind: 'error', text: err.message }) })
      if (!desktop && ownsFlow(flow)) setBusyUid(null)
    }
  }

  const runBatch = async (kind: 'checkin' | 'refresh' | 'travel' | 'credits') => {
    if (desktop && ((kind !== 'refresh' && kind !== 'credits') || targetUIDs.length < 1 || targetUIDs.length > 20)) {
      setNotice({ kind: 'warn', text: '请选择 1–20 个账号' }); return
    }
    let flow: MaintenanceFlow | null = null
    try {
      flow = kind === 'refresh' || kind === 'credits' ? startMaintenance(kind, targetUIDs) : null
      setNotice(null)
      const lease = await flow?.ready
      if (!ownsFlow(flow)) return
      const fn = kind === 'checkin' ? api.batchCheckin : kind === 'refresh' ? api.batchRefresh
        : kind === 'travel' ? api.batchTravel : api.batchCredits
      const result = await fn(targetUIDs, lease)
      if (ownsFlow(flow)) setTask({ id: result.id, flow })
    } catch (err) {
      if (ownsFlow(flow)) setNotice({ kind: 'error', text: err instanceof Error ? err.message : '任务提交失败' })
      await finishMaintenance(flow, true).catch(() => undefined)
    }
  }

  const doDelete = async () => {
    if (deleting || maintaining || deleteTargets.length === 0) return
    const targets = [...deleteTargets]
    let flow: MaintenanceFlow | null = null
    let removed = 0
    setDeleting(true)
    setNotice(null)
    try {
      flow = startMaintenance('delete', targets.map(a => a.uid))
      const lease = await flow?.ready
      if (!ownsFlow(flow)) return
      for (const target of targets) {
        if (!ownsFlow(flow)) break
        const res = await api.deleteAccount(target.uid, lease)
        if (!ownsFlow(flow)) return
        if (!res.ok) throw new Error(`${displayName(target)}：${res.message}`)
        removed++
        setSelected(previous => { const next = new Set(previous); next.delete(target.uid); return next })
        if (desktop) setNotes(previous => { const next = { ...previous }; delete next[target.uid]; return next })
      }
      if (ownsFlow(flow)) {
        setNotice({ kind: 'ok', text: `已删除 ${removed} 个账号凭证` })
        setDeleteTargets([])
        await load(true)
      }
    } catch (err) {
      if (ownsFlow(flow)) {
        setNotice({ kind: removed ? 'warn' : 'error', text: `已确认删除 ${removed}/${targets.length} 个；其余未确认或未执行。${err instanceof Error ? err.message : '删除失败'}` })
        setDeleteTargets([])
        if (removed > 0) await load(true)
      }
    } finally {
      await finishMaintenance(flow).catch(err => { if (ownsFlow(flow)) setNotice({ kind: 'error', text: `维护尚未结束：${err.message}，请点击取消维护重试` }) })
      if (ownsFlow(flow) || ownsFlow(null)) { setDeleting(false); setDeleteTargets([]) }
    }
  }

  const selectForDeletion = () => {
    const targets = accounts.filter(a => selected.has(a.uid))
    if (targets.length !== selected.size || targets.some(a => !a.has_file)) {
      setNotice({ kind: 'warn', text: '所选账号存在缺失凭证或列表已变化，请刷新后重新选择' }); return
    }
    if (targets.length < 1 || targets.length > 100) {
      setNotice({ kind: 'warn', text: '请选择 1–100 个账号后再删除' }); return
    }
    setDeleteTargets(targets)
  }

  const setEnabled = async (a: Account) => {
    setBusyUid(a.uid)
    try { await desktopCommand('account.setEnabled', { uid: a.uid, enabled: !!a.manual_disabled }); await load(true) }
    catch (error) { setNotice({ kind: 'error', text: error instanceof Error ? error.message : '操作失败' }) }
    finally { setBusyUid(null) }
  }
  const taskFinished = async (finished: AccountTask) => {
    if (!ownsFlow(finished.flow)) return
    try {
      await finishMaintenance(finished.flow)
      if (ownsFlow(null)) await load(true)
    } catch (error) {
      if (ownsFlow(finished.flow)) setNotice({ kind: 'error', text: error instanceof Error ? error.message : '结束维护失败' })
    }
  }
  const closeTask = async (closing?: AccountTask) => {
    // The banner may cancel the current flow; a task callback carries its original flow.
    const flow = closing ? closing.flow : maintenance.current
    if (!ownsFlow(flow) && !(closing && maintenance.current === null)) return
    try {
      await finishMaintenance(flow, true)
      if (!ownsFlow(null)) return
      setTask((current) => (closing ? current === closing : current?.flow === flow) ? null : current)
      await load(true)
    } catch (error) {
      if (ownsFlow(flow)) setNotice({ kind: 'error', text: error instanceof Error ? error.message : '取消失败' })
      throw error
    }
  }
  const writeDisabled = session.read_only || maintaining || deleting

  return (
    <>
      <div className="page-head">
        <div>
          <h1>账号管理</h1>
          <p>
            共 {accounts.length} 个账号
            {selected.size > 0 && <span className="text-accent"> · 已选 {selected.size} 个</span>}
          </p>
        </div>
        <div className="page-actions">
          <button className="btn" onClick={() => void load()} disabled={loading}>
            {loading && <Spinner />} 刷新
          </button>
          <button className="btn" onClick={() => setShowImport(true)} disabled={writeDisabled} title={writeDisabled ? '只读模式' : ''}>
            导入凭证
          </button>
          <Link className="btn btn-primary" to="/login">
            添加账号
          </Link>
        </div>
      </div>

      {session.read_only && <Alert kind="warn">服务端已开启只读模式，签到 / 刷新 / 删除等写操作已禁用。</Alert>}
      {maintaining && <Alert kind="info">账号维护进行中。<button className="btn" onClick={() => void closeTask().catch(() => undefined)}>取消维护</button></Alert>}
      {notice && (
        <Alert kind={notice.kind} onClose={() => setNotice(null)}>
          {notice.text}
        </Alert>
      )}
      {error && <Alert kind="error">{error}</Alert>}
      {!gatewayOK && (
        <Alert kind="warn">
          网关当前不可达{gatewayError ? `：${gatewayError}` : ''}。账号状态显示的是磁盘凭证信息，运行态为未知。
        </Alert>
      )}
      {fileIssues.length > 0 && (
        <Alert kind="warn">
          <strong>凭证目录存在无法解析的文件：</strong>
          <ul style={{ margin: '5px 0 0', paddingLeft: 18 }}>
            {fileIssues.map((i) => (
              <li key={i} className="mono" style={{ fontSize: 12 }}>
                {i}
              </li>
            ))}
          </ul>
        </Alert>
      )}

      {/* 批量操作区 */}
      <div className="card">
        <div className="card-head">
          <h2>批量操作</h2>
          <span className="hint">
            {selected.size > 0 ? `作用于已选 ${selected.size} 个账号` : desktop ? '请选择 1–20 个账号' : '未选择时作用于全部账号'}
          </span>
        </div>
        <div className="page-actions">
          {!desktop && <button className="btn" onClick={() => void runBatch('checkin')} disabled={writeDisabled || accounts.length === 0}>
            批量签到
          </button>}
          <button className="btn" onClick={() => void runBatch('refresh')} disabled={writeDisabled || accounts.length === 0}>
            批量刷新 Token
          </button>
          {!desktop && <button className="btn" onClick={() => void runBatch('travel')} disabled={writeDisabled || accounts.length === 0}>
            批量猫猫旅行
          </button>}
          <button className="btn" onClick={() => void runBatch('credits')} disabled={writeDisabled || accounts.length === 0}>
            批量查积分
          </button>
          <button className="btn btn-danger" onClick={selectForDeletion}
            disabled={writeDisabled || deleting || selected.size === 0 || (!desktop && !session.dangerous_ops)}>
            批量删除{selected.size > 0 ? `（${selected.size}）` : ''}
          </button>
          {selected.size > 0 && (
            <button className="btn btn-ghost" onClick={() => setSelected(new Set())}>
              清除选择
            </button>
          )}
        </div>
        <div className="field" style={{ marginTop: 13, marginBottom: 0 }}>
          <div className="desc">
            {desktop ? '批量任务在维护期间执行。取消或离开页面将停止任务，排空后恢复原先的 API 状态。' : '批量任务在后台串行执行，可关闭窗口后继续。'}
          </div>
        </div>
      </div>

      {/* 账号列表 */}
      <div className="card">
        <div className="card-head">
          <h2>账号列表</h2>{desktop && <span className="hint">运行状态观测：{observedAt ? fmtISO(observedAt) : '未读取'}</span>}
          <div className="page-actions account-filters">
            <input
              type="text"
              aria-label="搜索账号"
              placeholder="搜索 uid / 昵称 / 备注…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              style={{ width: 190 }}
            />
            {desktop && <select aria-label="账号区域" value={region} onChange={event => setRegion(event.target.value)}><option value="all">全部区域</option><option value="cn">CN</option><option value="global">Global</option><option value="unknown">未知区域</option></select>}
            <select aria-label="账号状态" value={filter} onChange={(e) => setFilter(e.target.value as Filter)} style={{ width: 150 }}>
              <option value="all">全部</option>
              <option value="healthy">网关可调度</option>
              <option value="cooling">仅冷却中</option>
              <option value="problem">仅异常</option>
              <option value="expiring">Token 需刷新</option>
            </select>
          </div>
        </div>

        {loading && accounts.length === 0 ? (
          <Spinner label="加载中…" />
        ) : filtered.length === 0 ? (
          <Empty>
            {accounts.length === 0 ? (
              <>
                还没有任何账号。
                <div style={{ marginTop: 10 }}>
                  <Link className="btn btn-primary btn-sm" to="/login">
                    添加第一个账号
                  </Link>
                </div>
              </>
            ) : (
              '没有符合筛选条件的账号'
            )}
          </Empty>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th style={{ width: 34 }}>
                    <input type="checkbox" checked={allSelected} onChange={toggleAll} aria-label="全选" />
                  </th>
                  <th>账号</th>
                  <th>状态</th>
                  <th className="num">积分</th>
                  <th className="num">成功 / 失败</th>
                  <th>Token 有效期</th>
                  <th>最近活动</th>
                  <th style={{ minWidth: 210 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((a) => {
                  const b = statusBadge(a.status)
                  const busy = busyUid === a.uid
                  return (
                    <tr key={a.uid}>
                      <td>
                        <input
                          type="checkbox"
                          checked={selected.has(a.uid)}
                          onChange={() => toggleOne(a.uid)}
                          aria-label={`选择 ${a.uid}`}
                        />
                      </td>
                      <td>
                        <button
                          className="btn btn-ghost btn-sm"
                          style={{ padding: 0, color: 'var(--text)' }}
                          onClick={() => setDetailUid(a.uid)}
                          title="查看详情"
                        >
                          {displayName(a, notes[a.uid])}
                        </button>
                        <div className="mono text-faint" style={{ fontSize: 11 }}>
                          {a.uid.slice(0, 8)}
                          {desktop && <> · {({ cn: 'CN', global: 'Global', unknown: '未知区域' })[accountRegion(a)]} <button className="btn btn-ghost btn-sm" onClick={() => void copyUID(a.uid)}>复制 UID</button>
                            <button className="btn btn-ghost btn-sm" disabled={maintaining || noteBusy} onClick={() => { setNoteTarget(a.uid); setNoteDraft(notes[a.uid] ?? '') }}>备注</button></>}
                          {a.in_gateway && a.in_flight > 0 && (
                            <span className="text-accent"> · 在途 {a.in_flight}</span>
                          )}
                        </div>
                      </td>
                      <td>
                        <Badge cls={b.cls}>{b.text}</Badge>
                        <div style={{ marginTop: 4 }} title={a.credits_error}><Badge cls={creditStatusBadge(a.credits_status).cls}>{creditStatusBadge(a.credits_status).text}</Badge></div>
                        {a.cooling && (
                          <div className="text-faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                            {coolKindText(a.cool_kind)} · 剩 {fmtDuration(a.cool_remaining_sec)}
                          </div>
                        )}
                        {a.manual_disabled && <div className="text-danger">已手动暂停{a.manual_reason ? ` · ${a.manual_reason}` : ''}</div>}
                        {a.disabled && a.reason && (
                          <div className="text-faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                            {a.reason}
                          </div>
                        )}
                        {a.status === 'token_expired' && (
                          <div className="text-faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                            需刷新或重新登录
                          </div>
                        )}
                      </td>
                      <td className="num">{desktop ? (observedCredits(a) === undefined ? '未知' : fmtNum(observedCredits(a))) : fmtNum(a.live_credits ?? a.credits)}
                        {desktop && observedCredits(a) !== undefined && <div className="text-faint">观测：{fmtISO(a.credits_at)}</div>}</td>
                      <td className="num text-dim" style={{ fontSize: 12 }}>
                        {fmtNum(a.success_count)} / {a.err_total > 0 ? <span className="text-danger">{a.err_total}</span> : '0'}
                      </td>
                      <td style={{ fontSize: 12 }}>
                        {a.expired ? (
                          <span className="text-danger">已过期</span>
                        ) : a.needs_refresh ? (
                          <span className="text-warn">即将过期</span>
                        ) : (
                          <span className="text-ok">有效</span>
                        )}
                        <div className="text-faint" style={{ fontSize: 11 }}>
                          {fmtTime(a.expires_at)}
                        </div>
                      </td>
                      <td style={{ fontSize: 12 }} className="text-dim">
                        {fmtISO(a.last_success || a.last_err)}
                      </td>
                      <td>
                        <div className="page-actions" style={{ gap: 5 }}>
                          {!desktop && <button
                            className="btn btn-sm"
                            disabled={busy || writeDisabled}
                            onClick={() => void runSingle(a, 'checkin')}
                            title="签到并刷新余额"
                          >
                            签到
                          </button>}
                          <button
                            className="btn btn-sm"
                            disabled={busy || writeDisabled}
                            onClick={() => void runSingle(a, 'refresh')}
                            title="刷新 access token"
                          >
                            刷新
                          </button>
                          {!desktop && <button
                            className="btn btn-sm"
                            disabled={busy || writeDisabled}
                            onClick={() => void runSingle(a, 'travel')}
                            title="推进一趟猫猫旅行"
                          >
                            猫猫
                          </button>}
                          <button className="btn btn-sm" disabled={busy || maintaining} onClick={() => void runSingle(a, 'credits')} title="查询余额">
                            积分
                          </button>
                          {desktop && <>
                            <Link className="btn btn-sm" to={`/login?uid=${encodeURIComponent(a.uid)}`}>重新授权</Link>
                            <button className="btn btn-sm" disabled={busy || maintaining || !a.in_gateway} onClick={() => void setEnabled(a)}>{a.manual_disabled ? '恢复' : '暂停'}</button>
                          </>}
                          <button
                            className="btn btn-sm btn-danger"
                            disabled={busy || writeDisabled || (!desktop && !session.dangerous_ops) || !a.has_file}
                            onClick={() => setDeleteTargets([a])}
                            title={
                              !a.has_file
                                ? '该账号在网关池中但没有本地凭证文件，无法删除'
                                : !desktop && !session.dangerous_ops
                                  ? '需在服务端开启 dangerous_ops 才能删除账号'
                                  : session.read_only
                                    ? '只读模式'
                                    : '删除该账号凭证文件'
                            }
                          >
                            删除
                          </button>
                          {busy && <Spinner />}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {detailUid && <AccountDetail uid={detailUid} runtime={accounts.find(account => account.uid === detailUid)} runtimeAt={observedAt} gatewayOK={gatewayOK} note={notes[detailUid]} onClose={() => setDetailUid(null)} />}
      {noteTarget && <Modal title="本地账号备注" onClose={() => { if (!noteBusy) setNoteTarget(null) }} dismissDisabled={noteBusy} footer={<><button className="btn" disabled={noteBusy} onClick={() => setNoteTarget(null)}>取消</button><button className="btn btn-primary" disabled={noteBusy || new TextEncoder().encode(noteDraft).length > 256} onClick={() => void saveNote()}>保存备注</button></>}>
        <p className="mono">{noteTarget}</p><div className="field"><label htmlFor="account-note">备注（最多 256 字节）</label><input id="account-note" value={noteDraft} onChange={event => setNoteDraft(event.target.value)} /></div><p className="desc">仅本机保存，留空可清除；不修改上游昵称。</p></Modal>}

      {task && (
        <TaskProgress
          key={task.id}
          taskId={task.id}
          onClose={() => closeTask(task)}
          onCancel={desktop ? () => closeTask(task) : undefined}
          onFinished={() => taskFinished(task)}
        />
      )}

      {showImport && (
        <ImportDialog
          onClose={() => setShowImport(false)}
          onDone={(msg) => {
            setShowImport(false)
            setNotice({ kind: 'ok', text: msg })
            void load(true)
          }}
          onError={(msg) => setNotice({ kind: 'error', text: msg })}
        />
      )}

      {deleteTargets.length > 0 && (
        <ConfirmDialog
          title={deleteTargets.length > 1 ? '批量删除账号凭证' : '删除账号凭证'}
          danger
          confirmText={deleteTargets.length > 1 ? `确认删除 ${deleteTargets.length} 个账号` : '确认删除'}
          busy={deleting}
          onCancel={() => setDeleteTargets([])}
          onConfirm={() => void doDelete()}
          message={<>
            <p>即将删除以下 {deleteTargets.length} 个账号的本地凭证，请核对名称和 UID：</p>
            <ul style={{ maxHeight: 180, overflowY: 'auto' }}>{deleteTargets.map(a => (
              <li key={a.uid}><strong>{displayName(a)}</strong> <span className="mono">{a.uid}</span></li>
            ))}</ul>
            <p>删除后需重新授权才能恢复这些本地凭证。{desktop ? '维护完成后按原有状态恢复 API。' : '网关需重启才能移出账号池。'}遇到失败会停止，未确认删除的账号保留选择。</p>
          </>}
        />
      )}
    </>
  )
}

/** AccountDetail 账号详情弹窗：实时积分、猫档案、冷却/熔断细节。 */
function AccountDetail({ uid, onClose, runtime, runtimeAt, gatewayOK, note }: {
  uid: string; onClose: () => void; runtime?: Account; runtimeAt?: string; gatewayOK: boolean; note?: string
}) {
  const desktop = hasDesktopBridge()
  const [profile, setProfile] = useState<AccountProfile | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    api
      .account(uid, desktop)
      .then((p) => {
        if (!cancelled) setProfile(p)
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof ApiError ? err.message : '加载详情失败')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [uid, desktop])

  const cached = profile?.account
  const current = runtime?.uid === uid && runtime.in_gateway && gatewayOK ? runtime : undefined
  const a = desktop && cached && current ? { ...cached,
    status: current.status, cooling: current.cooling, cool_kind: current.cool_kind, cool_remaining_sec: current.cool_remaining_sec,
    disabled: current.disabled, manual_disabled: current.manual_disabled, manual_reason: current.manual_reason, reason: current.reason,
    in_flight: current.in_flight, breaker_fails: current.breaker_fails, breaker_until: current.breaker_until, soft_streak: current.soft_streak,
    success_count: current.success_count, err_total: current.err_total, last_success: current.last_success, last_err: current.last_err,
  } : cached

  return (
    <Modal title={`账号详情 · ${a ? displayName(a, note) : uid.slice(0, 8)}`} onClose={onClose} wide>
      {loading && <Spinner label={desktop ? "正在读取本机缓存…" : "正在查询上游…"} />}
      {error && <Alert kind="error">{error}</Alert>}
      {!desktop && profile?.upstream_error && <Alert kind="warn">上游查询失败：{profile.upstream_error}</Alert>}

      {a && (
        <>
          <div className="grid grid-stats" style={{ marginBottom: 16 }}>
            <div className="stat">
              <div className="stat-label">剩余积分</div>
              <div className="stat-value small">
                {desktop ? (observedCredits(a) === undefined ? '未知' : fmtNum(observedCredits(a))) : profile?.credits ? fmtNum(profile.credits.remain) : fmtNum(a.live_credits ?? a.credits)}
              </div>
              {desktop && observedCredits(a) !== undefined && <div className="stat-sub">缓存观测：{fmtISO(a.credits_at)}</div>}
              {profile?.credits && <div className="stat-sub">总量 {fmtNum(profile.credits.size)}</div>}
              {profile?.credits_error && <div className="stat-sub text-danger">查询失败</div>}
            </div>
            <div className="stat">
              <div className="stat-label">累计成功</div>
              <div className={`stat-value small ${desktop && !current ? 'text-warn' : 'text-ok'}`}>{desktop && !current ? '未知' : fmtNum(a.success_count)}</div>
              <div className="stat-sub">失败 {desktop && !current ? '未知' : fmtNum(a.err_total)}</div>
            </div>
            <div className="stat">
              <div className="stat-label">在途请求</div>
              <div className="stat-value small">{desktop && !current ? '未知' : a.in_flight}</div>
              <div className="stat-sub">熔断计数 {desktop && !current ? '未知' : a.breaker_fails}</div>
            </div>
            {!desktop && <div className="stat">
              <div className="stat-label">猫猫旅行</div>
              <div className="stat-value small" style={{ fontSize: 15 }}>
                {profile?.buddy ? profile.buddy.name || '已领养' : profile?.buddy_error ? '查询失败' : '未领养'}
              </div>
              <div className="stat-sub">
                {profile?.travel
                  ? `状态 ${profile.travel.state}${profile.travel.daily_limit_reached ? '（今日已派出）' : ''}`
                  : '—'}
              </div>
            </div>}
          </div>

          <dl className="kv">
            <dt>UID</dt>
            <dd className="mono">{a.uid}</dd>
            {desktop && <><dt>本地备注</dt><dd>{note || '未设置'}</dd><dt>区域</dt><dd>{({ cn: 'CN', global: 'Global', unknown: '未知' })[accountRegion(a)]}</dd><dt>运行状态观测</dt><dd>{current ? fmtISO(runtimeAt) : '无可用网关快照'}</dd></>}
            <dt>昵称</dt>
            <dd>{a.nickname || '—'}</dd>
            <dt>企业 ID</dt>
            <dd className="mono">{a.enterprise_id || '—'}</dd>
            <dt>Domain</dt>
            <dd className="mono">{a.domain || '—'}</dd>
            <dt>凭证文件</dt>
            <dd className="mono">{a.file_name || '（无文件，仅网关内存）'}</dd>
            <dt>Token 过期</dt>
            <dd>{fmtTime(a.expires_at)}</dd>
            <dt>Refresh Token</dt>
            <dd>{a.has_refresh_token ? '存在' : '缺失（无法刷新，需重登）'}</dd>
            <dt>网关状态</dt>
            <dd>
              <Badge cls={statusBadge(a.status).cls}>{statusBadge(a.status).text}</Badge>
              {a.reason && <span className="text-faint"> · {a.reason}</span>}
            </dd>
            {a.cooling && (
              <>
                <dt>冷却类型</dt>
                <dd>
                  {coolKindText(a.cool_kind)} · 剩余 {fmtDuration(a.cool_remaining_sec)}
                  {a.soft_streak ? ` · 连续软限流 ${a.soft_streak} 次` : ''}
                </dd>
                <dt>冷却截止</dt>
                <dd>{fmtISO(a.breaker_until) !== '—' ? fmtISO(a.breaker_until) : fmtTime(a.expires_at)}</dd>
              </>
            )}
            <dt>最近成功</dt>
            <dd>{fmtISO(a.last_success)}</dd>
            <dt>最近失败</dt>
            <dd>{fmtISO(a.last_err)}</dd>
          </dl>
        </>
      )}

      <div className="modal-foot">
        <button className="btn" onClick={onClose}>
          关闭
        </button>
      </div>
    </Modal>
  )
}

type ImportDialogProps = { onClose: () => void; onDone: (message: string) => void; onError: (message: string) => void }
function ImportDialog(props: ImportDialogProps) {
  return hasDesktopBridge() ? <DesktopImportDialog {...props} /> : <WebImportDialog {...props} />
}

function DesktopImportDialog({ onClose, onDone }: ImportDialogProps) {
  const [preview, setPreview] = useState<ImportPreview | null>(null)
  const [overwrite, setOverwrite] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const active = useRef(true)
  const operation = useRef<CancelableOperation<unknown> | null>(null)
  const currentPreview = useRef<ImportPreview | null>(null)
  const clear = async () => {
    const pending = operation.current
    if (pending) { await pending.cancel(); if (operation.current === pending) operation.current = null }
    const value = currentPreview.current
    if (value) { await cancelAuthPreview(value.preview_id); if (currentPreview.current === value) currentPreview.current = null }
  }
  useEffect(() => {
    active.current = true
    return () => { active.current = false; void clear().catch(() => undefined) }
  }, [])
  const choose = async () => {
    setBusy(true); setError(null); setOverwrite(false)
    try {
      await clear()
      if (!active.current) return
      setPreview(null)
      const pending = previewAuthFile(); operation.current = pending
      const value = await pending.ready
      if (!active.current) { await cancelAuthPreview(value.preview_id); return }
      currentPreview.current = value; setPreview(value)
    } catch (error) { if (active.current) setError(error instanceof Error ? error.message : '无法读取凭证文件') }
    finally { operation.current = null; if (active.current) setBusy(false) }
  }
  const submit = async () => {
    const value = currentPreview.current
    if (!value || (value.overwrite_required && !overwrite)) return
    setBusy(true); setError(null)
    const pending = confirmAuthFile(value.preview_id, overwrite)
    operation.current = pending; currentPreview.current = null; setPreview(null)
    try { const result = await pending.ready; if (active.current && result.ok) onDone('账号凭证已导入') }
    catch (error) { if (active.current) setError(error instanceof Error ? error.message : '导入失败，请重新选择文件') }
    finally { operation.current = null; if (active.current) setBusy(false) }
  }
  const close = async () => {
    active.current = false; setBusy(true)
    try { await clear(); onClose() }
    catch (error) { active.current = true; setBusy(false); setError(error instanceof Error ? error.message : '取消尚未完成，请重试') }
  }
  return <Modal title="导入账号凭证" onClose={() => void close()} wide footer={<>
    <button className="btn" onClick={() => void close()}>取消</button>
    <button className="btn btn-primary" disabled={busy || !preview || (preview.overwrite_required && !overwrite)} onClick={() => void submit()}>{busy && <Spinner />}确认导入</button>
  </>}>
    {error && <Alert kind="error">{error}</Alert>}
    <div className="field"><button className="btn" disabled={busy} onClick={() => void choose()}>选择凭证 JSON 文件</button></div>
    <div className="desc">文件最大 1 MiB。请核对账号与区域后确认导入。</div>
    {preview && <>
      <dl className="kv"><dt>UID</dt><dd className="mono">{preview.uid}</dd><dt>区域</dt><dd>{preview.region === 'cn' ? '国内' : '海外'}</dd><dt>Token 过期</dt><dd>{fmtTime(preview.expires_at)}</dd></dl>
      {preview.overwrite_required && <label><input type="checkbox" checked={overwrite} onChange={(event) => setOverwrite(event.target.checked)} /> 已存在同区域账号，确认覆盖其凭证</label>}
    </>}
  </Modal>
}

/** ImportDialog 手工导入凭证（从别的机器迁移 / 复用 login.sh 产物）。 */
function WebImportDialog({
  onClose,
  onDone,
  onError,
}: {
  onClose: () => void
  onDone: (msg: string) => void
  onError: (msg: string) => void
}) {
  const [raw, setRaw] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    setError(null)
    setBusy(true)
    try {
      const res = await api.importAccount({ raw_json: raw })
      if (res.ok) onDone(res.message)
      else setError(res.message)
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : '导入失败'
      setError(msg)
      onError(msg)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="导入账号凭证"
      onClose={onClose}
      dismissDisabled={busy}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button className="btn btn-primary" onClick={() => void submit()} disabled={busy || !raw.trim()}>
            {busy ? <Spinner /> : null}
            导入
          </button>
        </>
      }
    >
      {error && <Alert kind="error">{error}</Alert>}
      <div className="field">
        <label>粘贴凭证 JSON</label>
        <textarea
          rows={12}
          value={raw}
          onChange={(e) => setRaw(e.target.value)}
          placeholder={`支持两种格式：

1) workbuddy2api / 插件的嵌套格式（推荐，直接 cat auths/workbuddy-*.json 的内容）：
{
  "account": { "uid": "...", "enterpriseId": "", "nickname": "..." },
  "auth": { "accessToken": "...", "refreshToken": "...", "expiresAt": 1794289203, "domain": "copilot.tencent.com" }
}

2) 扁平格式：
{ "accessToken": "...", "refreshToken": "...", "uid": "...", "nickname": "..." }`}
        />
      </div>
      <div className="desc">
        导入后需重启网关容器，账号才会进入账号池（可在「系统」页一键重启）。若无 refreshToken，该账号在 token
        过期后必须重新登录。
      </div>
    </Modal>
  )
}
