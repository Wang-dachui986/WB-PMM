#!/usr/bin/env python3
"""M04 native permit/cleanup checks. Only /tmp fake helpers, fake data and dynamic ports.
No Go build, upstream traffic, real credentials, fixed user ports or git operations.
Usage: python3 scripts/verify-m04-native.py [--work /tmp/workbuddy-m04-previous]
"""
import argparse
import json
import os
from pathlib import Path
import socket
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--work', type=Path)
parser.add_argument('--swift-cache', type=Path)
parser.add_argument('--webkit-only', action='store_true')
parser.add_argument('--accounts-only', action='store_true')
args = parser.parse_args()
WORK = args.work or Path(tempfile.mkdtemp(prefix='workbuddy-m04-', dir='/tmp'))
assert str(WORK.resolve()).startswith(('/private/tmp/', '/tmp/'))
WORK.mkdir(parents=True, exist_ok=True)
CACHE = args.swift_cache or WORK / 'swift-cache'
print(f'WORK={WORK}', flush=True)
RUN=Path(tempfile.mkdtemp(prefix='run-',dir=WORK))
BIN = WORK / 'bin'
BIN.mkdir(exist_ok=True)
ENV = {k: v for k, v in os.environ.items() if not k.startswith(('WB2A_', 'WBGUI_')) and 'proxy' not in k.lower()}
LOG=RUN/'verification.log'

def run(command, env=ENV):
    result = subprocess.run([str(x) for x in command], cwd=ROOT, env=env, text=True, capture_output=True, timeout=180)
    with LOG.open('a') as log:
        log.write(result.stdout+result.stderr)
        log.write(f'EXIT={result.returncode}\n')
    print(result.stdout, end='', flush=True)
    if result.returncode:
        print(result.stderr, end='', flush=True)
        raise SystemExit(result.returncode)

