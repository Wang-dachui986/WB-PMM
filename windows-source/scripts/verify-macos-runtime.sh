#!/usr/bin/env bash
# ============================================================================
# 原生 App 运行期验证（WB-02 / T02–T06）
#
# 覆盖编译产物之外、只有真跑起来才看得见的行为：
#   T02 面板内置并可加载：wbgui 以 --desktop 启动，动态端口，仅回环监听，HTTP 200
#   T03 数据目录与配置：权限 0700/0600，listen 回环，排程全关，admin 关闭
#   T04 网关可用性：wb2api 用生成的配置启动，/healthz 可读，无 key 401
#   T05 单实例：第二个副本自行退出，不产生第二套服务
#   T06 无孤儿：强杀父进程后子进程随 stdin EOF 退出；flock 由内核释放
#
# 用法：
#   ./scripts/verify-macos-runtime.sh                 # 用默认构建产物
#   APP=<.app 路径> ./scripts/verify-macos-runtime.sh
#
# 退出码：0 全部通过；1 存在失败项
#
# 注意：本脚本会真实启动应用、写入 ~/Library 下的数据目录，
#       并在结束时停掉它启动的所有进程。不读取也不修改任何真实凭证。
# ============================================================================
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${CONFIG:-Debug}"
APP="${APP:-$HOME/workbuddy2api-build/macos-derived/Build/Products/$CONFIG/WB-PMM.app}"
DATA="$HOME/Library/Application Support/WorkBuddy2APIDesktop"
LOG="$HOME/Library/Logs/WorkBuddy2APIDesktop/desktop.log"

PASS=0
FAIL=0
APP_PID=""

ok()   { printf '\033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS + 1)); }
bad()  { printf '\033[31mFAIL\033[0m  %s\n' "$1"; FAIL=$((FAIL + 1)); }
head1() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }

stop_all() {
  [[ -n "$APP_PID" ]] && kill -TERM "$APP_PID" 2>/dev/null
  pkill -f 'WorkBuddyDesktop' 2>/dev/null
  sleep 1
  pkill -9 -f 'WorkBuddyDesktop' 2>/dev/null
  true
}
trap 'stop_all' EXIT

# 起一个干净的会话
stop_all
sleep 1

if [[ ! -x "$APP/Contents/MacOS/WorkBuddyDesktop" ]]; then
  echo "错误：找不到可执行文件 $APP/Contents/MacOS/WorkBuddyDesktop" >&2
  echo "提示：先运行 scripts/verify-macos-build.sh" >&2
  exit 1
fi

echo "被测产物：$APP"

launch() {
  "$APP/Contents/MacOS/WorkBuddyDesktop" >"$1" 2>&1 &
  APP_PID=$!
  # 脱离作业控制：本脚本会主动强杀这些进程，不脱离的话
  # bash 会在每次 kill 时打印 "Killed: 9" 之类的作业通知，污染证据输出。
  disown 2>/dev/null || true
}

panel_port() {
  lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk '/wbgui/ {print $9}' \
    | sed -E 's/.*:([0-9]+)$/\1/' | head -1
}

# ---------------------------------------------------------------------------
head1 "T02 面板内置与加载"
launch /tmp/wbd-runtime-1.out
sleep 8

HELPER_PID="$(pgrep -f 'Helpers/wbgui' | head -1)"
if [[ -n "$HELPER_PID" ]]; then
  ok "wbgui 已由 App 拉起（PID $HELPER_PID）"
else
  bad "wbgui 未启动"
  tail -10 "$LOG" 2>/dev/null
  exit 1
fi

# 必须是包内的 helper，而不是 PATH 上某个同名程序。
#
# 用 lsof 取可执行文件路径，**不用 `ps`**：本环境下 `ps` 会被沙箱拦截
# （`operation not permitted`），拿不到任何输出，于是这里会误报成「路径异常」。
# 校验脚本本身依赖一个被环境禁掉的工具，是脚本的缺陷，不是被测对象的缺陷。
HELPER_PATH="$(lsof -nP -p "$HELPER_PID" -a -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | head -1)"
if [[ "$HELPER_PATH" == "$APP/Contents/Helpers/wbgui" ]]; then
  ok "使用的是 Bundle 内的 helper"
else
  bad "helper 路径异常：$HELPER_PATH"
fi

PORT="$(panel_port)"
if [[ -n "$PORT" ]]; then
  ok "面板监听动态端口 $PORT"
