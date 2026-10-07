#!/usr/bin/env python3
"""Verify a built gateway with isolated synthetic history and no accounts/provider calls."""
import argparse, http.client, json, os, secrets, socket, subprocess, tempfile, time
from datetime import datetime, timezone, timedelta
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--gateway',required=True);args=p.parse_args()
with tempfile.TemporaryDirectory(prefix='wbpmm-token-check-') as d:
 root=Path(d);(root/'auths').mkdir(mode=0o700)
 now=datetime.now(timezone.utc).replace(hour=0,minute=0,second=0,microsecond=0)
 seed={'since':now.isoformat(),'rows':[{'hour':int(now.timestamp()),'model':'synthetic:test','requests':2,'observed':1,'input':120,'output':30}],'dropped':3}
 (root/'token-history.json').write_text(json.dumps(seed));(root/'token-history.json').chmod(0o600)
 key,management=secrets.token_hex(32),secrets.token_hex(32)
 for run in range(2):
  with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
  cfg={'listen':f'127.0.0.1:{port}','api_key':key,'auth_dir':str(root/'auths'),'state_file':str(root/'state.json'),'schedule':{f'{j}_enabled':False for j in ('checkin','travel','activity','keepalive','school','cat')},'session_sticky':{'enabled':False},'desktop':{'proxy_url':''}}
  (root/'config.json').write_text(json.dumps(cfg));(root/'config.json').chmod(0o600)
  proc=subprocess.Popen([args.gateway,'--desktop','--config',str(root/'config.json')],cwd=d,env={'PATH':'/usr/bin:/bin','HOME':d,'TMPDIR':d},stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  try:
   proc.stdin.write((json.dumps({'management_key':management})+'\n').encode());proc.stdin.flush()
   def req(path,auth=True,method='GET',admin=False):
    c=http.client.HTTPConnection('127.0.0.1',port,timeout=3)
    try:
     headers={'Authorization':'Bearer '+key} if auth else {}
     if admin:headers['X-WorkBuddy-Management-Key']=management
     c.request(method,path,headers=headers);r=c.getresponse();return r.status,r.read()
    finally:c.close()
   for _ in range(100):
    try:
     if req('/healthz')[0]==503:break
    except OSError:pass
    assert proc.poll() is None,'helper exited';time.sleep(.05)
   path='/v1/token-history?from='+now.strftime('%Y-%m-%dT%H:%M:%SZ')+'&to='+(now+timedelta(days=1)).strftime('%Y-%m-%dT%H:%M:%SZ')
   assert req(path,False)[0]==401
   status,body=req(path);data=json.loads(body)
   assert status==200 and data['rows']==seed['rows'] and '3' in data['warning'],(status,data)
   assert req('/v1/stats/reset',method='POST',admin=True)[0]==200
   assert json.loads(req(path)[1])['rows']==seed['rows']
  finally:
   proc.terminate()
   try:proc.wait(timeout=8)
   except subprocess.TimeoutExpired:proc.kill();proc.wait();raise
 print('PASS packaged gateway: authenticated history, missing usage, persisted warning, reset isolation, restart restore; zero provider calls')