FAKE = r'''
import http.server, json, os, pathlib, signal, sys, threading, time
from urllib.parse import urlparse
base = pathlib.Path.cwd()
init = json.loads(sys.stdin.readline())
panel = pathlib.Path(sys.argv[0]).name == 'wbgui'
options = lambda: json.loads((base/'fixture.json').read_text()) if (base/'fixture.json').exists() else {}
def event(name, **fields):
    with (base/'events.jsonl').open('a') as f:
        f.write(json.dumps(dict(event=name, pid=os.getpid(), time=time.monotonic(), **fields))+'\n')
if panel:
    assert len(init['panel_control_key']) == 64
    assert init['panel_control_key'] not in (init['password'], init['gateway_api_key'])
    event('panel_start', separated=True)
    port=0
else:
    cfg=json.loads(pathlib.Path(sys.argv[sys.argv.index('-config')+1]).read_text())
    port=int(cfg['listen'].rsplit(':',1)[1])
    event('gateway_start')
state={'generation':0, 'accepting':True}
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def reply(self, body, status=200, cookie=False):
        raw=json.dumps(body).encode()
        self.send_response(status)
        self.send_header('Content-Type','application/json')
        self.send_header('Content-Length',str(len(raw)))
        if cookie: self.send_header('Set-Cookie','wbgui_session=fake-cookie; Path=/; HttpOnly; SameSite=Strict')
        self.end_headers()
        try: self.wfile.write(raw)
        except (BrokenPipeError, ConnectionResetError): pass
    def do_GET(self):
        opt=options()
        if panel and self.path=='/api/accounts':
            assert self.headers.get('Cookie')=='wbgui_session=fake-cookie'
            self.reply({'accounts':[{'uid':'fake001','has_file':True,'domain':opt['existing_domain']}] if 'existing_domain' in opt else []}); return
        if panel and self.path.startswith('/api/desktop/login/'):
            assert self.headers.get('X-WorkBuddy-Control-Key')==init['panel_control_key']
            assert self.headers.get('X-WorkBuddy-Panel-Instance')==init['instance_id']
            assert self.headers.get('X-WorkBuddy-Maintenance-Generation')==str(state['generation'])
            assert self.headers.get('Origin')=='http://127.0.0.1:'+str(self.server.server_port)
            assert not self.headers.get('Cookie')
            time.sleep(opt.get('lookup_delay',0))
            event('lookup')
            self.reply({'session_id':self.path.rsplit('/',1)[1], 'auth_url':opt.get('auth_url','https://copilot.tencent.com/authorize?state=SYNTHETIC'), 'expires_at':'2099-01-01T00:00:00Z'}); return
        if not panel and self.path=='/healthz':
            self.reply({'service':'workbuddy2api','healthy':0,'total':0},503)
        elif panel:
            if self.path=='/m04.js':
                raw=(pathlib.Path(sys.argv[0]).resolve().parent.parent/'m04-browser.js').read_bytes()
                content='application/javascript'
            else:
                raw=b"<!doctype html><html><body>M04 isolated WebKit<script src='/m04.js'></script><script>window.received=[]; window.__workbuddyBridgeReceive=r=>window.received.push(r); window.send=(id,payload)=>window.webkit.messageHandlers.workbuddyNative.postMessage({id,action:'account.maintenance',payload,document_id:window.__workbuddyDocumentID});</script></body></html>"
                content='text/html'
            self.send_response(200); self.send_header('Content-Type',content)
            self.send_header('Content-Length',str(len(raw))); self.end_headers(); self.wfile.write(raw)
        else: self.reply({},404)
    def do_DELETE(self):
        assert panel and self.path=='/api/accounts/fake001?confirm=fake001'
        self.business('delete')
        event('delete')
        opt=options()
        self.reply({'ok':not opt.get('delete_fail',False)},500 if opt.get('delete_fail') else 200)
    def business(self,action):
        assert self.headers.get('Cookie')=='wbgui_session=fake-cookie'
        assert self.headers.get('X-WorkBuddy-Panel-Instance')==init['instance_id']
        assert self.headers.get('X-WorkBuddy-Maintenance-Generation')==str(state['generation'])
        assert self.headers.get('Origin')=='http://127.0.0.1:'+str(self.server.server_port)
        assert state['action']==action and state['active']
        assert not self.headers.get('X-WorkBuddy-Control-Key')
    def do_POST(self):
        body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))) or '{}')
        opt=options()
        if panel and self.path=='/api/login':
            valid=body=={'username':init['username'],'password':init['password']}
            self.reply({'username':'desktop'},200 if valid else 401,cookie=valid)
            return
        if panel and self.path=='/api/accounts/import':
            self.business('import')
            raw=json.loads(body['raw_json'])
            assert (raw.get('account') or raw)['uid']=='fake001'
            event('import',overwrite=body['overwrite'])
            self.reply({'ok':True}); return
        if not panel and self.path.startswith('/admin/accounts/fake001/'):
            assert self.headers.get('X-WorkBuddy-Management-Key')==init['management_key']
            enabled=self.path.endswith('/enable')
            event('manual_enable' if enabled else 'manual_disable')
            self.reply({'uid':'fake001','manual_disabled':not enabled,'disabled':True,'changed':True}); return
        if panel and self.path=='/api/desktop/maintenance':
            if self.headers.get('X-WorkBuddy-Control-Key') != init['panel_control_key']:
                self.reply({},403); return
            if self.headers.get('Cookie') or self.headers.get('Authorization'):
                self.reply({},403); return
            assert self.headers.get('Origin')=='http://127.0.0.1:'+str(self.server.server_port)
            assert body['instance_id']==init['instance_id']
            op=body['operation']; gen=body['generation']
            gateway_alive=False
            starts=[json.loads(row) for row in (base/'events.jsonl').read_text().splitlines() if json.loads(row)['event']=='gateway_start']
            if starts:
                try: os.kill(starts[-1]['pid'],0); gateway_alive=True
                except ProcessLookupError: pass
            event('permit:'+op,generation=gen,gateway_alive=gateway_alive)
            if op=='begin':
                assert gen>state['generation']
                state.update(generation=gen,action=body['action'],active=True)
                time.sleep(opt.get('begin_delay',0))
            else:
                time.sleep(opt.get('end_delay',0))
                if opt.get('end_fail'): self.reply({},503); return
                state['active']=False
                event('writer_done',generation=gen)
            reply={'ok':True,'drained':not opt.get('false_drain',False),'instance_id':init['instance_id'],'generation':gen}
            if opt.get('wrong_instance'): reply['instance_id']='different-instance'
            self.reply(reply)
            return
        if not panel and self.path=='/admin/desktop/drain':
            if self.headers.get('X-WorkBuddy-Management-Key')!=init['management_key']:
                self.reply({},403); return
            if opt.get('drain_unsupported'): self.reply({},404); return
            op=body['operation']; gen=body['generation']
            event('drain:'+op,generation=gen)
            if op=='status' and opt.get('status_unsupported'): self.reply({},404); return
            if op=='begin': state.update(generation=gen,accepting=False)
            assert gen==state['generation']
            if op=='end':
                if opt.get('cancel_fail'): self.reply({},500); return
                state['accepting']=True
            self.reply({'ok':True,'generation':gen,'drained':not opt.get('in_flight',False),'accepting':state['accepting']})
            return
        self.reply({},404)
server=http.server.ThreadingHTTPServer(('127.0.0.1',port),Handler)
if panel:
    print('@@WBGUI-READY@@ '+json.dumps({'service':'wbgui','protocol':1,'version':'fake-m04','port':server.server_port,'instance_id':init['instance_id'],'pid':os.getpid()}),flush=True)
def stop(*_):
    event('panel_exit' if panel else 'gateway_exit')
    os._exit(0)
signal.signal(signal.SIGTERM,stop)
threading.Thread(target=lambda: (sys.stdin.read(),stop()),daemon=True).start()
server.serve_forever()
'''
for name in ('wbgui', 'wb2api'):
    path=BIN/name
    path.write_text('#!'+sys.executable+'\n'+FAKE)
    path.chmod(0o700)

