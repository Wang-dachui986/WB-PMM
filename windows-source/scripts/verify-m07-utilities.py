#!/usr/bin/env python3
"""Bounded fake utilities checks. Products/data under /tmp. With --preferences-once,
use one exclusive synthetic UserDefaults UUID domain and always remove it afterwards;
CFPreferences may place this explicit test artifact in the internal user's Library.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import uuid

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--work', type=Path, default=Path('/tmp/workbuddy-m07-r1'))
parser.add_argument('--preferences-once', action='store_true')
args = parser.parse_args()
work = args.work.resolve()
assert str(work).startswith('/private/tmp/')
run = Path(tempfile.mkdtemp(prefix='utilities-', dir=work))
env = {k: v for k, v in os.environ.items() if not k.startswith(('WB_', 'WB2A_', 'WBGUI_', 'CFFIXED_')) and 'proxy' not in k.lower()}
env['WB_TEST_ROOT'] = str(run / 'data')
log = run / 'verification.log'
commands = []
def invoke(command, extra=None):
    command = list(map(str, command))
    commands.append(command)
    result = subprocess.run(command, env={**env, **(extra or {})}, capture_output=True, text=True, timeout=60)
    with log.open('a') as stream:
        stream.write(result.stdout + result.stderr + f'EXIT={result.returncode}\n')
    print(result.stdout + result.stderr, end='', flush=True)
    if result.returncode:
        raise RuntimeError('isolated check failed')

swift = root / 'macos/WorkBuddyDesktop'
# Only the utility subset; other native feature owners can keep working independently.
files = [swift / 'Core' / name for name in ('AppPaths.swift', 'AppLog.swift', 'AppSettings.swift', 'DesktopUtilities.swift')]
files += [swift / 'Bridge/NativeBridge.swift', swift / 'Services/GatewayState.swift']
print('RUN=' + str(run), flush=True)
compile_command = ['swiftc', '-parse-as-library', '-swift-version', '6', '-DDEBUG', '-module-cache-path', work / 'swift-cache', *files]
invoke([*compile_command, root / 'scripts/verify-m07-utilities.swift', '-o', run / 'utilities'])
invoke([run / 'utilities'])
invoke(['node', root / 'scripts/verify-m07-web.cjs', run])
invoke([*compile_command, root / 'scripts/verify-m07-webkit.swift', '-o', run / 'webkit'])
invoke([run / 'webkit'], {'WB_TEST_ROOT': str(run)})
if args.preferences_once:
    # Authorized one-time cleanup of the earlier unsuccessful path-location experiment.
    invoke([run / 'utilities', 'cleanup-location-probe'])
    domain = 'workbuddy-m07-synthetic-' + str(uuid.uuid4())
    (run / 'synthetic-preferences-domain.txt').write_text(domain + '\n')
    extra = {'WB_TEST_PREFERENCES_DOMAIN': domain}
    try:
        invoke([run / 'utilities', 'preferences-write'], extra)
        invoke([run / 'utilities', 'preferences-read'], extra)
    finally:
        invoke([run / 'utilities', 'preferences-cleanup'], extra)
(run / 'commands.json').write_text(json.dumps(commands, indent=2) + '\n')
print('LOG=' + str(log), flush=True)
