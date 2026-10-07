// Build only the owned pages, then drive real React in an ephemeral WebKit.
// All API/native responses are synthetic. No provider, credentials or real service.
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import http from 'node:http'
const root = path.resolve(import.meta.dirname, '..'), web = path.join(root, 'panel/web')
const out = fs.realpathSync(process.argv[2])
assert.ok(out.startsWith('/private/tmp/'))
const require = createRequire(path.join(web, 'package.json')), esbuild = require('esbuild')
const view = { revision: 'a'.repeat(64), editable: { port: 19081, upstream: { timeout_seconds: 120, header_timeout_seconds: 120, idle_timeout_seconds: 300 }, pool: { max_in_flight: 3 }, session_sticky: { enabled: true, ttl: '30m' }, desktop: { proxy_url: '' } }, safe_summary: { listen_host: '127.0.0.1', schedules_disabled: true }, key_configured: true }
esbuild.buildSync({entryPoints:[path.join(web,'src/pages/ConfigPage.tsx')],bundle:true,platform:'node',format:'cjs',outfile:path.join(out,'config.cjs')})
const {desktopConfigDraft,desktopConfigPatch} = createRequire(import.meta.url)(path.join(out,'config.cjs'))
const draft = desktopConfigDraft(view)
assert.deepEqual(desktopConfigPatch(draft,view),{})
assert.deepEqual(desktopConfigPatch({...draft,port:'19555'},view),{port:19555})
assert.deepEqual(desktopConfigPatch({...draft,checkin:true},view),{schedule:{checkin_enabled:true}})
assert.deepEqual(desktopConfigPatch({...draft,checkin:true,checkinHours:'8，20'},view),{schedule:{checkin_enabled:true,checkin_hours:[8,20]}})
for(const checkinHours of ['', '24', '-1', '9.5', '9abc', '9,9'])assert.throws(()=>desktopConfigPatch({...draft,checkinHours},view))
const enabledView={...view,editable:{...view.editable,schedule:{checkin_enabled:true,checkin_hours:[8,20]}}}
assert.deepEqual(desktopConfigPatch({...desktopConfigDraft(enabledView),checkin:false},enabledView),{schedule:{checkin_enabled:false}})
assert.deepEqual(desktopConfigPatch({...draft,proxy:'http://[::1]:8080',ttl:'1h30m'},view),{session_sticky:{ttl:'1h30m'},desktop:{proxy_url:'http://[::1]:8080'}})
for(const [field,value] of [['port',''],['port','NaN'],['port','1023'],['port','65536'],['timeout','0'],['idle','86401'],['max','-1'],['max','9007199254740992'],['ttl','0s'],['ttl','721h'],['proxy','https://proxy:443'],['proxy','http://u:p@proxy:80'],['proxy','http://proxy:80/'],['proxy','http://proxy:80?key=x']])assert.throws(()=>desktopConfigPatch({...draft,[field]:value},view))
console.log('PASS bounded patch validation and changed-fields-only projection')
const entry = `
import React from 'react'; import {createRoot} from 'react-dom/client'; import {BrowserRouter, Routes, Route, Link} from 'react-router-dom';
import ConfigPage, { desktopConfigDraft, desktopConfigPatch } from ${JSON.stringify(path.join(web,'src/pages/ConfigPage.tsx'))};
import ApiModelsPage from ${JSON.stringify(path.join(web,'src/pages/ApiModelsPage.tsx'))};
let view=${JSON.stringify(view)}; let pending; let confirmations=0; let allowLeave=false; let checkingLocal=false;
window.commands=[]; window.network=[]; window.copied=''; window.checks=[];
Object.defineProperty(window,'__workbuddyDocumentID',{value:'m06-fake-document'});
function answer(r,data){window.__workbuddyBridgeReceive({id:r.id,ok:true,data})}
Object.defineProperty(window,'webkit',{value:{messageHandlers:{workbuddyNative:{postMessage(r){window.commands.push(r);if(r.action==='gateway.applyConfig'||r.action==='api.rotateKey'){pending=r;return}if(r.action==='desktop.checkProxy'){queueMicrotask(()=>answer(r,{revision:view.revision,result:'reachable',tcp_only:true,checked_at:'2026-09-26T00:00:00Z'}));return}queueMicrotask(()=>answer(r,r.action==='api.copyKey'?{copied:true}:{protocol:1,status:{gateway_state:'running_unavailable',healthy_accounts:0,total_accounts:0}}))}}}},configurable:true});
Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.copied=text}},configurable:true});
// Stub covers React leave decisions only; real WK confirm UI remains a separate pending integration.
window.confirm=()=>{confirmations++;return allowLeave};
window.fetch=async url=>{window.network.push(String(url));if(checkingLocal)throw Error('Local check must not call HTTP/models/provider');if(url==='/api/config')return new Response(JSON.stringify(view));if(url==='/api/models')return new Response(JSON.stringify({count:2,data:[{id:'global:synthetic-model',object:'model',owned_by:'',created:0,context_length:0},{id:'local-test',object:'model',owned_by:'fake',created:0,context_length:4096}]}));throw Error('Unexpected network '+url)};
const wait=async predicate=>{for(let until=Date.now()+5000;Date.now()<until;){if(predicate())return;await new Promise(r=>setTimeout(r,15))}throw Error('timeout '+predicate)};
const check=(value,name)=>{if(!value)throw Error(name);window.checks.push(name)};
const button=text=>Array.from(document.querySelectorAll('button')).find(b=>b.textContent.includes(text));
const input=(id,value)=>{const el=document.getElementById(id);Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(el,value);el.dispatchEvent(new Event('input',{bubbles:true}))};
const root=createRoot(document.getElementById('root'));const session={authenticated:true,username:'fake',read_only:false,dangerous_ops:false,using_default_password:false,gateway_url:'http://127.0.0.1:19081'};
root.render(<BrowserRouter><Link to='/config'>配置</Link><Link to='/api-models'>接入</Link><Routes><Route path='/config' element={<ConfigPage session={session}/>}/><Route path='/api-models' element={<ApiModelsPage/>}/></Routes></BrowserRouter>);
window.runTests=async()=>{
 await wait(()=>document.getElementById('config-port'));
 check(JSON.stringify(desktopConfigPatch({...desktopConfigDraft(view),checkin:true,checkinHours:'8, 20'},view))==='{"schedule":{"checkin_enabled":true,"checkin_hours":[8,20]}}','WebKit validates spaced checkin hours');
 check(!document.getElementById('config-checkin').checked&&document.getElementById('config-checkinHours').value==='9, 21','legacy config keeps checkin disabled with default hours');
 document.getElementById('config-checkin').click();await wait(()=>!button('保存并应用').disabled);input('config-checkinHours','8, 20');await new Promise(r=>setTimeout(r,30));
 button('保存并应用').click();await wait(()=>pending);check(JSON.stringify(pending.payload.patch)==='{"schedule":{"checkin_enabled":true,"checkin_hours":[8,20]}}','checkin uses existing native config transaction');
 view={...view,editable:{...view.editable,schedule:{checkin_enabled:true,checkin_hours:[8,20]}},safe_summary:{...view.safe_summary,schedules_disabled:false}};answer(pending,{config:view});pending=null;
 await wait(()=>button('保存并应用').disabled&&!document.getElementById('config-checkin').disabled);check(document.body.textContent.includes('自动签到：已开启'),'saved checkin state shown');
 document.getElementById('config-checkin').click();await wait(()=>!button('保存并应用').disabled);button('保存并应用').click();await wait(()=>pending);
 check(JSON.stringify(pending.payload.patch)==='{"schedule":{"checkin_enabled":false}}','disable checkin does not overwrite hours or other tasks');
 view={...view,editable:{...view.editable,schedule:{checkin_enabled:false,checkin_hours:[8,20]}},safe_summary:{...view.safe_summary,schedules_disabled:true}};answer(pending,{config:view});pending=null;await wait(()=>button('保存并应用').disabled&&!document.getElementById('config-checkin').disabled);window.commands=[];
 input('config-port','19444');input('config-timeout','222');input('config-header','333');await wait(()=>!button('保存并应用').disabled);
 button('响应头恢复为当前请求超时').click();await wait(()=>document.getElementById('config-header').value==='222');check(window.commands.length===0,'header default copies current request timeout into draft only');
 button('恢复默认到草稿').click();await wait(()=>document.getElementById('config-timeout').value==='120');
 check(document.getElementById('config-port').value==='19444'&&document.getElementById('config-header').value==='120'&&document.getElementById('config-idle').value==='300'&&document.getElementById('config-max').value==='3'&&document.getElementById('config-sticky').checked&&document.getElementById('config-ttl').value==='30m'&&document.getElementById('config-proxy').value===''&&document.getElementById('config-checkin').checked&&document.getElementById('config-checkinHours').value==='9, 21'&&window.commands.length===0,'defaults preserve port and modify only known draft fields');
 button('放弃修改').click();await wait(()=>document.getElementById('config-port').value==='19081');
 button('检查已提交代理 TCP').click();await wait(()=>document.body.textContent.includes('TCP 端口可连接'));
 check(window.commands.at(-1).action==='desktop.checkProxy'&&JSON.stringify(window.commands.at(-1).payload)===JSON.stringify({revision:view.revision})&&document.body.textContent.includes('未验证 HTTP 转发或上游'),'proxy command targets committed revision and accurately labels TCP only');window.commands=[];
 input('config-port','invalid');await wait(()=>!button('保存并应用').disabled);button('保存并应用').click();await wait(()=>document.body.textContent.includes('端口须为'));
 check(document.getElementById('config-port').value==='invalid'&&window.commands.length===0,'invalid draft remains with no native write');
 button('放弃修改').click();await wait(()=>document.getElementById('config-port').value==='19081');check(true,'discard restores loaded value');
 input('config-port','19555');await wait(()=>!button('保存并应用').disabled);
 document.querySelector('a[href="/api-models"]').click();await new Promise(r=>setTimeout(r,30));check(confirmations===1&&!!document.getElementById('config-port'),'stubbed confirm cancellation preserves dirty form');
 button('保存并应用').click();await wait(()=>pending);check(pending.action==='gateway.applyConfig'&&pending.payload.revision==='a'.repeat(64)&&JSON.stringify(pending.payload.patch)==='{"port":19555}','save sends revision and changed whitelist patch only');
 window.__workbuddyBridgeReceive({id:pending.id,ok:false,error:{code:'CONFIG_CONFLICT',message:'conflict'}});pending=null;
 await wait(()=>document.body.textContent.includes('草稿不会被覆盖'));check(document.getElementById('config-port').value==='19555','conflict retains original draft');
 view={...view,revision:'b'.repeat(64),editable:{...view.editable,port:19099,upstream:{...view.editable.upstream,timeout_seconds:200}}};button('重新读取').click();await wait(()=>document.body.textContent.includes('b'.repeat(64)));
 check(document.getElementById('config-port').value==='19555','reload updates baseline revision without overwriting draft');
 button('保存并应用').click();await wait(()=>pending);check(pending.payload.revision==='b'.repeat(64)&&JSON.stringify(pending.payload.patch)==='{"port":19555}','retry uses latest revision without overwriting untouched external fields');
 view={...view,revision:'c'.repeat(64),editable:{...view.editable,port:19555}};answer(pending,{config:view});pending=null;await wait(()=>document.body.textContent.includes('配置已应用'));await new Promise(r=>setTimeout(r,40));
 check(document.getElementById('config-port').value==='19555','successful application establishes clean loaded draft');
 document.querySelector('a[href="/api-models"]').click();await wait(()=>document.getElementById('api-model'));
 check(document.body.textContent.includes('http://127.0.0.1:19555/v1/chat/completions'),'actual committed endpoint displayed');
 check(document.getElementById('api-model').value==='global:synthetic-model'&&document.body.textContent.includes('未提供'),'original model prefix and absent metadata preserved');
 check(window.network.every(url=>url==='/api/config'||url==='/api/models'),'page mount submits no model prompt');
 button('复制密钥').click();await wait(()=>window.commands.at(-1).action==='api.copyKey');await wait(()=>!button('复制密钥').disabled);check(window.copied==='','key copy uses native command without JS clipboard secret');
 button('复制完整模型 ID').click();await wait(()=>window.copied==='global:synthetic-model');check(window.copied==='global:synthetic-model','copy preserves complete selected model ID including global prefix');
 button('复制占位密钥示例').click();await wait(()=>window.copied.includes('YOUR_API_KEY'));check(window.copied.includes('global:synthetic-model'),'example uses selected ID and placeholder key');
 const beforeLocalNetwork=window.network.length,beforeLocalCommands=window.commands.length;checkingLocal=true;
 button('本地连通检查').click();await wait(()=>document.body.textContent.includes('此检查仅读取本机状态'));
 check(window.network.length===beforeLocalNetwork,'local check makes zero HTTP/models/provider requests');
 check(window.commands.length===beforeLocalCommands+1&&window.commands.at(-1).action==='desktop.getStatus','local check only requests native status');
 check(document.body.textContent.includes('当前网关状态：running_unavailable'),'local check reads actual protocol/status envelope');checkingLocal=false;
 button('轮换密钥').click();await wait(()=>pending);check(pending.action==='api.rotateKey'&&Object.keys(pending.payload).join()==='revision','rotation request contains revision and no key');
 view={...view,revision:'d'.repeat(64)};answer(pending,{config:view});pending=null;await wait(()=>!button('轮换密钥').disabled);
 check(!document.body.textContent.includes('SYNTHETIC_SECRET'),'no key material rendered');
 document.querySelector('a[href="/config"]').click();await wait(()=>document.getElementById('config-port'));input('config-port','19666');await wait(()=>!button('保存并应用').disabled);await new Promise(r=>setTimeout(r,40));
 window.history.back();await wait(()=>confirmations===2);await wait(()=>location.pathname==='/config');
 check(document.getElementById('config-port').value==='19666','stubbed confirm cancellation restores history and keeps draft');
 const event=new Event('beforeunload',{cancelable:true});window.dispatchEvent(event);check(event.defaultPrevented,'dirty form guards document reload and close');
 root.unmount();Object.defineProperty(window,'webkit',{value:undefined,configurable:true});
 window.fetch=async url=>{check(url==='/api/config','legacy web uses original config API');return new Response(JSON.stringify({config:{listen:':19888',api_key:'SYNTHETIC_WEB_ONLY',custom:{retained:true}},meta:{path:'synthetic-config.json',exists:true,size:123,is_valid_json:true,restart_note:''}}))};
 const legacy=createRoot(document.getElementById('root'));legacy.render(<ConfigPage session={session}/>);await wait(()=>document.querySelector('input[value=":19888"]'));
 check(!!document.querySelector('input[value="SYNTHETIC_WEB_ONLY"]')&&document.body.textContent.includes('JSON 源码'),'ordinary web retains original full config editor');legacy.unmount();window.done=true;
};window.runTests().catch(e=>{window.failure=JSON.stringify({error:String(e),checks:window.checks,inputs:Array.from(document.querySelectorAll('input')).map(el=>[el.id,el.value,el.checked]),body:document.body.innerText});window.done=true});
`
esbuild.buildSync({stdin:{contents:entry,resolveDir:web,loader:'tsx'},bundle:true,format:'iife',outfile:path.join(out,'pages.js')})
fs.writeFileSync(path.join(out,'pages.html'),'<!doctype html><html><head><meta charset="utf-8"></head><body><div id="root"></div><script>window.uncaught=[];addEventListener("error",e=>window.uncaught.push(e.message));addEventListener("unhandledrejection",e=>window.uncaught.push(String(e.reason)))</script><script src="/pages.js"></script></body></html>')
const swift = `import AppKit
import WebKit
@main struct PageChecks {
 @MainActor static func main() {
  let app = NSApplication.shared; app.setActivationPolicy(.prohibited)
  Task { @MainActor in
   let config = WKWebViewConfiguration(); config.websiteDataStore = .nonPersistent()
   let web = WKWebView(frame: NSRect(x:0,y:0,width:1000,height:800),configuration:config)
   let window = NSWindow(contentRect:web.frame,styleMask:[.titled],backing:.buffered,defer:false); window.contentView=web
   web.load(URLRequest(url:URL(string:CommandLine.arguments[1])!))
   do {
    for _ in 0..<1500 {
     if (try await web.evaluateJavaScript("window.done===true")) as? Bool == true {
      if let error = try await web.evaluateJavaScript("window.failure || null") as? String { print("FAIL \\(error)"); exit(1) }
      let checks = try await web.evaluateJavaScript("window.checks") as! [String]
      checks.forEach { print("PASS \\($0)") }; print("M06_PAGES_WEBKIT_PASS"); exit(0)
     }
     try await Task.sleep(for:.milliseconds(20))
    }
    print("FAIL timeout", try await web.evaluateJavaScript("JSON.stringify({errors:window.uncaught,body:document.body.innerText,checks:window.checks,href:location.href})") as Any); exit(1)
   } catch { print("FAIL \\(error)"); exit(1) }
  }; app.run()
 }
}
`
fs.writeFileSync(path.join(out,'Pages.swift'),swift)
const execute=promisify(execFile)
const swiftCache=process.argv[3]??path.join(out,'web-swift-cache')
assert.ok(path.resolve(swiftCache).startsWith('/private/tmp/')||path.resolve(swiftCache).startsWith('/tmp/'))
await execute('swiftc',['-parse-as-library','-swift-version','6','-module-cache-path',swiftCache,path.join(out,'Pages.swift'),'-o',path.join(out,'verify-pages')],{timeout:60000})
const server=http.createServer((req,res)=>{res.setHeader('Content-Type',req.url==='/pages.js'?'text/javascript; charset=utf-8':'text/html; charset=utf-8');res.end(fs.readFileSync(path.join(out,req.url==='/pages.js'?'pages.js':'pages.html')))})
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve))
try { const result=await execute(path.join(out,'verify-pages'),['http://127.0.0.1:'+server.address().port+'/config'],{timeout:45000});console.log(result.stdout);if(result.stderr)console.error(result.stderr) }
finally { server.close() }
