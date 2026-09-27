// TaskProgress.tsx 批量任务进度弹窗：提交任务后轮询进度直到完成。
import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { TaskView } from '../types'
import { Alert, Badge, Modal, Spinner } from '../ui'

export default function TaskProgress({
  taskId,
  onClose,
  onFinished,
  onCancel,
}: {
  taskId: string
  onClose: () => void | Promise<void>
  /** 任务结束时回调，供调用方刷新账号列表。 */
  onFinished?: (task: TaskView) => void | Promise<void>
  onCancel?: () => void | Promise<void>
}) {
  const [task, setTask] = useState<TaskView | null>(null)
  const [error, setError] = useState<string | null>(null)
  const finishedRef = useRef(false)
  const callbacks = useRef({ onFinished, onCancel, onClose })
  callbacks.current = { onFinished, onCancel, onClose }
  const [closing, setClosing] = useState(false)

  useEffect(() => {
    let cancelled = false
    finishedRef.current = false
    let timer: number | undefined

    const poll = async () => {
      try {
        const t = await api.task(taskId)
        if (cancelled) return
        setTask(t)
        setError(null)
        if (!t.running) {
          if (!finishedRef.current) {
            finishedRef.current = true
            await callbacks.current.onFinished?.(t)
          }
          return // 完成即停止轮询
        }
      } catch (err) {
        if (cancelled) return
        setError(err instanceof Error ? err.message : '查询任务失败')
      }
      timer = window.setTimeout(poll, 1000)
    }

    void poll()
    return () => {
      cancelled = true
      if (timer) window.clearTimeout(timer)
    }
  }, [taskId])

  const close = async () => {
    if (closing) return
    setClosing(true)
    try {
      if ((!task || task.running) && callbacks.current.onCancel) await callbacks.current.onCancel()
      else await callbacks.current.onClose()
    } catch (error) { setError(error instanceof Error ? error.message : '取消失败'); setClosing(false) }
  }

  const pct = task && task.total > 0 ? Math.round((task.done / task.total) * 100) : 0

  return (
    <Modal
      title={task?.title || '执行中…'}
      onClose={() => void close()}
      dismissDisabled={closing || (!!task?.running && !onCancel)}
      wide
      footer={
        <button className="btn" onClick={() => void close()} disabled={closing || (!!task?.running && !onCancel)}>
          {closing ? '正在结束…' : (!task || task.running) && onCancel ? '取消任务' : task?.running ? '执行中…' : '关闭'}
        </button>
      }
    >
      {error && <Alert kind="error">{error}</Alert>}
      {!task && <><Spinner /><Badge cls="badge-warn">正在获取任务状态…</Badge></>}

      {task && (
        <>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8, fontSize: 13 }}>
            <span>
              {task.running ? <><Spinner /><Badge cls="badge-warn">执行中</Badge></> : task.failed > 0 || task.error
                ? <Badge cls="badge-danger">已结束（有失败）</Badge> : <Badge cls="badge-ok">已完成</Badge>}
            </span>
            <span className="text-dim">
              {task.done} / {task.total} · 成功 <span className="text-ok">{task.ok}</span> · 失败 <span className={task.failed > 0 ? 'text-danger' : 'text-dim'}>{task.failed}</span>
            </span>
          </div>
          <div className="progress" style={{ marginBottom: 16 }}>
            <div className="progress-bar" style={{ width: `${pct}%`, background: task.failed > 0 || task.error ? 'var(--danger)' : task.running ? 'var(--warn)' : 'var(--ok)' }} />
          </div>

          {task.error && <Alert kind="error">{task.error}</Alert>}

          {task.items && task.items.length > 0 && (
            <div className="table-wrap" style={{ maxHeight: 330, overflowY: 'auto' }}>
              <table>
                <thead>
                  <tr>
                    <th>账号</th>
                    <th>结果</th>
                  </tr>
                </thead>
                <tbody>
                  {task.items.map((it, i) => (
                    <tr key={`${it.uid}-${i}`}>
                      <td style={{ whiteSpace: 'nowrap' }}>
                        {it.nickname || it.uid.slice(0, 8)}
                        {it.reward ? <span className="text-ok"> +{it.reward}</span> : null}
                      </td>
                      <td style={{ fontSize: 12.5 }}>
                        {it.ok ? (
                          <span className="text-ok">{it.message || '成功'}</span>
                        ) : (
                          <span className="text-danger">{it.message || '失败'}</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </Modal>
  )
}
