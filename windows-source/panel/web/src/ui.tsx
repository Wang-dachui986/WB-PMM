// ui.tsx 通用展示组件与格式化工具。

import React, { useEffect, useId, useRef } from 'react'

/** 把 Unix 秒格式化为本地时间字符串。 */
export function fmtTime(sec?: number): string {
  if (!sec || sec <= 0) return '—'
  const d = new Date(sec * 1000)
  const now = Date.now()
  const diff = d.getTime() - now
  const abs = Math.abs(diff)
  let rel = ''
  if (abs < 60_000) rel = diff > 0 ? '即将' : '刚刚'
  else if (abs < 3_600_000) rel = diff > 0 ? `${Math.round(abs / 60_000)} 分钟后` : `${Math.round(abs / 60_000)} 分钟前`
  else if (abs < 86_400_000) rel = diff > 0 ? `${Math.round(abs / 3_600_000)} 小时后` : `${Math.round(abs / 3_600_000)} 小时前`
  else rel = diff > 0 ? `${Math.round(abs / 86_400_000)} 天后` : `${Math.round(abs / 86_400_000)} 天前`
  return `${d.toLocaleString('zh-CN', { hour12: false })}（${rel}）`
}

/** 把 ISO 时间字符串格式化为本地时间（相对时间优先）。 */
export function fmtISO(iso?: string): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (!t || t <= 0) return '—'
  // Go 的零值时间（0001-01-01）按「无记录」处理。
  if (new Date(t).getFullYear() < 1980) return '—'
  const d = new Date(t)
  const diff = Date.now() - t
  if (diff < 0) return d.toLocaleString('zh-CN', { hour12: false })
  if (diff < 60_000) return '刚刚'
  if (diff < 3_600_000) return `${Math.round(diff / 60_000)} 分钟前`
  if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)} 小时前`
  if (diff < 30 * 86_400_000) return `${Math.round(diff / 86_400_000)} 天前`
  return d.toLocaleString('zh-CN', { hour12: false })
}

/** 格式化秒数为「1h 23m 45s」。 */
export function fmtDuration(sec?: number): string {
  if (sec === undefined || sec === null || sec <= 0) return '—'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = Math.floor(sec % 60)
  if (h > 0) return `${h}小时${m}分`
  if (m > 0) return `${m}分${s}秒`
  return `${s}秒`
}

/** 千分位数字。 */
export function fmtNum(n?: number): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

/** uid 缩写展示。 */
export function shortUID(uid: string): string {
  return uid.length > 8 ? uid.slice(0, 8) : uid
}

/** 账号展示名：昵称优先，其次 uid 前缀。 */
export function displayName(a: { nickname?: string; uid: string }, note?: string): string {
  return note || a.nickname || shortUID(a.uid)
}

/** 展示名不参与请求路由；型号后缀和完整 ID 始终保留。 */
export function modelName(model: { id: string; name?: string }): string {
  const id = model.id.replace(/^(cn|global):/, '')
  const modes: Record<string, string> = { auto: '自动选择', 'default-model': '默认模式', 'fast-model': '快速模式', 'balanced-model': '均衡模式', 'deep-model': '深度模式' }
  if (modes[id]) return modes[id]
  const name = model.name?.trim() || id
  if (/[\u3400-\u9fff]/.test(name)) return name
  if (/^hy\d/i.test(id)) return `混元 ${name.replace(/^hy/i, 'HY')}`
  if (/^glm-/i.test(id)) return `智谱 ${name.replace(/^glm/i, 'GLM')}`
  if (/^minimax-/i.test(id)) return `MiniMax ${name.replace(/^minimax[-\s]*/i, '')}`
  if (/^kimi-/i.test(id)) return `Kimi ${name.replace(/^kimi[-\s]*/i, '')}`
  return model.name?.trim() || model.id
}

export function modelLabel(model: { id: string; name?: string }): string {
  const name = modelName(model)
  return name === model.id ? name : `${name}（${model.id}）`
}

export function reasoningEffortLabel(effort: string): string {
  const names: Record<string, string> = { none: '不推理', minimal: '最低', low: '低', medium: '中', high: '高', xhigh: '更高', max: '最高', ultra: '超高' }
  return names[effort] ? `${names[effort]}（${effort}）` : effort
}

/** Credential domain is authoritative; an empty gateway-only record has no region evidence. */
export function accountRegion(a: { domain?: string; has_file?: boolean }): 'cn' | 'global' | 'unknown' {
  if (a.domain === 'copilot.tencent.com' || a.domain === 'codebuddy.cn' || a.domain === 'www.codebuddy.cn' || (!a.domain && a.has_file)) return 'cn'
  if (a.domain === 'www.workbuddy.ai' || a.domain === 'workbuddy.ai') return 'global'
  return 'unknown'
}
export function observedCredits(a: { live_credits?: number; credits_at?: string }): number | undefined {
  const time = a.credits_at ? Date.parse(a.credits_at) : NaN
  return Number.isFinite(time) && time >= Date.UTC(1980, 0, 1) && typeof a.live_credits === 'number' && Number.isFinite(a.live_credits)
    ? a.live_credits : undefined
}

/** 状态徽章配色与文案。 */
export function statusBadge(status: string): { cls: string; text: string } {
  switch (status) {
    case 'healthy':
      return { cls: 'badge-ok', text: '正常' }
    case 'cooling':
      return { cls: 'badge-warn', text: '冷却中' }
    case 'disabled':
      return { cls: 'badge-danger', text: '已禁用' }
    case 'token_expired':
      return { cls: 'badge-danger', text: 'Token 过期' }
    case 'gateway_unreachable':
      return { cls: 'badge-danger', text: '网关不可达' }
    case 'missing_credential':
      return { cls: 'badge-danger', text: '凭证缺失' }
    default:
      return { cls: 'badge-warn', text: '未加载' }
  }
}

/** 原生 API 状态的文案与语义色；不把进程存活误当成可调用。 */
export function gatewayStatusBadge(status: unknown): { cls: string; text: string } {
  switch (status) {
    case 'running_ready':
      return { cls: 'badge-ok', text: '运行中 · 可调用' }
    case 'running_unavailable':
      return { cls: 'badge-warn', text: '运行中 · 暂不可调用' }
    case 'stopped':
      return { cls: 'badge-danger', text: '已停止' }
    case 'failed':
      return { cls: 'badge-danger', text: '启动失败' }
    case 'starting':
      return { cls: 'badge-warn', text: '正在启动' }
    case 'stopping':
      return { cls: 'badge-warn', text: '正在停止' }
    case 'maintenance':
      return { cls: 'badge-warn', text: '维护中 · API 暂停' }
    default:
      return { cls: 'badge-warn', text: typeof status === 'string' && status ? `未知状态：${status}` : '状态未读取' }
  }
}

/** 冷却原因的中文说明。 */
export function coolKindText(kind?: string): string {
  switch (kind) {
    case 'hard_credit':
      return '余额不足'
    case 'soft_rate':
      return '被限流'
    default:
      return '冷却'
  }
}

export function Badge({ cls, children }: { cls: string; children: React.ReactNode }) {
  return (
    <span className={`badge ${cls}`}>
      <span className="badge-dot" />
      {children}
    </span>
  )
}

/**
 * 提示条图标：一套 24×24 线性符号（stroke 2、currentColor）。
 *
 * 原实现用 emoji 当图标。emoji 是彩色位图，无法跟随提示条的语义色，
 * 在浅色底上尤其突兀；而且不同字族的 emoji 尺寸与基线不一致，和旁边 12.5px 的
 * 正文对不齐。系统应用的提示图标是单色线性符号，这里照此对齐。
 */
const ALERT_ICON: Record<string, React.ReactNode> = {
  error: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="m9.2 9.2 5.6 5.6" />
      <path d="m14.8 9.2-5.6 5.6" />
    </>
  ),
  warn: (
    <>
      <path d="M12 4.2 21 19.6H3z" />
      <path d="M12 10v4.2" />
      <path d="M12 17.3v.2" />
    </>
  ),
  ok: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="m8.2 12.4 2.6 2.6 5-5.4" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11.2v5.4" />
      <path d="M12 7.6v.2" />
    </>
  ),
}

export function Alert({
  kind,
  children,
  onClose,
}: {
  kind: 'error' | 'warn' | 'ok' | 'info'
  children: React.ReactNode
  onClose?: () => void
}) {
  return (
    <div className={`alert alert-${kind}`}>
      <span className="alert-icon">
        <svg
          viewBox="0 0 24 24"
          width="15"
          height="15"
          fill="none"
          stroke="currentColor"
          strokeWidth={2}
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          {ALERT_ICON[kind]}
        </svg>
      </span>
      <div style={{ flex: 1, minWidth: 0 }}>{children}</div>
      {onClose && (
        <button className="btn btn-ghost btn-sm" onClick={onClose} aria-label="关闭">
          ✕
        </button>
      )}
    </div>
  )
}

export function Modal({
  title,
  onClose,
  children,
  wide,
  footer,
  dismissDisabled = false,
}: {
  title: string
  onClose: () => void
  children: React.ReactNode
  wide?: boolean
  footer?: React.ReactNode
  dismissDisabled?: boolean
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const titleID = useId()
  // 原生 dialog 负责背景 inert 和焦点限制；卸载时显式恢复触发元素焦点。
  useEffect(() => {
    const element = dialog.current
    const returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    element?.showModal()
    return () => {
      element?.close()
      if (returnFocus?.isConnected) returnFocus.focus()
    }
  }, [])

  return (
    <dialog
      ref={dialog}
      className={`modal ${wide ? 'modal-wide' : ''}`}
      aria-labelledby={titleID}
      onCancel={(event) => { event.preventDefault(); if (!dismissDisabled) onClose() }}
      onMouseDown={(e) => {
        if (dismissDisabled) return
        const bounds = e.currentTarget.getBoundingClientRect()
        if (e.target === e.currentTarget && (e.clientX < bounds.left || e.clientX > bounds.right || e.clientY < bounds.top || e.clientY > bounds.bottom)) onClose()
      }}
    >
        <div className="modal-head">
          <h2 id={titleID}>{title}</h2>
          <button className="btn btn-ghost btn-sm" onClick={onClose} aria-label="关闭" disabled={dismissDisabled}>
            ✕
          </button>
        </div>
        {children}
        {footer && <div className="modal-foot">{footer}</div>}
    </dialog>
  )
}

export function Spinner({ label }: { label?: string }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
      <span className="spinner" />
      {label && <span className="text-dim">{label}</span>}
    </span>
  )
}

export function Empty({ children }: { children: React.ReactNode }) {
  return <div className="empty">{children}</div>
}

/** ConfirmDialog 危险操作二次确认。 */
export function ConfirmDialog({
  title,
  message,
  confirmText = '确认',
  danger,
  onConfirm,
  onCancel,
  busy,
}: {
  title: string
  message: React.ReactNode
  confirmText?: string
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
  busy?: boolean
}) {
  return (
    <Modal
      title={title}
      onClose={onCancel}
      dismissDisabled={busy}
      footer={
        <>
          <button className="btn" onClick={onCancel} disabled={busy}>
            取消
          </button>
          <button
            className={`btn ${danger ? 'btn-danger' : 'btn-primary'}`}
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? <Spinner /> : null}
            {confirmText}
          </button>
        </>
      }
    >
      <div style={{ fontSize: 13.5, lineHeight: 1.7, color: 'var(--text-dim)' }}>{message}</div>
    </Modal>
  )
}
