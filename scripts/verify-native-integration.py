#!/usr/bin/env python3
"""Bounded central Native checks; synthetic helpers and private temp roots only.
Reuses the accepted M04 fake helper and exact existing Swift module cache path.
"""
import argparse, ast, hashlib, json, os, socket, subprocess, sys, tempfile
from pathlib import Path
p=argparse.ArgumentParser(); p.add_argument('--work',type=Path,required=True); p.add_argument('--swift-cache',type=Path,required=True)
p.add_argument('--finish-webkit',action='store_true'); p.add_argument('--finish-native',action='store_true'); p.add_argument('--scenario',action='append'); p.add_argument('--webkit-only',action='store_true'); a=p.parse_args()

if a.finish_webkit:a.webkit_only=True
root=Path(__file__).resolve().parents[1]; work=a.work;work.mkdir(parents=True,exist_ok=True)
assert str(work.resolve()).startswith('/private/tmp/')
run=Path(tempfile.mkdtemp(prefix='run-',dir=work)).resolve(); binary=work/'bin';binary.mkdir(exist_ok=True)
env={k:v for k,v in os.environ.items() if not k.startswith(('WBGUI_','WB2A_')) and 'proxy' not in k.lower()}
env.update(GOFLAGS='-buildvcs=false',GIT_OPTIONAL_LOCKS='0',GOPROXY='off')
def execute(args,extra=None,timeout=180):
 r=subprocess.run([str(x) for x in args],cwd=root,env={**env,**(extra or {})},text=True,capture_output=True,timeout=timeout)
 with (run/'verification.log').open('a') as f:f.write(r.stdout+r.stderr+f'EXIT={r.returncode}\n')
 print(r.stdout,end='',flush=True)
 if r.returncode:print(r.stderr);raise SystemExit(r.returncode)
source=ast.parse((root/'scripts/verify-m04-native.py').read_text())
fake=next(ast.literal_eval(n.value) for n in source.body if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id=='FAKE' for t in n.targets))
fake=fake.replace('import http.server, json, os, pathlib, signal, sys, threading, time','import http.server, json, os, pathlib, signal, sys, threading, time, hashlib, socket')
fake=fake.replace("event('panel_start', separated=True)","event('panel_start', separated=True, gateway_url=init['gateway_url'])")
fake=fake.replace("init['gateway_api_key']", "init.get('gateway_api_key','')")
fake=fake.replace("event('gateway_start')","event('gateway_start', port=port)")
extra=r'''
config_path=pathlib.Path(init['config_file']) if panel else pathlib.Path(sys.argv[sys.argv.index('-config')+1])
config_lock=threading.Lock()
def revision(raw):return hashlib.sha256(raw).hexdigest()
def view(raw):
 d=json.loads(raw);host,port=d['listen'].rsplit(':',1)
 return {'revision':revision(raw),'editable':{'port':int(port),'allow_lan':host=='0.0.0.0','upstream':{'timeout_seconds':120,'header_timeout_seconds':120,'idle_timeout_seconds':300},'pool':{'max_in_flight':d.get('pool',{}).get('max_in_flight',3)},'session_sticky':{'enabled':False,'ttl':'30m'},'desktop':{'proxy_url':''}},'safe_summary':{'listen_host':host,'schedules_disabled':True},'key_configured':bool(d['api_key'].strip())}
def replace(raw):
 temp=config_path.with_suffix('.candidate');temp.write_bytes(raw);temp.chmod(0o600);temp.replace(config_path)
'''
fake=fake.replace("raw=b\"<!doctype html>"+fake.split("raw=b\"<!doctype html>",1)[1].split('\n',1)[0], "raw=b\"<!doctype html><html><body><div id='root'></div><script src='/m04.js'></script></body></html>\"") if a.webkit_only else fake
fake=fake.replace('class Handler(http.server.BaseHTTPRequestHandler):',extra+'\nclass Handler(http.server.BaseHTTPRequestHandler):')
fake=fake.replace("        if panel and self.path=='/api/accounts':",r'''        if panel and self.path=='/api/config':
            assert self.headers.get('Cookie')=='wbgui_session=fake-cookie'
            event('config:get')
            if opt.get('config_get_fail'):self.reply({'code':'CONFIG_STORAGE'},500);return
            self.reply(view(config_path.read_bytes()));return
        if panel and self.path=='/api/models':self.reply({'data':[]});return
        if not panel and self.path=='/v1/stats':
            self.reply({'ok':True},200 if self.headers.get('Authorization')=='Bearer '+cfg['api_key'] else 401);return
        if panel and self.path=='/api/accounts':''',1)
