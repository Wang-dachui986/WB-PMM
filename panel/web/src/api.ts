import { desktopCommand, hasDesktopBridge, reconnectDesktop, type MaintenanceLease } from './desktopBridge'
// api.ts 后端接口封装。所有请求携带 Cookie（同源），并统一处理 401 → 跳登录。
import type {
  AccountProfile,
  AccountsResponse,
  ChatDelta,
  ChatMessage,
  ConfigResponse,
  DesktopConfigView,
  Credits,
  ModelsResponse,
  OpResult,
  Overview,
  SessionInfo,
  StatsResponse,
  SystemInfo,
  TaskListResponse,
  TaskView,
  LoginSession,
} from './types'

/** ApiError 携带 HTTP 状态码，便于调用方区分 401 / 403 / 502。 */
export class ApiError extends Error {
  status: number
  code?: string
  constructor(status: number, message: string, code?: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

/** onUnauthorized 由 App 注入：收到 401 时切回登录页。 */
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

async function handleUnauthorized(): Promise<void> {
  if (hasDesktopBridge()) {
    // 合并并发恢复。原请求继续报 401，绝不重发写请求或聊天。
    await reconnectDesktop().catch(() => undefined)
  } else {
    onUnauthorized?.()
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
    ...init,
  })

  if (res.status === 401 && !path.startsWith('/api/session') && path !== '/api/login') {
    await handleUnauthorized()
  }

  const text = await res.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = { error: text }
    }
  }

  if (!res.ok) {
    // 错误体有两种形态：标准错误用 error，单账号操作结果（OpResult）用 message。
    // 两者都要读，否则导入凭证这类接口的具体原因会被通用的「请求失败」盖掉。
    const obj = (payload ?? {}) as { error?: string; message?: string; code?: string }
    const detail = obj.error || obj.message || `请求失败（HTTP ${res.status}）`
    throw new ApiError(res.status, detail, obj.code)
  }
  return payload as T
}

const get = <T,>(p: string, headers?: Record<string, string>) => request<T>(p, { headers })
const post = <T,>(p: string, body?: unknown, headers?: Record<string, string>) =>
  request<T>(p, {
    method: 'POST',
    body: body === undefined ? '{}' : JSON.stringify(body),
    headers: { 'Content-Type': 'application/json', ...headers },
  })
const put = <T,>(p: string, body: unknown) =>
  request<T>(p, { method: 'PUT', body: JSON.stringify(body) })
const del = <T,>(p: string, headers?: Record<string, string>) => request<T>(p, { method: 'DELETE', headers })

function leaseHeaders(lease?: MaintenanceLease): Record<string, string> | undefined {
  if (!lease) {
    if (hasDesktopBridge()) throw new Error('请先进入账号维护')
    return undefined
  }
  return { 'X-WorkBuddy-Panel-Instance': lease.instance_id, 'X-WorkBuddy-Maintenance-Generation': lease.generation }
}

