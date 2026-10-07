import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { bounds, summarize, type Period, type TokenHistory, type Summary } from './tokenData'
import './tokens.css'
const modes = { heat: '方块热力图', pie: '扇形图', bar: '柱状图', line: '曲线图' }
const periods = { day: '日', week: '周', month: '月', year: '年度' }
const colors = ['#945e3e', '#66816c', '#b78d45', '#6e85a0', '#a77078', '#81709d', '#548b8b', '#a29277']
const num = (v: number) => v.toLocaleString('zh-CN')
const compact = (v: number) => v >= 1e8 ? `${(v / 1e8).toFixed(2)}亿` : v >= 1e4 ? `${(v / 1e4).toFixed(2)}万` : num(v)
const value = (s: Summary) => s.input + s.output
const display = (s: Summary, n: number) => s.requests > 0 && s.observed === 0 ? '未知' : compact(n)
export default function TokenPage() {
 const [period, setPeriod] = useState<Period>('month')
 const [date, setDate] = useState(new Date().toISOString().slice(0, 10))
 const [mode, setMode] = useState<keyof typeof modes>('heat')
 const [model, setModel] = useState('')
 const [data, setData] = useState<TokenHistory | null>(null)
 const [error, setError] = useState(''), [busy, setBusy] = useState(false), [refresh, setRefresh] = useState(0)
 const sequence = useRef(0)
 useEffect(() => {
  const id = ++sequence.current; const { start, end } = bounds(period, date)
  setBusy(true); setError(''); setData(null)
  api.tokenHistory(start.toISOString(), end.toISOString()).then(d => { if (id === sequence.current) setData(d) })
   .catch(e => { if (id === sequence.current) setError(e.message) })
   .finally(() => { if (id === sequence.current) setBusy(false) })
  return () => { sequence.current++ }
 }, [period, date, refresh])
 const stats = useMemo(() => summarize(data?.rows || [], period, date, model), [data, period, date, model])
 const allModels = [...new Set(data?.rows.map(r => r.model) || [])].sort()
 const { total, points, models } = stats
 const peak = Math.max(0, ...points.map(value)), max = peak || 1
 const { start, end } = bounds(period, date)
 const detail = (p: Summary, label: string) => `${label} · ${display(p, value(p))} Token · 输入 ${num(p.input)} / 输出 ${num(p.output)} · 用量已返回 ${p.observed}/${p.requests} 次`
 let angle = 0
 const pie = models.filter(([, s]) => value(s) > 0).map(([, s], i) => {
  const first = angle; angle += value(s) / (value(total) || 1) * 360
  return `${colors[i % colors.length]} ${first}deg ${angle}deg`
 }).join(',')
 return <div className="token-page">
  <div className="page-head"><div><h1>Token 统计</h1><p className="text-dim">按时间与模型查看真实用量 · 沿用 UTC 时间，避免跨设备时区重复计数</p></div><button className="btn" disabled={busy} onClick={() => setRefresh(x => x + 1)}>{busy ? '读取中…' : '刷新统计'}</button></div>
  <div className="card token-filters">
   <div className="token-switch" role="group" aria-label="统计周期">{Object.entries(periods).map(([k, title]) => <button className="btn" key={k} aria-pressed={period === k} onClick={() => setPeriod(k as Period)}>{title}</button>)}</div>
   <label>选择日期 <input type="date" value={date} onChange={e => { if (/^\d{4}-\d{2}-\d{2}$/.test(e.target.value)) setDate(e.target.value) }} /></label>
   <label>模型 <select value={model} onChange={e => setModel(e.target.value)}><option value="">全部模型</option>{model && !allModels.includes(model) && <option value={model}>{model}</option>}{allModels.map(m => <option key={m}>{m}</option>)}</select></label>
  </div>
  {error && <div className="alert alert-error" role="alert">{error}</div>}
  {data && <>
   {data.warning && <div className="alert alert-warn" role="alert">{data.warning}</div>}
   <div className="card token-summary">{[['所选周期 Token', display(total, value(total))], ['输入 Token', display(total, total.input)], ['输出 Token', display(total, total.output)], [period === 'day' ? '每小时峰值' : '每日峰值', display(total, peak)], ['用量已返回 / 请求', `${num(total.observed)} / ${num(total.requests)}`]].map(([title, n]) => <div key={title}><strong>{n}</strong><span>{title}</span></div>)}</div>
   <div className="card">
    <div className="card-head"><h2>{mode === 'pie' ? '模型用量占比' : 'Token 活动'}</h2><div className="token-switch" role="group" aria-label="图表类型">{Object.entries(modes).map(([k, title]) => <button className="btn btn-sm" key={k} aria-pressed={mode === k} onClick={() => setMode(k as keyof typeof modes)}>{title}</button>)}</div></div>
    <p className="text-dim">{start.toISOString().slice(0, 10)} 至 {new Date(+end - 1).toISOString().slice(0, 10)} · {model || '全部模型'}</p>
    {total.requests === 0 && <p className="token-empty">所选周期暂无请求记录。</p>}
    {total.requests > total.observed && <p className="text-warn">{num(total.requests - total.observed)} 次请求未返回 usage，未计入 Token；部分字段缺失时仅累计已返回值。</p>}
    {mode === 'heat' && <><div className={`token-heat ${period === 'year' ? 'token-heat-year' : ''}`}>{points.map(p => <button key={p.time} title={detail(p, p.label)} aria-label={detail(p, p.label)} style={{ background: value(p) ? `rgba(148,94,62,${0.2 + 0.8 * value(p) / max})` : undefined }} className={p.requests > p.observed ? 'token-partial' : ''}><span>{p.label}</span></button>)}</div><p className="text-dim">浅 → 深：用量从少到多 · 虚线：含未返回用量的请求 · 每格{period === 'day' ? '一小时' : '一天'}，悬停或键盘聚焦查看明细</p></>}
    {mode === 'pie' && <div className="token-pie-layout"><div className="token-pie" role="img" aria-label={`模型 Token 占比，已观测总量 ${num(value(total))}，详情见模型表`} style={{ background: pie ? `conic-gradient(${pie})` : 'var(--border)' }} /><ul>{models.map(([name, s], i) => <li key={name}><i style={{ background: colors[i % colors.length] }} />{name} · {value(total) ? (value(s) / value(total) * 100).toFixed(1) : '0'}% · {display(s, value(s))}</li>)}</ul></div>}
    {(mode === 'bar' || mode === 'line') && <div className="token-trend"><svg viewBox="0 0 1000 260" role="img" aria-label={`${modes[mode]}，${period === 'day' ? '按小时' : '按天'}统计`}>
     {[0, 0.5, 1].map(n => <g key={n}><line x1="65" x2="985" y1={220 - n * 190} y2={220 - n * 190} stroke="var(--border)"/><text x="0" y={224 - n * 190}>{compact(max * n)}</text></g>)}
     {mode === 'line' && <polyline fill="none" stroke="#945e3e" strokeWidth="2" points={points.map((p, i) => `${65 + (i + 0.5) * 920 / points.length},${220 - value(p) / max * 190}`).join(' ')} />}
     {points.map((p, i) => <g key={p.time} tabIndex={0} aria-label={detail(p, p.label)}><title>{detail(p, p.label)}</title>{mode === 'bar' ? <rect x={65 + i * 920 / points.length} y={220 - value(p) / max * 190} width={Math.max(1, 920 / points.length - 2)} height={Math.max(1, value(p) / max * 190)} fill="#945e3e" /> : <circle cx={65 + (i + 0.5) * 920 / points.length} cy={220 - value(p) / max * 190} r={points.length > 40 ? 2 : 4} fill="#945e3e" />}</g>)}
     <text x="65" y="252">{points[0]?.label}</text><text textAnchor="end" x="985" y="252">{points[points.length - 1]?.label}</text>
    </svg></div>}
   </div>
   <div className="card"><div className="card-head"><h2>各模型用量</h2><span className="hint">按总 Token 降序 · 缓存属于输入，不重复相加</span></div><div className="table-wrap"><table><thead><tr><th>模型</th><th>输入</th><th>输出</th><th>总 Token</th><th>占比</th><th>用量已返回 / 请求</th></tr></thead><tbody>{models.map(([name, s]) => <tr key={name}><td>{name}</td><td>{s.observed ? num(s.input) : '未知'}</td><td>{s.observed ? num(s.output) : '未知'}</td><td>{s.observed ? num(value(s)) : '未知'}</td><td>{value(total) ? `${(value(s) / value(total) * 100).toFixed(1)}%` : '—'}</td><td>{s.observed} / {s.requests}</td></tr>)}</tbody></table></div></div>
   <p className="text-dim">开始记录：{new Date(data.since).toLocaleString()} · 保留最近 {data.retention_days} 天；升级前历史无法补回。正常退出会保存，异常终止可能丢失最后一分钟记录。此页不受「重置本次统计」影响。</p>
  </>}
 </div>
}
