import { useEffect, useMemo, useRef, useState } from 'react'
import { desktopCommand, hasDesktopBridge } from '../desktopBridge'
import { Alert, Spinner } from '../ui'

type Source = 'app' | 'panel' | 'gateway'
type Entry = { sequence: number; timestamp: number; source: Source; level: string; event: string; exit_code?: number; discarded_bytes?: string }
type Tail = { run_id: string; cursor: number; reset: boolean; entries: Entry[] }
type Cursor = { run_id?: string; cursor?: number }
type Preview = { preview_id: string; files: string[]; sections: string[]; bytes: number }
let pendingTail = false

/** One tail request across page remounts; hidden/disposed pages neither append nor reschedule. */
export function pollSafeLogs(source: Source, cursor: Cursor, onPage: (page: Tail) => void,
  onError: () => void, intervalMs = 1000): () => void {
  let active = true
  let timer: ReturnType<typeof setTimeout> | undefined
  const schedule = () => {
    if (timer) clearTimeout(timer)
    if (active && !document.hidden) timer = setTimeout(() => void poll(), intervalMs)
  }
  const poll = async () => {
    if (!active || document.hidden) return
    if (pendingTail) { schedule(); return }
    pendingTail = true
    try {
      const page = await desktopCommand('logs.tail', { source, ...cursor, limit: 200 }) as unknown as Tail
      if (!active || document.hidden) return
      cursor.run_id = page.run_id; cursor.cursor = page.cursor
      onPage({ ...page, entries: page.entries.slice(0, 200) })
    } catch { if (active && !document.hidden) onError() }
    finally { pendingTail = false; schedule() }
  }
  const visibility = () => {
    if (timer) clearTimeout(timer)
    if (!document.hidden) void poll()
  }
  document.addEventListener('visibilitychange', visibility)
  void poll()
  return () => { active = false; if (timer) clearTimeout(timer); document.removeEventListener('visibilitychange', visibility) }
}
export function appendSafeLogs(previous: Entry[], page: Tail): Entry[] {
  const old = page.reset ? [] : previous
  const last = old[old.length - 1]?.sequence ?? -1
  return [...old, ...page.entries.filter((entry) => entry.sequence > last)].slice(-2000)
}
function line(entry: Entry): string {
  return `${new Date(entry.timestamp * 1000).toLocaleString()} [${entry.level}] ${entry.event}`
    + (entry.exit_code === undefined ? '' : ` exit=${entry.exit_code}`)
    + (entry.discarded_bytes === undefined ? '' : ` discarded_bytes=${entry.discarded_bytes}`)
}

