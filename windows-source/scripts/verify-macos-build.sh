#!/usr/bin/env bash
# ============================================================================
# 原生 App 编译验证
#
# 为什么必须把源码同步到内置磁盘再编译：
#   本仓库所在卷是 exFAT —— chmod 是空操作、每文件生成 ._* AppleDouble 伴生文件、
#   Xcode 的增量构建与签名在该卷上不可靠。这是实测得出的环境硬约束。
#
# 用法：
#   ./scripts/verify-macos-build.sh              # Debug 构建
#   CONFIG=Release ./scripts/verify-macos-build.sh
#   WB_HELPERS_SRC=/path/to/bin ./scripts/verify-macos-build.sh
#
# 退出码：0 构建成功且产物校验通过；非 0 构建失败
# ============================================================================
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${WB_MACOS_BUILD_DIR:-$HOME/workbuddy2api-build/macos-src}"
DERIVED="${WB_MACOS_DERIVED:-$HOME/workbuddy2api-build/macos-derived}"
HELPERS="${WB_HELPERS_SRC:-$HOME/workbuddy2api-build/bin}"
CONFIG="${CONFIG:-Debug}"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "== 1/4 同步源码到内置磁盘 =="
if [ ! -d "$REPO/macos/WorkBuddyDesktop.xcodeproj" ]; then
  echo "错误：找不到 macos/WorkBuddyDesktop.xcodeproj" >&2
  echo "提示：先运行 python3 scripts/gen-xcodeproj.py" >&2
  exit 1
fi

# 用 rsync --delete 让工作区与源码树严格一致（上次构建的残留文件不会被带进来）。
# 刻意不用 `rm -rf` 兜底：这个目录在用户主目录下，误删代价太高；
# 宁可明确报错让用户自己处理。
if ! command -v rsync >/dev/null 2>&1; then
  echo "错误：未找到 rsync，无法安全同步源码。" >&2
  echo "请手动清空 $WORK 后重试。" >&2
  exit 1
fi

mkdir -p "$WORK"
rsync -a --delete --exclude '._*' --exclude '.DS_Store' "$REPO/macos/" "$WORK/"
echo "   $REPO/macos → $WORK"

echo
echo "== 2/4 编译（${CONFIG}）=="
LOG="$DERIVED/build.log"
mkdir -p "$DERIVED"
cd "$WORK"
set +e
xcodebuild \
  -project WorkBuddyDesktop.xcodeproj \
  -scheme WorkBuddyDesktop \
  -configuration "$CONFIG" \
  -derivedDataPath "$DERIVED" \
  WB_HELPERS_SRC="$HELPERS" \
  WORKBUDDY_BUILD_TIME="$BUILD_TIME" \
  CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO \
  build > "$LOG" 2>&1
STATUS=$?
set -e

# 只打印诊断行：错误、警告、以及最终的成败标记
echo "   完整日志：$LOG"
DIAG="$(grep -E '(error|warning): ' "$LOG" | sort -u || true)"
if [ -n "$DIAG" ]; then
  echo "   ── 诊断 ──"
  echo "$DIAG" | sed 's/^/   /'
fi

if [ $STATUS -ne 0 ]; then
  echo
  echo "结果：构建失败（退出码 ${STATUS}）" >&2
  grep -E 'error: ' "$LOG" | sort -u | sed 's/^/  /' >&2
  exit 1
fi
echo "   xcodebuild 成功"

echo
echo "== 3/4 校验产物 =="
APP="$DERIVED/Build/Products/$CONFIG/WB-PMM.app"
[ -d "$APP" ] || { echo "错误：找不到产物 $APP" >&2; exit 1; }

fail=0
RECORDED_TIME="$(/usr/libexec/PlistBuddy -c 'Print WorkBuddyBuildTime' "$APP/Contents/Info.plist")"
[ "$RECORDED_TIME" = "$BUILD_TIME" ] || { echo "错误：构建时间未正确写入成品" >&2; exit 1; }
printf '   UTC build time: %s\n' "$RECORDED_TIME"
check() { # 描述 路径
  if [ -e "$2" ]; then printf '   OK    %s\n' "$1"; else printf '   FAIL  %s（%s）\n' "$1" "$2"; fail=1; fi
}
check "可执行文件"            "$APP/Contents/MacOS/WorkBuddyDesktop"
check "Info.plist"            "$APP/Contents/Info.plist"
check "应用图标 WBPMM"         "$APP/Contents/Resources/WBPMM.icns"
check "内置面板 wbgui"        "$APP/Contents/Helpers/wbgui"
check "内置网关 wb2api"       "$APP/Contents/Helpers/wb2api"

for name in wbgui wb2api; do
  f="$APP/Contents/Helpers/$name"
  if [ -x "$f" ]; then printf '   OK    %s 可执行位\n' "$name"; else printf '   FAIL  %s 缺少可执行位\n' "$name"; fail=1; fi
done

# 打包禁止携带 AppleDouble（开发计划 §17.3）
STRAY="$(find "$APP" -name '._*' -o -name '.DS_Store' | head -20 || true)"
if [ -z "$STRAY" ]; then
  printf '   OK    无 ._* / .DS_Store 残留\n'
else
  printf '   FAIL  产物内存在伴生文件：\n%s\n' "$STRAY"
  fail=1
fi

# 架构与签名
ARCH="$(lipo -archs "$APP/Contents/MacOS/WorkBuddyDesktop" 2>/dev/null || echo '未知')"
printf '   架构：%s\n' "$ARCH"
printf '   签名：%s\n' "$(codesign -dv "$APP" 2>&1 | grep -E '^Signature|^Identifier' | tr '\n' ' ' || echo '未签名')"

echo
echo "== 4/4 产物信息 =="
printf '   路径：%s\n' "$APP"
printf '   大小：%s\n' "$(du -sh "$APP" | cut -f1)"
printf '   版本：%s (%s)\n' \
  "$(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$APP/Contents/Info.plist" 2>/dev/null || echo '?')" \
  "$(/usr/libexec/PlistBuddy -c 'Print CFBundleVersion' "$APP/Contents/Info.plist" 2>/dev/null || echo '?')"
printf '   标识：%s\n' \
  "$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$APP/Contents/Info.plist" 2>/dev/null || echo '?')"

echo
if [ $fail -eq 0 ]; then
  echo "结果：构建通过，产物校验全部通过"
else
  echo "结果：构建成功但产物校验有问题，见上" >&2
  exit 1
fi