export const api = {
  desktopConfig: () => get<DesktopConfigView>('/api/config'),
  // 会话
  session: () => get<SessionInfo>('/api/session'),
  login: (username: string, password: string) =>
    post<{ token: string; username: string }>('/api/login', { username, password }),
  logout: () => post<{ ok: boolean }>('/api/logout'),
  changePassword: (current: string, next: string, newUsername?: string) =>
    post<{ ok: boolean; username: string; message: string; relogin?: boolean }>('/api/password', {
      current_password: current,
      new_password: next,
      new_username: newUsername,
    }),

  // 总览 / 账号
  overview: () => get<Overview>('/api/overview'),
  accounts: () => get<AccountsResponse>('/api/accounts'),
  account: (uid: string, silent = false) =>
    get<AccountProfile>(`/api/accounts/${encodeURIComponent(uid)}${silent ? '?silent=true' : ''}`),
  deleteAccount: async (uid: string, lease?: MaintenanceLease): Promise<{ ok: boolean; message: string }> => {
    const headers = leaseHeaders(lease)
    if (hasDesktopBridge()) {
      if (lease?.action !== 'delete' || lease.uids.length !== 1 || lease.uids[0] !== uid) throw new Error('删除许可与账号不匹配')
      return await desktopCommand('account.delete', { uid }) as { ok: boolean; message: string }
    }
    return del(`/api/accounts/${encodeURIComponent(uid)}?confirm=${encodeURIComponent(uid)}`, headers)
  },
  importAccount: (payload: Record<string, unknown>) => post<OpResult>('/api/accounts/import', payload),

  accountCheckin: (uid: string) => post<OpResult>(`/api/accounts/${encodeURIComponent(uid)}/checkin`),
  accountRefresh: (uid: string, lease?: MaintenanceLease) => post<OpResult>(`/api/accounts/${encodeURIComponent(uid)}/refresh`, undefined, leaseHeaders(lease)),
  accountTravel: (uid: string) => post<OpResult>(`/api/accounts/${encodeURIComponent(uid)}/travel`),
  accountCredits: (uid: string, lease?: MaintenanceLease) => post<Credits>(`/api/accounts/${encodeURIComponent(uid)}/credits`, undefined, leaseHeaders(lease)),

  // 批量任务
  batchCheckin: (uids: string[]) => post<TaskView>('/api/tasks/checkin', { uids }),
  batchRefresh: (uids: string[], lease?: MaintenanceLease) => post<TaskView>('/api/tasks/refresh', { uids }, leaseHeaders(lease)),
  batchTravel: (uids: string[]) => post<TaskView>('/api/tasks/travel', { uids }),
  batchCredits: (uids: string[], lease?: MaintenanceLease) => post<TaskView>('/api/tasks/credits', { uids }, leaseHeaders(lease)),
  tasks: () => get<TaskListResponse>('/api/tasks'),
  task: (id: string) => get<TaskView>(`/api/tasks/${encodeURIComponent(id)}`),

  // 网页登录
  loginStart: (region: 'cn' | 'global', lease?: MaintenanceLease) => post<LoginSession>('/api/login/start', { region }, leaseHeaders(lease)),
  loginPoll: (id: string, lease?: MaintenanceLease) => post<LoginSession>(`/api/login/${encodeURIComponent(id)}/poll`, undefined, leaseHeaders(lease)),
  loginStatus: (id: string, lease?: MaintenanceLease) => get<LoginSession>(`/api/login/${encodeURIComponent(id)}`, leaseHeaders(lease)),
  loginCancel: (id: string, lease?: MaintenanceLease) => post<LoginSession>(`/api/login/${encodeURIComponent(id)}/cancel`, undefined, leaseHeaders(lease)),

  loginConfirmOverwrite: (id: string, uid: string, lease?: MaintenanceLease) => post<LoginSession>(`/api/login/${encodeURIComponent(id)}/confirm-overwrite`, { uid }, leaseHeaders(lease)),

  // 模型 / 聊天
  models: () => get<ModelsResponse>('/api/models'),
  // 请求统计（按模型聚合 + 时间维度）+ 官方价换算
  tokenHistory: (from: string, to: string) => get<import('./pages/tokenData').TokenHistory>(`/api/token-history?${new URLSearchParams({ from, to })}`),
  stats: (opt?: {
    mode?: 'peak' | 'offpeak'
    range?: string
    from?: string
    to?: string
    interval?: 'hour' | 'day' | 'week'
    model?: string
  }) => {
    const q = new URLSearchParams()
    if (opt?.mode) q.set('mode', opt.mode)
    if (opt?.range) q.set('range', opt.range)
    if (opt?.from) q.set('from', opt.from)
    if (opt?.to) q.set('to', opt.to)
    if (opt?.interval) q.set('interval', opt.interval)
    if (opt?.model) q.set('model', opt.model)
    const qs = q.toString()
    return get<StatsResponse>(`/api/stats${qs ? `?${qs}` : ''}`)
  },
  resetStats: async () => {
    if (hasDesktopBridge()) {
      await desktopCommand('stats.reset')
      return { ok: true, message: '本次运行统计已重置' }
    }
    return post<{ ok: boolean; message: string }>('/api/stats/reset')
  },
  // 官方价格表编辑
  savePrice: (p: {
    model: string
    cached_input: number
    miss_input: number
    output: number
    off_peak_ratio?: number
    note?: string
  }) => put<{ ok: boolean; message: string }>('/api/pricing', p),
  deletePrice: (model: string) =>
    del<{ ok: boolean; message: string }>(`/api/pricing/${encodeURIComponent(model)}`),
  chat: (payload: { model: string; messages: ChatMessage[]; extra?: Record<string, unknown>; conversationId?: string }, signal?: AbortSignal) =>
    request<{ result: import('./types').ChatResult; elapsed_ms: number }>('/api/chat', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(payload.conversationId ? { 'X-Conversation-Id': payload.conversationId } : {}),
      },
      body: JSON.stringify({ model: payload.model, messages: payload.messages, stream: false, extra: payload.extra }),
      signal,
    }),

  // 配置
  config: () => get<ConfigResponse>('/api/config'),
  saveConfig: (doc: Record<string, unknown>) => put<{ ok: boolean; message: string }>('/api/config', doc),
  resetConfig: () => post<{ ok: boolean; message: string }>('/api/config/reset'),

  // 系统
  system: () => get<SystemInfo>('/api/system'),
  restart: async () => {
    if (hasDesktopBridge()) {
      await desktopCommand('gateway.restart')
      return { ok: true, message: 'API 服务已重启' }
    }
    return post<{ ok: boolean; message: string }>('/api/system/restart')
  },
}

