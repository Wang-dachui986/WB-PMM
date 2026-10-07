#!/usr/bin/env bash
# ============================================================================
# 桌面模式启动握手验证
#
# 验证 wbgui --desktop 与原生壳之间的启动契约（开发计划 §7.1）：
#   1. 从 stdin 管道读取启动参数，超时上限 10 秒
#   2. 监听 127.0.0.1:0，由系统分配端口
#   3. 向 stdout 输出机器可读 ready 消息（服务名/协议版本/端口/实例ID，不含密钥）
#   4. 普通日志走 stderr，不污染 stdout
#   5. 内部口令生效；默认口令 admin/workbuddy 失效
#   6. 网页改口令能力关闭（password_changeable=false）
#   7. WBGUI_* 环境变量无法改写监听地址（上游存在该覆盖通道）
#   8. stdin EOF（父进程消失）时子进程优雅退出
#
# 用法：./scripts/verify-desktop-handshake.sh [wbgui 可执行文件路径]
# 退出码：0 全部通过；1 存在失败项
#
# 不使用生产端口，不读取真实凭证，不触碰用户数据目录。
# ============================================================================
set -uo pipefail

WBGUI="${1:-${WBGUI:-$HOME/workbuddy2api-build/bin/wbgui}}"

if [[ ! -x "$WBGUI" ]]; then
  echo "错误：找不到可执行的 wbgui：$WBGUI" >&2
  echo "用法：$0 <wbgui 路径>" >&2
  exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/wb-desktop-XXXXXX")"
STDIN_FIFO="$WORK/stdin.pipe"
OUT="$WORK/stdout.log"
ERR="$WORK/stderr.log"
PID_FILE="$WORK/helper.pid"
PID=""

PASS=0
FAIL=0
NOTES=()

