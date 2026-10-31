// Isolated React + real browser tests. All requests are synthetic loopback, no providers.
import assert from 'node:assert/strict'
import http from 'node:http'
import {createRequire} from 'node:module'
import {fileURLToPath} from 'node:url'
import {build} from 'esbuild'
import {readFile} from 'node:fs/promises'
const require=createRequire(import.meta.url)
const {chromium}=require(process.env.PLAYWRIGHT_MODULE||'playwright')
const {outputFiles}=await build({stdin:{contents:`import React from 'react'; import {createRoot} from 'react-dom/client'; import {HashRouter,Routes,Route} from 'react-router-dom'; import Accounts from './src/pages/Accounts'; import LoginWizard from './src/pages/LoginWizard'; import * as bridge from './src/desktopBridge'; import {api} from './src/api'; window.m04={...bridge,api}; const session={read_only:false,dangerous_ops:true}; createRoot(document.getElementById('root')).render(<HashRouter><Routes><Route path='/accounts' element={<Accounts session={session}/>} /><Route path='/login' element={<LoginWizard session={session} onDone={async()=>{window.done=(window.done||0)+1;return null}}/>}/></Routes></HashRouter>);`,loader:'tsx',resolveDir:fileURLToPath(new URL('../',import.meta.url))},bundle:true,write:false,format:'iife',jsx:'automatic',plugins:process.env.M04_ACCOUNTS_BASELINE?[{name:'accounts-baseline',setup(builder){builder.onLoad({filter:/[/\\]pages[/\\]Accounts\.tsx$/},async args=>({contents:await readFile(process.env.M04_ACCOUNTS_BASELINE,'utf8'),loader:'tsx',resolveDir:fileURLToPath(new URL('../src/pages',import.meta.url))}))}}]:[]})
const requests=[]
const heldAccounts=[],heldBatch=[],heldLoginStarts=[]
let holdNextAccounts=false,holdNextBatch=false,holdNextLoginStart=false
let pollMode='pending',confirmed=false,pollActive=0,pollMax=0,taskRunning=true
const login=()=>({id:'a'.repeat(32),region:'cn',status:pollMode==='pending'?'pending':'success',saved:confirmed,uid:'fake001',message:'synthetic',requires_overwrite_confirmation:pollMode==='overwrite'&&!confirmed,save_error_code:pollMode==='save_failure'?'CREDENTIAL_DOMAIN_UNSUPPORTED':''})
const presentation=process.argv.includes('--presentation-only')
const account={uid:'fake001',nickname:'Synthetic',domain:'copilot.tencent.com',status:'healthy',in_gateway:true,has_file:true,has_refresh_token:true,manual_disabled:false}
const presentationAccounts=[{...account,domain:'',credits:0,live_credits:0,credits_at:'2026-09-26T00:00:00Z',success_count:5,err_total:2,in_flight:2},
 {...account,uid:'global002',nickname:'Global synthetic',domain:'www.workbuddy.ai',status:'cooling',credits:0},
 {...account,uid:'cnalias004',nickname:'CodeBuddy bare synthetic',domain:'codebuddy.cn'},
 {...account,uid:'cnalias005',nickname:'CodeBuddy www synthetic',domain:'www.codebuddy.cn'},
 {...account,uid:'memory003',nickname:'Memory synthetic',domain:'',has_file:false,status:'unknown',credits:0}]
