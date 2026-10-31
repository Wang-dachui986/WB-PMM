#!/usr/bin/env bash
# macOS/Linux 交叉打包：build-cross.sh 之后运行，产出 WB-PMM-Setup-<版本>-x64-compile-check.exe。
# 与 package.ps1 同一套 NSIS 脚本与清单规则；本机无法验证微软引导程序签名，所以只出 compile-check 包。
#   APP_DIR       发布目录（默认 windows/dist/app，与 build-cross.sh 的 OUT 一致）
#   OUTPUT_DIR    输出目录（默认 windows/dist）
#   BOOTSTRAPPER  WebView2 引导程序（默认 windows/dist/dependencies/MicrosoftEdgeWebview2Setup.exe）
#   MAKENSIS      makensis 路径（默认取 PATH）
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd -P)"
APP_DIR="${APP_DIR:-$ROOT/dist/app}"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT/dist}"
BOOTSTRAPPER="${BOOTSTRAPPER:-$ROOT/dist/dependencies/MicrosoftEdgeWebview2Setup.exe}"
MAKENSIS="${MAKENSIS:-makensis}"

# makensis 必须拿绝对路径：WB-PMM.nsi 以 "${__FILEDIR__}\..." 引用同目录文件，相对路径在非 Windows 上拼不出来。
APP_DIR="$(cd "$APP_DIR" && pwd -P)"
mkdir -p "$OUTPUT_DIR"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd -P)"
[[ -f "$BOOTSTRAPPER" ]] || { echo "缺少 WebView2 引导程序：$BOOTSTRAPPER（从微软官方下载 MicrosoftEdgeWebview2Setup.exe 放到该位置）" >&2; exit 1; }
BOOTSTRAPPER="$(cd "$(dirname "$BOOTSTRAPPER")" && pwd -P)/$(basename "$BOOTSTRAPPER")"
(( $(wc -c < "$BOOTSTRAPPER") >= 100000 )) || { echo 'WebView2 引导程序文件无效' >&2; exit 1; }
command -v "$MAKENSIS" >/dev/null || { echo '未找到 makensis（NSIS 3 Unicode），可用 MAKENSIS 指定' >&2; exit 1; }

VERSION="$(sed -n 's#.*<Version>\(.*\)</Version>.*#\1#p' "$ROOT/WBPMM/WBPMM.csproj" | head -1)"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo '项目版本号无效' >&2; exit 1; }
[[ -f "$APP_DIR/WB-PMM.deps.json" ]] && ! grep -q "\"WB-PMM/$VERSION\"" "$APP_DIR/WB-PMM.deps.json" && {
  echo "发布目录不是 $VERSION 的产物（WB-PMM.deps.json 版本不符），请先运行 build-cross.sh" >&2; exit 1;
}

SETUP="$OUTPUT_DIR/WB-PMM-Setup-$VERSION-x64-compile-check.exe"
MANIFEST="$OUTPUT_DIR/payload-$VERSION.nsh"
[[ ! -e "$SETUP" ]] || { echo "安装包已存在，不覆盖：$SETUP" >&2; exit 1; }

python3 -I "$ROOT/gen-payload-manifest.py" "$APP_DIR" "$MANIFEST"
# 清单与安装脚本含中文：makensis 在非 Windows 上按区域设置解码，未设置时报 Bad text encoding。
LC_ALL=en_US.UTF-8 LANG=en_US.UTF-8 "$MAKENSIS" -WX -V2 \
  "-DAPP_DIR=$APP_DIR" "-DWEBVIEW2_BOOTSTRAPPER=$BOOTSTRAPPER" "-DPAYLOAD_MANIFEST=$MANIFEST" \
  "-DSETUP_OUT=$SETUP" "-DAPP_VERSION=$VERSION" "$ROOT/installer/WB-PMM.nsi"
[[ -f "$SETUP" ]] || { echo 'NSIS 安装包编译失败' >&2; exit 1; }
(cd "$OUTPUT_DIR" && shasum -a 256 "$(basename "$SETUP")" > "$(basename "$SETUP").sha256")
printf 'Created: %s\n' "$SETUP"
cat "$SETUP.sha256"
echo '未验证：WebView2 引导程序 Authenticode 签名、Windows 实机安装与运行。'
