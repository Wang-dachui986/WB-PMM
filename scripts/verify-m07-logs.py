#!/usr/bin/env python3
"""Compile production logging/process classes and run bounded synthetic-only checks.
All products and logs stay below /private/tmp; no service ports or real data are used.
"""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--work', type=Path)
parser.add_argument('--writer-only', action='store_true', help='Only the real two-process writer regression')
args = parser.parse_args()
work = (args.work or Path(tempfile.mkdtemp(prefix='workbuddy-m07-', dir='/tmp'))).resolve()
assert str(work).startswith('/private/tmp/')
work.mkdir(parents=True, exist_ok=True)
swift = root / 'macos/WorkBuddyDesktop'
env = {key: value for key, value in os.environ.items() if not key.startswith(('WB_', 'WB2A_', 'WBGUI_')) and 'proxy' not in key.lower()}
data = Path(tempfile.mkdtemp(prefix='data-', dir=work))
env['WB_TEST_ROOT'] = str(data)
print('WORK=' + str(work), flush=True)
print('WB_TEST_ROOT=' + str(data), flush=True)
commands = [
    ['swiftc', '-typecheck', '-swift-version', '6', '-DDEBUG', '-module-cache-path', str(work / 'swift-cache'),
     *map(str, sorted(p for p in swift.rglob('*.swift') if not p.name.startswith('._')))],
    ['swiftc', '-parse-as-library', '-swift-version', '6', '-DDEBUG', '-module-cache-path', str(work / 'swift-cache'),
     *map(str, [swift / 'Core/AppPaths.swift', swift / 'Core/AppLog.swift', swift / 'Services/PanelHandshake.swift',
                swift / 'Services/ManagedProcess.swift', root / 'scripts/verify-m07-logs.swift']), '-o', str(work / 'verify-logs')],
    [str(work / 'verify-logs'), *(['writer-only'] if args.writer_only else [])],
]
with (data / 'self-check.log').open('w') as log:
    for command in commands:
        result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=60)
        log.write(result.stdout + result.stderr)
        log.flush()
        print(result.stdout + result.stderr, end='', flush=True)
        if result.returncode:
            raise SystemExit(result.returncode)
print('LOG=' + str(data / 'self-check.log'), flush=True)
