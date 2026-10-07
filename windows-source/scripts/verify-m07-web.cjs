const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const web = path.resolve(__dirname, '../panel/web')
const out = path.resolve(process.argv[2])
assert.ok(out.startsWith('/private/tmp/'))
const esbuild = require(path.join(web, 'node_modules/esbuild'))
esbuild.buildSync({entryPoints:[path.join(web,'src/pages/LogsPage.tsx')],bundle:true,platform:'node',format:'cjs',outfile:path.join(out,'logs.cjs')})
const commands = []
class Visibility extends EventTarget { hidden = false }
global.document = new Visibility()
global.window = {__workbuddyDocumentID:'m07-fake-document',webkit:{messageHandlers:{workbuddyNative:{postMessage(request){commands.push(request)}}}}}
const {pollSafeLogs,appendSafeLogs} = require(path.join(out,'logs.cjs'))
const delay = () => new Promise(resolve=>setTimeout(resolve,15))
const reply = (request, entries=[]) => window.__workbuddyBridgeReceive({id:request.id,ok:true,data:{run_id:'run',cursor:entries.at(-1)?.sequence??0,reset:false,entries}})
const entry = n => ({sequence:n,timestamp:0,source:'app',level:'info',event:'processStarted'})
;(async()=>{
  const pages=[]; let errors=0
  let stop=pollSafeLogs('app',{},page=>pages.push(page),()=>errors++,5)
  assert.equal(commands.length,1); await delay(); assert.equal(commands.length,1)
  console.log('PASS unresolved tail never overlaps another request')
  document.hidden=true; document.dispatchEvent(new Event('visibilitychange')); reply(commands[0],[entry(1)])
  await delay(); assert.equal(commands.length,1); assert.equal(pages.length,0)
  console.log('PASS hidden page neither appends late result nor polls')
  document.hidden=false; document.dispatchEvent(new Event('visibilitychange')); assert.equal(commands.length,2)
  reply(commands[1],Array.from({length:205},(_,i)=>entry(i+1))); await Promise.resolve()
  assert.equal(pages[0].entries.length,200)
  console.log('PASS visible page resumes with response limited to 200 entries')
  stop(); const count=commands.length; await delay(); assert.equal(commands.length,count)
  console.log('PASS disposed or paused subscription stops scheduling')
  stop=pollSafeLogs('panel',{},()=>assert.fail('disposed page appended'),()=>errors++,5)
  const old=commands.at(-1); stop()
  const other=[]
  const stopNew=pollSafeLogs('gateway',{},page=>other.push(page),()=>errors++,5)
  assert.equal(commands.at(-1).id,old.id); reply(old)
  await delay(); assert.equal(commands.at(-1).payload.source,'gateway')
  reply(commands.at(-1)); await Promise.resolve(); stopNew()
  assert.equal(other.length,1); assert.equal(errors,0)
  console.log('PASS remount waits for preceding pending tail and ignores disposed result')
  let rows=[]
  for(let page=0;page<11;page++)rows=appendSafeLogs(rows,{reset:false,entries:Array.from({length:200},(_,i)=>entry(page*200+i)),run_id:'run',cursor:page*200+199})
  assert.equal(rows.length,2000);assert.equal(rows[0].sequence,200)
  assert.equal(appendSafeLogs(rows,{reset:false,entries:[entry(2199)],run_id:'run',cursor:2199}).length,2000)
  assert.deepEqual(appendSafeLogs(rows,{reset:true,entries:[entry(1)],run_id:'new',cursor:1}).map(e=>e.sequence),[1])
  console.log('PASS log cache cap, duplicate exclusion and run reset')
  const finishOnly=process.argv.includes('--finish-only')
  const browserEntry = `
    import React from 'react'; import {createRoot} from 'react-dom/client'; import {MemoryRouter} from 'react-router-dom';
    import App from ${JSON.stringify(path.join(web,'src/App.tsx'))};
    import LogsPage from ${JSON.stringify(path.join(web,'src/pages/LogsPage.tsx'))};
    const root=createRoot(document.getElementById('root'));
    window.requests=[]; window.network=[]; window.copied=''; window.saveRequest=null;
    let next=0; let readinessReady=false; let readinessFails=false; let gatewayState='stopped'; let prefs={theme:'system',autoStartGatewayOnLaunch:false,hasCompletedFirstRun:false,notes:{},about:{app_version:'test',app_build:'1',build_time:'unrecorded',gateway_commit:'trusted-gateway',panel_commit:'trusted-panel',architecture:'arm64',os_version:'test'}};
    Object.defineProperty(document,'hidden',{get:()=>false,configurable:true});
    Object.defineProperty(window,'__workbuddyDocumentID',{value:'m07-fake-document'});
    function answer(r,data){window.__workbuddyBridgeReceive({id:r.id,ok:true,data})}
    Object.defineProperty(window,'webkit',{value:{messageHandlers:{workbuddyNative:{postMessage(r){
      window.requests.push(r);
      queueMicrotask(()=>{
        if(r.action==='preferences.get') return answer(r,prefs);
        if(r.action==='preferences.update'){prefs={...prefs,...r.payload};if(r.payload.note){prefs.notes={...prefs.notes,[r.payload.note.uid]:r.payload.note.value};if(!r.payload.note.value)delete prefs.notes[r.payload.note.uid]}return answer(r,prefs)}
        if(r.action==='desktop.getStatus')return answer(r,{protocol:1,status:{gateway_state:gatewayState,panel_state:'ready',api_base_url:'http://127.0.0.1:15432/v1',healthy_accounts:0,total_accounts:0,uptime_seconds:7}});
        if(r.action==='desktop.getBridgeInfo')return answer(r,{protocol:1,commands:['desktop.getStatus','preferences.get']});
        if(r.action==='desktop.checkReadiness'){
          if(readinessFails)return window.__workbuddyBridgeReceive({id:r.id,ok:false,error:{code:'check_failed',message:'本机检查失败'}});
          return answer(r,{ready:readinessReady,port:15432,port_result:readinessReady?'free':'occupied',config_result:'ok',checked_at:'2026-09-26T00:00:00Z',checks:[{check:'wbgui',result:'ok'}]});
        }
        if(r.action==='gateway.start'){gatewayState='running_unavailable';return answer(r,{})}
        if(r.action==='desktop.openDirectory')return answer(r,{opened:true});
        if(r.action==='logs.clearArchives')return answer(r,{cleared:0,cancelled:true});
        if(r.action==='logs.tail')return answer(r,{run_id:'safe-run',cursor:++next,reset:false,entries:[{sequence:next,timestamp:0,source:r.payload.source,level:'info',event:'processStarted'}]});
        if(r.action==='diagnostics.export'){
          if(r.payload.operation==='preview')return answer(r,{preview_id:'fixed-preview',files:['workbuddy-diagnostics.json'],sections:['safe events'],bytes:256});
          if(r.payload.operation==='save'){window.saveRequest=r;return}
          if(r.payload.operation==='cancel'){answer(r,{cancelled:true});if(window.saveRequest){answer(window.saveRequest,{cancelled:true});window.saveRequest=null}return}
        }
        throw Error('unexpected '+r.action)
      })
    }}}},configurable:true});
    Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.copied=text}},configurable:true});
    window.fetch=async url=>{
      window.network.push(url);
      if(url==='/api/system')throw Error('desktop system endpoint forbidden');
      const data=url==='/api/tasks'?{tasks:[],running:[]}
        :url==='/api/config'?{revision:'a'.repeat(64),editable:{port:15432,upstream:{timeout_seconds:120,header_timeout_seconds:120,idle_timeout_seconds:300},pool:{max_in_flight:3},session_sticky:{enabled:true,ttl:'30m'},desktop:{proxy_url:''},schedule:{checkin_enabled:false,checkin_hours:[9,21]}},safe_summary:{listen_host:'127.0.0.1',schedules_disabled:false},key_configured:true}
        :url==='/api/models'?{data:[{id:'cn:synthetic-model',name:'合成模型',owned_by:'synthetic'}],count:1}
        :url==='/api/overview'?{gateway_url:'http://127.0.0.1:15432',gateway_ok:true,health:{healthy:0,total:0,service:'workbuddy2api'},total:0,healthy:0,cooling:0,disabled:0,in_flight_full:0,in_flight:0,sticky_sessions:0,redis_mode:'memory',file_count:0,expired:0,expiring:0,warnings:[],file_issues:[],credits:{remain:0,used:0,size:0,accounts:0,ok:0,failed:0},read_only:false,dangerous_ops:false,server_time:'2026-09-27T00:00:00Z'}
        :url==='/api/accounts'?{accounts:[],file_issues:[],gateway_ok:true,summary:{total:0,healthy:0,cooling:0,disabled:0,in_flight_full:0,sticky_sessions:0,redis_mode:'memory'}}
        :url==='/api/stats'?{scope:'current_run',stats:{enabled:true,total:{requests:0,usage_observed_requests:0,total_tokens:0}}}
        :{authenticated:true,username:'synthetic',gateway_url:'http://127.0.0.1:15432',read_only:false,dangerous_ops:false,using_default_password:false};
      return new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json'}})
    };
    const wait=async predicate=>{for(let n=0;n<100;n++){if(predicate())return;await new Promise(r=>setTimeout(r,20))}throw Error('timeout')};
    const check=(condition,name)=>{if(!condition)throw Error(name);(window.checks??=[]).push(name)};
    const button=text=>Array.from(document.querySelectorAll('button')).find(b=>b.textContent.includes(text)||b.getAttribute('aria-label')===text);
    window.runTests=async()=>{
      root.render(React.createElement(MemoryRouter,{initialEntries:['/settings']},React.createElement(App)));
      await wait(()=>button('开始设置'));check(!prefs.hasCompletedFirstRun,'welcome does not mark setup complete');
      button('开始设置').click();await wait(()=>button('检查本机准备'));button('检查本机准备').click();await wait(()=>document.body.textContent.includes('其他程序占用'));
      check(button('下一步').disabled&&!!button('打开网关设置修改端口'),'occupied port blocks readiness and offers existing settings');
      readinessReady=true;button('检查本机准备').click();await wait(()=>!button('下一步').disabled);
      readinessFails=true;button('检查本机准备').click();await wait(()=>document.body.textContent.includes('本机检查失败'));
      check(button('下一步').disabled&&!prefs.hasCompletedFirstRun,'failed recheck clears prior readiness success');
      readinessFails=false;button('检查本机准备').click();await wait(()=>!button('下一步').disabled);
      const checksBefore=window.requests.filter(r=>r.action==='desktop.checkReadiness').length;
      readinessReady=false;button('下一步').click();await wait(()=>document.body.textContent.includes('其他程序占用')&&button('下一步').disabled);
      check(window.requests.filter(r=>r.action==='desktop.checkReadiness').length===checksBefore+1&&!button('跳过账号，继续'),'fresh failed readiness blocks advance despite old success');
      readinessReady=true;button('检查本机准备').click();await wait(()=>!button('下一步').disabled);button('下一步').click();await wait(()=>button('跳过账号，继续'));
      check(!prefs.hasCompletedFirstRun,'readiness success keeps first-run incomplete');button('跳过账号，继续').click();await wait(()=>button('启动 API 服务'));
      button('启动 API 服务').click();await wait(()=>document.body.textContent.includes('进程正在运行，但当前没有可用账号'));
      check(gatewayState==='running_unavailable'&&!prefs.hasCompletedFirstRun,'empty account process is running but not model-ready');
      gatewayState='failed';button('继续接入说明').click();await wait(()=>document.body.textContent.includes('API 服务未运行'));
      check(button('继续接入说明').disabled&&!button('完成首次设置')&&!prefs.hasCompletedFirstRun,'stopped gateway blocks ordinary continue');
      button('启动 API 服务').click();await wait(()=>!button('继续接入说明').disabled);
      button('继续接入说明').click();await wait(()=>button('下一步'));button('下一步').click();await wait(()=>document.body.textContent.includes('复制 API 密钥'));button('下一步').click();await wait(()=>button('完成首次设置'));
      check(document.body.textContent.includes('聊天测试')&&document.body.textContent.includes('手动发送'),'handoff explicitly remains untested');
      gatewayState='stopped';const savesBefore=window.requests.filter(r=>r.action==='preferences.update').length;
      button('完成首次设置').click();await wait(()=>document.body.textContent.includes('API 服务未运行'));
      check(!!button('完成首次设置')&&!prefs.hasCompletedFirstRun&&window.requests.filter(r=>r.action==='preferences.update').length===savesBefore,'latest stopped gateway blocks final completion persist');
      gatewayState='running_unavailable';button('完成首次设置').click();await wait(()=>prefs.hasCompletedFirstRun);check(true,'only final completion writes completed flag');
      check(window.network.every(url=>!String(url).includes('chat')),'wizard never sends a model prompt');
      prefs={...prefs,hasCompletedFirstRun:false};root.render(React.createElement(MemoryRouter,{key:'skip-run',initialEntries:['/settings']},React.createElement(App)));
      await wait(()=>button('跳过使用引导'));const starts=window.requests.filter(r=>r.action==='gateway.start').length;button('跳过使用引导').click();await wait(()=>!button('跳过使用引导'));
      check(prefs.hasCompletedFirstRun&&document.body.textContent.includes('未全部验证'),'explicit skip records completion but labels untested');
      check(window.requests.filter(r=>r.action==='gateway.start').length===starts,'skip does not start gateway');
      const systemLink=Array.from(document.querySelectorAll('a')).find(a=>a.getAttribute('href')==='/system');systemLink.click();await wait(()=>document.body.textContent.includes('本机内置服务状态'));
      check(!window.network.includes('/api/system')&&!document.body.textContent.includes('重启网关容器')&&!button('修改密码'),'desktop system excludes forbidden web controls and API');
      Array.from(document.querySelectorAll('a')).find(a=>a.getAttribute('href')==='/settings').click();await wait(()=>document.getElementById('preference-theme'));
      const select=document.getElementById('preference-theme');select.value='dark';select.dispatchEvent(new Event('change',{bubbles:true}));
      button('保存偏好').click();await wait(()=>prefs.theme==='dark');check(true,'settings saves selected theme');
      button('打开数据目录').click();button('打开日志目录').click();await wait(()=>window.requests.filter(r=>r.action==='desktop.openDirectory').length===2);
      check(window.requests.filter(r=>r.action==='desktop.openDirectory').map(r=>r.payload.kind).join(',')==='data,logs','directory buttons send only fixed kinds');
      if(${finishOnly}){root.unmount();window.done=true;return}
      root.render(React.createElement(LogsPage));await wait(()=>document.getElementById('safe-log-content')?.value.includes('processStarted'));
      const area=document.getElementById('safe-log-content');area.focus();area.setSelectionRange(0,10);area.dispatchEvent(new Event('select',{bubbles:true}));document.dispatchEvent(new Event('selectionchange'));
      await wait(()=>!button('复制选中内容').disabled);button('复制选中内容').click();await wait(()=>window.copied.length>0);check(window.copied===area.value.slice(0,10),'copy uses actual selected safe text');
      button('暂停读取').click();const before=window.requests.filter(r=>r.action==='logs.tail').length;await new Promise(r=>setTimeout(r,1100));check(window.requests.filter(r=>r.action==='logs.tail').length===before,'pause stops React page polling');
      const beforeClear=area.value;button('清理安全日志归档').click();await wait(()=>document.body.textContent.includes('已取消清理'));
      check(document.getElementById('safe-log-content').value===beforeClear,'cancelled archive cleanup preserves displayed log events');
      button('准备诊断文件').click();await wait(()=>button('选择保存位置'));check(document.body.textContent.includes('workbuddy-diagnostics.json'),'export lists file before opening picker');
      button('选择保存位置').click();await wait(()=>window.saveRequest);root.render(null);
      await wait(()=>window.requests.some(r=>r.action==='diagnostics.export'&&r.payload.operation==='cancel'&&r.payload.preview_id==='fixed-preview'));
      check(true,'SPA unmount cancels exact active diagnostic preview');window.done=true;
    };
    window.runTests().catch(error=>{window.failure=String(error);window.done=true});
  `
  esbuild.buildSync({stdin:{contents:browserEntry,resolveDir:web,loader:'tsx'},bundle:true,format:'iife',outfile:path.join(out,'browser.js')})
  fs.writeFileSync(path.join(out,'browser.html'),'<!doctype html><html><body><div id="root"></div><script src="browser.js"></script></body></html>')
  console.log('M07_WEB_POLLING_PASS')
})().catch(error=>{console.error(error);process.exitCode=1})
