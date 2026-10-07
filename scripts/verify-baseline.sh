#!/usr/bin/env bash
# verify-baseline.sh — 在隔离环境复现 P0 端点矩阵（docs/p0-baseline.md §2）
#
# 安全约束：
#  - 使用非默认端口（网关 17863 / 面板 18787），不触碰生产端口 7863/8787。
#  - 只使用本脚本生成的合成凭证（uid=p0synthetic），不读取任何真实凭证。
#  - 退出时清理全部子进程与端口（trap EXIT）。
#
# 用法：OUT=<bin 目录> ./scripts/verify-baseline.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${OUT:-$HOME/workbuddy2api-build/bin}"
RUN="${RUN:-$HOME/workbuddy2api-p0}"
ISO="$RUN/iso"
GW_PORT="${GW_PORT:-17863}"
PN_PORT="${PN_PORT:-18787}"
GW="http://127.0.0.1:$GW_PORT"
PN="http://127.0.0.1:$PN_PORT"

PASS=0; FAIL=0
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s  (期望 %s，实际 %s)\n' "$1" "$2" "$3"; FAIL=$((FAIL+1)); }
note() { printf '  \033[33mNOTE\033[0m  %s\n' "$1"; }
check(){ [[ "$3" == "$2" ]] && ok "$1" || bad "$1" "$2" "$3"; }

for b in wbgui wb2api; do
  [[ -x "$OUT/$b" ]] || { echo "错误：缺少 $OUT/$b，请先运行 scripts/build.sh" >&2; exit 1; }
done

# ── 隔离环境 ─────────────────────────────────────────────────────────────────
rm -rf "$ISO"
mkdir -p "$ISO/gateway/auths" "$ISO/gateway/data" "$ISO/panel/backups"
KEY="$(/usr/bin/python3 -c 'import secrets;print(secrets.token_hex(32))')"

/usr/bin/python3 - "$ISO" "$KEY" "$GW_PORT" "$PN_PORT" <<'PY'
import json, sys
iso, key, gwp, pnp = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
gw = {
  "listen": f"127.0.0.1:{gwp}", "api_key": key,
  "auth_dir": f"{iso}/gateway/auths", "state_file": f"{iso}/gateway/data/state.json",
  "cooldown": {"soft_rate": "600s", "soft_rate_max": "2h"},
  "schedule": {f"{k}_enabled": False for k in
               ("checkin","travel","activity","keepalive","school","cat")},
  "admin": {"enabled": True},
  "global": {"enabled": True, "chat_base": "", "billing_base": ""},
  "upstream": {"timeout_seconds": 120, "client_name": "WorkBuddy"},
  "features": {"sanitize_blacklist_fingerprints": True},
  "prompt": {"mode": "passthrough", "file": ""},
  "upstash": {"url": "", "token": ""},
  "pool": {"max_in_flight": 3, "max_in_flight_global": 2},
  "session_sticky": {"enabled": True, "ttl": "30m", "gc_interval": "5m"},
}
open(f"{iso}/gateway/config.json","w").write(json.dumps(gw, ensure_ascii=False, indent=2)+"\n")
pn = {
  "listen": f"127.0.0.1:{pnp}", "gateway_url": f"http://127.0.0.1:{gwp}",
  "gateway_api_key": key, "auth_dir": f"{iso}/gateway/auths",
  "config_file": f"{iso}/gateway/config.json", "backup_dir": f"{iso}/panel/backups",
  "credentials_file": "", "docker_container": "", "dangerous_ops": False,
  "read_only": False, "refresh_config_on_login": False, "timeout_seconds": 120,
  "ui": {"username": "admin", "password": "workbuddy", "session_ttl": "12h"},
}
open(f"{iso}/panel/config.json","w").write(json.dumps(pn, ensure_ascii=False, indent=2)+"\n")
PY