/** 每次请求只发出一次终态；取消和断流不会被当作完成。 */
export function streamChat(
  payload: { model: string; messages: ChatMessage[]; extra?: Record<string, unknown>; conversationId?: string },
  onDelta: (d: ChatDelta) => void,
  onError: (msg: string) => void,
  onClose: (state: 'completed' | 'stopped' | 'interrupted') => void,
): () => void {
  const controller = new AbortController()
  let closed = false
  const close = (state: 'completed' | 'stopped' | 'interrupted') => {
    if (closed) return
    closed = true
    onClose(state)
  }

  void (async () => {
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
    try {
      const res = await fetch('/api/chat/stream', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
          ...(payload.conversationId ? { 'X-Conversation-Id': payload.conversationId } : {}),
        },
        body: JSON.stringify({ model: payload.model, messages: payload.messages, stream: true, extra: payload.extra }),
        signal: controller.signal,
      })
      if (!res.ok) {
        const text = await res.text()
        let msg = `请求失败（HTTP ${res.status}）`
        try {
          const obj = JSON.parse(text) as { error?: string }
          if (obj.error) msg = obj.error
        } catch { /* 保留 HTTP 错误信息。 */ }
        if (res.status === 401) await handleUnauthorized()
        throw new ApiError(res.status, msg)
      }
      reader = res.body?.getReader()
      if (!reader) throw new Error('服务器未返回流式响应')
      const decoder = new TextDecoder('utf-8', { fatal: true })
      let buffer = ''
      while (!closed) {
        const { done, value } = await reader.read()
        if (closed) return
        buffer += done ? decoder.decode() : decoder.decode(value, { stream: true })
        const parts = buffer.split(/\r?\n\r?\n/)
        buffer = parts.pop() ?? ''
        for (const part of parts) {
          const data = part.split(/\r?\n/)
            .filter(line => line.startsWith('data:'))
            .map(line => line.slice(5).replace(/^ /, '')).join('\n')
          if (!data) continue
          let delta: ChatDelta
          try {
            delta = JSON.parse(data) as ChatDelta
            if (!delta || typeof delta !== 'object' || Array.isArray(delta)) throw new Error()
          } catch { throw new Error('连接中断：流式响应格式无效') }
          if (delta.error) throw new Error(delta.error)
          onDelta(delta)
          if (delta.done === true) {
            close('completed')
            return
          }
        }
        if (done) throw new Error('连接中断：未收到完成标记，已保留收到的内容')
      }
    } catch (err) {
      if (closed) return
      if (controller.signal.aborted) close('stopped')
      else {
        onError(err instanceof TypeError ? '连接中断：请检查网络后手动重试' : err instanceof Error ? err.message : String(err))
        close('interrupted')
      }
    } finally {
      await reader?.cancel().catch(() => undefined)
    }
  })()

  return () => {
    controller.abort()
    close('stopped')
  }
}
