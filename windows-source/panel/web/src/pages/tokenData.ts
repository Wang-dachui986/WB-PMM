export type Period = 'day' | 'week' | 'month' | 'year'
export type TokenBucket = { hour: number; model: string; requests: number; observed: number; input: number; output: number }
export type TokenHistory = { since: string; rows: TokenBucket[]; warning: string; retention_days: number }
export type Summary = { input: number; output: number; requests: number; observed: number }
export const empty = (): Summary => ({ input: 0, output: 0, requests: 0, observed: 0 })
export function add(a: Summary, b: Summary) { a.input += b.input; a.output += b.output; a.requests += b.requests; a.observed += b.observed; return a }
// UTC calendar boundaries keep every bucket unambiguous across clients and DST changes.
export function bounds(period: Period, date: string) {
 const start = new Date(`${date}T00:00:00Z`)
 if (period === 'week') start.setUTCDate(start.getUTCDate() - (start.getUTCDay() + 6) % 7)
 if (period === 'month') start.setUTCDate(1)
 if (period === 'year') { start.setUTCMonth(0, 1) }
 const end = new Date(start)
 if (period === 'day') end.setUTCDate(end.getUTCDate() + 1)
 if (period === 'week') end.setUTCDate(end.getUTCDate() + 7)
 if (period === 'month') end.setUTCMonth(end.getUTCMonth() + 1)
 if (period === 'year') end.setUTCFullYear(end.getUTCFullYear() + 1)
 return { start, end }
}
export function summarize(rows: TokenBucket[], period: Period, date: string, model: string) {
 const { start, end } = bounds(period, date)
 const total = empty(), models = new Map<string, Summary>()
 const points: (Summary & { label: string; time: number })[] = []
 const step = period === 'day' ? 3600000 : 86400000
 for (let t = +start; t < +end; t += step) points.push({ ...empty(), time: t, label: new Date(t).toISOString().slice(period === 'day' ? 11 : 0, period === 'day' ? 16 : 10) })
 for (const r of rows) {
  const t = r.hour * 1000
  if (t < +start || t >= +end || (model && r.model !== model)) continue
  add(total, r); const m = models.get(r.model) || empty(); add(m, r); models.set(r.model, m)
  add(points[Math.floor((t - +start) / step)], r)
 }
 return { total, points, models: [...models].sort((a, b) => (b[1].input + b[1].output) - (a[1].input + a[1].output)) }
}
