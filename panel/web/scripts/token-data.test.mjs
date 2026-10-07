import assert from 'node:assert/strict'
import fs from 'node:fs'
import ts from 'typescript'
const source = fs.readFileSync(new URL('../src/pages/tokenData.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ES2020 } }).outputText
const { bounds, summarize } = await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`)
assert.equal(bounds('week', '2026-10-07').start.toISOString(), '2026-10-05T00:00:00.000Z')
assert.equal(bounds('month', '2024-02-15').end.toISOString(), '2024-03-01T00:00:00.000Z')
assert.equal(summarize([], 'year', '2024-01-01', '').points.length, 366)
const hour = Date.parse('2026-10-07T12:00:00Z') / 1000
const rows = [{hour, model:'a', input:100,output:20,requests:2,observed:1}, {hour,model:'b',input:30,output:10,requests:1,observed:1}, {hour:hour+86400,model:'a',input:500,output:0,requests:1,observed:1}]
const day=summarize(rows,'day','2026-10-07','')
assert.deepEqual(day.total,{input:130,output:30,requests:3,observed:2})
assert.equal(day.points[12].input,130)
assert.equal(day.models[0][0],'a')
assert.equal(summarize(rows,'day','2026-10-07','b').total.input,30)
assert.equal(summarize(rows,'month','2026-10-01','').total.input,630)
console.log('Token data: calendar/leap year/model filter/observed usage/bucket totals passed')