else
  bad "面板未监听任何端口"
fi

# 桌面模式的证据取自**应用日志里的握手记录**：只有 --desktop 模式才会向 stdout 输出
# @@WBGUI-READY@@，原生层才会记下「面板已就绪：127.0.0.1:<port>」。
# 这比读 argv 更有说服力 ——「传了标志」不等于「握手成功」，而后者才是契约。
if [[ -n "$PORT" ]] && grep -q "面板已就绪：127.0.0.1:$PORT" "$LOG" 2>/dev/null; then
  ok "桌面模式握手完成（日志记录：面板已就绪：127.0.0.1:$PORT）"
else
  bad "日志中未找到与当前端口匹配的握手记录"
fi

BOUND="$(lsof -nP -p "$HELPER_PID" -a -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR>1 {print $9}' | head -1)"
if [[ "$BOUND" == "127.0.0.1:$PORT" ]]; then
  ok "仅回环监听（$BOUND）"
else
  bad "监听地址不是回环：$BOUND"
fi

if [[ -n "$PORT" ]]; then
  CODE="$(curl -s -o /dev/null -w '%{http_code}' --noproxy '*' --max-time 5 "http://127.0.0.1:$PORT/" 2>/dev/null || echo 000)"
  [[ "$CODE" == "200" ]] && ok "面板首页可访问（200）" || bad "面板首页返回 $CODE"
fi

# ---------------------------------------------------------------------------
head1 "T03 数据目录与网关配置"

for d in "" "/auths" "/backups" "/run"; do
  P="$(stat -f '%Sp' "$DATA$d" 2>/dev/null)"
  if [[ "$P" == "drwx------" ]]; then
    ok "目录权限 0700：$DATA$d"
  else
    bad "目录权限异常（$P）：$DATA$d"
  fi
done

CFG="$DATA/gateway-config.json"
if [[ -f "$CFG" ]]; then
  ok "网关配置已生成"
  P="$(stat -f '%Sp' "$CFG")"
  # 配置里有明文 api_key，必须是 0600
  [[ "$P" == "-rw-------" ]] && ok "配置权限 0600（含明文 api_key）" || bad "配置权限为 $P，应为 0600"

  /usr/bin/python3 - "$CFG" <<'PY' > /tmp/wbd-cfg-check.txt 2>&1
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
def emit(cond, good, bad_):
    print(("OK\t" if cond else "NG\t") + (good if cond else bad_))
emit(d.get('listen') == '127.0.0.1:7863', "listen 为回环 127.0.0.1:7863", f"listen 异常：{d.get('listen')}")
emit(bool(d.get('api_key')) and len(d.get('api_key','')) >= 24, "api_key 已生成长随机值", "api_key 缺失或过短")
emit(d.get('api_key') != 'test_key', "未使用示例密钥 test_key", "使用了示例密钥 test_key")
sched = d.get('schedule') or {}
keys = ['checkin_enabled','travel_enabled','activity_enabled','keepalive_enabled','school_enabled','cat_enabled']
off = [k for k in keys if sched.get(k) is False]
emit(len(off) == len(keys), "6 项排程全部显式关闭", f"排程未全关：{sorted(set(keys)-set(off))}")
emit((d.get('admin') or {}).get('enabled') is False, "admin 管理端点已关闭", "admin 端点仍开启")
PY
  while IFS=$'\t' read -r st msg; do
    [[ "$st" == "OK" ]] && ok "$msg" || bad "$msg"
  done < /tmp/wbd-cfg-check.txt
else
  bad "未生成网关配置 $CFG"
fi

