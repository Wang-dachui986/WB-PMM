#!/usr/bin/env bash
# build.sh — WB-PMM 前端与内置服务构建脚本
#
# 产出：<OUT>/wb2api、<OUT>/wbgui（macOS arm64，CGO_ENABLED=0）
#
# 设计要点：
#  1. 前端必须先构建，再由 Go embed 打包完整页面及资源。
#  2. 前端产物写入 panel/internal/webui/dist，服务可执行文件写入 OUT。
#  3. GO 和 NODE_BIN_DIR 可覆盖自动探测到的工具链。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATEWAY="$ROOT/gateway"
PANEL="$ROOT/panel"
OUT="${OUT:-$HOME/workbuddy2api-build/bin}"
VERSION="${VERSION:-0.1.0}"

# ── 工具链探测 ────────────────────────────────────────────────────────────────
find_go() {
  if [[ -n "${GO:-}" ]]; then echo "$GO"; return; fi
  local c
  for c in \
    "$HOME/.workbuddy-ai/binaries/go/go/bin/go" \
    /usr/local/go/bin/go \
    /opt/homebrew/bin/go ; do
    [[ -x "$c" ]] && { echo "$c"; return; }
  done
  command -v go 2>/dev/null || true
}
GO_BIN="$(find_go)"
[[ -n "$GO_BIN" && -x "$GO_BIN" ]] || { echo "错误：未找到 Go 工具链。请设置 GO=<go 可执行文件路径>" >&2; exit 1; }

find_node_bin() {
  if [[ -n "${NODE_BIN_DIR:-}" ]]; then echo "$NODE_BIN_DIR"; return; fi
  local d
  for d in "$HOME"/.workbuddy-ai/binaries/node/versions/*/bin ; do
    [[ -x "$d/node" ]] && { echo "$d"; return; }
  done
  command -v node >/dev/null 2>&1 && { dirname "$(command -v node)"; return; }
  true
}
NODE_DIR="$(find_node_bin)"
[[ -n "$NODE_DIR" ]] || { echo "错误：未找到 Node.js。请设置 NODE_BIN_DIR=<包含 node 的目录>" >&2; exit 1; }
export PATH="$NODE_DIR:$PATH"

echo "== 工具链 =="
echo "  go   : $($GO_BIN version)"
echo "  node : $(node -v)   npm: $(npm -v)"
echo "  输出 : $OUT"
echo

mkdir -p "$OUT"

# ── 1) 前端生产构建 ──────────────────────────────────────────────────────────
echo "== [1/3] 前端构建（npm ci + vite build）=="
cd "$PANEL/web"
if [[ ! -d node_modules ]]; then
  npm ci --no-audit --no-fund
else
  echo "  node_modules 已存在，跳过 npm ci（如需干净构建请先删除）"
fi
npm run build

DIST="$PANEL/internal/webui/dist"
[[ -f "$DIST/index.html" ]] || { echo "错误：前端未产出 $DIST/index.html" >&2; exit 1; }
# 关键断言：index.html 引用的 assets 必须真实存在，否则打包出白屏面板。
grep -o '/assets/[A-Za-z0-9._-]*' "$DIST/index.html" | sort -u | while read -r a; do
  f="$DIST${a}"
  [[ -f "$f" ]] || { echo "错误：index.html 引用了不存在的资源 $a —— 前端构建不完整" >&2; exit 1; }
done
echo "  前端产物校验通过：$(ls "$DIST/assets" | grep -v '^\._' | tr '\n' ' ')"
# vite 会在 web/ 下留下 vite.config.ts.timestamp-*.mjs 临时配置产物（上游 .gitignore 未覆盖）。
# 在构建脚本里清掉，而不是去改上游的 .gitignore —— 少一个本地补丁。
rm -f "$PANEL/web"/vite.config.ts.timestamp-*.mjs
echo

# ── 2) 面板后端 ──────────────────────────────────────────────────────────────
echo "== [2/3] 面板后端 wbgui =="
cd "$PANEL"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$GO_BIN" build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/wbgui" ./cmd/server
echo "  $OUT/wbgui"

# ── 3) 网关后端 ──────────────────────────────────────────────────────────────
echo "== [3/3] 网关后端 wb2api =="
cd "$GATEWAY"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 "$GO_BIN" build -buildvcs=false -trimpath -ldflags "-s -w" -o "$OUT/wb2api" ./cmd/server
echo "  $OUT/wb2api"

# ── 汇总 ─────────────────────────────────────────────────────────────────────
echo
echo "== 构建完成 =="
# 清理 go build 在 exFAT 卷上留下的 0 字节权限探测残留
find "$OUT" -maxdepth 1 -name '*-go-tmp-umask' -size 0 -delete 2>/dev/null || true
for f in wbgui wb2api; do
  printf "  %-8s %8s  %s  %s\n" "$f" \
    "$(du -h "$OUT/$f" | cut -f1)" \
    "$(file -b "$OUT/$f" | cut -d, -f1-2)" \
    "$(shasum -a 256 "$OUT/$f" | cut -c1-16)"
done