config_handler=r'''        if panel and self.path=='/api/desktop/config':
            assert self.headers.get('X-WorkBuddy-Control-Key')==init['panel_control_key']
            assert self.headers.get('Origin')=='http://127.0.0.1:'+str(self.server.server_port)
            assert not self.headers.get('Cookie') and not self.headers.get('Authorization')
            assert body['instance_id']==init['instance_id']
            op=body['operation'];event('config:'+op)
            with config_lock:
                if op=='prepare':
                    raw=config_path.read_bytes()
                    if body['revision']!=revision(raw):self.reply({'code':'CONFIG_CONFLICT'},409);return
                    d=json.loads(raw)
                    if body.get('rotate_key'):d['api_key']='SYNTHETIC_ROTATED_'+os.urandom(32).hex()
                    else:
                        patch=body['patch']
                        for key,value in patch.items():
                            if key=='port':d['listen']=d['listen'].rsplit(':',1)[0]+':'+str(value)
                            elif key=='allow_lan':d['listen']=('0.0.0.0' if value else '127.0.0.1')+':'+d['listen'].rsplit(':',1)[1]
                            elif isinstance(value,dict):d.setdefault(key,{}).update(value)
                    candidate=json.dumps(d,sort_keys=True,separators=(',',':')).encode()
                    state['tx']={'id':os.urandom(16).hex(),'old':raw,'next':candidate,'committed':False}
                    time.sleep(opt.get('prepare_delay',0));tx=state['tx']
                    self.reply({'ok':True,'backup_id':tx['id'],'revision':revision(raw),'candidate_revision':revision(candidate)});return
                if op=='finish' and body['backup_id']==state.get('last_finished'):
                    if opt.get('drop_finish_retry'):
                        self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                    self.reply({'ok':True});return
                tx=state.get('tx')
                if not tx or tx['id']!=body['backup_id']:self.reply({'code':'CONFIG_TRANSACTION'},409);return
                if op=='commit':
                    time.sleep(opt.get('commit_delay',0))
                    replace(tx['next']);tx['committed']=True;event('config:written')
                    if opt.get('external_conflict'):
                        d=json.loads(tx['next']);d['external_write']=True;replace(json.dumps(d).encode())
                    if opt.get('drop_commit_reply'):
                        self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                    self.reply({'ok':True,'config':view(tx['next'])});return
                if op=='restore':
                    if opt.get('restore_fail'):self.reply({'code':'CONFIG_STORAGE'},500);return
                    if body['current_revision']!=revision(config_path.read_bytes()):self.reply({'code':'CONFIG_CONFLICT'},409);return
                    replace(tx['old']);state.pop('tx');event('config:restored');self.reply({'ok':True,'config':view(tx['old'])});return
                if op=='finish':
                    if opt.get('finish_panel_exit'):os._exit(3)
                    if opt.get('finish_fail'):self.reply({'code':'CONFIG_STORAGE'},500);return
                    state['last_finished']=tx['id'];state.pop('tx');event('config:finished')
                    time.sleep(opt.get('finish_delay',0))
                    if opt.get('drop_finish_reply'):
                        self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                    self.reply({'ok':True});return
'''
fake=fake.replace("        if panel and self.path=='/api/login':",config_handler+"        if panel and self.path=='/api/login':",1)
fake=fake.replace("            if op=='end':\n                if opt.get('cancel_fail')", "            if op=='end':\n                time.sleep(opt.get('drain_end_delay',0))\n                if opt.get('cancel_fail')",1)
for name in ('wbgui','wb2api'):
 f=binary/name;f.write_text('#!'+sys.executable+'\n'+fake);f.chmod(0o700)

