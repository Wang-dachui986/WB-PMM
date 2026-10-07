type Action = 'desktop.checkReadiness' | 'desktop.checkProxy' | 'desktop.getStatus' | 'desktop.getBridgeInfo' | 'gateway.start' |
  'gateway.stop' | 'gateway.restart' | 'desktop.reconnect' | 'stats.reset' |
  'account.maintenance' | 'auth.openBrowser' | 'files.importAuth' | 'account.setEnabled' | 'account.delete' |
  'logs.tail' | 'preferences.get' | 'preferences.update' | 'diagnostics.export' |
  'gateway.applyConfig' | 'api.rotateKey' | 'api.copyKey' | 'desktop.openDirectory' | 'logs.clearArchives'
type Reply = { id: string; ok: boolean; data?: Record<string, unknown>; error?: { code: string; message: string } }
declare global {
  interface Window {
    webkit?: { messageHandlers?: { workbuddyNative?: { postMessage(body: unknown): void } } }
    __workbuddyBridgeReceive?: (reply: Reply) => void
    readonly __workbuddyDocumentID?: string
  }
}

export class DesktopError extends Error {
  constructor(public code: string, message: string) { super(message) }
}
export type MaintenanceAction = 'login' | 'reauthorize' | 'refresh' | 'credits' | 'delete'
export interface MaintenanceLease { instance_id: string; generation: string; action: string; uids: string[]; expires_at: string }
export interface ImportPreview { preview_id: string; uid: string; region: 'cn' | 'global'; expires_at: number; overwrite_required: boolean }
export interface CancelableOperation<T> { ready: Promise<T>; cancel: () => Promise<void> }
export interface MaintenanceFlow extends CancelableOperation<MaintenanceLease> { end: () => Promise<void> }
export const hasDesktopBridge = () => !!window.webkit?.messageHandlers?.workbuddyNative
const pending = new Map<string, { resolve: (data: Record<string, unknown>) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>()
let sequence = 0

function send(action: Action, payload: Record<string, unknown>, timeout = 30000,
  onTimeout?: () => Promise<void>): { id: string; promise: Promise<Record<string, unknown>> } {
  const id = `desktop-${Date.now()}-${++sequence}`
  const promise = new Promise<Record<string, unknown>>((resolve, reject) => {
    const handler = window.webkit?.messageHandlers?.workbuddyNative
    const documentID = window.__workbuddyDocumentID
    if (!handler || !documentID) { reject(new Error('当前文档尚未建立原生连接')); return }
    if (pending.size >= (payload.operation === 'cancel_pending' || payload.operation === 'cancel' ? 64 : 32)) { reject(new Error('原生操作过多，请稍后重试')); return }
    window.__workbuddyBridgeReceive = (reply) => {
      const entry = pending.get(reply.id)
      if (!entry) return
      pending.delete(reply.id)
      clearTimeout(entry.timer)
      if (reply.ok) entry.resolve(reply.data ?? {})
      else entry.reject(new DesktopError(reply.error?.code ?? 'operation_failed', reply.error?.message ?? '原生操作失败'))
    }
    const timer = setTimeout(() => {
      const entry = pending.get(id)
      if (!entry) return
      // Keep the pending correlation until native cleanup acknowledges cancellation.
      const fail = (error: Error) => { pending.delete(id); reject(error) }
      entry.resolve = () => undefined
      entry.reject = () => undefined
      if (onTimeout) void onTimeout().then(() => fail(new Error('原生操作超时，已取消')), (error) => fail(error))
      else fail(new Error('原生操作超时'))
    }, timeout)
    pending.set(id, { resolve, reject, timer })
    try { handler.postMessage({ id, action, payload, document_id: documentID }) }
    catch (error) { pending.delete(id); clearTimeout(timer); reject(error) }
  })
  return { id, promise }
}

export interface DesktopCommandOptions { timeoutMs?: 30000 | 150000; signal?: AbortSignal }
export function desktopCommand(action: Action, payload: Record<string, unknown> = {}, options: DesktopCommandOptions = {}): Promise<Record<string, unknown>> {
  if (options.signal?.aborted) return Promise.reject(new DOMException('操作已取消', 'AbortError'))
  if (['gateway.applyConfig', 'api.rotateKey', 'gateway.stop', 'gateway.restart', 'desktop.checkProxy'].includes(action) && payload.operation !== 'cancel_pending') {
    const operation = cancelable<Record<string, unknown>>(action as ServiceAction | 'desktop.checkProxy', payload, action === 'desktop.checkProxy' ? 10000 : 1050000)
    const signal = options.signal
    if (!signal) return operation.ready
    return new Promise((resolve, reject) => {
      let cancelling = false
      const abort = () => {
        cancelling = true
        void operation.cancel().then(() => reject(new DOMException('操作已取消', 'AbortError')), reject)
      }
      signal.addEventListener('abort', abort, { once: true })
      void operation.ready.then(value => { if (!cancelling) resolve(value) }, error => { if (!cancelling) reject(error) })
        .finally(() => signal.removeEventListener('abort', abort))
    })
  }
  // Service cancellation may await the sent request, exact restore and one restart.
  // Keep its acknowledgement correlated through that bounded native cleanup.
  const serviceCancel = ['gateway.applyConfig', 'api.rotateKey', 'gateway.stop', 'gateway.restart'].includes(action) && payload.operation === 'cancel_pending'
  const timeout = serviceCancel || (options.timeoutMs === 150000 && action === 'diagnostics.export' && payload.operation === 'save') ? 150000 : 30000
  const request = send(action, payload, timeout)
  const signal = options.signal
  if (!signal) return request.promise
  return new Promise((resolve, reject) => {
    const abort = () => {
      const entry = pending.get(request.id)
      if (entry) { clearTimeout(entry.timer); pending.delete(request.id) }
      reject(new DOMException('操作已取消', 'AbortError'))
    }
    signal.addEventListener('abort', abort, { once: true })
    void request.promise.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort))
  })
}

