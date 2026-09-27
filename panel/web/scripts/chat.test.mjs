// PLAYWRIGHT_MODULE=/path/to/playwright/index.js node scripts/chat.test.mjs
// Synthetic loopback endpoints; bundle in memory, no build output.
import assert from 'node:assert/strict'
import http from 'node:http'
import {createRequire} from 'node:module'
import {fileURLToPath} from 'node:url'
import {readFile} from 'node:fs/promises'
import {build} from 'esbuild'
const require=createRequire(import.meta.url)
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright')
const {outputFiles}=await build({stdin:{contents:`import React from 'react';import {createRoot} from 'react-dom/client';import Playground from './src/pages/Playground';import {api,streamChat,setUnauthorizedHandler} from './src/api';window.chatAPI={api,streamChat,setUnauthorizedHandler};createRoot(document.getElementById('root')).render(<Playground/>);`,loader:'tsx',resolveDir:fileURLToPath(new URL('../',import.meta.url))},bundle:true,write:false,format:'iife',jsx:'automatic'})
const css=await readFile(new URL('../src/styles.css',import.meta.url),'utf8')
const requests=[],pending=[]
let modelIds=['synthetic-model']
const modelMetadata={
 'cn:hy3':{name:'Hy3',description:'合成混元模型',context_length:192000,max_output_tokens:512,reasoning_supported_efforts:['low','high'],reasoning_default_effort:'high'},
 'global:hy3':{name:'Hy3',reasoning_supported_efforts:['low']},
}
const nextRequest=()=>new Promise(resolve=>pending.push(resolve))
const server=http.createServer(async(req,res)=>{
 if(req.url==='/chat.js'){res.setHeader('Content-Type','text/javascript');res.end(outputFiles[0].text);return}
 if(req.url==='/api/models'){res.setHeader('Content-Type','application/json');res.end(JSON.stringify({data:modelIds.map(id=>({id,context_length:1000,...modelMetadata[id]}))}));return}
 if(req.url.startsWith('/api/chat')){
  let body='';for await(const chunk of req)body+=chunk
  const item={res,payload:JSON.parse(body),path:req.url,closed:false};res.on('close',()=>{item.closed=true});requests.push(item)
  const resolve=pending.shift();assert.ok(resolve,'unexpected extra/replayed request');resolve(item);return
 }
 res.setHeader('Content-Type','text/html');res.end(`<!doctype html><html lang="zh"><head><style>${css}</style></head><body><div id="root"></div><script src="/chat.js"></script></body></html>`)
})
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
let browser
try{
 browser=await chromium.launch({headless:true});const page=await browser.newPage({viewport:{width:1280,height:900}})
 const origin=`http://127.0.0.1:${server.address().port}`
 await page.addInitScript(()=>Object.defineProperty(window,'__workbuddyDocumentID',{value:'c10-test-document',writable:false,configurable:false}))
 await page.route('**/*',route=>route.request().url().startsWith(origin+'/')?route.continue():route.abort())
 const errors=[];page.on('pageerror',err=>errors.push(err.message));await page.goto(origin);await page.locator('#chat-model').selectOption('synthetic-model')
 const input=page.locator('textarea[placeholder^="输入消息"]'),answer=()=>page.locator('.msg-assistant').last()
 const settle=()=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
 const start=async text=>{await input.fill(text);const arrived=nextRequest();await page.getByRole('button',{name:'发送',exact:true}).click();return arrived}
 const frame=(item,data)=>{if(!item.res.headersSent)item.res.setHeader('Content-Type','text/event-stream');item.res.write(`data: ${JSON.stringify(data)}\n\n`)}
 const finish=item=>{frame(item,{done:true,finish_reason:'stop'});item.res.end()}
 const waitClosed=async item=>{for(let n=0;n<100&&!item.closed;n++)await new Promise(resolve=>setTimeout(resolve,10));assert.ok(item.closed,'abort must close backend request')}
 let item; if (!process.argv.includes('--lifecycle-only')) {
 modelIds=['cn:hy3','global:hy3','cn:hy3-b','cn:auto','cn:glm-5.3','synthetic-model'];await page.getByRole('button',{name:'刷新模型列表',exact:true}).click();
 await page.locator('#chat-model').selectOption('cn:hy3');await settle();
 assert.equal(await page.locator('#chat-model option:checked').textContent(),'混元 HY3（cn:hy3）');
 assert.equal(await page.locator('#chat-model option[value="cn:hy3-b"]').textContent(),'混元 HY3-b（cn:hy3-b）');
 assert.equal(await page.locator('#chat-model option[value="cn:auto"]').textContent(),'自动选择（cn:auto）');
 assert.equal(await page.locator('#chat-model option[value="cn:glm-5.3"]').textContent(),'智谱 GLM-5.3（cn:glm-5.3）');
 assert.deepEqual(await page.locator('#chat-effort option').evaluateAll(items=>items.map(item=>item.value)),['','low','high']);
 assert.match(await page.locator('#chat-effort option:checked').textContent(),/high/);
 await page.locator('#chat-effort').selectOption('high');await page.getByLabel('最大输出 Token（留空用默认）',{exact:true}).fill('513');await input.fill('bounded output');
 const beforeInvalid=requests.length;await page.getByRole('button',{name:'发送',exact:true}).click();await page.getByText('最大输出不能超过该模型的 512 Token',{exact:true}).waitFor();assert.equal(requests.length,beforeInvalid);
 await page.locator('#chat-max-tokens').fill('200');item=await start('selected effort');assert.equal(item.payload.model,'cn:hy3');assert.deepEqual(item.payload.extra,{max_tokens:200,reasoning_effort:'high'});finish(item);await answer().getByText('已完成',{exact:true}).waitFor();
 modelMetadata['cn:hy3'].reasoning_supported_efforts=['low'];await page.getByRole('button',{name:'刷新模型列表',exact:true}).click();await page.waitForFunction(()=>document.getElementById('chat-effort').options.length===2);assert.equal(await page.locator('#chat-effort').inputValue(),'','removed effort is not retained');
 await page.locator('#chat-effort').selectOption('low');await page.locator('#chat-model').selectOption('global:hy3');await settle();assert.equal(await page.locator('#chat-effort').inputValue(),'','switching region resets effort even when supported');
 await page.locator('#chat-max-tokens').fill('');await page.getByLabel('流式输出（SSE）').uncheck();item=await start('model default');assert.equal(item.payload.model,'global:hy3');assert.equal(item.payload.extra.reasoning_effort,undefined);item.res.setHeader('Content-Type','application/json');item.res.end(JSON.stringify({result:{content:'default effort',finish_reason:'stop'}}));await answer().getByText('已完成',{exact:true}).waitFor();
 await page.getByLabel('流式输出（SSE）').check();await page.getByRole('button',{name:'清空对话',exact:true}).click();await page.locator('#chat-model').selectOption('synthetic-model');assert.equal(await page.locator('#chat-effort').isDisabled(),true);
 console.log('PASS: localized labels preserve IDs, model-specific effort reaches request, model/metadata changes clear effort, output limit blocks excess');
 modelIds=['replacement-model','synthetic-model'];await page.getByRole('button',{name:'刷新模型列表',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#chat-model')?.options.length===2);assert.equal(await page.locator('#chat-model').inputValue(),'synthetic-model','refresh retains a still available model');
 modelIds=['replacement-model'];await page.getByRole('button',{name:'刷新模型列表',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#chat-model')?.value==='replacement-model');assert.equal(await page.locator('#chat-model').inputValue(),'replacement-model','refresh replaces a removed model');
 modelIds=[];await page.getByRole('button',{name:'刷新模型列表',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#chat-model')?.value==='');assert.equal(await page.getByRole('button',{name:'发送',exact:true}).isDisabled(),true,'empty model list blocks send');
 modelIds=['synthetic-model'];await page.getByRole('button',{name:'刷新模型列表',exact:true}).click();await page.waitForFunction(()=>document.querySelector('#chat-model')?.value==='synthetic-model');console.log('PASS: model refresh retains valid choice, replaces removed choice and disables send when empty');
 item=await start('first draft');item.res.setHeader('Content-Type','text/event-stream')
 const bytes=Buffer.from('data:{"reasoning":"你好"}\r\n\r\n'),split=bytes.indexOf(Buffer.from('你'))+1
 item.res.write(bytes.subarray(0,split));item.res.write(bytes.subarray(split));await answer().getByText('你好',{exact:false}).waitFor()
 assert.equal(await input.inputValue(),'first draft');await new Promise(resolve=>setTimeout(resolve,90));frame(item,{content:'first answer'});finish(item)
 await answer().getByText('已完成',{exact:true}).waitFor();await answer().getByText('未返回用量',{exact:true}).waitFor();assert.equal(await input.inputValue(),'')
 const text=await answer().innerText(),first=Number(text.match(/首字 (\d+) ms/)[1]),elapsed=Number(text.match(/总耗时 (\d+) ms/)[1]);assert.ok(elapsed-first>=70,`wrong TTFB: ${text}`)
 item=await start('second draft');assert.deepEqual(item.payload.messages.map(m=>m.content),['first draft','first answer','second draft'])
 frame(item,{content:'partial stopped'});await answer().getByText('partial stopped',{exact:true}).waitFor();await page.getByRole('button',{name:'停止',exact:true}).click()
 await answer().getByText('已停止',{exact:true}).waitFor();await waitClosed(item);assert.equal(await input.inputValue(),'second draft')
 item=await start('after stopped');assert.deepEqual(item.payload.messages.map(m=>m.content),['first draft','first answer','after stopped']);frame(item,{content:'network partial'});item.res.end()
 await answer().getByText(/连接中断：未收到完成标记/).waitFor();assert.equal(await input.inputValue(),'after stopped');assert.equal(await answer().getByText('已完成',{exact:true}).count(),0)
 item=await start('after interrupted');assert.deepEqual(item.payload.messages.map(m=>m.content),['first draft','first answer','after interrupted'])
 frame(item,{content:'limited',finish_reason:'length',usage:{prompt_tokens:0}});frame(item,{done:true,finish_reason:'length'});item.res.end()
 await answer().getByText('回答达到输出上限，内容未完成',{exact:true}).waitFor();assert.equal(await answer().getByText('输入 0 tok',{exact:true}).count(),1);assert.equal(await answer().getByText(/输出 .* tok/).count(),0);assert.equal(await input.inputValue(),'after interrupted')
 item=await start('scroll request');assert.deepEqual(item.payload.messages.map(m=>m.content),['first draft','first answer','scroll request'])
 frame(item,{content:Array(160).fill('scroll line').join('\n')});await answer().getByText(/scroll line/).first().waitFor()
 const before=await page.locator('.chat-log').evaluate(log=>{log.scrollTop=0;log.dispatchEvent(new Event('scroll'));return log.scrollTop})
 frame(item,{content:'\nnew delta'});await answer().getByText(/new delta/).waitFor();assert.equal(await page.locator('.chat-log').evaluate(log=>log.scrollTop),before)
 await input.fill('newer unsent draft');finish(item);await answer().getByText('已完成',{exact:true}).waitFor();assert.equal(await input.inputValue(),'newer unsent draft')
 const count=requests.length;await input.dispatchEvent('compositionstart');await input.dispatchEvent('keydown',{key:'Enter',code:'Enter',metaKey:true,isComposing:true});await settle();assert.equal(requests.length,count)
 await input.dispatchEvent('compositionend');const shortcut=nextRequest();await input.press('Meta+Enter');item=await shortcut;frame(item,{content:'shortcut'});finish(item);await answer().getByText('已完成',{exact:true}).waitFor()
 await page.getByLabel('流式输出（SSE）').uncheck();item=await start('nonstream cancelled');assert.equal(item.path,'/api/chat')
 await page.getByRole('button',{name:'停止',exact:true}).click();await answer().getByText('已停止',{exact:true}).waitFor();await waitClosed(item);assert.equal(await input.inputValue(),'nonstream cancelled')
 for(const usage of [undefined,{total_tokens:4},{prompt_tokens:0,completion_tokens:0,total_tokens:0}]){
  item=await start('nonstream usage');item.res.setHeader('Content-Type','application/json');item.res.end(JSON.stringify({result:{content:'sync answer',finish_reason:'stop',usage},elapsed_ms:1}))
  await answer().getByText('已完成',{exact:true}).waitFor()
  await answer().getByText(usage===undefined?'未返回用量':`总计 ${usage.total_tokens} tok`,{exact:true}).waitFor()
  if(usage!==undefined)assert.equal(await answer().getByText('未返回用量',{exact:true}).count(),0)
 }
 // Already-buffered old responses can ignore abort. They must not alter new state.
 for(const fail of [false,true]){
  await page.evaluate(()=>{window.realFetch=window.fetch;window.held=[];window.fetch=(url,init)=>url==='/api/chat'?new Promise((resolve,reject)=>window.held.push({resolve,reject})):window.realFetch(url,init)})
  await input.fill('old buffered');await page.getByRole('button',{name:'发送',exact:true}).click();await page.getByRole('button',{name:'清空对话',exact:true}).click()
  await input.fill('new buffered');await page.getByRole('button',{name:'发送',exact:true}).click()
  await page.evaluate(fail=>{const old=window.held[0];if(fail)old.reject(new Error('late failure'));else old.resolve(new Response(JSON.stringify({result:{content:'late obsolete',finish_reason:'stop'},elapsed_ms:1}),{status:200}))},fail)
  await settle();assert.equal(await page.getByText('late obsolete',{exact:true}).count(),0);assert.equal(await page.getByRole('button',{name:'停止',exact:true}).count(),1)
  await page.evaluate(()=>{window.held[1].resolve(new Response(JSON.stringify({result:{content:'current answer',finish_reason:'stop'},elapsed_ms:1}),{status:200}));window.fetch=window.realFetch})
  await answer().getByText('current answer',{exact:true}).waitFor();await answer().getByText('已完成',{exact:true}).waitFor()
 }
 for(const wire of ['data: {"error":"synthetic SSE error"}\n\ndata: {"done":true}\n\n','data: invalid\n\ndata: {"done":true}\n\n','data: {"content":"half"}']){
  const arrived=nextRequest();const result=page.evaluate(()=>new Promise(resolve=>{let deltas=[];let error;window.chatAPI.streamChat({model:'synthetic-model',messages:[]},d=>deltas.push(d),e=>error=e,state=>resolve({state,error,deltas}))}))
  item=await arrived;item.res.setHeader('Content-Type','text/event-stream');item.res.end(wire);const outcome=await result
  assert.equal(outcome.state,'interrupted');assert.ok(outcome.error);assert.equal(outcome.deltas.some(d=>d.done),false)
 }
 // Exactly one native recovery per 401, no replay; web login callback unchanged.
 for(const desktop of [true,false]){
  await page.evaluate(desktop=>{window.recovered=0;window.unauthorized=0;window.chatAPI.setUnauthorizedHandler(()=>window.unauthorized++);if(desktop)window.webkit={messageHandlers:{workbuddyNative:{postMessage(body){if(body.document_id!=='c10-test-document')throw new Error('invalid document_id');window.recovered++;queueMicrotask(()=>window.__workbuddyBridgeReceive({id:body.id,ok:true,data:{}}))}}}};else delete window.webkit},desktop)
  for(const stream of [true,false]){
   const arrived=nextRequest(),countBefore=requests.length
   const result=page.evaluate(stream=>stream?new Promise(resolve=>window.chatAPI.streamChat({model:'synthetic-model',messages:[]},()=>{},()=>{},state=>resolve(state))):window.chatAPI.api.chat({model:'synthetic-model',messages:[]}).catch(e=>e.status),stream)
   item=await arrived;item.res.statusCode=401;item.res.end(JSON.stringify({error:'synthetic unauthorized'}));assert.equal(await result,stream?'interrupted':401);assert.equal(requests.length,countBefore+1)
  }
  assert.deepEqual(await page.evaluate(()=>[window.recovered,window.unauthorized]),desktop?[2,0]:[0,2])
 }
 }
 let epoch=1;
 const lifecycle=async(kind,id='c10-test-document',value=String(epoch++))=>page.evaluate(({kind,id,value})=>window.dispatchEvent(new CustomEvent('workbuddy:lifecycle',{detail:{kind,document_id:id,epoch:value}})),{kind,id,value});
 for(const streaming of [true,false]){
  await page.getByLabel('流式输出（SSE）').setChecked(streaming);
  item=await start(streaming?'sleep stream':'wake nonstream');
  if(streaming){frame(item,{content:'before sleep'});await answer().getByText('before sleep',{exact:true}).waitFor()}
  await lifecycle('sleep','obsolete-document');await settle();assert.equal(await page.getByRole('button',{name:'停止',exact:true}).count(),1);
  await lifecycle(streaming?'sleep':'wake');await answer().getByText('已中断',{exact:true}).waitFor();await waitClosed(item);
  assert.equal(await answer().getByText('已停止',{exact:true}).count(),0);const count=requests.length;
  await lifecycle('wake');await settle();assert.equal(requests.length,count,'wake must not replay chat');
  item=await start('new after lifecycle');await lifecycle('sleep','c10-test-document','1');await settle();assert.equal(await page.getByRole('button',{name:'停止',exact:true}).count(),1,'old epoch must not abort new chat');
  await page.getByRole('button',{name:'停止',exact:true}).click();await answer().getByText('已停止',{exact:true}).waitFor();await waitClosed(item);
 }
 await page.getByLabel('流式输出（SSE）').uncheck();
 await page.evaluate(()=>{window.realFetch=window.fetch;window.held=[];window.fetch=(url,init)=>url==='/api/chat'?new Promise((resolve,reject)=>window.held.push({resolve,reject})):window.realFetch(url,init)});
 await input.fill('late after wake');await page.getByRole('button',{name:'发送',exact:true}).click();await lifecycle('wake');await answer().getByText('已中断',{exact:true}).waitFor();
 await input.fill('new after wake');await page.getByRole('button',{name:'发送',exact:true}).click();
 await page.evaluate(()=>window.held[0].resolve(new Response(JSON.stringify({result:{content:'obsolete lifecycle answer',finish_reason:'stop'},elapsed_ms:1}))));await settle();
 assert.equal(await page.getByText('obsolete lifecycle answer',{exact:true}).count(),0);assert.equal(await page.getByRole('button',{name:'停止',exact:true}).count(),1);
 await page.evaluate(()=>{window.held[1].resolve(new Response(JSON.stringify({result:{content:'new lifecycle answer',finish_reason:'stop'},elapsed_ms:1})));window.fetch=window.realFetch});await answer().getByText('已完成',{exact:true}).waitFor();
 console.log('PASS LIFECYCLE: owner, monotonic epoch, stream/nonstream interruption, late result exclusion, no replay, manual stop preserved');
 assert.deepEqual(errors,[])
 if (!process.argv.includes('--lifecycle-only')) console.log('PASS: UTF8/CRLF/no-space SSE, missing/partial/zero usage, first-token timing, EOF/error/length, multi-turn history, both-mode cancel, stale callbacks, draft, scroll, IME/CmdEnter, desktop401 no replay, web401')
}finally{if(browser)await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve))}