if a.finish_native:
 import threading
 listener=socket.socket();listener.bind(('127.0.0.1',0));listener.listen();listener.settimeout(0.2)
 proxy_port=listener.getsockname()[1]; received=[];stopped=threading.Event()
 def accept_tcp():
  while not stopped.is_set():
   try:conn,_=listener.accept()
   except (TimeoutError,OSError):continue
   with conn:
    conn.settimeout(3)
    try:received.append(len(conn.recv(8192)))
    except TimeoutError:received.append(-1)
 thread=threading.Thread(target=accept_tcp);thread.start()
 harness=work/'FinishNative.swift'
 harness.write_text(r'''
import Foundation
import AppKit
import Darwin
@main struct FinishNative {
 @MainActor static func main() async throws {
  enum Failure:Error { case check(String) }
  var count=0
  func check(_ value:Bool,_ text:String) throws {guard value else {throw Failure.check(text)};count+=1;print("PASS FINISH "+text)}
  let env=ProcessInfo.processInfo.environment, root=URL(fileURLWithPath:ProcessInfo.processInfo.environment["WB_TEST_ROOT"]!)
  try check(AppSettings.validTestRoot(root.path),"DEBUG accepts only existing private test root")
  let target=root.appendingPathComponent("sentinel");try FileManager.default.createDirectory(at:target,withIntermediateDirectories:false,attributes:[.posixPermissions:0o700])
  let link=root.appendingPathComponent("symlink");try FileManager.default.createSymbolicLink(at:link,withDestinationURL:target)
  try check(!AppSettings.validTestRoot(link.path) && !AppSettings.validTestRoot(link.appendingPathComponent("nested").path),"DEBUG rejects root and ancestor symlinks before writes")
  try check(!FileManager.default.fileExists(atPath:target.appendingPathComponent("data").path),"isolated symlink sentinel untouched")
  let settings=AppSettings.forApplication();settings.hasCompletedFirstRun=true;settings.autoStartGateway=true;settings.theme="dark"
  let fresh=AppSettings.forApplication()
  try check(fresh.apiPort==Int(env["WB_TEST_PORT"]!) && !fresh.autoStartGateway && !fresh.hasCompletedFirstRun && fresh.theme=="system","DEBUG preferences remain memory-only and disable auto start")
  try check(PortInspector.probe(port:0) == .unavailable && PortInspector.probe(port:65536) == .unavailable,"invalid port is unavailable, never free")
  let services=ServiceManager(settings:fresh,maintenanceConfirmation:{_ in .abort})
  let owner=NativeBridge.DocumentOwner()
  func request(_ action:String,_ payload:[String:Any]=[:],_ id:String=UUID().uuidString) async throws -> [String:Any] {
   try await services.performCheck(.init(id:id,action:action,payload:payload),owner:owner)
  }
  func proxy(_ port:Int) async throws {
   _ = try await services.performConfiguration(.apply(revision:GatewayConfigFile.readSnapshot().revision,patch:["desktop":["proxy_url":"http://127.0.0.1:\(port)"]],requestID:UUID().uuidString),owner:owner)
  }
  do {
   try check(services.prepareForLaunch(),"prepare isolated app with explicit dynamic port")
   let ready=try await request("desktop.checkReadiness")
   try check(ready["ready"] as? Bool==true && ready["port_result"] as? String=="free","readiness reports real available committed port")
   let leftovers=try FileManager.default.subpathsOfDirectory(atPath:root.path).filter{$0.contains(".readiness-")}
   try check(leftovers.isEmpty,"readiness write probes removed")
   let direct=try await request("desktop.checkProxy",["revision":GatewayConfigFile.readSnapshot().revision])
   try check(direct["result"] as? String=="not_applicable","direct mode explicitly not applicable")
   let raw=try Data(contentsOf:AppPaths.gatewayConfigFile)
   var document=try JSONSerialization.jsonObject(with:raw) as! [String:Any];document["pool"]=["max_in_flight":8]
   try JSONSerialization.data(withJSONObject:document).write(to:AppPaths.gatewayConfigFile);chmod(AppPaths.gatewayConfigFile.path,0o600)
   let changed=try await request("desktop.checkReadiness")
   try check(changed["ready"] as? Bool==false && changed["config_result"] as? String=="configuration_changed","external legal config cannot report ready against stale committed snapshot")
   try raw.write(to:AppPaths.gatewayConfigFile);chmod(AppPaths.gatewayConfigFile.path,0o600)
   try check(await services.startPanelService(),"synthetic panel starts")
   var confirmation:CheckedContinuation<ServiceManager.MaintenanceStopMode?,Never>?
   services.operationConfirmation={_,_ in await withCheckedContinuation{confirmation=$0}}
   let config=Task { @MainActor in _ = try await services.performConfiguration(.apply(revision:GatewayConfigFile.readSnapshot().revision,patch:["pool":["max_in_flight":4]],requestID:"readiness-busy"),owner:owner)}
   while confirmation == nil {await Task.yield()}
   let busy=try await request("desktop.checkReadiness")
   try check(busy["ready"] as? Bool==false && busy["config_result"] as? String=="busy","configuration confirmation blocks readiness")
   do {_ = try await request("desktop.checkProxy",["revision":GatewayConfigFile.readSnapshot().revision]);throw Failure.check("proxy accepted busy")}
   catch let error as NativeBridge.OperationFailure {try check(error.code=="busy","configuration lock rejects proxy check")}
   confirmation?.resume(returning:nil);_ = try? await config.value;services.operationConfirmation=nil
   try await proxy(Int(env["WB_PROXY_PORT"]!)!)
   let connected=try await request("desktop.checkProxy",["revision":GatewayConfigFile.readSnapshot().revision])
   try check(connected["result"] as? String=="reachable" && connected["tcp_only"] as? Bool==true,"plain loopback TCP reports only TCP reachability")
   try check(await services.startGateway(),"owned fake gateway starts")
   let own=try await request("desktop.checkReadiness")
   try check(own["port_result"] as? String=="owned" && own["ready"] as? Bool==true,"owned listening gateway is not external conflict")
   try await Task.sleep(for:.milliseconds(1100))
   try check((services.statusSnapshot()["uptime_seconds"] as? Int64 ?? -1)>=1,"uptime comes from current actual process start")
   await services.handleLifecycle(sleep:true);await services.handleLifecycle(sleep:false)
   try check(services.statusSnapshot()["lifecycle_epoch"] as? String=="2" && services.gatewayState == .runningUnavailable,"synthetic sleep/wake rechecks owned empty gateway without restart")
   try check(await services.stopGateway(),"ordinary stop uses existing controlled path")
   await services.handleLifecycle(sleep:false)
   try check(services.gatewayState == .stopped && services.statusSnapshot()["uptime_seconds"]==nil,"wake never starts stopped gateway and no stale uptime")
   try await proxy(Int(env["WB_CLOSED_PORT"]!)!)
   let revision=try GatewayConfigFile.readSnapshot().revision
   let first=Task { @MainActor in _ = try await request("desktop.checkProxy",["revision":revision],"proxy-A")}
   try await Task.sleep(for:.milliseconds(20))
   do {_ = try await request("desktop.checkProxy",["revision":revision]);throw Failure.check("overlap accepted")}
   catch let error as NativeBridge.OperationFailure {try check(error.code=="busy","proxy requests remain singleflight through DNS and connection")}
   _ = try await request("desktop.checkProxy",["operation":"cancel_pending","request_id":"proxy-A"])
   do {_ = try await first.value;throw Failure.check("cancel returned success")}
   catch let error as NativeBridge.OperationFailure {try check(error.code=="cancelled","cancel ACK waits actual connection cancellation")}
   let second=Task { @MainActor in _ = try await request("desktop.checkProxy",["revision":revision],"proxy-B")}
   try await Task.sleep(for:.milliseconds(20))
   _ = try await request("desktop.checkProxy",["operation":"cancel_pending","request_id":"proxy-A"])
   do {_ = try await request("desktop.checkProxy",["revision":revision]);throw Failure.check("old cancel cleared new")}
   catch let error as NativeBridge.OperationFailure {try check(error.code=="busy","old request cancel cannot release new proxy flight")}
   services.retireDocument(owner)
   do {_ = try await second.value;throw Failure.check("retired returned success")}
   catch let error as NativeBridge.OperationFailure {try check(error.code=="cancelled","document retirement cancels TCP check without JS assistance")}
   let current=NativeBridge.DocumentOwner()
   let timeout=Task { @MainActor in _ = try await services.performCheck(.init(id:"proxy-timeout",action:"desktop.checkProxy",payload:["revision":revision]),owner:current)}
   do {_ = try await timeout.value;throw Failure.check("unreachable returned success")}
   catch let error as NativeBridge.OperationFailure {try check(error.code=="expired","short deadline cancels pending TCP and DNS")}
   for payload:[String:Any] in [["revision":revision,"url":"http://127.0.0.1:1"],["operation":"cancel_pending","request_id":"x","revision":revision]] {
    do {_ = try await services.performCheck(.init(id:"invalid",action:"desktop.checkProxy",payload:payload),owner:current);throw Failure.check("invalid accepted")}
    catch let error as NativeBridge.OperationFailure {try check(error.code=="invalid_payload","proxy rejects target and mixed payload")}
   }
   await services.shutdown();try check(services.didFinishShutdown,"all owned helpers confirmed stopped")
   print("FINISH_NATIVE_CHECKS=\(count)")
  } catch {await services.shutdown();throw error}
 }
}
''')
 sources=sorted(p for p in (root/'macos/WorkBuddyDesktop').rglob('*.swift') if not p.name.startswith('._') and p.parent.name in ('Core','Services','Bridge'))
 try:
  execute(['swiftc','-parse-as-library','-swift-version','6','-DDEBUG','-module-cache-path',a.swift_cache,*sources,harness,'-o',work/'finish-native'])
  ports=[]
  for _ in range(2):
   with socket.socket() as sock:sock.bind(('127.0.0.1',0));ports.append(sock.getsockname()[1])
  assert 7863 not in [proxy_port,*ports]
  target=run/'finish';target.mkdir(mode=0o700)
  execute([work/'finish-native'],{'WB_TEST_ROOT':str(target),'WB_TEST_PORT':str(ports[0]),'WB_CLOSED_PORT':str(ports[1]),'WB_PROXY_PORT':str(proxy_port),'WB_HELPERS_DIR':str(binary.resolve())},timeout=45)
  assert received and all(value==0 for value in received),received
  print('PASS TCP receiver observed zero HTTP/CONNECT/application bytes')
  for row in map(json.loads,(target/'data/events.jsonl').read_text().splitlines()):
   if row['event'] in ('panel_start','gateway_start'):
    try:os.kill(row['pid'],0)
    except ProcessLookupError:pass
    else:raise AssertionError('owned helper remains')
 finally:stopped.set();listener.close();thread.join(timeout=5)
 print('LOG='+str(run/'verification.log'));sys.exit(0)