ok()   { printf '\033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS + 1)); }
bad()  { printf '\033[31mFAIL\033[0m  %s\n' "$1"; FAIL=$((FAIL + 1)); }
note() { printf '\033[33mNOTE\033[0m  %s\n' "$1"; NOTES+=("$1"); }

cleanup() {
  # 关闭写端，让子进程自然收到 EOF；仍存活则强杀，避免留下孤儿。
  exec 3>&- 2>/dev/null || true
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null || true
    sleep 1
    kill -KILL "$PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# 启动一次桌面会话
# ---------------------------------------------------------------------------
PASSWORD="$(openssl rand -hex 24)"
INSTANCE_ID="$(openssl rand -hex 8)"
USERNAME="desktop-$(openssl rand -hex 4)"

mkdir -p "$WORK/auths"
mkfifo "$STDIN_FIFO"

INIT_JSON=$(cat <<JSON
{"protocol":1,"instance_id":"$INSTANCE_ID","gateway_url":"http://127.0.0.1:7863","auth_dir":"$WORK/auths","config_file":"$WORK/gateway-config.json","backup_dir":"$WORK/backups","username":"$USERNAME","password":"$PASSWORD"}
JSON
)

echo "== 启动 wbgui --desktop =="
echo "   可执行文件: $WBGUI"
echo "   临时目录:   $WORK"
echo ""

# stdout 必须走**真管道**：原生壳读子进程 stdout 用的就是 `Process` + `Pipe`。
# 用文件重定向会掩盖「对管道做 fsync 失败」这类只在管道上出现的问题。
#
# 为什么要用「管道左侧的子 shell」而不是 `mkfifo` + `cat &`：
#   mkfifo 的读端与写入端存在竞争 —— 若写入发生在读端 open() 之前，
#   读端会永久阻塞在 open() 上，stdout 内容直接丢失。实测确认过这个失败模式。
#   匿名管道由内核在 fork 时建好，不存在这个窗口。
# 子 shell 的作用是拿到 helper 的真实 PID（整条管道的 $! 只会给到最后一个元素）。
{
  # 故意设置 WBGUI_LISTEN 与 WBGUI_PASSWORD：桌面模式必须无视它们。
  WBGUI_LISTEN=":9999" WBGUI_PASSWORD="should-be-ignored" \
    "$WBGUI" --desktop < "$STDIN_FIFO" 2> "$ERR" &
  echo $! > "$PID_FILE"
  wait
} | cat > "$OUT" &

# 等 helper PID 落盘
for _ in $(seq 1 100); do
  [[ -s "$PID_FILE" ]] && break
  sleep 0.05
done
PID="$(cat "$PID_FILE" 2>/dev/null || true)"
if [[ -z "$PID" ]]; then
  bad "无法取得 helper 进程号"
  exit 1
fi

# 打开 stdin 写端并保持打开（父进程存活期间 stdin 不关闭）
exec 3> "$STDIN_FIFO"
printf '%s\n' "$INIT_JSON" >&3

# ---------------------------------------------------------------------------
# 1. 等待 ready 消息
# ---------------------------------------------------------------------------
READY_LINE=""
for _ in $(seq 1 100); do   # 最多 10 秒
  if [[ -s "$OUT" ]]; then
    READY_LINE="$(grep -m1 '^@@WBGUI-READY@@ ' "$OUT" 2>/dev/null || true)"
    [[ -n "$READY_LINE" ]] && break
  fi
  sleep 0.1
done

if [[ -z "$READY_LINE" ]]; then
  bad "未在 10 秒内收到 ready 握手消息"
  echo "--- stdout ---"; cat "$OUT" 2>/dev/null
  echo "--- stderr ---"; cat "$ERR" 2>/dev/null
  exit 1
fi
ok "收到 ready 握手消息"

READY_JSON="${READY_LINE#@@WBGUI-READY@@ }"
echo "   $READY_JSON"
echo ""

# ---------------------------------------------------------------------------
# 1b. 握手之后进程必须仍然存活
#
# 专门盯住这个失败模式：stdout 是管道时，如果在写出就绪消息后调用 fsync，
# 会拿到 EINVAL，于是出现「消息已经送达、进程却立即退出」的现象。
# 用文件重定向做 harness 时（fsync 对普通文件有效）完全看不到这个问题。
# ---------------------------------------------------------------------------
if kill -0 "$PID" 2>/dev/null; then
  ok "握手后进程仍存活（stdout 为管道时未因写后 flush 失败退出）"
else
  bad "握手后进程立即退出 —— 极可能是对 stdout 管道做了 fsync"
  echo "--- stderr ---"; cat "$ERR" 2>/dev/null
  exit 1
fi

json_get() { printf '%s' "$1" | /usr/bin/python3 -c "import json,sys;print(json.load(sys.stdin).get('$2',''))" 2>/dev/null; }

SERVICE="$(json_get "$READY_JSON" service)"
PROTOCOL="$(json_get "$READY_JSON" protocol)"
PORT="$(json_get "$READY_JSON" port)"
RET_ID="$(json_get "$READY_JSON" instance_id)"

[[ "$SERVICE" == "wbgui" ]] && ok "service = wbgui" || bad "service 应为 wbgui，实际 '$SERVICE'"
[[ "$PROTOCOL" == "1" ]] && ok "protocol = 1" || bad "protocol 应为 1，实际 '$PROTOCOL'"
[[ "$PORT" =~ ^[0-9]+$ ]] && [[ "$PORT" -ge 1024 && "$PORT" -le 65535 ]] \
  && ok "port = $PORT（有效的高位端口）" || bad "port 非法：'$PORT'"
[[ "$RET_ID" == "$INSTANCE_ID" ]] && ok "instance_id 与下发值一致" || bad "instance_id 不一致：'$RET_ID' != '$INSTANCE_ID'"

# ---------------------------------------------------------------------------
# 2. ready 消息不得含任何密钥
# ---------------------------------------------------------------------------
if printf '%s' "$READY_JSON" | grep -q "$PASSWORD"; then
  bad "ready 消息泄漏了内部口令"
else
  ok "ready 消息不含内部口令"
fi
if printf '%s' "$READY_JSON" | grep -qiE 'password|api_key|secret|token'; then
  bad "ready 消息含疑似密钥字段名"
else
  ok "ready 消息无密钥类字段"
fi

# ---------------------------------------------------------------------------
# 3. 监听地址必须是回环，且不受 WBGUI_LISTEN 影响
# ---------------------------------------------------------------------------
LISTEN_INFO="$(lsof -nP -p "$PID" -a -iTCP -sTCP:LISTEN 2>/dev/null | tail -n +2 | head -1 || true)"
if [[ -n "$LISTEN_INFO" ]]; then
  BOUND="$(printf '%s' "$LISTEN_INFO" | awk '{print $9}')"
  if [[ "$BOUND" == "127.0.0.1:$PORT" ]]; then
    ok "监听 $BOUND（回环 + 实际端口）"
  else
    bad "监听地址异常：$BOUND（期望 127.0.0.1:$PORT）"
  fi
else
  bad "无法用 lsof 确认监听地址"
fi

if [[ "$PORT" != "9999" ]]; then
  ok "WBGUI_LISTEN=:9999 未生效（桌面模式忽略环境变量覆盖）"
else
  bad "WBGUI_LISTEN 覆盖生效了 —— 环境变量残留可改写监听地址"
fi

# ---------------------------------------------------------------------------
# 4. 普通日志走 stderr，stdout 只有握手
# ---------------------------------------------------------------------------
STDOUT_LINES="$(wc -l < "$OUT" | tr -d ' ')"
# 用 grep | wc -l 而不是 grep -c：后者在「无匹配」时退出码为 1，
# 与 `|| echo 0` 叠加会把结果变成两行。
NON_READY="$(grep -v '^@@WBGUI-READY@@ ' "$OUT" 2>/dev/null | wc -l | tr -d ' ')"
[[ "$NON_READY" == "0" ]] && ok "stdout 仅有握手消息（$STDOUT_LINES 行）" \
  || bad "stdout 混入 $NON_READY 行非握手输出"
[[ -s "$ERR" ]] && ok "普通日志已输出到 stderr" || bad "stderr 为空，日志去向可疑"

# ---------------------------------------------------------------------------
# 5. 内部口令生效；默认口令失效
# ---------------------------------------------------------------------------
BASE="http://127.0.0.1:$PORT"
JAR="$WORK/cookies.txt"

CODE_OK="$(curl -s -o "$WORK/login-ok.json" -w '%{http_code}' -c "$JAR" \
  -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  --data "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" 2>/dev/null || echo 000)"
[[ "$CODE_OK" == "200" ]] && ok "内部随机口令登录成功（200）" || bad "内部随机口令登录失败（$CODE_OK）"

CODE_DEFAULT="$(curl -s -o /dev/null -w '%{http_code}' \
  -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
  --data '{"username":"admin","password":"workbuddy"}' 2>/dev/null || echo 000)"
[[ "$CODE_DEFAULT" == "401" ]] && ok "默认口令 admin/workbuddy 被拒绝（401）" \
  || bad "默认口令未被拒绝（$CODE_DEFAULT）"

# ---------------------------------------------------------------------------
# 6. 会话信息：改口令能力关闭
# ---------------------------------------------------------------------------
SESSION="$(curl -s -b "$JAR" "$BASE/api/session" 2>/dev/null || echo '{}')"
CHANGEABLE="$(json_get "$SESSION" password_changeable)"
DEFAULT_PW="$(json_get "$SESSION" using_default_password)"
AUTHED="$(json_get "$SESSION" authenticated)"

[[ "$AUTHED" == "True" || "$AUTHED" == "true" ]] && ok "会话已建立（authenticated=true）" \
  || bad "会话未建立：$SESSION"
[[ "$CHANGEABLE" == "False" || "$CHANGEABLE" == "false" ]] && ok "网页改口令能力已关闭（password_changeable=false）" \
  || bad "网页改口令仍可用：password_changeable=$CHANGEABLE"
[[ "$DEFAULT_PW" == "False" || "$DEFAULT_PW" == "false" ]] && ok "未使用默认口令（using_default_password=false）" \
  || bad "仍报告使用默认口令"

# ---------------------------------------------------------------------------
# 7. 静态资源可用（面板页面能打开）
# ---------------------------------------------------------------------------
CODE_INDEX="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/" 2>/dev/null || echo 000)"
[[ "$CODE_INDEX" == "200" ]] && ok "面板首页可访问（200）" || bad "面板首页返回 $CODE_INDEX"

# ---------------------------------------------------------------------------
# 8. stdin EOF → 子进程优雅退出
# ---------------------------------------------------------------------------
echo ""
echo "== 关闭 stdin（模拟父进程消失）=="
exec 3>&-
EXITED=0
for _ in $(seq 1 100); do   # 最多 10 秒
  if ! kill -0 "$PID" 2>/dev/null; then EXITED=1; break; fi
  sleep 0.1
done
if [[ "$EXITED" == "1" ]]; then
  ok "stdin EOF 后子进程已退出（无孤儿）"
  PID=""
else
  bad "stdin EOF 后子进程仍在运行 —— 会产生长期孤儿服务"
fi

# ---------------------------------------------------------------------------
echo ""
echo "────────────────────────────────────────────────────────"
echo "结果：PASS=$PASS  FAIL=$FAIL"
if [[ ${#NOTES[@]} -gt 0 ]]; then
  echo "备注："
  for n in "${NOTES[@]}"; do echo "  · $n"; done
fi
[[ "$FAIL" == "0" ]] && exit 0 || exit 1