HARNESS = r'''
import Foundation
import Darwin

@main struct Check {
    @MainActor static func main() async throws {
        let defaults=UserDefaults(suiteName:"workbuddy-m04-test")!
        defaults.setVolatileDomain(["apiPort":Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!],forName:UserDefaults.argumentDomain)
        var mode=ServiceManager.MaintenanceStopMode.abort
        let services=ServiceManager(settings:AppSettings(defaults:defaults), maintenanceLifetime:2,
            maintenanceConfirmation:{ _ in mode })
        let owner=NativeBridge.DocumentOwner()
        func check(_ ok:Bool,_ label:String) {
            guard ok else { FileHandle.standardError.write(Data("FAIL M04 \(label)\n".utf8)); exit(1) }
            FileHandle.standardOutput.write(Data("PASS M04 \(label)\n".utf8))
        }
        func fixture(_ value:[String:Any]=[:]) throws {
            try JSONSerialization.data(withJSONObject:value).write(to:AppPaths.dataDirectory.appendingPathComponent("fixture.json"),options:.atomic)
        }
        func events() -> [[String:Any]] {
            let url=AppPaths.dataDirectory.appendingPathComponent("events.jsonl")
            return ((try? String(contentsOf:url,encoding:.utf8)) ?? "").split(separator:"\n").compactMap {
                try? JSONSerialization.jsonObject(with:Data($0.utf8)) as? [String:Any]
            }
        }
        func count(_ name:String) -> Int { events().filter { $0["event"] as? String == name }.count }
        func wait(_ label:String,_ predicate:() -> Bool) async throws {
            for _ in 0..<400 {
                if predicate() { return }
                try await Task.sleep(for:.milliseconds(20))
            }
            check(false,label)
        }
        func generation() -> UInt64 { UInt64(services.statusSnapshot()["maintenance_generation"] as? String ?? "0")! }
        func begin() async throws -> [String:Any] {
            try await services.performMaintenance(.begin(action:"refresh",uids:["fake001"]),owner:owner)
        }
        func end(_ data:[String:Any]) async throws {
            _ = try await services.performMaintenance(.end(instanceID:data["instance_id"] as! String,
                generation:UInt64(data["generation"] as! String)!),owner:owner)
        }
        check(services.prepareForLaunch(),"isolated startup")
        try fixture()
        let panelStarted=await services.startPanelService()
        if !panelStarted { print("PANEL_FAILURE",services.lastError ?? "unknown") }
        check(panelStarted,"private key handshake and panel login")
        let stopped=try await begin()
        check(services.gatewayState == .maintenance && services.isBusy,"stopped gateway enters maintenance")
        check(await services.performBridgeAction("gateway.start") == "busy","shared native lock prevents gateway start")
        do { _ = try await services.performMaintenance(.end(instanceID:"old",generation:1)); check(false,"stale instance") }
        catch { check(services.isBusy,"stale end cannot release current lock") }
        try await end(stopped)
        check(services.gatewayState == .stopped && !services.isBusy && count("gateway_start")==0,"originally stopped remains stopped")
        check(await services.startGateway(),"fake owned gateway started")
        let started=count("gateway_start")
        let permit=try await begin()
        check(count("permit:begin")==2 && events().filter { $0["event"] as? String == "permit:begin" }.allSatisfy { $0["gateway_alive"] as? Bool == false },"gateway exit precedes grant")
        try fixture(["end_delay":0.35])
        let finish=Task { try await end(permit) }
        try await Task.sleep(for:.milliseconds(100))
        check(count("gateway_start")==started && services.isBusy,"no restore before writer drain")
        try await finish.value
        check(count("gateway_start")==started+1 && services.gatewayState == .runningUnavailable,"restore only after successful drain")
        let rows=events(); let done=rows.lastIndex { $0["event"] as? String == "writer_done" }!
        let restart=rows.lastIndex { $0["event"] as? String == "gateway_start" }!
        check(done<restart,"observed write drain precedes replacement gateway")

        mode = .wait
        try fixture(["drain_unsupported":true])
        let beforeUnsupported=count("gateway_exit")
        do { _ = try await begin(); check(false,"missing drain support") }
        catch { check((error as? NativeBridge.MaintenanceError) == .not_supported,"unsupported drain is explicit") }
        check(!services.isBusy && count("gateway_exit")==beforeUnsupported,"unsupported wait never aborts gateway")
        try fixture(["in_flight":true,"status_unsupported":true])
        let cancelledDrains=count("drain:end")
        do { _ = try await begin(); check(false,"unsupported status") }
        catch { check(!services.isBusy && count("drain:end")==cancelledDrains+1,"unsupported status still revokes previously installed drain") }
        try fixture(["in_flight":true,"cancel_fail":true])
        let waiting=Task { do { _ = try await begin(); return false } catch { return true } }
        try await wait("wait admission closure") { count("drain:begin")>0 && generation()>0 }
        let pending:[String:Any] = ["instance_id":services.instanceID,"generation":String(generation())]
        do { try await end(pending); check(false,"cancel failure") }
        catch { check(services.isBusy && services.statusSnapshot()["maintenance_phase"] as? String == "blocked","failed admission reopen retains native lock") }
        try fixture(["in_flight":true])
        try await end(pending)
        check(await waiting.value && !services.isBusy && count("gateway_exit")==beforeUnsupported,"cancel reopens admission without stopping gateway")
        try fixture()
        let drainCount=count("drain:begin")
        let waited=try await begin()
        check(count("drain:begin")==drainCount+1,"wait closes admission even with zero in-flight")
        try await end(waited)

        mode = .abort
        try fixture(["begin_delay":0.4])
        let grants=count("permit:begin")
        let late=Task { do { _ = try await begin(); return false } catch { return true } }
        try await wait("grant pending") { count("permit:begin")>grants }
        await services.panelConnectionLost(instanceID:services.instanceID,ownerID:owner.id)
        check(await late.value && !services.isBusy && services.gatewayState == .runningUnavailable,"page loss waits for late grant then revokes and restores")
        try fixture()
        let active=try await begin()
        await services.panelConnectionLost(instanceID:services.instanceID,ownerID:UUID())
        check(services.isBusy,"old page owner cannot cancel new maintenance")
        try await end(active)
        let expiring=try await begin()
        try await wait("finite lease expires") { !services.isBusy }
        check(services.gatewayState == .runningUnavailable,"expiry cleans and restores")
        let newer=try await begin()
        do { try await end(expiring); check(false,"stale generation") }
        catch { check(services.isBusy,"old generation cannot cancel a new flow") }
        try await end(newer)

        try fixture(["false_drain":true])
        let failedDrain=try await begin()
        let oldID=services.instanceID
        let starts=count("gateway_start")
        try await end(failedDrain)
        check(services.panelPort == nil && count("panel_exit")>0,"false drain forces only owned panel to exit")
        check(count("gateway_start")==starts+1,"confirmed panel exit allows gateway restore")
        try fixture()
        check(await services.reconnectPanel() && services.instanceID != oldID,"fresh panel instance after forced exit")
        try fixture(["end_delay":12])
        let timeoutPermit=try await begin()
        let completions=count("writer_done")
        try await end(timeoutPermit)
        check(services.panelPort == nil && count("writer_done")==completions,"real request timeout stops panel before delayed writer completion")
        check(services.gatewayState == .runningUnavailable,"timeout restores only after owned panel exit")
        try fixture()
        check(await services.reconnectPanel(),"recover after timed out drain")
        let crashed=try await begin()
        let panelPID=events().last { $0["event"] as? String == "panel_start" }!["pid"] as! Int
        check(kill(Int32(panelPID),SIGKILL)==0,"kill only recorded fake child")
        try await wait("panel crash cleanup") { !services.isBusy && services.gatewayState == .runningUnavailable }
        check(await services.reconnectPanel(),"recover after panel crash")
        let postCrash=try await begin()
        do { try await end(crashed); check(false,"dead panel generation") }
        catch { check(services.isBusy,"dead panel end cannot affect replacement") }
        try await end(postCrash)
        try fixture(["wrong_instance":true])
        do { _ = try await begin(); check(false,"wrong echo") }
        catch { check(!services.isBusy && services.panelPort == nil,"wrong instance acknowledgment fails closed and exits panel") }
        try fixture()
        check(await services.reconnectPanel(),"reconnect for shutdown check")
        _ = try await begin()
        await services.shutdown()
        check(services.didFinishShutdown && services.panelPort == nil,"shutdown terminates maintenance without restoration")
        let second=ServiceManager(settings:AppSettings(defaults:defaults),maintenanceConfirmation:{ _ in .abort })
        check(second.prepareForLaunch(),"completed shutdown releases instance lock")
        check(await second.startPanelService() && second.panelPort != nil,"second native controller uses own panel")
        check(await second.startGateway(),"second owned gateway started")
        try fixture(["begin_delay":3])
        let pendingGrants=count("permit:begin")
        let stopping=Task { do { _ = try await second.performMaintenance(.begin(action:"refresh",uids:["fake001"])); return false } catch { return true } }
        try await wait("in-flight grant before shutdown") { count("permit:begin")>pendingGrants }
        let beforeShutdown=count("gateway_start"), shutdownAt=Date()
        await second.shutdown()
        check(await stopping.value && second.didFinishShutdown && Date().timeIntervalSince(shutdownAt)<10 && count("gateway_start")==beforeShutdown,"shutdown cancels in-flight grant within deadline and never restores")

        let invalid:[[String:Any]] = [
            ["operation":"begin","action":"refresh","uids":[]],
            ["operation":"begin","action":"refresh","uids":["fake001","fake001"]],
            ["operation":"begin","action":"refresh","uids":["../escape"]],
            ["operation":"begin","action":"refresh","uids":["fake001"],"url":"https://example.invalid"],
            ["operation":"begin","action":"reauthorize","uids":[]],
            ["operation":"begin","action":"import","uids":["fake001"]],
            ["operation":"end","instance_id":"x","generation":1],
            ["operation":"end","instance_id":"x","generation":"01"],
        ]
        check(invalid.allSatisfy { NativeBridge.maintenanceRequest($0)==nil },"strict actions UID counts keys and decimal generation")
        check(NativeBridge.maintenanceRequest(["operation":"begin","action":"login","uids":[]]) != nil,"new login supports unknown UID")
        check(NativeBridge.maintenanceRequest(["operation":"begin","action":"reauthorize","uids":["fake001"]]) != nil,"reauthorization requires selected UID")
        let status=await NativeBridge.dispatch(.init(id:"status",action:"desktop.getStatus",payload:[:]),
            statusProvider:{ ["panel_control_key":"sentinel","password":"sentinel","maintenance_generation":"2"] },performAction:{ _ in nil })
        check(!NativeBridge.encodeForJavaScript(status).contains("sentinel"),"bridge status does not disclose secrets")
        print("M04_NATIVE_PASS")
    }
}
'''
harness=WORK/'Maintenance.swift'
harness.write_text(HARNESS)
swift=ROOT/'macos/WorkBuddyDesktop'
all_swift=sorted(p for p in swift.rglob('*.swift') if not p.name.startswith('._'))
run(['swiftc','-typecheck','-swift-version','6','-DDEBUG','-module-cache-path',CACHE,*all_swift])
sources=[p for p in all_swift if p.parent.name in ('Core','Services','Bridge')]
with socket.socket() as s:
    s.bind(('127.0.0.1',0)); api_port=s.getsockname()[1]