if a.webkit_only:
 web=root/'panel/web'
 entry=work/'central-browser.tsx'
 entry.write_text("""
import React from 'react';import {createRoot} from 'react-dom/client';import {BrowserRouter,Routes,Route,Link} from 'react-router-dom';
import ConfigPage from 'CONFIG';import ApiModelsPage from 'MODELS';export * from 'BRIDGE';
const root=createRoot(document.getElementById('root'));window.unmountRoot=()=>root.unmount();
const session={authenticated:true,username:'fake',read_only:false,dangerous_ops:false,using_default_password:false,gateway_url:''};
root.render(<BrowserRouter><Link to='/config'>配置</Link><Link to='/api-models'>接入</Link><Routes><Route path='*' element={<ConfigPage session={session}/>}/><Route path='/api-models' element={<ApiModelsPage/>}/></Routes></BrowserRouter>);
window.button=text=>Array.from(document.querySelectorAll('button')).find(b=>b.textContent.includes(text));
window.input=(id,value)=>{const el=document.getElementById(id);Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(el,value);el.dispatchEvent(new Event('input',{bubbles:true}))};
""".replace('CONFIG',str(web/'src/pages/ConfigPage.tsx')).replace('MODELS',str(web/'src/pages/ApiModelsPage.tsx')).replace('BRIDGE',str(web/'src/desktopBridge.ts')))
 bundle="const esbuild=require("+json.dumps(str(web/'node_modules/esbuild'))+");esbuild.buildSync({entryPoints:["+json.dumps(str(entry))+"],bundle:true,format:'iife',globalName:'m04',nodePaths:["+json.dumps(str(web/'node_modules'))+"],outfile:"+json.dumps(str(work/'m04-browser.js'))+"});"
 execute(['node','-e',bundle])
 sources=sorted(p for p in (root/'macos/WorkBuddyDesktop').rglob('*.swift') if not p.name.startswith('._') and p.parent.name in ('Core','Services','Bridge'))
 palette_source=(root/'macos/WorkBuddyDesktop/App/MainWindowView.swift').read_text()
 palette=work/'DesktopPalette.swift'
 palette.write_text('import SwiftUI\n'+palette_source[palette_source.index('enum DesktopPalette {'):palette_source.index('// MARK: - 原生服务条')])
 execute(['swiftc','-parse-as-library','-swift-version','6','-DDEBUG','-module-cache-path',a.swift_cache,*sources,palette,root/'macos/WorkBuddyDesktop/Web/PanelWebView.swift',root/'scripts/verify-m04-webkit.swift','-o',work/'central-webkit'])
 with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
 targets=[]
 for _ in range(5):
  with socket.socket() as sock:sock.bind(('127.0.0.1',0));targets.append(sock.getsockname()[1])
 assert 7863 not in [port,*targets]
 execute([work/'central-webkit'],{'WB_TEST_ROOT':str(run/'webkit'),'WB_HELPERS_DIR':str(binary.resolve()),'WB_TEST_PORT':str(port),'WB_CENTRAL_ONLY':'1','WB_FINISH_WEBKIT':'1' if a.finish_webkit else '0','WB_TEST_TARGET_PORTS':','.join(map(str,targets))})
 for line in (run/'webkit/data/events.jsonl').read_text().splitlines():
  event=json.loads(line)
  if event['event'] in ('panel_start','gateway_start'):
   try:os.kill(event['pid'],0)
   except ProcessLookupError:pass
   else:raise AssertionError('owned helper remains')
 print('LOG='+str(run/'verification.log'));sys.exit(0)