if ls "$DATA"/*.tmp >/dev/null 2>&1; then
  bad "数据目录存在 .tmp 残留"
else
  ok "无 .tmp 残留"
fi

# ---------------------------------------------------------------------------
head1 "T05 单实例"
"$APP/Contents/MacOS/WorkBuddyDesktop" >/tmp/wbd-runtime-2.out 2>&1 &
SECOND=$!
disown 2>/dev/null || true
sleep 5
if kill -0 "$SECOND" 2>/dev/null; then
  bad "第二个副本仍在运行"
  kill -TERM "$SECOND" 2>/dev/null
else
  ok "第二个副本自行退出（未产生第二套服务）"
fi

COUNT="$(pgrep -f 'Helpers/wbgui' 2>/dev/null | wc -l | tr -d ' ')"
[[ "$COUNT" == "1" ]] && ok "面板进程数仍为 1" || bad "面板进程数为 $COUNT"

# ---------------------------------------------------------------------------
head1 "T04 网关（用生成的配置启动）"

GW="$APP/Contents/Helpers/wb2api"
if lsof -nP -iTCP:7863 -sTCP:LISTEN >/dev/null 2>&1; then
  bad "端口 7863 已被占用，跳过网关检查"
else
  "$GW" -config "$CFG" >/tmp/wbd-gw.out 2>&1 &
  GW_PID=$!
  sleep 6

  if kill -0 "$GW_PID" 2>/dev/null; then
    ok "wb2api 已启动（PID $GW_PID）"
  else
    bad "wb2api 启动后立即退出"
  fi

  B="$(lsof -nP -p "$GW_PID" -a -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR>1 {print $9}' | head -1)"
  [[ "$B" == "127.0.0.1:7863" ]] && ok "仅回环监听（$B）" || bad "监听地址异常：$B（上游默认 :7863 是全网卡）"

  H="$(curl -s --noproxy '*' --max-time 3 'http://127.0.0.1:7863/healthz' 2>/dev/null || true)"
  if printf '%s' "$H" | grep -q '"service":"workbuddy2api"'; then
    ok "/healthz 可读（$H）"
  else
    bad "/healthz 响应异常：$H"
  fi

  C="$(curl -s -o /dev/null -w '%{http_code}' --noproxy '*' --max-time 3 'http://127.0.0.1:7863/v1/models' 2>/dev/null || echo 000)"
  [[ "$C" == "401" ]] && ok "无 API Key 访问被拒（401）" || bad "无 Key 访问返回 $C，期望 401"

  # 排程必须真的被禁用 —— 检查网关自己的启动日志，而不是只看配置文件
  DISABLED="$(grep -c '已禁用（schedule' /tmp/wbd-gw.out 2>/dev/null || echo 0)"
  [[ "$DISABLED" -ge 6 ]] && ok "网关确认 6 项排程均已禁用" \
    || bad "网关仅确认 $DISABLED 项排程被禁用，期望 6"

  kill -TERM "$GW_PID" 2>/dev/null
  sleep 2
  kill -0 "$GW_PID" 2>/dev/null && kill -9 "$GW_PID" 2>/dev/null
  ok "wb2api 已停止"
fi

# ---------------------------------------------------------------------------
head1 "T06 无孤儿（强杀父进程）"

HELPER_PID="$(pgrep -f 'Helpers/wbgui' | head -1)"
if [[ -z "$HELPER_PID" ]]; then
  bad "找不到面板进程，跳过孤儿检查"
else
  kill -9 "$APP_PID" 2>/dev/null
  APP_PID=""
  sleep 5

  if kill -0 "$HELPER_PID" 2>/dev/null; then
    bad "强杀父进程后 wbgui 仍在运行 —— 会产生长期孤儿服务"
    kill -9 "$HELPER_PID" 2>/dev/null
  else
    ok "强杀父进程后 wbgui 自行退出（stdin EOF 生效）"
  fi

  # flock 由内核释放，应当能立刻重新启动
  launch /tmp/wbd-runtime-3.out
  sleep 8
  if [[ -n "$(pgrep -f 'Helpers/wbgui' | head -1)" ]]; then
    ok "强杀后单实例锁已被内核释放，可正常重启"
  else
    bad "强杀后无法重新启动（锁未释放？）"
    tail -8 "$LOG" 2>/dev/null
  fi
fi

# ---------------------------------------------------------------------------
head1 "优雅退出"

kill -TERM "$APP_PID" 2>/dev/null
sleep 4
APP_PID=""

if pgrep -f 'WorkBuddyDesktop' >/dev/null 2>&1; then
  bad "SIGTERM 后仍有 WorkBuddyDesktop 进程"
  pgrep -lf 'WorkBuddyDesktop'
else
  ok "App 已退出"
fi
if pgrep -f 'Helpers/wbgui' >/dev/null 2>&1; then
  bad "SIGTERM 后仍有 wbgui 进程"
else
  ok "内置面板已随 App 退出"
fi

# ---------------------------------------------------------------------------
printf '\n%s\n' "────────────────────────────────────────────────────────"
printf '结果：PASS=%d  FAIL=%d\n' "$PASS" "$FAIL"
[[ "$FAIL" == "0" ]] && exit 0 || exit 1
