// ConfigPage.tsx 网关 config.json 在线编辑：表单模式 + JSON 源码模式。
//
// 关键设计：表单读写的是**完整原始文档对象**（只改动已知字段），
// 因此配置文件里 GUi 不认识的自定义字段会被原样保留，不会因为一次保存被抹掉。
import { useCallback, useContext, useEffect, useRef, useState } from 'react'
import { Link, UNSAFE_NavigationContext } from 'react-router-dom'
import { desktopCommand, DesktopError, hasDesktopBridge } from '../desktopBridge'
import { api, ApiError } from '../api'
import type { ConfigMeta, DesktopConfigView, SessionInfo } from '../types'
import { Alert, ConfirmDialog, fmtISO, Spinner } from '../ui'

type Doc = Record<string, unknown>

/** 按路径读写嵌套字段。 */
function getPath(doc: Doc, path: string): unknown {
  return path.split('.').reduce<unknown>((acc, key) => {
    if (acc && typeof acc === 'object') return (acc as Doc)[key]
    return undefined
  }, doc)
}

/** 设置嵌套字段（不可变更新，保证 React 看到新对象）。 */
function setPath(doc: Doc, path: string, value: unknown): Doc {
  const keys = path.split('.')
  const clone: Doc = { ...doc }
  let cur: Doc = clone
  for (let i = 0; i < keys.length - 1; i++) {
    const k = keys[i]
    const next = cur[k]
    cur[k] = next && typeof next === 'object' ? { ...(next as Doc) } : {}
    cur = cur[k] as Doc
  }
  cur[keys[keys.length - 1]] = value
  return clone
}

/** 数字字段：空串表示"未设置"，返回 undefined 以便从配置中删除。 */
function numOrUndefined(v: string): number | undefined {
  if (v.trim() === '') return undefined
  const n = Number(v)
  return Number.isFinite(n) ? n : undefined
}

/** 小时数组的文本互转：逗号/空格分隔。 */
function hoursToText(v: unknown): string {
  if (Array.isArray(v)) return v.join(', ')
  return ''
}