type ServiceAction = 'gateway.applyConfig' | 'api.rotateKey' | 'gateway.stop' | 'gateway.restart'
function cancelable<T>(action: 'account.maintenance' | 'files.importAuth' | ServiceAction | 'desktop.checkProxy', payload: Record<string, unknown>, timeout: number): CancelableOperation<T> {
  let cancelling: Promise<void> | undefined
  let cancelled = false
  const cancel = () => {
    cancelled = true
    if (!cancelling) {
      cancelling = desktopCommand(action, { operation: 'cancel_pending', request_id: request.id })
        .then((result) => {
          if (result.applied === true) throw new DesktopError('operation_applied', '配置已生效，取消到达时操作已完成。请重新读取当前配置。')
          if (result.outcome_unknown === true) throw new DesktopError('operation_settled', '当前请求已无活动操作，请重新读取确认最终状态。')
        }).catch((error) => {
          if (error instanceof DesktopError && error.code === 'stale_maintenance') return
          cancelling = undefined
          throw error
        })
    }
    return cancelling
  }
  const request = send(action, payload, timeout, cancel)
  return {
    cancel,
    ready: request.promise.then(async (value) => {
      if (cancelled) { await cancel(); throw new DesktopError('cancelled', '操作已取消') }
      return value as T
    }),
  }
}

export function beginMaintenance(action: MaintenanceAction, uids: string[]): MaintenanceFlow {
  // Confirmation <= 120s, lease <= 900s, with 30s for cleanup. The default stays 30s.
  const operation = cancelable<MaintenanceLease>('account.maintenance', { operation: 'begin', action, uids }, 1050000)
  let ending: Promise<void> | undefined
  return { ...operation, end: () => {
    if (!ending) ending = operation.ready.then((lease) => desktopCommand('account.maintenance', {
      operation: 'end', instance_id: lease.instance_id, generation: lease.generation,
    })).then(() => undefined).catch(async (error) => { await operation.cancel(); throw error })
    return ending
  } }
}

export function previewAuthFile(): CancelableOperation<ImportPreview> {
  return cancelable<ImportPreview>('files.importAuth', { operation: 'preview' }, 150000)
}
export function confirmAuthFile(preview_id: string, overwrite: boolean): CancelableOperation<{ ok: boolean }> {
  return cancelable('files.importAuth', { operation: 'confirm', preview_id, overwrite }, 1050000)
}
export async function cancelAuthPreview(preview_id: string): Promise<void> {
  try { await desktopCommand('files.importAuth', { operation: 'cancel', preview_id }) }
  catch (error) { if (!(error instanceof DesktopError && error.code === 'stale_maintenance')) throw error }
}

let reconnecting: Promise<void> | null = null
export function reconnectDesktop(): Promise<void> {
  if (!reconnecting) {
    reconnecting = desktopCommand('desktop.reconnect').then(() => undefined).finally(() => { reconnecting = null })
  }
  return reconnecting
}

export type DesktopLifecycle = { kind: 'sleep' | 'wake'; epoch: string }
/** Fixed native event, bound to the injected document and strictly increasing epoch. */
export function subscribeDesktopLifecycle(receive: (event: DesktopLifecycle) => void): () => void {
  let lastEpoch = 0n
  const documentID = window.__workbuddyDocumentID
  const listener = (event: Event) => {
    const value = (event as CustomEvent).detail
    if (!documentID || window.__workbuddyDocumentID !== documentID || value?.document_id !== documentID ||
        !['sleep', 'wake'].includes(value?.kind) || typeof value?.epoch !== 'string' || !/^[1-9][0-9]{0,19}$/.test(value.epoch)) return
    const epoch = BigInt(value.epoch)
    if (epoch <= lastEpoch || epoch > 18446744073709551615n) return
    lastEpoch = epoch
    receive({ kind: value.kind, epoch: value.epoch })
  }
  window.addEventListener('workbuddy:lifecycle', listener)
  return () => window.removeEventListener('workbuddy:lifecycle', listener)
}

/** A status change is only a read trigger; native state stays behind desktop.getStatus. */
export function subscribeDesktopGatewayStatus(receive: () => void): () => void {
  let lastEpoch = 0n
  const documentID = window.__workbuddyDocumentID
  const listener = (event: Event) => {
    const value = (event as CustomEvent).detail
    if (!documentID || window.__workbuddyDocumentID !== documentID || value?.document_id !== documentID ||
        typeof value?.epoch !== 'string' || !/^[1-9][0-9]{0,19}$/.test(value.epoch)) return
    const epoch = BigInt(value.epoch)
    if (epoch <= lastEpoch || epoch > 18446744073709551615n) return
    lastEpoch = epoch
    receive()
  }
  window.addEventListener('workbuddy:gateway-status', listener)
  return () => window.removeEventListener('workbuddy:gateway-status', listener)
}