const server=http.createServer(async(req,res)=>{
 if(req.url==='/bundle.js'){res.setHeader('Content-Type','text/javascript');res.end(outputFiles[0].text);return}
 if(!req.url.startsWith('/api/')){res.setHeader('Content-Type','text/html');res.end('<!doctype html><div id="root"></div><script src="/bundle.js"></script>');return}
 let raw='';for await(const chunk of req)raw+=chunk
 requests.push({path:req.url,headers:req.headers,method:req.method,body:raw?JSON.parse(raw):null})
 const send=value=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(value))}
 if(req.url==='/api/accounts'){if(holdNextAccounts){holdNextAccounts=false;heldAccounts.push(send);return}send({accounts:presentation?presentationAccounts:[account],gateway_ok:true});return}
 if(req.url==='/api/accounts/fake001'||req.url==='/api/accounts/fake001?silent=true'){send({account:{...presentationAccounts[0],status:'unknown',success_count:0,err_total:0,in_flight:0},credits:{remain:0,used:9,size:9,packages:1}});return}
 if(req.url==='/api/login/start'){if(holdNextLoginStart){holdNextLoginStart=false;heldLoginStarts.push(send);return}send(login());return}
 if(req.url.endsWith('/confirm-overwrite')){confirmed=true;send(login());return}
 if(req.url.endsWith('/cancel')){send({...login(),status:'cancelled'});return}
 if(req.url.endsWith('/poll')){pollActive++;pollMax=Math.max(pollMax,pollActive);await new Promise(resolve=>setTimeout(resolve,100));pollActive--;send(login());return}
 if(req.url==='/api/tasks/refresh'){if(holdNextBatch){holdNextBatch=false;heldBatch.push((value,status=200)=>{res.statusCode=status;send(value)});return}send({id:'task1',running:true});return}
 if(req.url==='/api/tasks/task1'){send({id:'task1',title:'Synthetic batch',running:taskRunning,total:1,done:taskRunning?0:1,ok:taskRunning?0:1,failed:0,items:[]});return}
 if(req.url.endsWith('/refresh')){send({ok:true,message:'refreshed'});return}
 res.statusCode=404;send({error:'unexpected test endpoint'})
})
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
let browser
try{
 browser=await chromium.launch({headless:true,...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE?{executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE}:{})});const page=await browser.newPage()
 const errors=[];page.on('pageerror',error=>errors.push(error.message))
 const origin=`http://127.0.0.1:${server.address().port}`
 await page.route('**/*',route=>route.request().url().startsWith(origin+'/')?route.continue():route.abort())
 await page.addInitScript((presentation)=>{
  window.notes=presentation?{fake001:'Local alias'}:{};
  if(presentation)Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.copied=text}}});
  Object.defineProperty(window,'__workbuddyDocumentID',{value:'m04-test-document',writable:false,configurable:false})
  window.nativeCalls=[];window.held=[];window.generation=0;window.mode='normal';window.timeouts=[]
  const realTimeout=window.setTimeout.bind(window)
  window.setTimeout=(fn,ms,...args)=>{window.timeouts.push(ms);return realTimeout(fn,window.forceTimeout&&ms===1050000?0:ms,...args)}
  window.respond=(body,data={})=>window.__workbuddyBridgeReceive({id:body.id,ok:true,data})
  window.webkit={messageHandlers:{workbuddyNative:{postMessage(body){
   if(body.document_id!=='m04-test-document')throw Error('bad document_id')
   window.nativeCalls.push(body)
   if(body.action==='preferences.get'){queueMicrotask(()=>window.respond(body,{notes:window.notes}));return}
   if(body.action==='preferences.update'){window.notes={...window.notes,[body.payload.note.uid]:body.payload.note.value};queueMicrotask(()=>window.respond(body,{notes:window.notes}));return}
   if(body.action==='account.delete')delete window.notes[body.payload.uid]
   if(body.action==='account.maintenance'&&body.payload.operation==='begin'){
    const lease={instance_id:'fake-instance',generation:String(++window.generation),action:body.payload.action==='reauthorize'?'login':body.payload.action,uids:body.payload.uids,expires_at:'2099-01-01T00:00:00Z'}
    if(window.mode==='hold_begin'||window.forceTimeout){window.held.push({body,data:lease});return}
    queueMicrotask(()=>window.respond(body,lease));return
   }
   if(body.action==='auth.openBrowser'&&window.mode==='fail_open'){
    queueMicrotask(()=>window.__workbuddyBridgeReceive({id:body.id,ok:false,error:{code:'operation_failed',message:'模拟浏览器不可用'}}));return
   }
   if(body.action==='files.importAuth'&&body.payload.operation==='preview'){
    const data={preview_id:'00000000-0000-0000-0000-000000000001',uid:'fake001',region:'cn',expires_at:1999999999,overwrite_required:true}
    if(window.mode==='hold_preview'){window.held.push({body,data});return}
    queueMicrotask(()=>window.respond(body,data));return
   }
   if((window.mode==='hold_end'&&body.payload.operation==='end')||(window.forceTimeout&&body.payload.operation==='cancel_pending')){window.held.push({body,data:{drained:true}});return}
   queueMicrotask(()=>window.respond(body,{ok:true,opened:true,drained:true,cancelled:true,message:'deleted'}))
  }}}}
 },presentation)
 const calls=()=>page.evaluate(()=>window.nativeCalls)
 const count=async(op)=>(await calls()).filter(x=>x.payload.operation===op).length
 const waitCall=async op=>page.waitForFunction(op=>window.nativeCalls.some(x=>x.payload.operation===op),op)
 const settle=()=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
 if(presentation){
  await page.goto(origin+'/#/accounts');await page.getByRole('button',{name:'Local alias',exact:true}).waitFor();
  const row=page.locator('tbody tr').filter({has:page.getByRole('button',{name:'Local alias',exact:true})});
  assert.match(await row.innerText(),/CN/);assert.match(await row.innerText(),/观测/);assert.equal(await row.locator('td').nth(3).innerText().then(text=>text.split('\n')[0]),'0');
  assert.match(await page.locator('tbody tr').filter({hasText:'Global synthetic'}).innerText(),/未知/);
  await page.getByLabel('账号区域').selectOption('unknown');await page.getByRole('button',{name:'Memory synthetic',exact:true}).waitFor();assert.equal(await page.locator('tbody tr').count(),1);
  await page.getByLabel('账号区域').selectOption('global');await page.getByRole('button',{name:'Global synthetic',exact:true}).waitFor();assert.equal(await page.locator('tbody tr').count(),1);
  await page.getByLabel('账号区域').selectOption('cn');await page.getByRole('button',{name:'Local alias',exact:true}).waitFor();
  assert.equal(await page.locator('tbody tr').count(),3,'both CodeBuddy credential domains are CN');
  await row.getByRole('button',{name:'复制 UID',exact:true}).click();assert.equal(await page.evaluate(()=>window.copied),'fake001');
  console.log('PASS PRESENTATION credential region, gateway-only unknown, observed zero, unknown balance and full UID copy');
  await row.getByRole('button',{name:'备注',exact:true}).click();await page.getByLabel('备注（最多 256 字节）').fill('Updated note');await page.getByRole('button',{name:'保存备注',exact:true}).click();await page.getByRole('button',{name:'Updated note',exact:true}).waitFor();
  await page.getByRole('button',{name:'Updated note',exact:true}).click();await page.getByText('本地备注',{exact:true}).waitFor();assert.equal(await page.getByText('猫猫旅行',{exact:true}).count(),0);assert.equal(await page.getByText('正在查询上游…',{exact:true}).count(),0);
  assert.equal(requests.filter(r=>r.path.startsWith('/api/accounts/fake001')).at(-1)?.path,'/api/accounts/fake001?silent=true','desktop detail must use cached-only API mode');
  assert.match(await page.locator('.modal').innerText(),/运行状态观测/);assert.equal(await page.locator('.modal .stat').nth(1).locator('.stat-value').innerText(),'5');
  await page.getByRole('button',{name:'关闭',exact:true}).last().click();console.log('PASS PRESENTATION notes edit and detail use cached profile plus matching runtime snapshot');
  const web=await browser.newPage();await web.route('**/*',route=>route.request().url().startsWith(origin+'/')?route.continue():route.abort());
  await web.goto(origin+'/#/accounts');await web.getByRole('button',{name:'Synthetic',exact:true}).click();await web.getByText('猫猫旅行',{exact:true}).waitFor();
  assert.equal(requests.filter(r=>r.path.startsWith('/api/accounts/fake001')).at(-1)?.path,'/api/accounts/fake001','ordinary web detail retains normal API mode');
  await web.close();console.log('PASS PRESENTATION desktop detail uses silent URL and web detail keeps normal URL');
  await page.locator('tbody tr').filter({hasText:'Updated note'}).getByRole('button',{name:'删除',exact:true}).click();await page.getByRole('button',{name:'确认删除',exact:true}).click();
  await page.getByRole('button',{name:'Synthetic',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'Updated note',exact:true}).count(),0);assert.equal(await page.evaluate(()=>window.notes.fake001),undefined);
  assert.equal(requests.filter(r=>/credits|travel|poll|refresh/.test(r.path)).length,0);console.log('PASS PRESENTATION successful delete clears visible note cache; details never query provider');
 } else {
 const waitServer=async predicate=>{for(let i=0;i<200;i++){if(predicate())return;await new Promise(resolve=>setTimeout(resolve,10))}throw Error('held request not reached')}
 await page.goto(origin+'/#/login');await page.reload();await page.evaluate(()=>window.mode='fail_open')
 const pendingPoll=page.waitForResponse(response=>response.url().endsWith('/poll'))
 await page.getByRole('button',{name:/发起授权/}).click()
 await page.getByRole('button',{name:'打开授权页面',exact:true}).waitFor()
 assert.equal(await count('begin'),1)
 assert.ok((await page.evaluate(()=>window.timeouts)).includes(1050000),'maintenance uses bounded action deadline')
 await page.waitForFunction(()=>window.nativeCalls.filter(x=>x.action==='auth.openBrowser').length===1)
 await page.getByText(/未能自动打开浏览器.*请点击“打开授权页面”重试/).waitFor()
 await pendingPoll
 assert.equal(await page.getByText(/未能自动打开浏览器/).count(),1,'polling preserves browser retry guidance')
 assert.equal(await count('end'),0,'browser launch failure keeps maintenance lease')
 await page.evaluate(()=>window.mode='normal')
 await page.getByRole('button',{name:'打开授权页面',exact:true}).click()
 await page.waitForFunction(()=>window.nativeCalls.filter(x=>x.action==='auth.openBrowser').length===2)
 assert.deepEqual((await calls()).filter(x=>x.action==='auth.openBrowser').map(x=>x.payload),[{session_id:'a'.repeat(32)},{session_id:'a'.repeat(32)}])
 await page.getByText(/未能自动打开浏览器/).waitFor({state:'hidden'})
 pollMode='overwrite'
 await page.getByRole('button',{name:'立即检查',exact:true}).click();await page.getByRole('button',{name:'立即检查',exact:true}).click()
 await page.getByRole('button',{name:'确认覆盖同一账号',exact:true}).waitFor()
 assert.equal(pollMax,1,'poll is singleflight');assert.equal(await count('end'),0,'saved=false holds same lease')
 await page.getByRole('button',{name:'确认覆盖同一账号',exact:true}).click()
 await page.waitForFunction(()=>window.done===1)
 assert.equal(await count('begin'),1);assert.equal(await count('end'),1)
 const writes=requests.filter(x=>x.path.startsWith('/api/login/'))
 assert.ok(writes.every(x=>x.headers['x-workbuddy-panel-instance']==='fake-instance'&&x.headers['x-workbuddy-maintenance-generation']==='1'))
 assert.equal(writes.filter(x=>x.path==='/api/login/start').length,1,'save retry reuses session')
  console.log('PASS M04 React auto browser once, manual retry after failure, lease headers, singleflight login and overwrite retry')
  pollMode='pending';confirmed=false
  await page.goto(origin+'/#/login');await page.reload();await page.getByRole('button',{name:/发起授权/}).click();await page.getByRole('button',{name:'打开授权页面',exact:true}).waitFor()
  pollMode='save_failure';await page.getByRole('button',{name:'立即检查',exact:true}).click();await page.getByText(/凭证区域暂不受支持/).waitFor()
  assert.equal(await count('end'),0,'save failure retains the same lease');await page.getByRole('button',{name:'取消',exact:true}).click()
  console.log('PASS M04 React safe save code is visible while the unsaved session stays open')
  // An unmounted SPA flow cancels its precise begin ID even before a lease arrives.
  pollMode='pending';confirmed=false
  await page.goto(origin+'/#/login');await page.reload();await page.evaluate(()=>window.mode='hold_begin')
 const starts=requests.filter(x=>x.path==='/api/login/start').length
 await page.getByRole('button',{name:/发起授权/}).click();await waitCall('begin')
 await page.getByRole('link',{name:'← 返回账号管理',exact:true}).click();await waitCall('cancel_pending')
 let all=await calls();assert.equal(all.find(x=>x.payload.operation==='cancel_pending').payload.request_id,all.find(x=>x.payload.operation==='begin').id)
 await page.evaluate(()=>{for(const held of window.held.splice(0))window.respond(held.body,held.data)})
 await settle();assert.equal(requests.filter(x=>x.path==='/api/login/start').length,starts)
 assert.equal((await calls()).filter(x=>x.action==='auth.openBrowser').length,0,'unmount before lease cannot open browser')
 console.log('PASS M04 React unmount before begin reply cancels exact request and prevents login start')
 await page.goto(origin+'/#/login');await page.reload();holdNextLoginStart=true
 await page.getByRole('button',{name:/发起授权/}).click();await waitServer(()=>heldLoginStarts.length===1)
 await page.getByRole('button',{name:'取消等待',exact:true}).click();await waitCall('cancel_pending')
 heldLoginStarts.shift()(login());await settle()
 assert.equal((await calls()).filter(x=>x.action==='auth.openBrowser').length,0,'cancel while loginStart is pending cannot open browser')
 console.log('PASS M04 React cancelled login start never opens browser')
 await page.goto(origin+'/#/login');await page.reload();await page.getByRole('button',{name:/发起授权/}).click();await page.getByRole('button',{name:'打开授权页面',exact:true}).waitFor()
 await page.getByRole('link',{name:'← 返回账号管理',exact:true}).click();await waitCall('cancel_pending')
 assert.ok(requests.some(x=>x.path.endsWith('/cancel')))
 console.log('PASS M04 React unmount cancels backend login then native maintenance')
 // Batch keeps its lease until task really reports completion.
 await page.goto(origin+'/#/accounts');await page.reload();await page.getByText('Synthetic',{exact:true}).first().waitFor()
 await page.locator('thead input[type=checkbox]').check()
 await page.evaluate(()=>window.mode='hold_end');await page.getByRole('button',{name:'批量刷新 Token',exact:true}).click()
 await page.getByText('Synthetic batch',{exact:true}).waitFor();assert.equal(await count('end'),0)
 taskRunning=false;await waitCall('end')
 assert.equal(await page.getByRole('button',{name:'批量刷新 Token',exact:true}).isDisabled(),true,'end ack retains maintenance UI')
 await page.evaluate(()=>{window.mode='normal';for(const held of window.held.splice(0))window.respond(held.body,held.data)})
 await page.getByRole('button',{name:'批量刷新 Token',exact:true}).waitFor({state:'visible'});await page.waitForFunction(()=>!Array.from(document.querySelectorAll('button')).find(b=>b.textContent==='批量刷新 Token').disabled)
 await page.getByRole('button',{name:'关闭',exact:true}).last().click()
 assert.ok(requests.find(x=>x.path==='/api/tasks/refresh').headers['x-workbuddy-maintenance-generation'])
 console.log('PASS M04 React batch completion retains lease through end acknowledgement')
 // R2 P1: A's continuation must never clean up or overwrite the next flow B.
 const freshAccounts=async()=>{taskRunning=true;await page.goto(origin+'/#/accounts');await page.reload();await page.getByText('Synthetic',{exact:true}).first().waitFor();await page.locator('thead input[type=checkbox]').check()}
 const cancelA=async()=>{await page.getByRole('button',{name:'取消维护',exact:true}).click();await page.waitForFunction(()=>!Array.from(document.querySelectorAll('button')).find(b=>b.textContent==='批量刷新 Token').disabled)}
 const startB=async()=>{await page.locator('thead input[type=checkbox]').check();await page.getByRole('button',{name:'批量刷新 Token',exact:true}).click();await page.getByText('Synthetic batch',{exact:true}).waitFor();return (await calls()).filter(x=>x.payload.operation==='begin').at(-1)}
 const assertBActive=async begin=>{
  const history=await calls()
  assert.equal(history.filter(x=>x.action==='account.maintenance'&&((x.payload.operation==='end'&&x.payload.generation==='2')||(x.payload.operation==='cancel_pending'&&x.payload.request_id===begin.id))).length,0,'old A must not end/cancel B')
  assert.equal(await page.getByRole('button',{name:'批量刷新 Token',exact:true}).isDisabled(),true,'B keeps maintenance active')
  await page.getByText('Synthetic batch',{exact:true}).waitFor()
 }
 for(const operation of ['single','delete']){
  await freshAccounts();holdNextAccounts=true
  const row=page.locator('tbody tr').filter({hasText:'Synthetic'})
  if(operation==='single')await row.getByRole('button',{name:'刷新',exact:true}).click()
  else{await row.getByRole('button',{name:'删除',exact:true}).click();await page.getByRole('button',{name:'确认删除',exact:true}).click()}
  await waitServer(()=>heldAccounts.length===1)
  await cancelA();const b=await startB()
  const reply=page.waitForResponse(response=>response.url()===origin+'/api/accounts')
  heldAccounts.shift()({accounts:[{...account,nickname:'Obsolete A list'}],gateway_ok:false})
  await (await reply).finished();await settle()
  await assertBActive(b);assert.equal(await page.getByText('Obsolete A list',{exact:true}).count(),0,'old list cannot overwrite B UI')
  console.log(`PASS M04 R2 ${operation} A late list cannot end/cancel or overwrite B`)
 }
 for(const outcome of ['success','failure']){
  await freshAccounts();holdNextBatch=true
  await page.getByRole('button',{name:'批量刷新 Token',exact:true}).click();await waitServer(()=>heldBatch.length===1)
  await cancelA();const b=await startB()
  const reply=page.waitForResponse(response=>response.url()===origin+'/api/tasks/refresh')
  if(outcome==='success')heldBatch.shift()({id:'task-obsolete',running:false})
  else heldBatch.shift()({error:'Obsolete A failure'},503)
  await (await reply).finished();await settle()
  await assertBActive(b)
  assert.equal(requests.filter(request=>request.path==='/api/tasks/task-obsolete').length,0,'old batch task must not mount or poll')
  assert.equal(await page.getByText('Obsolete A failure',{exact:true}).count(),0,'old error must not replace B UI')
  console.log(`PASS M04 R2 batch A late ${outcome} cannot end/cancel or replace B task`)
 }
 await page.getByRole('button',{name:'取消任务',exact:true}).click()
 await page.getByText('Synthetic batch',{exact:true}).waitFor({state:'hidden'})
 // File contents never enter web; existing account needs explicit overwrite.
 await page.getByRole('button',{name:'导入凭证',exact:true}).click();assert.equal(await page.locator('textarea').count(),0)
 await page.getByRole('button',{name:'选择凭证 JSON 文件',exact:true}).click();await page.getByText('已存在同区域账号，确认覆盖其凭证',{exact:false}).waitFor()
 assert.equal(await page.getByRole('button',{name:'确认导入',exact:true}).isDisabled(),true)
 await page.getByRole('checkbox',{name:'已存在同区域账号，确认覆盖其凭证'}).check();await page.getByRole('button',{name:'确认导入',exact:true}).click()
 await page.getByText('账号凭证已导入',{exact:true}).waitFor();all=await calls()
 assert.deepEqual(all.find(x=>x.action==='files.importAuth'&&x.payload.operation==='confirm').payload,{operation:'confirm',preview_id:'00000000-0000-0000-0000-000000000001',overwrite:true})
 assert.equal(requests.filter(x=>x.path==='/api/accounts/import').length,0)
 await page.getByRole('button',{name:'导入凭证',exact:true}).click();await page.evaluate(()=>window.mode='hold_preview')
 await page.getByRole('button',{name:'选择凭证 JSON 文件',exact:true}).click();await page.getByRole('button',{name:'取消',exact:true}).click()
 await page.waitForFunction(()=>window.nativeCalls.some(x=>x.action==='files.importAuth'&&x.payload.operation==='cancel_pending'))
 console.log('PASS M04 React native-only file preview, explicit overwrite and pending picker cancellation')
 // A deadline cannot report cancellation until native cleanup acknowledges.
 await page.evaluate(()=>{window.mode='normal';window.forceTimeout=true;window.deadline='pending';const flow=window.m04.beginMaintenance('refresh',['fake001']);flow.ready.then(()=>window.deadline='resolved',()=>window.deadline='rejected')})
 await page.waitForFunction(()=>window.held.some(x=>x.body.action==='account.maintenance'&&x.body.payload.operation==='cancel_pending'))
 assert.equal(await page.evaluate(()=>window.deadline),'pending')
 await page.evaluate(()=>{const i=window.held.findIndex(x=>x.body.action==='account.maintenance'&&x.body.payload.operation==='cancel_pending');const held=window.held.splice(i,1)[0];window.respond(held.body,held.data)})
 await page.waitForFunction(()=>window.deadline==='rejected')
 console.log('PASS M04 bridge action deadline waits for exact native cleanup acknowledgement')
 // Ordinary web retains raw import and does not require native headers.
 const web=await browser.newPage();await web.route('**/*',route=>route.request().url().startsWith(origin+'/')?route.continue():route.abort());await web.goto(origin+'/#/accounts');await web.getByRole('button',{name:'导入凭证',exact:true}).click();await web.locator('textarea').waitFor();assert.equal(await web.locator('textarea').count(),1);await web.close();console.log('PASS M04 ordinary web retains credential import form')
 }
 assert.deepEqual(errors,[])
 console.log('M04_ACCOUNTS_REACT_PASS')
} finally {if(browser)await browser.close();await new Promise(resolve=>server.close(resolve))}