function textToHours(text: string): number[] | undefined {
  const parts = text
    .split(/[,，\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  if (parts.length === 0) return undefined
  const nums = parts.map((s) => parseInt(s, 10))
  if (nums.some((n) => Number.isNaN(n))) return undefined
  return nums
}

export default function ConfigPage({ session }: { session: SessionInfo }) {
  return hasDesktopBridge() ? <DesktopConfigPage session={session} /> : <WebConfigPage session={session} />
}

function WebConfigPage({ session }: { session: SessionInfo }) {
  const [doc, setDoc] = useState<Doc | null>(null)
  const [meta, setMeta] = useState<ConfigMeta | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [tab, setTab] = useState<'form' | 'json'>('form')
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState<string | null>(null)
  const [showApiKey, setShowApiKey] = useState(false)
  const [confirmReset, setConfirmReset] = useState(false)
  const [resetting, setResetting] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.config()
      setDoc(res.config)
      setMeta(res.meta)
      setJsonText(JSON.stringify(res.config, null, 2))
      setError(null)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '加载配置失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** 表单字段更新：同时同步 JSON 文本，保证两个视图一致。 */
  const update = (path: string, value: unknown) => {
    setDoc((prev) => {
      if (!prev) return prev
      const next = setPath(prev, path, value)
      setJsonText(JSON.stringify(next, null, 2))
      return next
    })
  }

  const saveForm = async () => {
    if (!doc) return
    setSaving(true)
    setNotice(null)
    try {
      const res = await api.saveConfig(doc)
      setNotice(res.message)
      setError(null)
      // 保存后重新读取：首次保存会生成备份文件，meta 里的 backup_path 随之出现
      //（「恢复备份」按钮依赖它）；顺带同步文件修改时间。
      await reloadMeta()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  /** reloadMeta 只刷新元信息与已保存内容，用于保存成功后同步状态。 */
  const reloadMeta = async () => {
    try {
      const res = await api.config()
      setMeta(res.meta)
      // 用服务端返回的规范化内容覆盖本地（例如 JSON 缩进、字段顺序）。
      setDoc(res.config)
      setJsonText(JSON.stringify(res.config, null, 2))
    } catch {
      // 刷新失败不影响"已保存"这一事实，保留当前视图。
    }
  }

  const saveJSON = async () => {
    setJsonError(null)
    let parsed: Doc
    try {
      parsed = JSON.parse(jsonText) as Doc
    } catch (err) {
      setJsonError(err instanceof Error ? err.message : 'JSON 语法错误')
      return
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      setJsonError('配置顶层必须是一个 JSON 对象')
      return
    }
    setSaving(true)
    setNotice(null)
    try {
      const res = await api.saveConfig(parsed)
      setNotice(res.message)
      setError(null)
      await reloadMeta()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  const doReset = async () => {
    setResetting(true)
    try {
      const res = await api.resetConfig()
      setConfirmReset(false)
      setNotice(res.message)
      await load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '恢复失败')
    } finally {
      setResetting(false)
    }
  }

  if (loading && !doc) return <Spinner label="正在读取网关配置…" />

  const writeDisabled = session.read_only
  const str = (path: string) => {
    const v = getPath(doc ?? {}, path)
    return v === undefined || v === null ? '' : String(v)
  }
  const bool = (path: string) => getPath(doc ?? {}, path) === true
  const numStr = (path: string) => {
    const v = getPath(doc ?? {}, path)
    return v === undefined || v === null ? '' : String(v)
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>网关配置</h1>
          <p>
            在线编辑 <span className="mono">{meta?.path || 'config.json'}</span>
            {meta?.mod_time && <span className="text-faint"> · 最后修改 {fmtISO(meta.mod_time)}</span>}
          </p>
        </div>
        <div className="page-actions">
          <button className="btn" onClick={() => void load()} disabled={loading}>
            {loading && <Spinner />} 重新读取
          </button>
          {meta?.backup_path && (
            <button
              className="btn btn-danger"
              onClick={() => setConfirmReset(true)}
              disabled={writeDisabled || !session.dangerous_ops}
              title={!session.dangerous_ops ? '需在服务端开启 dangerous_ops' : '从首次备份恢复'}
            >
              恢复备份
            </button>
          )}
        </div>
      </div>

      {writeDisabled && <Alert kind="warn">服务端已开启只读模式，配置无法保存。</Alert>}
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
      {meta && !meta.exists && (
        <Alert kind="warn">
          配置文件尚不存在（{meta.path}）。保存后会以当前内容创建；网关首次启动时用默认值。
        </Alert>
      )}

      <Alert kind="info">
        <strong>修改后需要重启网关才生效。</strong> 保存只会写文件，不会自动重启。
        {session.dangerous_ops ? (
          <>
            {' '}可在「系统」页一键重启容器。
          </>
        ) : (
          <>
            {' '}（自动重启能力需在服务端开启 <span className="mono">dangerous_ops</span>）
          </>
        )}
        {meta?.backup_path ? (
          <div style={{ marginTop: 5, fontSize: 12.5 }}>
            首次保存前已自动备份原始文件到 <span className="mono">{meta.backup_path}</span>
            {meta.backup_at ? `（${fmtISO(meta.backup_at)}）` : ''}。随时可用右上角「恢复备份」回滚。
          </div>
        ) : (
          <div style={{ marginTop: 5, fontSize: 12.5 }}>
            尚未生成备份：首次保存时会自动把当前的 config.json 备份一份，之后可随时回滚。
          </div>
        )}
      </Alert>

      <div className="tabs">
        <button className={`tab ${tab === 'form' ? 'active' : ''}`} onClick={() => setTab('form')}>
          表单编辑
        </button>
        <button className={`tab ${tab === 'json' ? 'active' : ''}`} onClick={() => setTab('json')}>
          JSON 源码
        </button>
      </div>

      {tab === 'form' && doc && (
        <>
          <div className="card">
            <div className="card-head">
              <h2>基础设置</h2>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>监听地址 listen</label>
                <input type="text" value={str('listen')} onChange={(e) => update('listen', e.target.value)} placeholder=":7863" />
                <div className="desc">网关 HTTP 监听地址，冒号开头表示所有网卡。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>API 密钥 api_key</label>
                <div style={{ display: 'flex', gap: 6 }}>
                  <input
                    type={showApiKey ? 'text' : 'password'}
                    value={str('api_key')}
                    onChange={(e) => update('api_key', e.target.value)}
                    placeholder="留空 = 不鉴权（公网务必设置）"
                  />
                  <button className="btn shrink" onClick={() => setShowApiKey((v) => !v)} type="button">
                    {showApiKey ? '隐藏' : '显示'}
                  </button>
                </div>
                <div className="desc">
                  客户端调用 <span className="mono">/v1/chat/completions</span> 时需带
                  <span className="mono"> Authorization: Bearer &lt;api_key&gt;</span>。留空则任何人可用。
                </div>
              </div>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>凭证目录 auth_dir</label>
                <input type="text" value={str('auth_dir')} onChange={(e) => update('auth_dir', e.target.value)} placeholder="./auths" />
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>状态文件 state_file</label>
                <input type="text" value={str('state_file')} onChange={(e) => update('state_file', e.target.value)} placeholder="./data/state.json" />
              </div>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <h2>定时任务</h2>
              <span className="hint">时间按容器时区（compose 默认 Asia/Shanghai）</span>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>签到小时 checkin_hours</label>
                <input
                  type="text"
                  value={hoursToText(getPath(doc, 'schedule.checkin_hours'))}
                  onChange={(e) => update('schedule.checkin_hours', textToHours(e.target.value))}
                  placeholder="9, 21"
                />
                <div className="desc">逗号分隔的整点（0-23）。签到同时会推进猫猫旅行。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>保活小时 keepalive_hours</label>
                <input
                  type="text"
                  value={hoursToText(getPath(doc, 'schedule.keepalive_hours'))}
                  onChange={(e) => update('schedule.keepalive_hours', textToHours(e.target.value))}
                  placeholder="22"
                />
                <div className="desc">在该整点刷新全部账号的 token。</div>
              </div>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label className="checkbox">
                  <input
                    type="checkbox"
                    checked={bool('schedule.checkin_enabled')}
                    onChange={(e) => update('schedule.checkin_enabled', e.target.checked)}
                  />
                  启用签到
                </label>
                <div className="desc">关闭后签到与猫猫旅行都会停摆（旅行搭签到便车）。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label className="checkbox">
                  <input
                    type="checkbox"
                    checked={bool('schedule.keepalive_enabled')}
                    onChange={(e) => update('schedule.keepalive_enabled', e.target.checked)}
                  />
                  启用 Token 保活
                </label>
              </div>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <h2>账号池与冷却</h2>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>软限流冷却基数 cooldown.soft_rate</label>
                <input type="text" value={str('cooldown.soft_rate')} onChange={(e) => update('cooldown.soft_rate', e.target.value)} placeholder="600s" />
                <div className="desc">429/限流文案触发的冷却时长，连续触发按 2 倍指数退避。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>软冷却退避封顶 cooldown.soft_rate_max</label>
                <input type="text" value={str('cooldown.soft_rate_max')} onChange={(e) => update('cooldown.soft_rate_max', e.target.value)} placeholder="2h" />
              </div>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>单账号最大在途 pool.max_in_flight</label>
                <input
                  type="text"
                  value={numStr('pool.max_in_flight')}
                  onChange={(e) => update('pool.max_in_flight', numOrUndefined(e.target.value))}
                  placeholder="3"
                />
                <div className="desc">0 = 不限制并发。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>熔断阈值 pool.breaker_threshold</label>
                <input
                  type="text"
                  value={numStr('pool.breaker_threshold')}
                  onChange={(e) => update('pool.breaker_threshold', numOrUndefined(e.target.value))}
                  placeholder="3"
                />
                <div className="desc">连续失败达到该次数即熔断。</div>
              </div>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>熔断基础时长 pool.breaker_cooldown</label>
                <input type="text" value={str('pool.breaker_cooldown')} onChange={(e) => update('pool.breaker_cooldown', e.target.value)} placeholder="30m" />
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>熔断退避封顶 pool.breaker_cooldown_max</label>
                <input type="text" value={str('pool.breaker_cooldown_max')} onChange={(e) => update('pool.breaker_cooldown_max', e.target.value)} placeholder="6h" />
              </div>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>闲置补偿速率 pool.idle_weight_per_hour</label>
                <input
                  type="text"
                  value={numStr('pool.idle_weight_per_hour')}
                  onChange={(e) => update('pool.idle_weight_per_hour', numOrUndefined(e.target.value))}
                  placeholder="0.5"
                />
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>闲置补偿封顶 pool.idle_weight_max</label>
                <input
                  type="text"
                  value={numStr('pool.idle_weight_max')}
                  onChange={(e) => update('pool.idle_weight_max', numOrUndefined(e.target.value))}
                  placeholder="5.0"
                />
              </div>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <h2>上游超时</h2>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label>短 RPC 总超时（秒）</label>
                <input
                  type="text"
                  value={numStr('upstream.timeout_seconds')}
                  onChange={(e) => update('upstream.timeout_seconds', numOrUndefined(e.target.value))}
                  placeholder="120"
                />
                <div className="desc">刷新 token / 签到 / 余额 / 模型列表的硬上限。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>聊天首字节超时（秒）</label>
                <input
                  type="text"
                  value={numStr('upstream.header_timeout_seconds')}
                  onChange={(e) => update('upstream.header_timeout_seconds', numOrUndefined(e.target.value))}
                  placeholder="120"
                />
                <div className="desc">超时即换号重发；留空回落 timeout_seconds。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>聊天流空闲超时（秒）</label>
                <input
                  type="text"
                  value={numStr('upstream.idle_timeout_seconds')}
                  onChange={(e) => update('upstream.idle_timeout_seconds', numOrUndefined(e.target.value))}
                  placeholder="300"
                />
                <div className="desc">持续吐数据不受影响，静默超时才断流。</div>
              </div>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <h2>会话粘性与功能开关</h2>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 1 }}>
                <label className="checkbox">
                  <input
                    type="checkbox"
                    checked={bool('session_sticky.enabled')}
                    onChange={(e) => update('session_sticky.enabled', e.target.checked)}
                  />
                  启用会话粘性
                </label>
                <div className="desc">同一会话固定到同一账号，多轮对话不跳号。</div>
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>粘性 TTL</label>
                <input type="text" value={str('session_sticky.ttl')} onChange={(e) => update('session_sticky.ttl', e.target.value)} placeholder="30m" />
              </div>
              <div className="field" style={{ flex: 1 }}>
                <label>粘性 GC 周期</label>
                <input
                  type="text"
                  value={str('session_sticky.gc_interval')}
                  onChange={(e) => update('session_sticky.gc_interval', e.target.value)}
                  placeholder="5m"
                />
              </div>
            </div>
            <div className="field" style={{ marginBottom: 0 }}>
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={bool('features.sanitize_blacklist_fingerprints')}
                  onChange={(e) => update('features.sanitize_blacklist_fingerprints', e.target.checked)}
                />
                出站请求体指纹脱敏
              </label>
              <div className="desc">清洗请求体中的黑名单指纹字段，降低被上游识别为自动化工具的风险。</div>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <h2>Redis 镜像（可选）</h2>
              <span className="hint">留空 = 纯内存模式，功能照常</span>
            </div>
            <div className="row">
              <div className="field" style={{ flex: 2 }}>
                <label>Upstash URL</label>
                <input type="text" value={str('upstash.url')} onChange={(e) => update('upstash.url', e.target.value)} placeholder="rediss://… 或 https://xxx.upstash.io" />
              </div>
              <div className="field" style={{ flex: 2 }}>
                <label>Upstash Token</label>
                <input
                  type={showApiKey ? 'text' : 'password'}
                  value={str('upstash.token')}
                  onChange={(e) => update('upstash.token', e.target.value)}
                  placeholder="REST token"
                />
              </div>
            </div>
          </div>

          <div className="page-actions">
            <button className="btn btn-primary" onClick={() => void saveForm()} disabled={saving || writeDisabled}>
              {saving && <Spinner />} 保存配置
            </button>
            <button className="btn" onClick={() => void load()} disabled={saving}>
              放弃修改
            </button>
          </div>
        </>
      )}

      {tab === 'json' && (
        <div className="card">
          <div className="card-head">
            <h2>JSON 源码</h2>
            <span className="hint">适合编辑表单未覆盖的自定义字段</span>
          </div>
          {jsonError && <Alert kind="error">JSON 语法错误：{jsonError}</Alert>}
          <textarea
            rows={26}
            value={jsonText}
            onChange={(e) => {
              setJsonText(e.target.value)
              setJsonError(null)
            }}
            spellCheck={false}
            style={{ minHeight: 460 }}
          />
          <div className="page-actions" style={{ marginTop: 13 }}>
            <button className="btn btn-primary" onClick={() => void saveJSON()} disabled={saving || writeDisabled}>
              {saving && <Spinner />} 保存配置
            </button>
            <button className="btn" onClick={() => setJsonText(JSON.stringify(doc ?? {}, null, 2))}>
              还原为已加载内容
            </button>
          </div>
        </div>
      )}

      {confirmReset && (
        <ConfirmDialog
          title="从备份恢复配置"
          danger
          confirmText="确认恢复"
          busy={resetting}
          onCancel={() => setConfirmReset(false)}
          onConfirm={() => void doReset()}
          message={
            <>
              将用首次保存前的备份文件覆盖当前 <span className="mono">{meta?.path}</span>。
              <br />
              备份路径：<span className="mono">{meta?.backup_path}</span>
            </>
          }
        />
      )}
    </>
  )
}

// Desktop drafts stay as strings: invalid/empty input must never become zero or
// silently disappear. Only changed whitelist fields enter the transaction.
type DesktopDraft = { port: string; timeout: string; header: string; idle: string; max: string; sticky: boolean; ttl: string; proxy: string; checkin: boolean; checkinHours: string }
export function desktopConfigDraft(view: DesktopConfigView): DesktopDraft {
  const e = view.editable
  return { port: String(e.port), timeout: String(e.upstream.timeout_seconds), header: String(e.upstream.header_timeout_seconds),
    idle: String(e.upstream.idle_timeout_seconds), max: String(e.pool.max_in_flight), sticky: e.session_sticky.enabled,
    ttl: e.session_sticky.ttl, proxy: e.desktop.proxy_url,
    checkin: e.schedule?.checkin_enabled ?? false, checkinHours: (e.schedule?.checkin_hours ?? [9, 21]).join(', ') }
}
export function desktopConfigPatch(draft: DesktopDraft, loaded: DesktopConfigView, edited?: ReadonlySet<keyof DesktopDraft>): Record<string, unknown> {
  const old = desktopConfigDraft(loaded), patch: Record<string, unknown> = {}
  if (edited) draft = { ...old, ...Object.fromEntries([...edited].map((key) => [key, draft[key]])) }
  const integer = (value: string, min: number, max: number, label: string) => {
    const n = Number(value)
    if (!/^\d+$/.test(value) || !Number.isSafeInteger(n) || n < min || n > max) throw new Error(`${label}须为 ${min}–${max} 的整数`)
    return n
  }
  if (draft.port !== old.port) patch.port = integer(draft.port, 1024, 65535, '端口')
  const upstream: Record<string, number> = {}
  for (const [field, key] of [['timeout', 'timeout_seconds'], ['header', 'header_timeout_seconds'], ['idle', 'idle_timeout_seconds']] as const) {
    if (draft[field] !== old[field]) upstream[key] = integer(draft[field], 1, 86400, '超时秒数')
  }
  if (Object.keys(upstream).length) patch.upstream = upstream
  if (draft.max !== old.max) patch.pool = { max_in_flight: integer(draft.max, 0, Number.MAX_SAFE_INTEGER, '最大并发') }
  const sticky: Record<string, unknown> = {}
  if (draft.sticky !== old.sticky) sticky.enabled = draft.sticky
  if (draft.ttl !== old.ttl) {
    const units: Record<string, number> = { h: 3600, m: 60, s: 1, ms: .001, us: .000001, 'µs': .000001, ns: .000000001 }
    const parts = [...draft.ttl.matchAll(/(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h)/g)]
    const seconds = parts.reduce((sum, p) => sum + Number(p[1]) * units[p[2]], 0)
    if (parts.map((p) => p[0]).join('') !== draft.ttl || seconds < 1 || seconds > 720 * 3600) throw new Error('会话保持时长须为 1 秒至 720 小时，例如 30m、1h30m')
    sticky.ttl = draft.ttl
  }
  if (Object.keys(sticky).length) patch.session_sticky = sticky
  const schedule: Record<string, unknown> = {}
  if (draft.checkin !== old.checkin) schedule.checkin_enabled = draft.checkin
  if (draft.checkinHours !== old.checkinHours) {
    const hours = draft.checkinHours.split(/[,，\s]+/).filter(Boolean).map(text => integer(text, 0, 23, '签到小时'))
    if (hours.length === 0 || hours.length > 24 || new Set(hours).size !== hours.length) throw new Error('签到时间须填写 1–24 个不重复的小时，例如 9, 21')
    schedule.checkin_hours = hours
  }
  if (Object.keys(schedule).length) patch.schedule = schedule
  if (draft.proxy !== old.proxy) {
    if (draft.proxy) {
      const match = /^http:\/\/(?:\[[0-9a-fA-F:.]+\]|[^\s:/?#@%]+):(\d+)$/.exec(draft.proxy)
      if (!match || Number(match[1]) < 1 || Number(match[1]) > 65535) throw new Error('代理须留空或填写 http://host:port，不含用户名、密码或路径')
      try { if (!new URL(draft.proxy).hostname) throw new Error() } catch { throw new Error('代理地址无效') }
    }
    patch.desktop = { proxy_url: draft.proxy }
  }
  return patch
}

/** BrowserRouter does not offer useBlocker. Guard its navigation and browser
 * history while this form owns an unsaved draft; restore the original methods
 * on unmount. beforeunload also covers refresh/closing the containing WebView. */
function useUnsavedConfig(dirty: boolean, saving: boolean) {
  const { navigator } = useContext(UNSAFE_NavigationContext)
  useEffect(() => {
    if (!dirty && !saving) return
    const confirm = () => !saving && window.confirm('配置尚未保存，离开将放弃草稿。确定离开？')
    const push = navigator.push, replace = navigator.replace
    const guardedPush: typeof push = (...args) => { if (confirm()) push(...args) }
    const guardedReplace: typeof replace = (...args) => { if (confirm()) replace(...args) }
    navigator.push = guardedPush; navigator.replace = guardedReplace
    const index = window.history.state?.idx as number | undefined
    let restoring = false
    const pop = (event: PopStateEvent) => {
      if (restoring) { restoring = false; event.stopImmediatePropagation(); return }
      if (confirm()) return
      const next = event.state?.idx as number | undefined
      if (index !== undefined && next !== undefined && index !== next) {
        event.stopImmediatePropagation(); restoring = true; window.history.go(index - next)
      }
    }
    const unload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('popstate', pop, true); window.addEventListener('beforeunload', unload)
    return () => {
      if (navigator.push === guardedPush) navigator.push = push
      if (navigator.replace === guardedReplace) navigator.replace = replace
      window.removeEventListener('popstate', pop, true); window.removeEventListener('beforeunload', unload)
    }
  }, [dirty, saving, navigator])
}

function DesktopConfigPage({ session }: { session: SessionInfo }) {
  const [loaded, setLoaded] = useState<DesktopConfigView | null>(null)
  const [draft, setDraft] = useState<DesktopDraft | null>(null)
  const [edited, setEdited] = useState<Set<keyof DesktopDraft>>(new Set())
  const [busy, setBusy] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [conflict, setConflict] = useState(false)
  const applying = useRef<AbortController | null>(null)
  const proxyChecking = useRef<AbortController | null>(null)
  const [proxyBusy, setProxyBusy] = useState(false)
  const [proxyResult, setProxyResult] = useState<{ revision: string; text: string } | null>(null)
  const loadedRevision = useRef<string | null>(null)
  loadedRevision.current = loaded?.revision ?? null
  const invalidateProxy = () => {
    // Keep this request registered until desktopCommand receives native cleanup ACK.
    proxyChecking.current?.abort()
    setProxyResult(null)
  }
  useEffect(() => () => { proxyChecking.current?.abort(); proxyChecking.current = null }, [])
  useEffect(() => () => { applying.current?.abort(); applying.current = null }, [])
  const dirty = !!(draft && loaded && JSON.stringify(draft) !== JSON.stringify(desktopConfigDraft(loaded)))
  useUnsavedConfig(dirty, saving)
  const load = useCallback(async (preserveDraft = false) => {
    invalidateProxy(); setBusy(true)
    try {
      const view = await api.desktopConfig()
      setLoaded(view)
      if (!preserveDraft) { setDraft(desktopConfigDraft(view)); setEdited(new Set()) }
      setConflict(false); setError(null)
      if (preserveDraft) setNotice('已读取最新版本，草稿仍保留。请核对差异后再保存，或放弃草稿。')
    } catch (err) { setError(err instanceof Error ? err.message : '配置读取失败') }
    finally { setBusy(false) }
  }, [])
  useEffect(() => { void load() }, [load])
  const checkProxy = async () => {
    if (!loaded || proxyChecking.current || busy || saving) return
    const revision = loaded.revision
    const controller = new AbortController(); proxyChecking.current = controller
    setProxyBusy(true); setProxyResult(null)
    try {
      const value = await desktopCommand('desktop.checkProxy', { revision }, { signal: controller.signal })
      if (proxyChecking.current !== controller || controller.signal.aborted || loadedRevision.current !== revision || value.revision !== revision) return
      const text = value.result === 'reachable' ? 'TCP 端口可连接；未验证 HTTP 转发或上游。' : value.result === 'not_applicable' ? '当前已提交配置为直连，无代理端口可检查。' : 'TCP 端口未能连接。'
      setProxyResult({ revision, text: `${text} 检查时间：${String(value.checked_at)}` })
    } catch (error) {
      if (proxyChecking.current === controller && !controller.signal.aborted && loadedRevision.current === revision) setProxyResult({ revision, text: error instanceof Error ? error.message : '代理检查未完成' })
    } finally { if (proxyChecking.current === controller) { proxyChecking.current = null; setProxyBusy(false) } }
  }
  const resetDefaults = (headerOnly = false) => {
    if (!draft) return
    const values = headerOnly ? { header: draft.timeout } : { timeout: '120', header: '120', idle: '300', max: '3', sticky: true, ttl: '30m', proxy: '', checkin: true, checkinHours: '9, 21' }
    setDraft({ ...draft, ...values }); setEdited(new Set([...edited, ...Object.keys(values) as (keyof DesktopDraft)[]])); setNotice('默认值已填入草稿，保存并应用后才生效；端口与密钥保持不变。')
  }
  const save = async () => {
    if (!draft || !loaded) return
    setError(null); setNotice(null)
    let patch: Record<string, unknown>
    try { patch = desktopConfigPatch(draft, loaded, edited) } catch (err) { setError((err as Error).message); return }
    if (!Object.keys(patch).length) { setNotice('没有需要保存的修改'); return }
    invalidateProxy()
    const controller = new AbortController()
    applying.current = controller
    setSaving(true)
    try {
      const result = await desktopCommand('gateway.applyConfig', { revision: loaded.revision, patch }, { signal: controller.signal })
      if (applying.current !== controller) return
      const view = result.config as unknown as DesktopConfigView
      if (!view?.revision || !view.editable) throw new Error('配置结果未确认，请重新读取；草稿已保留。')
      setLoaded(view); setDraft(desktopConfigDraft(view)); setEdited(new Set()); setConflict(false); setNotice('配置已应用。服务启动状态已由原生流程确认。')
    } catch (err) {
      if (applying.current !== controller) return
      if ((err instanceof ApiError && err.status === 409) || (err instanceof DesktopError && err.code === 'CONFIG_CONFLICT')) {
        setConflict(true); setError('配置已被其他操作修改。请重新读取最新版本；当前草稿不会被覆盖。')
      } else { setError(err instanceof Error ? err.message : '配置应用失败，草稿已保留') }
    } finally { if (applying.current === controller) { applying.current = null; setSaving(false) } }
  }
  if (!draft || !loaded) return <>{error && <Alert kind="error">{error}</Alert>}<Spinner label="正在读取安全配置…" />{error && <button className="btn" onClick={() => void load()}>重试读取</button>}</>
  const edit = <K extends keyof DesktopDraft>(key: K, value: DesktopDraft[K]) => { setDraft({ ...draft, [key]: value }); setEdited(new Set([...edited, key])); setNotice(null) }
  const field = (key: Exclude<keyof DesktopDraft, 'sticky' | 'checkin'>, label: string, hint: string) => <div className="field"><label htmlFor={`config-${key}`}>{label}</label>
    <input id={`config-${key}`} value={draft[key]} disabled={saving || busy || session.read_only} onChange={(e) => edit(key, e.target.value)} /><div className="desc">{hint}</div></div>
  return <>
    <div className="page-head"><div><h1>网关配置</h1><p>仅本机监听；保存时由原生应用确认、应用并检查服务状态。</p></div>
      <div className="page-actions"><button className="btn" disabled={busy || saving} onClick={() => void load(dirty || conflict)}>重新读取{dirty ? '（保留草稿）' : ''}</button></div></div>
    {error && <Alert kind="error">{error}</Alert>}{notice && <Alert kind="ok">{notice}</Alert>}
    {session.read_only && <Alert kind="warn">当前为只读模式。</Alert>}
    {!loaded.key_configured && <Alert kind="warn">API 密钥尚未配置。请在「API 与模型」中通过原生确认轮换密钥。</Alert>}
    <div className="card"><div className="card-head"><h2>本机服务</h2></div>
      {field('port', 'API 端口', '1024–65535；监听地址固定为 127.0.0.1。')}
      {field('max', '最大并发请求', '非负整数；0 表示不限制。')}
      <p>API 密钥：{loaded.key_configured ? '已配置（不在页面显示）' : '未配置'} · 自动签到：{loaded.editable.schedule?.checkin_enabled ? '已开启' : '已关闭'}</p>
      <p className="desc">WorkBuddy 国内版与国际版：未开启「自定义协议」时地址以 /v1 结尾；开启时以 /v1/chat/completions 结尾。<Link to="/api-models">查看填写说明并复制地址</Link></p>
    </div>
    <div className="card"><div className="card-head"><h2>自动签到领积分</h2></div>
      <label className="checkbox"><input type="checkbox" id="config-checkin" checked={draft.checkin} disabled={saving || busy || session.read_only} onChange={(e) => edit('checkin', e.target.checked)} />启用自动签到</label>
      {field('checkinHours', '每日签到时间（本机时区）', '0–23 的整点小时，用逗号分隔；默认 9, 21，即每天 09:00 和 21:00。')}
      <p className="desc">保存并应用后，在 API 服务运行期间逐个为国内账号签到并刷新积分余额；国际账号与已禁用账号会跳过。实际奖励以账号活动和上游返回为准。</p>
      <p className="desc">请保持 API 服务运行，可在「应用设置」开启随应用启动 API。停止 API 或退出应用期间不会签到，重新启动后从下一个设定时间执行。</p>
    </div>
    <div className="card"><div className="card-head"><h2>上游连接</h2></div>
      {field('timeout', '请求超时（秒）', '1–86400。')}{field('header', '响应头超时（秒）', '1–86400。')}{field('idle', '空闲超时（秒）', '1–86400。')}
      {field('proxy', 'HTTP 代理', '留空表示直连；仅支持 http://host:port。本机面板与网关之间始终直连。')}
      <div className="page-actions"><button className="btn" disabled={saving || busy || session.read_only} onClick={() => resetDefaults(true)}>响应头恢复为当前请求超时</button>
        <button className="btn" disabled={proxyBusy || saving || busy} onClick={() => void checkProxy()}>检查已提交代理 TCP</button>
        {proxyBusy && <button className="btn" onClick={() => invalidateProxy()}>取消代理检查</button>}</div>
      <p className="hint">仅检查已保存修订的 TCP 端口；草稿不参与检查，不发送 HTTP 或上游请求。</p>
      {proxyResult?.revision === loaded.revision && <p>{proxyResult.text}</p>}
    </div>
    <div className="card"><div className="card-head"><h2>会话保持</h2></div>
      <label className="checkbox"><input type="checkbox" id="config-sticky" checked={draft.sticky} disabled={saving || busy || session.read_only} onChange={(e) => edit('sticky', e.target.checked)} />启用会话保持</label>
      {field('ttl', '会话保持时长', '1 秒至 720 小时；例如 30m、1h30m。')}
    </div>
    <p className="text-faint">配置版本：<span className="mono">{loaded.revision}</span>{dirty ? ' · 有未保存修改' : ' · 无未保存修改'}</p>
    <div className="page-actions"><button className="btn" disabled={saving || busy || session.read_only} onClick={() => resetDefaults()}>恢复默认到草稿</button><button className="btn btn-primary" disabled={!dirty || saving || busy || session.read_only || conflict} onClick={() => void save()}>{saving && <Spinner />}保存并应用</button>
      {saving && <button className="btn" onClick={() => applying.current?.abort()}>取消应用</button>}
      <button className="btn" disabled={!dirty || saving} onClick={() => { setDraft(desktopConfigDraft(loaded)); setEdited(new Set()); setError(null); setNotice('已放弃草稿'); setConflict(false) }}>放弃修改</button></div>
  </>
}
