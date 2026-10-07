#!/usr/bin/env python3
"""Real Go + Native integration with EMPTY auth only; isolated /tmp and dynamic ports."""
import argparse, hashlib, json, os, socket, subprocess, tempfile
from pathlib import Path
p=argparse.ArgumentParser()
p.add_argument('--go-manifest',type=Path,required=True)
p.add_argument('--work',type=Path,required=True)
p.add_argument('--swift-cache',type=Path,required=True)
p.add_argument('--central-only',action='store_true')
a=p.parse_args();root=Path(__file__).resolve().parents[1]
work=a.work;work.mkdir(parents=True,exist_ok=True)
assert str(work.resolve()).startswith('/private/tmp/')
run=Path(tempfile.mkdtemp(prefix='run-',dir=work)).resolve();binary=work/'bin';binary.mkdir(exist_ok=True)
env={k:v for k,v in os.environ.items() if not k.startswith(('WBGUI_','WB2A_')) and 'proxy' not in k.lower()}
env.update(GOFLAGS='-buildvcs=false',GIT_OPTIONAL_LOCKS='0',GOPROXY='off')
manifest=json.loads(a.go_manifest.read_text())
def verify():
    for name,digest in {**manifest['files'],**manifest.get('frozen_dependencies',{})}.items():
        assert hashlib.sha256((root/name).read_bytes()).hexdigest()==digest,name

def command(args,cwd=root,extra=None):
    result=subprocess.run([str(v) for v in args],cwd=cwd,env={**env,**(extra or {})},text=True,capture_output=True,timeout=180)
    with (run/'verification.log').open('a') as log:log.write(result.stdout+result.stderr+f'EXIT={result.returncode}\n')
    print(result.stdout,end='',flush=True)
    if result.returncode:print(result.stderr);raise SystemExit(result.returncode)
verify()
for module,name in [('gateway','wb2api'),('panel','wbgui')]:
    command(['go','build','-o',binary/name,'./cmd/server'],cwd=root/module,extra={'GOPROXY':'off','GOTOOLCHAIN':'local'})
verify()
sources=sorted(p for p in (root/'macos/WorkBuddyDesktop').rglob('*.swift') if p.parent.name in ('Core','Services','Bridge') and not p.name.startswith('._'))
snapshots={str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sources}
command(['swiftc','-parse-as-library','-swift-version','6','-DDEBUG','-module-cache-path',a.swift_cache,*sources,root/'scripts/verify-m04-real.swift','-o',work/'real-check'])
assert snapshots=={str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sources},'Native dependency changed during compile'
with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
assert port!=7863
with socket.socket() as s:s.bind(('127.0.0.1',0));next_port=s.getsockname()[1]
assert next_port!=7863 and next_port!=port
command([work/'real-check'],extra={'WB_TEST_ROOT':str(run/'runtime'),'WB_HELPERS_DIR':str(binary.resolve()),'WB_TEST_PORT':str(port),'WB_TEST_NEXT_PORT':str(next_port),'WB_CENTRAL_ONLY':'1' if a.central_only else '0'})
verify()
assert not list((run/'runtime/data/auths').iterdir())
(run/'evidence.json').write_text(json.dumps({'go_manifest':str(a.go_manifest),'go_before_after':True,'swift_sources':snapshots,'auth_empty':True,'dynamic_port':port},indent=2))
print('LOG='+str(run/'verification.log'))