HARNESS=r'''
import AppKit
import Foundation
import Darwin
@MainActor final class GatewayStatusCapture { var snapshots:[[String:Any]]=[] }
@main struct CentralCheck {
 @MainActor static func main() async throws {
  let defaults=UserDefaults(suiteName:"native-central-isolated")!
  let ports=ProcessInfo.processInfo.environment["WB_TEST_PORTS"]!.split(separator:",").map{Int($0)!}
  defaults.setVolatileDomain(["apiPort":ports[0]],forName:UserDefaults.argumentDomain)
  var mode=ServiceManager.MaintenanceStopMode.wait
  let services=ServiceManager(settings:AppSettings(defaults:defaults),maintenanceConfirmation:{_ in mode})
  let owner=NativeBridge.DocumentOwner()
  enum Failure:Error {case check(String)}
  func check(_ value:Bool,_ label:String) throws {guard value else {throw Failure.check(label+" / "+(services.lastError ?? ""))};print("PASS CENTRAL \(label)")}
  func fixture(_ value:[String:Any]=[:]) throws {try JSONSerialization.data(withJSONObject:value).write(to:AppPaths.dataDirectory.appendingPathComponent("fixture.json"),options:.atomic)}
  func events(_ name:String)->[[String:Any]] {
   let raw=(try? String(contentsOf:AppPaths.dataDirectory.appendingPathComponent("events.jsonl"),encoding:.utf8)) ?? ""
   return raw.split(separator:"\n").compactMap{try? JSONSerialization.jsonObject(with:Data($0.utf8)) as? [String:Any]}.filter{$0["event"] as? String==name}
  }
  func wait(_ name:String,_ count:Int) async throws {
   for _ in 0..<500 {if events(name).count>=count{return};try await Task.sleep(for:.milliseconds(10))};throw Failure.check("timeout "+name)
  }
  func configure(_ port:Int,_ id:String="apply") async throws -> [String:Any] {
   try await services.performConfiguration(.apply(revision:GatewayConfigFile.readSnapshot().revision,patch:["port":port],requestID:id),owner:owner)
  }
  func expectFailure(_ id:String,port:Int) async throws {
   do {_ = try await configure(port,id);throw Failure.check("unexpected success")}
   catch is NativeBridge.OperationFailure {}
  }
  do {
   try check(services.prepareForLaunch(),"isolated native preparation")
   var d=try JSONSerialization.jsonObject(with:Data(contentsOf:AppPaths.gatewayConfigFile)) as! [String:Any]
   d["listen"]="127.0.0.1:\(ports[1])"
   let scenario=ProcessInfo.processInfo.environment["WB_SCENARIO"]!
   if scenario=="empty-key" {d["api_key"]=""}
   try JSONSerialization.data(withJSONObject:d).write(to:AppPaths.gatewayConfigFile)
   try fixture();try check(await services.startPanelService(),"panel starts from committed snapshot despite stale defaults")
   try check(services.apiPort==ports[1] && events("panel_start").last?["gateway_url"] as? String=="http://127.0.0.1:\(ports[1])","panel stdin/status use committed port")
   let instance=services.instanceID
   if scenario=="empty-key" {
    try check(!services.startGateway().isCancelledPlaceholder,"placeholder")
   }
   if scenario=="cancel-queued" {
    var confirmations=0
    services.operationConfirmation={_,_ in confirmations += 1;return .wait}
    let task=Task {do {_ = try await configure(ports[2],"queued-a");return true}catch{return false}}
    let retire=Task {services.retireDocument(owner)}
    await retire.value;_ = await task.value
    try check(confirmations==0 && events("config:prepare").isEmpty && !services.isBusy,"cancelled queued work never opens a late confirmation")
   } else if scenario=="finish-lost" || scenario=="finish-cancel" || scenario=="finish-retire" || scenario=="finish-panel-exit" || scenario=="finish-fail" {
    try check(await services.startGateway(),"gateway ready for finish fault")
    let before=events("gateway_start").count
    var options:[String:Any]=["drop_finish_reply":true]
    if scenario=="finish-cancel" || scenario=="finish-retire" {options["finish_delay"]=0.3}
    if scenario=="finish-panel-exit" {options=["finish_panel_exit":true]}
    if scenario=="finish-fail" {options=["finish_fail":true,"config_get_fail":true]}
    try fixture(options)
    let task=Task {do {_ = try await configure(ports[2],"finish-a");return true}catch{return false}}
    if scenario=="finish-cancel" {
     try await wait("config:finished",1)
     let ack=try await services.performConfiguration(.cancelPending(action:"gateway.applyConfig",requestID:"finish-a"),owner:owner)
     try check(ack["applied"] as? Bool==true && ack["cancelled"] as? Bool==false,"cancel during finished response truthfully reports already applied")
    } else if scenario=="finish-retire" {try await wait("config:finished",1);services.retireDocument(owner)}
    let success=await task.value
    if scenario=="finish-panel-exit" || scenario=="finish-fail" {
     try check(!success && services.isBusy,"unconfirmed finish retains protection")
     let last=events("gateway_start").last!["pid"] as! Int32
     try check(kill(last,0) != 0 && errno==ESRCH,"candidate exits before panel-dependent recovery even when panel or GET unavailable")
     try check(events("config:restore").isEmpty,"unconfirmed finish never assumes backup remains")
    } else {
     try check(success && !services.isBusy && services.gatewayState == .runningUnavailable,"exact finish retry settles applied configuration")
     try check(events("config:commit").count==1 && events("config:finish").count==2 && events("config:restore").isEmpty,"lost finish reply retries only finish without commit replay or rollback")
     try check(events("gateway_start").count==before+1 && services.apiPort==ports[2],"verified candidate remains running with no old restart")
     if owner.isActive {
      let late=try await services.performConfiguration(.cancelPending(action:"gateway.applyConfig",requestID:"finish-a"),owner:owner)
      try check(late["applied"] as? Bool==true && late["cancelled"] as? Bool==false,"late cancel after completed flow still reports applied without cleanup")
     }

    }
   } else if scenario=="lan" {
    let revision=try GatewayConfigFile.readSnapshot().revision
    func configResponse(_ allowLAN:Any?,_ host:String)->[String:Any] {
     var editable:[String:Any]=["port":ports[1],"upstream":["timeout_seconds":120,"header_timeout_seconds":120,"idle_timeout_seconds":300],
       "pool":["max_in_flight":3],"session_sticky":["enabled":false,"ttl":"30m"],"desktop":["proxy_url":""]]
     if let allowLAN {editable["allow_lan"]=allowLAN}
     return ["revision":revision,"editable":editable,"safe_summary":["listen_host":host,"schedules_disabled":true],"key_configured":true]
    }
    try check(try !PanelSessionClient.ConfigView(configResponse(nil,"127.0.0.1")).allowLAN,"old config response defaults to loopback")
    try check(try PanelSessionClient.ConfigView(configResponse(true,"0.0.0.0")).allowLAN,"LAN config response validates host and projects flag")
    for (value,host):(Any,String) in [(true,"127.0.0.1"),(false,"0.0.0.0"),(1,"0.0.0.0")] {
     do {_ = try PanelSessionClient.ConfigView(configResponse(value,host));throw Failure.check("inconsistent LAN response accepted")}
     catch is PanelSessionClient.ConfigFailure {}
    }
   let accepted=NativeBridge.Request(id:"lan-enable",action:"gateway.applyConfig",payload:["revision":revision,"patch":["allow_lan":true]])
   let numeric=NativeBridge.Request(id:"lan-numeric",action:"gateway.applyConfig",payload:["revision":revision,"patch":["allow_lan":1]])
   try check(NativeBridge.configurationRequest(accepted) != nil && NativeBridge.configurationRequest(numeric) == nil,"native patch accepts only boolean LAN flag")
   try await Task.sleep(for:.milliseconds(20)) // Drain initial panel-start status before observing this transaction.
   let capture=GatewayStatusCapture()
   let observer=NotificationCenter.default.addObserver(forName:ServiceManager.gatewayStatusNotification,object:nil,queue:.main){ note in
    let instance=note.userInfo?["instance_id"] as? String
    MainActor.assumeIsolated {
     guard instance==services.instanceID else{return}
     capture.snapshots.append(services.statusSnapshot())
    }
   }
   defer {NotificationCenter.default.removeObserver(observer)}
   let enabled=await NativeBridge.dispatch(accepted,statusProvider:{services.statusSnapshot()},performAction:{_ in nil},
      performConfiguration:{try await services.performConfiguration($0,owner:owner)})
   try check(enabled["ok"] as? Bool==true && (try GatewayConfigFile.readSnapshot()).allowLAN,"bridge transaction commits wildcard listen while stopped")
   let stopped=services.statusSnapshot()
   try check(stopped["lan_enabled"] as? Bool==true && (stopped["lan_api_base_urls"] as? [String])?.isEmpty==true &&
      stopped["api_base_url"] as? String=="http://127.0.0.1:\(ports[1])/v1","stopped LAN config exposes no active remote URL and keeps local URL")
   for _ in 0..<50 {
    if capture.snapshots.contains(where:{$0["lan_enabled"] as? Bool==true && $0["busy"] as? Bool==false}) {break}
    try await Task.sleep(for:.milliseconds(10))
   }
   try check(capture.snapshots.contains(where:{$0["gateway_state"] as? String=="stopped" && $0["lan_enabled"] as? Bool==true &&
     $0["busy"] as? Bool==false && $0["api_base_url"] as? String=="http://127.0.0.1:\(ports[1])/v1"}),
     "stopped LAN commit broadcasts settled status and new local base URL")
    try check(PortInspector.probe(port:ports[1],host:"0.0.0.0") == .free,"readiness can probe wildcard bind")
    try check(PortInspector.isPrivateIPv4(0x0a000001) && PortInspector.isPrivateIPv4(0xac100001) &&
      PortInspector.isPrivateIPv4(0xac1f00ff) && PortInspector.isPrivateIPv4(0xc0a80101) &&
      !PortInspector.isPrivateIPv4(0xac200001) && !PortInspector.isPrivateIPv4(0x7f000001),"LAN address filter is RFC1918 only")
    try check(await services.startGateway(),"synthetic gateway starts from LAN configuration")
    let expected=PortInspector.lanIPv4Addresses().map{"http://\($0):\(ports[1])/v1"}
    let status=services.statusSnapshot()
    try check(status["lan_enabled"] as? Bool==true && status["lan_api_base_urls"] as? [String]==expected,"running LAN status lists only current private interface URLs")
    let statusReply=await NativeBridge.dispatch(.init(id:"lan-status",action:"desktop.getStatus",payload:[:]),
      statusProvider:{services.statusSnapshot()},performAction:{_ in nil})
    let projected=(statusReply["data"] as? [String:Any])?["status"] as? [String:Any]
    try check(projected?["lan_enabled"] as? Bool==true && projected?["lan_api_base_urls"] as? [String]==expected,"bridge publishes LAN status without secret data")
    let disabled=try await services.performConfiguration(.apply(revision:GatewayConfigFile.readSnapshot().revision,
      patch:["allow_lan":false],requestID:"lan-disable"),owner:owner)
    try check(disabled["applied"] as? Bool==true && !(try GatewayConfigFile.readSnapshot()).allowLAN &&
      services.statusSnapshot()["lan_enabled"] as? Bool==false && (services.statusSnapshot()["lan_api_base_urls"] as? [String])?.isEmpty==true,
      "existing transaction restores loopback and clears LAN URLs")
   } else if scenario=="basic" {
    _ = try await configure(ports[2])
    try check(services.gatewayState == .stopped && services.apiPort==ports[2],"stopped configuration commit stays stopped and updates port")
    try check(await services.startGateway() && services.gatewayState == .runningUnavailable,"committed endpoint starts with empty pool 503")
    let key=try GatewayConfigFile.readSnapshot().apiKey
    _ = try await services.performConfiguration(.rotate(revision:GatewayConfigFile.readSnapshot().revision,requestID:"rotate"),owner:owner)
    try check(try GatewayConfigFile.readSnapshot().apiKey != key,"rotation changes only native key material")
    try check(services.gatewayState == .runningUnavailable && services.instanceID==instance,"rotation restores gateway without restarting panel")
    try check(events("drain:begin").count==1,"zero-flight rotation still begins atomic drain")
    try check(await services.stopGateway(),"ordinary wait stop completes")
    try check(events("drain:begin").count==2 && services.gatewayState == .stopped,"ordinary stop also begins admission drain")
   } else if scenario=="cancel-wait" {
    try check(await services.startGateway(),"gateway ready for cancellable ordinary wait")
    try fixture(["in_flight":true,"drain_end_delay":0.25])
    let before=events("gateway_start").count
    let stop=Task {do {_ = try await services.performGateway(.init(id:"stop-a",action:"gateway.stop",payload:[:]),owner:owner);return true}catch{return false}}
    try await wait("drain:begin",1)
    let cancel=Task {try await services.performGateway(.init(id:"cancel",action:"gateway.stop",payload:["operation":"cancel_pending","request_id":"stop-a"]),owner:owner).isEmpty}
    try await wait("drain:end",1);try check(services.isBusy,"cancel retains lock through drain end acknowledgement")
    _ = try await cancel.value;_ = await stop.value
    try check(!services.isBusy && services.gatewayState == .runningUnavailable && events("gateway_start").count==before,"cancel restores admission without stopping or restarting gateway")
    try fixture(["drain_unsupported":true])
    try check(!(await services.stopGateway()) && services.gatewayState == .runningUnavailable,"unsupported wait refuses silent abort")
    mode = .abort;try check(await services.stopGateway() && services.gatewayState == .stopped,"explicit abort stops only owned process")
   } else if scenario=="cleanup-finish-lost" || scenario=="cleanup-drain-finish-lost" || scenario=="cleanup-finish-unconfirmed" {
    let draining=scenario != "cleanup-finish-lost"
    if draining {try check(await services.startGateway(),"original gateway ready for cleanup finish cancellation")}
    let starts=events("gateway_start").count
    var options:[String:Any]=["drop_finish_reply":true]
    if draining {options["in_flight"]=true;options["drain_end_delay"]=0.25}
    else {options["prepare_delay"]=0.3}
    if scenario=="cleanup-finish-unconfirmed" {options["drop_finish_retry"]=true}
    try fixture(options)
    let task=Task {do {_ = try await configure(ports[2],"cleanup-a");return true}catch{return false}}
    try await wait(draining ? "drain:begin" : "config:prepare",1)
    var ack:[String:Any]=[:]
    let cancel=Task { () throws -> Bool in
     ack=try await services.performConfiguration(.cancelPending(action:"gateway.applyConfig",requestID:"cleanup-a"),owner:owner)
     return true
    }
    if draining && scenario != "cleanup-finish-unconfirmed" {
     try await wait("drain:end",1)
     try check(services.isBusy,"cleanup finish ACK does not release lock before drain end ACK")
    }
    let cancelled=(try? await cancel.value) ?? false;_ = await task.value
    try check(events("config:commit").isEmpty && events("config:restore").isEmpty && events("config:finish").count==2,"cancelled preparation retries only same finish after lost reply")
    if scenario=="cleanup-finish-unconfirmed" {
     try check(!cancelled && services.isBusy && events("drain:end").isEmpty,"unconfirmed cleanup finish retains lock and closed admission")
    } else {
     try check(cancelled && !services.isBusy && ack["cancelled"] as? Bool==true && ack["applied"] as? Bool==false,"cleanup finish ACK truthfully reports cancelled and not applied")
     try check(events("gateway_start").count==starts && (!draining || services.gatewayState == .runningUnavailable),"cleanup restores admission without stopping original gateway")
    }
   } else if scenario=="cancel-prepare" {
    try fixture(["prepare_delay":0.3])
    let old=try Data(contentsOf:AppPaths.gatewayConfigFile)
    let task=Task {do {_ = try await configure(ports[2],"prepare-a");return true}catch{return false}}
    try await wait("config:prepare",1);services.retireDocument(owner)
    _ = await task.value
    try check(events("config:commit").isEmpty && events("config:finish").count==1,"retired prepare owner finishes backup without late commit")
    try check(!services.isBusy && (try Data(contentsOf:AppPaths.gatewayConfigFile))==old,"prepare cancellation preserves exact old bytes and unlocks")
   } else if scenario=="rollback" || scenario=="external" || scenario=="restore-fail" || scenario=="unknown" {
    try check(await services.startGateway(),"old gateway ready before transaction failure")
    let old=try Data(contentsOf:AppPaths.gatewayConfigFile),before=events("gateway_start").count
    var options:[String:Any]=["drop_commit_reply":true]
    if scenario=="external" {options["external_conflict"]=true}
    if scenario=="restore-fail" {options["restore_fail"]=true}
    if scenario=="unknown" {options=["commit_delay":12]}
    try fixture(options);try await expectFailure("failed-commit",port:ports[2])
    if scenario=="rollback" {
     try check((try Data(contentsOf:AppPaths.gatewayConfigFile))==old && events("config:restore").count==1,"lost commit response restores exact old bytes via matching backup")
     try check(!services.isBusy && services.gatewayState == .runningUnavailable && events("gateway_start").count==before+1,"old gateway restores at most once after rollback")
    } else {
     try check(services.isBusy,"conflict or unsettled result retains protection lock")
     try check(events("gateway_start").count==before,"blocked recovery never starts old service")
     if scenario=="unknown" {try check(events("config:finish").isEmpty && events("config:restore").isEmpty,"old revision after timeout is not proof of uncommitted request")}
    }
   } else if scenario=="cancel-commit" {
    try check(await services.startGateway(),"old gateway ready before commit cancellation")
    let old=try Data(contentsOf:AppPaths.gatewayConfigFile)
    try fixture(["commit_delay":0.35])
    let task=Task {do {_ = try await configure(ports[2],"commit-a");return true}catch{return false}}
    try await wait("config:commit",1)
    let cancelled=Task {try await services.performConfiguration(.cancelPending(action:"gateway.applyConfig",requestID:"commit-a"),owner:owner).isEmpty}
    try await Task.sleep(for:.milliseconds(50));try check(services.isBusy,"commit cancellation waits for actual request result")
    _ = try await cancelled.value;_ = await task.value
    try check((try Data(contentsOf:AppPaths.gatewayConfigFile))==old && !services.isBusy,"settled cancelled commit restores old bytes before releasing lock")
   }
   await services.shutdown();try check(services.didFinishShutdown,"owned helpers exited")
   try check(try FileManager.default.contentsOfDirectory(atPath:AppPaths.authDirectory.path).isEmpty,"no real account or provider operation")
  } catch {await services.shutdown();throw error}
 }
}
'''
HARNESS=HARNESS.replace('try check(!services.startGateway().isCancelledPlaceholder,"placeholder")','''try check(!(await services.startGateway()),"empty-key snapshot remains readable but gateway start is refused")
    _ = try await services.performConfiguration(.rotate(revision:GatewayConfigFile.readSnapshot().revision,requestID:"repair-key"),owner:owner)
    try check(services.gatewayState == .stopped && !(try GatewayConfigFile.readSnapshot().apiKey).isEmpty,"controlled key rotation repairs empty-key config without starting gateway")''')