# ── 启动与清理 ───────────────────────────────────────────────────────────────
GW_PID=""; PN_PID=""
cleanup() {
  [[ -n "$GW_PID" ]] && kill "$GW_PID" 2>/dev/null
  [[ -n "$PN_PID" ]] && kill "$PN_PID" 2>/dev/null
  sleep 1
  [[ -n "$GW_PID" ]] && kill -9 "$GW_PID" 2>/dev/null
  [[ -n "$PN_PID" ]] && kill -9 "$PN_PID" 2>/dev/null
  echo
  echo "-- 清理 --"
  for p in "$GW_PORT" "$PN_PORT"; do
    n=$(lsof -nP -iTCP:"$p" -sTCP:LISTEN 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')
    echo "  端口 $p 残留监听: $n（应为 0）"
  done
}
trap cleanup EXIT INT TERM

echo "== 启动隔离实例 =="
env -i PATH=/usr/bin:/bin HOME="$HOME" "$OUT/wb2api" -config "$ISO/gateway/config.json" \
  > "$RUN/logs-wb2api.txt" 2>&1 & GW_PID=$!
sleep 2
env -i PATH=/usr/bin:/bin HOME="$HOME" "$OUT/wbgui" -config "$ISO/panel/config.json" \
  > "$RUN/logs-wbgui.txt" 2>&1 & PN_PID=$!
sleep 2
echo "  网关 pid=$GW_PID  面板 pid=$PN_PID"

# 一次性探测函数：code <method> <url> [curl 额外参数...]
code() {
  local m="$1" u="$2"; shift 2
  curl -s --max-time 10 -o /tmp/vb-body.txt -w '%{http_code}' -X "$m" "$@" "$u"
}

# 带会话 Cookie 的面板探测（登录后使用；写请求需要会话，否则只会拿到 401）
JAR="$RUN/.cookies"; rm -f "$JAR"
pcode() {
  local m="$1" u="$2"; shift 2
  curl -s --max-time 10 -c "$JAR" -b "$JAR" -o /tmp/vb-body.txt -w '%{http_code}' -X "$m" "$@" "$u"
}

echo
echo "== 网关端点矩阵 =="
check "GET /  无管理首页"                    404 "$(code GET "$GW/")"
check "GET /healthz 无账号应 503"            503 "$(code GET "$GW/healthz")"
check "GET /v1/models 无 key 应 401"         401 "$(code GET "$GW/v1/models")"
check "GET /v1/models 带 key 应 200"         200 "$(code GET "$GW/v1/models" -H "Authorization: Bearer $KEY")"
check "GET /v1/stats 带 key 应 200"          200 "$(code GET "$GW/v1/stats" -H "Authorization: Bearer $KEY")"
check "GET /status 带 key 应 200"            200 "$(code GET "$GW/status" -H "Authorization: Bearer $KEY")"
check "POST /v1/stats/reset 应 200"          200 "$(code POST "$GW/v1/stats/reset" -H "Authorization: Bearer $KEY")"

# A03：推理 key 可到达管理端点（返回 404=鉴权通过；401 才是被正确拦截）
A03="$(code POST "$GW/admin/accounts/testuid/disable" -H "Authorization: Bearer $KEY")"
if [[ "$A03" == "401" ]]; then
  ok "A03 推理 key 被管理端点拒绝（已修复）"
else
  note "A03 未修复：推理 key 到达管理端点，返回 $A03（应为 401 才算隔离）"
fi
check "管理端点无 key 应 401"                401 "$(code POST "$GW/admin/accounts/testuid/disable")"

echo
echo "== 面板端点矩阵 =="
check "GET / 首页应 200"                     200 "$(code GET "$PN/")"
check "GET /api/session 未登录应 200"        200 "$(code GET "$PN/api/session")"
check "GET /api/accounts 未登录应 401"       401 "$(code GET "$PN/api/accounts")"
LOGIN="$(pcode POST "$PN/api/login" -H 'Content-Type: application/json' \
        -d '{"username":"admin","password":"workbuddy"}')"
if [[ "$LOGIN" == "200" ]]; then
  note "A02 未修复：默认口令 admin/workbuddy 可登录（桌面模式必须禁用）"
else
  ok "A02 默认口令已被拒绝"
fi
check "已登录 GET /api/accounts 应 200"      200 "$(pcode GET "$PN/api/accounts")"
# 同源校验：带跨源 Origin 应 403（需带会话 Cookie，否则先被 401 拦下）
CROSS="$(pcode POST "$PN/api/stats/reset" -H 'Content-Type: application/json' -H 'Origin: http://evil.example.com' -d '{}')"
check "跨源 Origin 写请求应 403"             403 "$CROSS"
NOORIG="$(pcode POST "$PN/api/stats/reset" -H 'Content-Type: application/json' -d '{}')"
if [[ "$NOORIG" == "200" ]]; then
  note "N6 同源缺口：不带 Origin 的写请求被放行（$NOORIG）"
else
  ok "N6 同源校验收紧：无 Origin 写请求被拒"
fi
check "POST /api/config/reset 应 403（dangerous_ops=false）" 403 \
      "$(pcode POST "$PN/api/config/reset" -H 'Content-Type: application/json' -d '{}')"
# N7：配置读取是否明文泄漏 api_key
pcode GET "$PN/api/config" >/dev/null
if /usr/bin/python3 -c "
import json,sys
d=json.load(open('/tmp/vb-body.txt')); c=d.get('config',d)
sys.exit(0 if isinstance(c.get('api_key'),str) and len(c['api_key'])>10 else 1)
" 2>/dev/null; then
  note "N7 未修复：GET /api/config 明文返回网关 api_key（A06 必须脱敏）"
else
  ok "N7 已修复：配置读取已脱敏"
fi

echo
echo "== 账号热加载（合成凭证，验证后删除）=="
FUT="$(/usr/bin/python3 -c 'import time;print(int(time.time())+86400*30)')"
/usr/bin/python3 - "$ISO/gateway/auths" "$FUT" <<'PY'
import json, sys
d, fut = sys.argv[1], int(sys.argv[2])
doc = {"auth": {"accessToken": "p0-fake-access-token-not-real",
                "refreshToken": "p0-fake-refresh-token-not-real",
                "expiresAt": fut, "domain": "copilot.tencent.com", "realm": "cn"},
       "account": {"uid": "p0synthetic", "enterpriseId": "0", "nickname": "P0 合成账号（非真实）"}}
open(f"{d}/workbuddy-p0synthetic.json", "w").write(json.dumps(doc, ensure_ascii=False, indent=2)+"\n")
PY
sleep 8   # 热加载周期 5s + 余量
TOTAL="$(curl -s --max-time 10 -H "Authorization: Bearer $KEY" "$GW/status" \
        | /usr/bin/python3 -c 'import json,sys;print(json.load(sys.stdin)["total"])' 2>/dev/null)"
check "热加载后池内账号数应为 1（无需重启）" 1 "$TOTAL"
check "healthz 应翻转为 200"                 200 "$(code GET "$GW/healthz")"
rm -f "$ISO/gateway/auths/workbuddy-p0synthetic.json"
sleep 8
TOTAL2="$(curl -s --max-time 10 -H "Authorization: Bearer $KEY" "$GW/status" \
         | /usr/bin/python3 -c 'import json,sys;print(json.load(sys.stdin)["total"])' 2>/dev/null)"
check "删除后池内账号数应回到 0"              0 "$TOTAL2"
check "healthz 应回到 503"                   503 "$(code GET "$GW/healthz")"

echo
echo "== 结果：PASS=$PASS  FAIL=$FAIL =="
echo "隔离环境：$ISO"
echo "服务日志：$RUN/logs-wb2api.txt  $RUN/logs-wbgui.txt"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
