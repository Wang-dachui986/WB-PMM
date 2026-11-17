import { useEffect, useState } from 'react'
import { api } from '../api'
import type { LanAccessEntry, LanAccessLog as LanLog } from '../types'

const resultLabel = (entry: LanAccessEntry): string => {
  switch (entry.result) {
    case 'ok': return '成功'
    case 'unauthorized': return '密钥错误（401）：对方填写的 API Key 与本机不一致'
    case 'blocked_path': return '接口仅限本机（403）：请填 /v1 开头的推理地址'
    case 'blocked_source': return '来源不在本机局域网（403）'
    default: return `已到达，处理失败（HTTP ${entry.status}）`
  }
}

/** Recent requests from other devices, so a failed call can be told apart from one that never arrived. */
export function LanAccessLog({ active }: { active: boolean }) {
  const [log, setLog] = useState<LanLog | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    if (!active) { setLog(null); setError(null); return }
    let alive = true
    let timer: ReturnType<typeof setTimeout> | undefined
    const read = async () => {
      if (!alive) return
      if (document.hidden) { timer = setTimeout(() => void read(), 15_000); return }
      try { const value = await api.lanAccess(); if (alive) { setLog(value); setError(null) } }
      catch (err) { if (alive) setError(err instanceof Error ? err.message : '局域网请求记录读取失败') }
      finally { if (alive) timer = setTimeout(() => void read(), 15_000) }
    }
    void read()
    return () => { alive = false; if (timer !== undefined) clearTimeout(timer) }
  }, [active])
  if (!active) return null
  const entries = log?.entries ?? []
  return <div className="lan-log">
    <h4>最近的局域网请求</h4>
    {error ? <p className="hint">{error}</p>
      : !log ? <p className="hint">读取中…</p>
        : entries.length === 0 ? <p className="hint">本次启动 API 以来，还没有收到其他设备的请求。若另一台设备调用失败而这里一直没有记录，说明请求没有到达本机：核对对方填写的地址（本机 IP 可能已变化，可改用上方电脑名地址）、两台设备是否连在同一网络、路由器是否开启访客 / AP 隔离、对方的代理软件是否对局域网地址直连。</p>
          : <div className="lan-log-table"><table>
            <thead><tr><th>时间</th><th>来源设备</th><th>接口</th><th>结果</th></tr></thead>
            <tbody>{entries.slice(0, 10).map((entry, index) => <tr key={`${entry.time}-${index}`}>
              <td>{new Date(entry.time).toLocaleTimeString()}</td>
              <td className="mono">{entry.peer}</td>
              <td className="mono">{entry.method} {entry.path}</td>
              <td className={entry.result === 'ok' ? '' : 'lan-log-problem'}>{resultLabel(entry)}</td>
            </tr>)}</tbody>
          </table></div>}
    <p className="hint">仅保存在内存中，最多 30 条，重启 API 后清空；只在本机可见。</p>
  </div>
}
