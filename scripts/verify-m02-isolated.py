#!/usr/bin/env python3
"""Bounded M02 checks: temporary data, numeric loopback ports, own child PIDs only.
No real credentials, upstream calls, git operations, global process kills, or repo build output.
Usage: python3 scripts/verify-m02-isolated.py [--work /tmp/previous-m02-run]
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--work', type=Path)
args = parser.parse_args()
WORK = args.work or Path(tempfile.mkdtemp(prefix='workbuddy-m02-', dir='/tmp'))
WORK.mkdir(parents=True, exist_ok=True)
assert str(WORK.resolve()).startswith('/private/tmp/') or str(WORK.resolve()).startswith('/tmp/')
print(f'WORK={WORK}', flush=True)
ENV = {k: v for k, v in os.environ.items() if not k.startswith(('WB2A_', 'WBGUI_')) and 'proxy' not in k.lower()}
ENV.update({'GOCACHE': str(Path.home() / 'Library/Caches/go-build'), 'GOTMPDIR': str(WORK), 'CGO_ENABLED': '0'})
BIN = WORK / 'bin'
BIN.mkdir(exist_ok=True)
GO = shutil.which('go')
NODE = shutil.which('node')
assert GO and NODE


def run(command, cwd=ROOT, env=ENV):
    result = subprocess.run([str(x) for x in command], cwd=cwd, env=env, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(result.stdout + result.stderr)
    print(result.stdout.strip() or f'PASS {Path(str(command[0])).name}', flush=True)


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


# Build only the two entrypoints. Go cache and binaries stay on the internal disk.
for module, name in [('gateway', 'wb2api'), ('panel', 'wbgui')]:
    run([GO, 'test', './cmd/server'], ROOT / module)
    run([GO, 'build', '-o', BIN / name, './cmd/server'], ROOT / module)

swift = ROOT / 'macos/WorkBuddyDesktop'
all_swift = sorted(p for p in swift.rglob('*.swift') if not p.name.startswith('._'))
run(['swiftc', '-typecheck', '-swift-version', '6', '-DDEBUG', '-module-cache-path', WORK / 'swift-cache', *all_swift])

# Real ServiceManager / ManagedProcess / NativeBridge with the real Go helpers.
harness = WORK / 'Lifecycle.swift'
harness.write_text(r'''
import Foundation
import Darwin

@main struct Check {
    @MainActor static func main() async throws {
        let defaults = UserDefaults(suiteName: "workbuddy-m02-test")!
        defaults.setVolatileDomain(["apiPort": Int(ProcessInfo.processInfo.environment["WB_TEST_PORT"]!)!], forName: UserDefaults.argumentDomain)
        let settings = AppSettings(defaults: defaults)
        let services = ServiceManager(settings: settings)
        var childSnapshots: [String] = []
        func check(_ ok: Bool, _ name: String) {
            if !ok {
                let diagnostic = "FAIL \(name)\n" + childSnapshots.joined(separator: "\n") + "\n"
                FileHandle.standardError.write(Data(diagnostic.utf8))
            }
            precondition(ok, name)
            print("PASS \(name)")
        }
        func children(_ name: String) throws -> [Int32] {
            // libproc returns this exact parent's children and their executable paths.
            // ps.comm may temporarily be '(wb2api)' and /tmp is a /private/tmp alias.
            var pids = [pid_t](repeating: 0, count: 16)
            let count = proc_listchildpids(getpid(), &pids, Int32(pids.count * MemoryLayout<pid_t>.stride))
            check(count >= 0 && count < pids.count, "bounded child PID enumeration")
            let expected = URL(fileURLWithPath: ProcessInfo.processInfo.environment["WB_HELPERS_DIR"]!)
                .appendingPathComponent(name).resolvingSymlinksInPath().path
            var matches: [Int32] = []
            var inspected: [String] = []
            for pid in pids.prefix(Int(count)) where pid > 0 {
                var path = [CChar](repeating: 0, count: 4 * Int(MAXPATHLEN))
                let length = proc_pidpath(pid, &path, UInt32(path.count))
                let actual = length > 0
                    ? URL(fileURLWithPath: String(cString: path)).resolvingSymlinksInPath().path
                    : "<proc_pidpath failed errno=\(errno)>"
                inspected.append("pid=\(pid) executable=\(actual)")
                if actual == expected { matches.append(pid) }
            }
            childSnapshots.append("parent=\(getpid()) expected=\(expected) matches=\(matches) children=[\(inspected.joined(separator: "; "))]")
            if childSnapshots.count > 8 { childSnapshots.removeFirst() }
            return matches
        }
        func bridge(_ action: String, _ payload: [String: Any] = [:]) async -> [String: Any] {
            await NativeBridge.dispatch(.init(id: "test", action: action, payload: payload),
                statusProvider: { services.statusSnapshot() }, performAction: { await services.performBridgeAction($0) })
        }
        check(services.prepareForLaunch(), "isolated data and instance lock")
        check(!(ServiceManager(settings: settings).prepareForLaunch()), "duplicate application lock rejected")
        check(await services.startPanelService(), "real panel handshake and native session")
        let firstID = services.instanceID
        let firstPort = services.panelPort!
        let cookie = services.panelSessionCookie!.value
        check(services.isCurrentPanel(instanceID: firstID, port: firstPort), "live owned instance gate")
        check((await bridge("desktop.getStatus"))["ok"] as? Bool == true, "bridge status")
        check((await bridge("desktop.getBridgeInfo"))["ok"] as? Bool == true, "bridge command list")
        check((await bridge("gateway.start"))["ok"] as? Bool == true, "bridge starts real gateway")
        check(services.gatewayState == .runningUnavailable, "no accounts is running unavailable")
        check((await bridge("stats.reset"))["ok"] as? Bool == true, "fixed private management stats reset")
        check((await bridge("stats.reset", ["url": "http://example.invalid"]))["ok"] as? Bool == false, "stats reset rejects arbitrary targets")
        check((await bridge("gateway.start"))["ok"] as? Bool == false, "duplicate start rejected")
        let firstGatewayChildren = try children("wb2api")
        check(firstGatewayChildren.count == 1, "one gateway child")
        let gatewayPID = firstGatewayChildren[0]
        check((await bridge("gateway.restart"))["ok"] as? Bool == true, "bridge restart")
        let restartedChildren = try children("wb2api")
        check(restartedChildren.count == 1 && restartedChildren[0] != gatewayPID, "restart replaces only owned child")
        try await Task.sleep(for: .milliseconds(150))
        check(services.gatewayState == .runningUnavailable, "late old exit cannot overwrite restarted state")
        check((await bridge("desktop.reconnect"))["ok"] as? Bool == true, "bridge session reconnect")
        check(services.panelSessionCookie?.value != cookie, "session replaced without credentials in bridge")
        let panelChildren = try children("wbgui")
        check(panelChildren.count == 1, "one panel child before crash injection")
        let panelPID = panelChildren[0]
        kill(panelPID, SIGKILL) // PID came only from this test parent's exact helper child.
        for _ in 0..<100 where services.panelPort != nil { try await Task.sleep(for: .milliseconds(20)) }
        check(services.panelPort == nil && services.panelSessionCookie == nil, "panel crash revokes old port and cookie")
        check(await services.reconnectPanel(), "native retry relaunches crashed panel")
        check(services.instanceID != firstID && !services.isCurrentPanel(instanceID: firstID, port: firstPort), "old panel instance rejected")
        check(try children("wbgui").count == 1, "one recovered panel")
        check((await bridge("gateway.stop"))["ok"] as? Bool == true, "bridge stop")
        try await Task.sleep(for: .milliseconds(150))
        check(services.gatewayState == .stopped && services.panelPort != nil, "normal stop retains panel and does not fail")
        check((await bridge("stats.reset"))["ok"] as? Bool == false, "stopped gateway rejects stats reset")
        check((await bridge("gateway.start", ["pid": 1]))["ok"] as? Bool == false, "nonempty payload refused")
        check((await bridge("files.importAuth"))["ok"] as? Bool == false, "future command fail closed")
        for body: [String: Any] in [[:], ["id": "x", "action": "gateway.start"], ["id": "x", "action": "gateway.start", "payload": []], ["id": "x", "action": "gateway.start", "payload": [:], "extra": true]] {
            check(NativeBridge.parse(body) == nil, "strict bridge envelope")
        }
        for (main, scheme, host, requestPort) in [(false,"http","127.0.0.1",firstPort), (true,"https","127.0.0.1",firstPort), (true,"http","localhost",firstPort), (true,"http","127.0.0.1",firstPort+1)] {
            check(!NativeBridge.isTrustedOrigin(isMainFrame: main, scheme: scheme, host: host, port: requestPort, panelPort: firstPort), "untrusted frame/origin refused")
        }
        let cancellable = Task { await services.startGateway() }
        for _ in 0..<200 where services.gatewayState != .starting { await Task.yield() }
        check(services.gatewayState == .starting, "startup entered cancellable state")
        check(await services.performBridgeAction("gateway.stop") == nil, "user stop cancels startup through bridge")
        check(!(await cancellable.value) && services.gatewayState == .stopped && services.panelPort != nil,
              "cancelled startup cleans gateway and retains panel")
        check(try children("wb2api").isEmpty, "cancelled startup leaves no gateway child")
        let starting = Task { await services.startGateway() }
        await Task.yield()
        check(await services.performBridgeAction("gateway.restart") == "busy", "concurrent native operation is busy")
        await services.shutdown()
        _ = await starting.value
        try await Task.sleep(for: .milliseconds(150))
        check(services.didFinishShutdown && services.gatewayState == .stopped && services.panelPort == nil, "shutdown cancels in-flight startup")
        check(try children("wbgui").isEmpty && children("wb2api").isEmpty, "no owned child remains")
        check(await services.performBridgeAction("gateway.start") == "shutting_down", "shutdown rejects new bridge operations")
    }
}
''')
sources = [p for p in all_swift if p.parent.name in ('Core', 'Services', 'Bridge')]
run(['swiftc', '-parse-as-library', '-swift-version', '6', '-DDEBUG', '-module-cache-path', WORK / 'swift-cache', *sources, harness, '-o', WORK / 'lifecycle'])
run([WORK / 'lifecycle'], env={**ENV, 'WB_TEST_ROOT': str(WORK / 'native'), 'WB_HELPERS_DIR': str(BIN), 'WB_TEST_PORT': str(port())})

# EOF and conflict checks do not use native preferences or any existing data.
data = WORK / 'go-runtime'
data.mkdir(exist_ok=True)
auths = data / 'auths'
auths.mkdir(exist_ok=True)
config = data / 'config.json'
base = {'listen': f'127.0.0.1:{port()}', 'api_key': secrets.token_hex(32), 'auth_dir': str(auths), 'state_file': str(data / 'state.json'), 'schedule': {k+'_enabled': False for k in ['checkin', 'travel', 'activity', 'keepalive', 'school', 'cat']}}
config.write_text(json.dumps(base))
key = secrets.token_hex(32)
private_init = {'protocol': 1, 'instance_id': 'isolated-test', 'gateway_url': 'http://'+base['listen'], 'gateway_api_key': base['api_key'], 'auth_dir': str(auths), 'config_file': str(config), 'backup_dir': str(data/'backups'), 'username': 'desktop', 'password': secrets.token_hex(32), 'dangerous_ops': False}
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for name, command, payload in [('gateway', [BIN/'wb2api', '-desktop', '-config', config], {'management_key': key}), ('panel', [BIN/'wbgui', '--desktop'], private_init)]:
    child = subprocess.Popen([str(x) for x in command], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=ENV)
    try:
        child.stdin.write((json.dumps(payload)+'\n').encode()); child.stdin.flush()
        if name == 'panel':
            ready = child.stdout.readline().decode()
            assert ready.startswith('@@WBGUI-READY@@ '), ready
        else:
            for _ in range(100):
                try:
                    http.open('http://'+base['listen']+'/healthz', timeout=.2)
                    break
                except urllib.error.HTTPError as err:
                    assert err.code == 503
                    break
                except OSError:
                    time.sleep(.05)
            else:
                raise AssertionError('gateway failed readiness')
        child.stdin.close()
        assert child.wait(timeout=8) == 0
        print(f'PASS {name} parent pipe EOF exits', flush=True)
    finally:
        if child.poll() is None:
            child.kill(); child.wait()

for host in ['0.0.0.0', 'localhost', '[::1]', '']:
    config.write_text(json.dumps({**base, 'listen': f'{host}:{port()}'}))
    rejected = subprocess.run([BIN/'wb2api', '-desktop', '-config', config], input=json.dumps({'management_key': key})+'\n', env=ENV, capture_output=True, text=True, timeout=5)
    assert rejected.returncode != 0 and '127.0.0.1 loopback listener' in rejected.stderr
print('PASS desktop rejects noncanonical listeners', flush=True)
with socket.socket() as foreign:
    foreign.bind(('127.0.0.1', 0)); foreign.listen()
    config.write_text(json.dumps({**base, 'listen': f'127.0.0.1:{foreign.getsockname()[1]}'}))
    rejected = subprocess.Popen([BIN/'wb2api', '-desktop', '-config', config], stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=ENV)
    try:
        rejected.stdin.write((json.dumps({'management_key': key})+'\n').encode()); rejected.stdin.flush()
        assert rejected.wait(timeout=5) != 0
        assert foreign.getsockname()[1] > 0
        print('PASS conflicting listener retained', flush=True)
    finally:
        rejected.stdin.close()
        if rejected.poll() is None:
            rejected.kill(); rejected.wait()

# Compile without writing tsbuildinfo/dist into the repository.
web = ROOT/'panel/web'
run([NODE, web/'node_modules/typescript/bin/tsc', '--noEmit', '-p', web/'tsconfig.json'])
jsdir = WORK/'js'
run([NODE, web/'node_modules/typescript/bin/tsc', '--target', 'ES2022', '--module', 'commonjs', '--lib', 'ES2022,DOM', '--strict', '--skipLibCheck', '--outDir', jsdir, web/'src/api.ts', web/'src/desktopBridge.ts'])
js = WORK/'check.cjs'
js.write_text(r'''
const assert = require('node:assert/strict')
let bridgeCalls = 0, writes = 0, streams = 0, loggedOut = 0
let reply
Object.assign(global, { window: { __workbuddyDocumentID: 'm02-test-document', webkit: { messageHandlers: { workbuddyNative: { postMessage(req) {
  bridgeCalls++; reply = () => window.__workbuddyBridgeReceive({ id: req.id, ok: true, data: {} })
} } } } } })
const {api, streamChat, setUnauthorizedHandler} = require('./js/api.js')
setUnauthorizedHandler(() => loggedOut++)
global.fetch = async (url) => {
  if (url === '/api/chat/stream') streams++; else writes++
  return new Response(JSON.stringify({error:'expired'}), {status:401})
}
;(async () => {
  const one = api.saveConfig({}).catch(e => e.status)
  const two = api.chat({model:'mock',messages:[]}).catch(e => e.status)
  const three = new Promise(resolve => streamChat({model:'mock',messages:[]},()=>{},()=>{},resolve))
  await new Promise(resolve => setTimeout(resolve, 10))
  assert.equal(bridgeCalls, 1); reply()
  assert.equal(await one, 401); assert.equal(await two, 401); await three
  assert.equal(writes, 2); assert.equal(streams, 1); assert.equal(loggedOut, 0)
  console.log('PASS desktop 401 coalesces reconnect without replaying writes or chat')
  const reset = api.resetStats()
  assert.equal(bridgeCalls, 2); reply(); assert.equal((await reset).ok, true)
  assert.equal(writes, 2)
  console.log('PASS desktop stats reset makes one bridge call and no panel write')
  delete window.webkit
  await api.resetStats().catch(()=>{})
  assert.equal(loggedOut, 1)
  console.log('PASS ordinary web 401 retains login handling')
})().catch(e => { console.error(e); process.exitCode=1 })
''')
run([NODE, js])
# Bundle the production API and bridge for a real WKWebView process.
browser_bundle = WORK/'m02-browser.js'
builder = WORK/'browser-bundle.cjs'
builder.write_text("const esbuild=require("+json.dumps(str(web/'node_modules/esbuild'))+"); esbuild.buildSync({stdin:{contents:"+json.dumps("export * from "+json.dumps(str(web/'src/api.ts'))+"; export * from "+json.dumps(str(web/'src/desktopBridge.ts'))+";")+",resolveDir:"+json.dumps(str(web))+"},bundle:true,format:'iife',globalName:'m02',outfile:"+json.dumps(str(browser_bundle))+"});")
run([NODE, builder])
fixture = WORK/'webkit-bin'
fixture.mkdir(exist_ok=True)
fixture_code = r'''#!/usr/bin/python3
import http.server,json,os,pathlib,signal,sys,time
name=pathlib.Path(sys.argv[0]).name
init=json.loads(sys.stdin.readline())
root=pathlib.Path.cwd()
(root/(name+'.pid')).write_text(str(os.getpid()))
signal.signal(signal.SIGTERM,signal.SIG_IGN)
counts={'login':0,'writes':0,'streams':0}
token=''
class HTTP(http.server.ThreadingHTTPServer): daemon_threads=True
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def respond(self,status,obj,cookie=None):
  data=json.dumps(obj).encode(); self.send_response(status)
  self.send_header('Content-Type','application/json')
  if cookie: self.send_header('Set-Cookie',cookie)
  self.send_header('Content-Length',str(len(data))); self.end_headers(); self.wfile.write(data)
 def do_GET(self):
  if self.path=='/healthz': return self.respond(503,{'service':'workbuddy2api','healthy':0,'total':0})
  if self.path=='/api/session': return self.respond(200,{'authenticated':bool(token and ('wbgui_session='+token) in self.headers.get('Cookie',''))})
  if self.path=='/counts': return self.respond(200,counts)
  if self.path=='/m02.js':
   data=(pathlib.Path(sys.argv[0]).resolve().parent.parent/'m02-browser.js').read_bytes()
   content='application/javascript'
  else:
   script=''
   if self.path in ('/frame','/foreign'):
    script="window.webkit.messageHandlers.workbuddyNative.postMessage({id:'untrusted',action:'gateway.stop',payload:{}})"
   data=("<!doctype html><html><body><script src='/m02.js'></script><script>"+script+"</script></body></html>").encode(); content='text/html'
  self.send_response(200); self.send_header('Content-Type',content); self.send_header('Content-Length',str(len(data))); self.end_headers(); self.wfile.write(data)
 def do_POST(self):
  global token
  body=self.rfile.read(int(self.headers.get('Content-Length',0)))
  if self.path=='/api/login':
   counts['login']+=1
   credentials=json.loads(body)
   if counts['login']==1 or credentials.get('password')!=init['password']: return self.respond(401,{'error':'injected first-login failure'})
   time.sleep(.08); token='synthetic-session-'+str(counts['login'])
   return self.respond(200,{'username':'desktop'},'wbgui_session='+token+'; Path=/; HttpOnly; SameSite=Strict')
  if self.path=='/api/chat/stream': counts['streams']+=1
  return self.respond(401,{'error':'injected expired session'})
 def do_PUT(self):
  self.rfile.read(int(self.headers.get('Content-Length',0))); counts['writes']+=1
  self.respond(401,{'error':'injected expired session'})
if name=='wb2api':
 config=json.loads(pathlib.Path(sys.argv[sys.argv.index('-config')+1]).read_text()); bindport=int(config['listen'].split(':')[-1])
else: bindport=0
server=HTTP(('127.0.0.1',bindport),Handler)
if name=='wbgui': print('@@WBGUI-READY@@ '+json.dumps({'service':'wbgui','protocol':1,'version':'isolated-fixture','port':server.server_port,'instance_id':init['instance_id'],'pid':os.getpid()}),flush=True)
server.serve_forever()
'''
for name in ['wbgui','wb2api']:
    helper=fixture/name
    helper.write_text(fixture_code)
    helper.chmod(0o700)
run(['swiftc', '-parse-as-library', '-swift-version', '6', '-DDEBUG', '-module-cache-path', WORK/'swift-cache', *sources, swift/'Web/PanelWebView.swift', ROOT/'scripts/verify-m02-webkit.swift', '-o', WORK/'webkit'])
try:
    run([WORK/'webkit'], env={**ENV, 'WB_TEST_ROOT': str(WORK/'webkit-data'), 'WB_HELPERS_DIR': str(fixture), 'WB_TEST_PORT': str(port())})
finally:
    # Failure cleanup: only PIDs created by these exact synthetic helper paths.
    for name in ['wbgui','wb2api']:
        record=WORK/'webkit-data/data'/f'{name}.pid'
        if not record.exists(): continue
        pid=int(record.read_text())
        command=subprocess.run(['/bin/ps','-p',str(pid),'-o','command='],capture_output=True,text=True).stdout
        if str(fixture/name) in command:
            os.kill(pid, 9)
print('M02_ISOLATED_PASS (real Go lifecycle, production native bridge and real WKWebView)', flush=True)
