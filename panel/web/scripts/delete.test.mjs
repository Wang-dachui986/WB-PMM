// Real browser, synthetic loopback API and native replies; never touches credentials.
import assert from 'node:assert/strict'
import http from 'node:http'
import {createRequire} from 'node:module'
import {fileURLToPath} from 'node:url'
import {build} from 'esbuild'
const require=createRequire(import.meta.url)
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright')
const {outputFiles}=await build({stdin:{contents:`import React from 'react'; import {createRoot} from 'react-dom/client'; import {HashRouter} from 'react-router-dom'; import Accounts from './src/pages/Accounts'; import {api} from './src/api'; window.testApi=api; createRoot(document.getElementById('root')).render(<HashRouter><Accounts session={{read_only:false,dangerous_ops:true}}/></HashRouter>);`,loader:'tsx',resolveDir:fileURLToPath(new URL('../',import.meta.url))},bundle:true,write:false,format:'iife',jsx:'automatic'})
let rows=[],failedUID='';const deleted=[]
const reset=count=>{rows=Array.from({length:count},(_,i)=>({uid:`fake-${String(i).padStart(3,'0')}`,nickname:`Synthetic ${i}`,domain:'www.codebuddy.cn',status:'healthy',has_file:true,in_gateway:true}));deleted.length=0;failedUID=''}
const server=http.createServer((req,res)=>{
 if(req.url==='/bundle.js'){res.setHeader('Content-Type','text/javascript');res.end(outputFiles[0].text);return}
 if(req.url==='/api/accounts'){res.setHeader('Content-Type','application/json');res.end(JSON.stringify({accounts:rows,gateway_ok:true}));return}
 if(req.url.startsWith('/api/')){res.writeHead(500);res.end('unexpected request');return}
 res.setHeader('Content-Type','text/html');res.end('<!doctype html><div id="root"></div><script src="/bundle.js"></script>')
})
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));let browser
try {
 browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE?{executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE}:{})})
 const page=await browser.newPage();page.setDefaultTimeout(10000)
 const origin=`http://127.0.0.1:${server.address().port}`;const errors=[];page.on('pageerror',e=>errors.push(e.message))
 await page.route('**/*',r=>r.request().url().startsWith(origin+'/')?r.continue():r.abort())
 await page.exposeFunction('nativeDelete',uid=>{deleted.push(uid);if(uid===failedUID)throw new Error('synthetic deletion failed');rows=rows.filter(a=>a.uid!==uid);return {ok:true,message:'deleted'}})
 await page.addInitScript(()=>{
  window.__workbuddyDocumentID='delete-test';window.calls=[];window.mode='normal';window.holdCleanup=false;window.held=[];let gen=0
  window.reply=(b,data={})=>window.__workbuddyBridgeReceive({id:b.id,ok:true,data})
  window.webkit={messageHandlers:{workbuddyNative:{postMessage(b){
   window.calls.push(b)
   if(b.action==='account.maintenance'&&b.payload.operation==='begin'){
    if(window.mode==='cancel'){queueMicrotask(()=>window.__workbuddyBridgeReceive({id:b.id,ok:false,error:{code:'cancelled',message:'已取消维护'}}));return}
    queueMicrotask(()=>window.reply(b,{instance_id:'fake',generation:String(++gen),action:b.payload.action,uids:b.payload.uids,expires_at:'2099-01-01T00:00:00Z'}));return
   }
   if(b.payload.operation==='cancel_pending'&&window.holdCleanup){window.held.push(b);return}
   if(b.action==='account.delete'){window.nativeDelete(b.payload.uid).then(data=>window.reply(b,data),()=>window.__workbuddyBridgeReceive({id:b.id,ok:false,error:{code:'operation_failed',message:'synthetic deletion failed'}}));return}
   queueMicrotask(()=>window.reply(b,b.action==='preferences.get'?{notes:{}}:{drained:true,cancelled:true,ok:true}))
  }}}}
 })
 const fresh=async count=>{reset(count);await page.goto(origin+'/#/accounts');await page.reload();await page.getByRole('button',{name:'Synthetic 0',exact:true}).waitFor()}
 const selectAll=()=>page.locator('thead input[type=checkbox]').check()
 const batch=()=>page.getByRole('button',{name:/^批量删除/})
 const confirm=()=>page.getByRole('button',{name:/^确认删除/})
 const unlocked=()=>page.waitForFunction(()=>!document.querySelector('.modal')&&!Array.from(document.querySelectorAll('button')).find(b=>b.textContent==='导入凭证').disabled)
 await fresh(3);assert.equal(await batch().isDisabled(),true)
 await selectAll();await batch().click();assert.equal(await page.locator('.modal li').count(),3)
 await page.getByRole('button',{name:'取消',exact:true}).click();await unlocked();assert.equal(deleted.length,0)
 assert.equal(await page.evaluate(()=>window.calls.filter(b=>b.action==='account.maintenance').length),0)
 console.log('PASS cancelling web confirmation sends no maintenance or deletion request')
 await batch().click();await page.evaluate(()=>{window.mode='cancel';window.holdCleanup=true});await confirm().click()
 await page.waitForFunction(()=>window.held.length>0)
 assert.equal(await batch().isDisabled(),true);assert.equal(deleted.length,0)
 await page.evaluate(()=>{window.holdCleanup=false;for(const b of window.held.splice(0))window.reply(b,{drained:true,cancelled:true})})
 await unlocked();assert.equal(rows.length,3)
 console.log('PASS native confirmation cancellation waits for cleanup then releases modal and controls')
 await page.evaluate(()=>window.mode='normal');await batch().click();await confirm().click();await unlocked()
 assert.equal(rows.length,0);assert.equal(deleted.length,3)
 assert.equal(await page.evaluate(()=>window.calls.filter(b=>b.action==='account.maintenance'&&b.payload.operation==='begin').length),2)
 assert.equal(await batch().isDisabled(),true)
 console.log('PASS cancelled deletion can be retried; one permit deletes exactly the three selected accounts')
 await fresh(3);failedUID='fake-001';await selectAll();await batch().click();await confirm().click();await unlocked()
 assert.deepEqual(deleted,['fake-000','fake-001']);assert.deepEqual(rows.map(a=>a.uid),['fake-001','fake-002'])
 assert.match(await batch().innerText(),/2/)
 console.log('PASS partial failure stops immediately and preserves remaining selection without retry')
 await fresh(1);await page.evaluate(()=>window.mode='cancel');await page.getByRole('button',{name:'删除',exact:true}).click();await confirm().click();await unlocked();assert.equal(deleted.length,0);assert.equal(rows.length,1)
 console.log('PASS single-account native cancellation leaves the page usable and credential intact')
 await fresh(100);await selectAll();await batch().click();assert.equal(await page.locator('.modal li').count(),100);await confirm().click();await unlocked()
 assert.equal(deleted.length,100);assert.equal(new Set(deleted).size,100);assert.equal(rows.length,0)
 const begins=await page.evaluate(()=>window.calls.filter(b=>b.action==='account.maintenance'&&b.payload.operation==='begin'))
 assert.equal(begins.length,1);assert.equal(begins[0].payload.uids.length,100)
 const before=deleted.length
 assert.equal(await page.evaluate(async()=>{try{await window.testApi.deleteAccount('not-selected',{action:'delete',uids:['fake-000'],instance_id:'fake',generation:'1'});return false}catch{return true}}),true)
 assert.equal(deleted.length,before)
 assert.deepEqual(errors,[])
 console.log('PASS 100-account deletion uses one bound permit; outside-UID deletion is rejected')
} finally {if(browser)await browser.close();await new Promise(resolve=>server.close(resolve))}