harness=work/'Central.swift';harness.write_text(HARNESS)
sources=sorted(p for p in (root/'macos/WorkBuddyDesktop').rglob('*.swift') if not p.name.startswith('._') and p.parent.name in ('Core','Services','Bridge'))
execute(['swiftc','-parse-as-library','-swift-version','6','-DDEBUG','-module-cache-path',a.swift_cache,*sources,harness,'-o',work/'central-check'])
for scenario in a.scenario or ['basic','empty-key','cancel-wait','cancel-prepare','rollback','cancel-commit','external','restore-fail','unknown','cancel-queued','finish-lost','finish-cancel','finish-retire','finish-panel-exit','finish-fail','cleanup-finish-lost','cleanup-drain-finish-lost','cleanup-finish-unconfirmed']:
 ports=[]
 for _ in range(3):
  with socket.socket() as sock:sock.bind(('127.0.0.1',0));ports.append(sock.getsockname()[1])
 assert 7863 not in ports
 print('SCENARIO='+scenario,flush=True)
 execute([work/'central-check'],{'WB_TEST_ROOT':str(run/scenario),'WB_HELPERS_DIR':str(binary.resolve()),'WB_TEST_PORTS':','.join(map(str,ports)),'WB_SCENARIO':scenario},timeout=90)
 for event in (run/scenario/'data/events.jsonl').read_text().splitlines():
  row=json.loads(event)
  if row['event'] in ('gateway_start','panel_start'):
   try:os.kill(row['pid'],0)
   except ProcessLookupError:pass
   else:raise AssertionError('owned helper remains')
print('LOG='+str(run/'verification.log'))