export default function LogsPage() {
  const [source, setSource] = useState<Source>('app')
  const [level, setLevel] = useState('all')
  const [keyword, setKeyword] = useState('')
  const [paused, setPaused] = useState(false)
  const [rows, setRows] = useState<Entry[]>([])
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [selection, setSelection] = useState(false)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [exporting, setExporting] = useState(false)
  const [clearing, setClearing] = useState(false)
  const cursor = useRef<Cursor>({})
  const previousSource = useRef(source)
  const text = useRef<HTMLTextAreaElement>(null)
  const previewID = useRef<string>()
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      const id = previewID.current
      previewID.current = undefined
      if (id) void desktopCommand('diagnostics.export', { operation: 'cancel', preview_id: id }).catch(() => undefined)
    }
  }, [])
  useEffect(() => {
    if (previousSource.current !== source) {
      previousSource.current = source; cursor.current = {}; setRows([]); setSelection(false)
    }
    if (paused || !hasDesktopBridge()) return
    return pollSafeLogs(source, cursor.current,
      (page) => { setRows((old) => appendSafeLogs(old, page)); setError(null) },
      () => setError('暂时无法读取日志，请检查原生连接'))
  }, [source, paused])
  const visible = useMemo(() => rows.filter((entry) => (level === 'all' || entry.level === level)
    && line(entry).toLocaleLowerCase().includes(keyword.toLocaleLowerCase())).map(line).join('\n'), [rows, level, keyword])
  useEffect(() => { if (!paused && text.current) text.current.scrollTop = text.current.scrollHeight }, [visible, paused])
  const copy = async () => {
    const box = text.current
    const value = box?.value.slice(box.selectionStart, box.selectionEnd)
    if (!value) { setNotice('请先选择要复制的日志文本'); return }
    try { await navigator.clipboard.writeText(value); setNotice('已复制选中的安全日志') }
    catch { setError('复制失败，请使用系统复制快捷键') }
  }
  const cancelPreview = async () => {
    const id = previewID.current
    previewID.current = undefined
    if (mounted.current) setPreview(null)
    if (id) await desktopCommand('diagnostics.export', { operation: 'cancel', preview_id: id }).catch(() => undefined)
  }
  const prepareExport = async () => {
    setExporting(true); setError(null); setNotice(null)
    try {
      await cancelPreview()
      const value = await desktopCommand('diagnostics.export', { operation: 'preview' }) as unknown as Preview
      if (!mounted.current) {
        await desktopCommand('diagnostics.export', { operation: 'cancel', preview_id: value.preview_id }).catch(() => undefined)
        return
      }
      previewID.current = value.preview_id; setPreview(value)
    } catch { if (mounted.current) setError('无法准备诊断文件') }
    finally { if (mounted.current) setExporting(false) }
  }
  const saveExport = async () => {
    const id = previewID.current
    if (!id) return
    setExporting(true); setError(null)
    try {
      const result = await desktopCommand('diagnostics.export', { operation: 'save', preview_id: id }, { timeoutMs: 150000 })
      if (mounted.current) setNotice(result.saved ? '诊断文件已保存' : '已取消保存')
    } catch { if (mounted.current && previewID.current === id) setError('诊断保存未完成，请重新准备后重试') }
    finally { await cancelPreview(); if (mounted.current) setExporting(false) }
  }
  const clearArchives = async () => {
    setClearing(true); setError(null); setNotice(null)
    try {
      const result = await desktopCommand('logs.clearArchives')
      if (mounted.current) setNotice(result.cancelled ? '已取消清理' : `已清理 ${result.cleared} 个归档；当前日志和已显示事件保留`)
    } catch { if (mounted.current) setError('归档清理未完成，请关闭其他应用实例后重试') }
    finally { if (mounted.current) setClearing(false) }
  }
  if (!hasDesktopBridge()) return <Alert kind="info">日志与诊断由 macOS 桌面应用提供。</Alert>
  return <>
    <div className="page-head"><div><h1>日志与诊断</h1><p>最近的安全事件；不包含授权凭据或聊天正文。</p></div>
      <div className="page-actions"><button className="btn" disabled={exporting} onClick={() => void prepareExport()}>准备诊断文件</button>
        <button className="btn" disabled={clearing} onClick={() => void clearArchives()}>清理安全日志归档</button></div></div>
    {error && <Alert kind="error" onClose={() => setError(null)}>{error}</Alert>}
    {notice && <Alert kind="ok" onClose={() => setNotice(null)}>{notice}</Alert>}
    <div className="card"><div className="row">
      <div className="field"><label htmlFor="log-source">来源</label><select id="log-source" value={source} onChange={(e) => setSource(e.target.value as Source)}><option value="app">应用</option><option value="panel">面板</option><option value="gateway">网关</option></select></div>
      <div className="field"><label htmlFor="log-level">级别</label><select id="log-level" value={level} onChange={(e) => setLevel(e.target.value)}><option value="all">全部</option><option value="info">信息</option><option value="warning">警告</option><option value="error">错误</option></select></div>
      <div className="field"><label htmlFor="log-keyword">关键词</label><input id="log-keyword" maxLength={128} value={keyword} onChange={(e) => setKeyword(e.target.value)} /></div>
    </div><div className="page-actions"><button className="btn" onClick={() => setPaused(!paused)}>{paused ? '继续读取' : '暂停读取'}</button>
      <button className="btn" disabled={!selection} onClick={() => void copy()}>复制选中内容</button><span className="hint">已缓存 {rows.length} / 2000 条</span></div>
      <label className="field" htmlFor="safe-log-content">安全事件</label><textarea id="safe-log-content" ref={text} readOnly rows={18} value={visible}
        onSelect={(e) => setSelection(e.currentTarget.selectionEnd > e.currentTarget.selectionStart)} aria-live="off" />
    </div>
    {preview && <div className="card"><div className="card-head"><h2>诊断文件清单</h2></div>
      <ul>{preview.files.map((file) => <li key={file}>{file}（{Math.ceil(preview.bytes / 1024)} KB）</li>)}</ul>
      <p>{preview.sections.join('、')}。不含账号备注、个人路径、凭据或原始请求。</p>
      <div className="page-actions"><button className="btn btn-primary" disabled={exporting} onClick={() => void saveExport()}>{exporting && <Spinner />}选择保存位置</button>
        <button className="btn" onClick={() => void cancelPreview()}>取消</button></div></div>}
  </>
}