if not args.webkit_only and not args.accounts_only:
    run(['swiftc','-parse-as-library','-swift-version','6','-DDEBUG','-module-cache-path',CACHE,*sources,harness,'-o',WORK/'maintenance'])
    run([WORK/'maintenance'],env={**ENV,'WB_TEST_ROOT':str((RUN/'native').resolve()),'WB_HELPERS_DIR':str(BIN),'WB_TEST_PORT':str(api_port)})
if not args.webkit_only:
    run(['swiftc','-parse-as-library','-swift-version','6','-DDEBUG','-module-cache-path',CACHE,*sources,ROOT/'scripts/verify-m04-accounts.swift','-o',WORK/'accounts'])
    run([WORK/'accounts'],env={**ENV,'WB_TEST_ROOT':str((RUN/'accounts').resolve()),'WB_HELPERS_DIR':str(BIN),'WB_TEST_PORT':str(api_port)})
if args.accounts_only:
    print(f'LOG={LOG}',flush=True)
    sys.exit(0)
web=ROOT/'panel/web'
node=shutil.which('node')
assert node
bundle="const esbuild=require("+json.dumps(str(web/'node_modules/esbuild'))+"); esbuild.buildSync({entryPoints:["+json.dumps(str(web/'src/desktopBridge.ts'))+"],bundle:true,format:'iife',globalName:'m04',outfile:"+json.dumps(str(WORK/'m04-browser.js'))+"});"
run([node,'-e',bundle])
run(['swiftc','-parse-as-library','-swift-version','6','-DDEBUG','-module-cache-path',CACHE,*sources,swift/'Web/PanelWebView.swift',ROOT/'scripts/verify-m04-webkit.swift','-o',WORK/'webkit'])
run([WORK/'webkit'],env={**ENV,'WB_TEST_ROOT':str((RUN/'webkit').resolve()),'WB_HELPERS_DIR':str(BIN),'WB_TEST_PORT':str(api_port)})
print(f'LOG={LOG}',flush=True)
