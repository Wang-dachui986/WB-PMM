#!/usr/bin/env python3
"""Check a built gateway over local sockets, with empty accounts and no upstream calls.
Usage: python3 scripts/verify-lan-runtime.py --gateway /path/wb2api --lan-ip 192.168.1.23
The LAN IP must belong to this Mac. This does not replace testing another device.
"""
import argparse
import http.client
import ipaddress
import json
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--gateway", type=Path, required=True)
parser.add_argument("--lan-ip", required=True)
args = parser.parse_args()
ip = ipaddress.IPv4Address(args.lan_ip)
assert any(ip in ipaddress.ip_network(n) for n in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"))
gateway = args.gateway.resolve(strict=True)

with tempfile.TemporaryDirectory(prefix="wbpmm-lan-") as directory:
    root = Path(directory)
    (root / "auths").mkdir(mode=0o700)
    key, management = secrets.token_hex(32), secrets.token_hex(32)
    env = {"PATH": "/usr/bin:/bin", "HOME": directory, "TMPDIR": directory}

    for host in ("0.0.0.0", "127.0.0.1"):
        with socket.socket() as reservation:
            reservation.bind((host, 0))
            port = reservation.getsockname()[1]
        config = root / "config.json"
        config.write_text(json.dumps({
            "listen": f"{host}:{port}", "api_key": key,
            "auth_dir": str(root / "auths"), "state_file": str(root / "state.json"),
            "schedule": {f"{job}_enabled": False for job in ("checkin", "travel", "activity", "keepalive", "school", "cat")},
            "session_sticky": {"enabled": False}, "desktop": {"proxy_url": ""},
        }))
        config.chmod(0o600)
        proc = subprocess.Popen([str(gateway), "--desktop", "--config", str(config)],
                                cwd=root, env=env, stdin=subprocess.PIPE,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            proc.stdin.write((json.dumps({"management_key": management}) + "\n").encode())
            proc.stdin.flush()

            def request(address, path, headers=None, method="GET"):
                connection = http.client.HTTPConnection(address, port, timeout=2)
                try:
                    connection.request(method, path, headers=headers or {})
                    response = connection.getresponse()
                    body = response.read()
                    return response.status, body
                finally:
                    connection.close()

            deadline = time.monotonic() + 8
            while True:
                try:
                    status, body = request("127.0.0.1", "/healthz")
                    assert status == 503 and json.loads(body)["service"] == "workbuddy2api"
                    break
                except OSError:
                    assert proc.poll() is None and time.monotonic() < deadline, "gateway failed to start"
                    time.sleep(0.1)
            assert request("127.0.0.1", "/v1/models", {"Authorization": f"Bearer {key}"})[0] == 200
            if host == "0.0.0.0":
                assert request(args.lan_ip, "/v1/models")[0] == 401
                assert request(args.lan_ip, "/v1/models", {"Authorization": "Bearer wrong"})[0] == 401
                status, body = request(args.lan_ip, "/v1/models", {"Authorization": f"Bearer {key}"})
                assert status == 200 and not json.loads(body)["data"]
                headers = {"Authorization": f"Bearer {key}", "X-WorkBuddy-Management-Key": management, "X-Forwarded-For": "127.0.0.1"}
                for path in ("/healthz", "/status", "/v1/stats", "/admin/desktop/drain"):
                    assert request(args.lan_ip, path, headers)[0] == 403
                for path in ("/v1/chat/completions", "/v1/responses", "/v1/messages"):
                    assert request(args.lan_ip, path, method="POST")[0] == 401
                print("PASS LAN on: real socket access, API key validation, local-only management")
            else:
                try:
                    request(args.lan_ip, "/v1/models")
                except OSError:
                    print("PASS LAN off: LAN socket closed, loopback API still works")
                else:
                    raise AssertionError("loopback gateway unexpectedly reachable through LAN IP")
        finally:
            proc.stdin.close()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
