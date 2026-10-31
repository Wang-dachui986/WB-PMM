# 从源码构建 WB-PMM

当前共用 `gateway/` 与 `panel/`，原生外壳分别在 `macos/` 和 `windows/`。Mac 当前版本 1.0.11（构建 12），Windows 当前版本 1.0.16；两端版本号独立。

## 工具与源码

- Go：最低版本以两个 `go.mod` 为准；当前验证工具链为 Go 1.26.5。
- 前端：Node.js 22、npm，使用 `panel/web/package-lock.json` 锁定依赖。
- Mac：macOS、完整 Xcode（Swift 6 与 macOS 14+ SDK）、Python 3、rsync、codesign、pkgbuild。
- Windows：.NET 10 SDK、NSIS 3 Unicode；交叉打包还需 Bash 与 Python 3。

```bash
git clone https://github.com/Wang-dachui986/WB-PMM.git
cd WB-PMM
```

两个 Go 组件直接包含在源码中，不需要初始化子模块。获取依赖需要联网；构建不需要实际账号、API Key 或模型请求。请使用独立构建目录。

## Mac App

从仓库根目录运行（路径可自行更改）：

```bash
export WBPMM_BUILD="$PWD/build/macos"
VERSION=1.0.11 OUT="$WBPMM_BUILD/bin" bash scripts/build.sh
python3 scripts/gen-xcodeproj.py
CONFIG=Release \
  WB_HELPERS_SRC="$WBPMM_BUILD/bin" \
  WB_MACOS_BUILD_DIR="$WBPMM_BUILD/macos-src" \
  WB_MACOS_DERIVED="$WBPMM_BUILD/derived" \
  bash scripts/verify-macos-build.sh
```

顺序为 React 前端 → Go embed / 两个 arm64 helper → 原生 App。生成的 App 在 `build/macos/derived/Build/Products/Release/WB-PMM.app`。Go helpers 为 arm64，当前包只支持 Apple Silicon。已有 `node_modules` 时脚本会复用它；需要干净依赖时在前端目录执行 `npm ci`。

原生验证脚本会对 `WB_MACOS_BUILD_DIR` 执行 `rsync --delete`，必须指向可覆盖的专用目录，不能指向源码或个人资料目录。请使用支持文件权限与签名的文件系统；exFAT 不适合作为 Xcode 和签名输出位置。

## Mac PKG

先准备独立 App 副本，补齐许可，再签名、打包。以下为临时签名示例，不是 Apple 公证：

```bash
export WBPMM_RELEASE="$PWD/build/releases/macos-1.0.11"
export WBPMM_APP="$WBPMM_RELEASE/app/WB-PMM.app"
mkdir -p "$WBPMM_RELEASE/app"
ditto "$WBPMM_BUILD/derived/Build/Products/Release/WB-PMM.app" "$WBPMM_APP"
mkdir -p "$WBPMM_APP/Contents/Resources/Legal"
cp licenses/*.txt "$WBPMM_APP/Contents/Resources/Legal/"
cp LICENSE "$WBPMM_APP/Contents/Resources/Legal/WB-PMM-LICENSE.txt"
cp THIRD_PARTY_NOTICES.md "$WBPMM_APP/Contents/Resources/Legal/"
cp gateway/LICENSE "$WBPMM_APP/Contents/Resources/Legal/gateway-LICENSE.txt"
cp panel/LICENSE "$WBPMM_APP/Contents/Resources/Legal/panel-LICENSE.txt"
chmod 0644 "$WBPMM_APP/Contents/Resources/Legal/"*
codesign --force --sign - "$WBPMM_APP/Contents/Helpers/wbgui"
codesign --force --sign - "$WBPMM_APP/Contents/Helpers/wb2api"
codesign --force --sign - --identifier cn.workbuddy.desktop \
  --entitlements macos/WorkBuddyDesktop/WorkBuddyDesktop.entitlements "$WBPMM_APP"
codesign --verify --deep --strict "$WBPMM_APP"
APP="$WBPMM_APP" OUT="$WBPMM_RELEASE" VERSION=1.0.11 bash scripts/package-pkg.sh
```

安装器固定覆盖 `/Applications/WB-PMM.app`，保留外部账号与设置并更新已有 Dock 入口。安装前须完全退出旧版，不能只关闭窗口或从 Dock 退出。PKG 签名与 App 签名相互独立；正式发布者需另行完成 Developer ID 签名和公证。打包脚本保留传入 App 的签名，不会将临时签名升级为正式签名。

## Windows Setup

在 Windows PowerShell 从仓库根目录运行：

```powershell
.\windows\build.ps1
.\windows\package.ps1
```

输出到 `windows/dist/`。`package.ps1` 获取微软官方 WebView2 引导程序并在 Windows 检查微软签名；如已准备文件，可使用 `-BootstrapperPath` 指定。此验证不等于给 WB-PMM 自身进行 Authenticode 签名。

在 Mac / Linux 交叉构建：

```bash
OUT="$PWD/windows/dist/app-1.0.16" bash windows/build-cross.sh
APP_DIR="$PWD/windows/dist/app-1.0.16" \
  BOOTSTRAPPER=/absolute/path/to/MicrosoftEdgeWebview2Setup.exe \
  bash windows/package-cross.sh
```

`DOTNET`、`GO`、`MAKENSIS` 可分别指定工具路径；`BOOTSTRAPPER` 由维护者从微软官方获取，交叉打包脚本不下载它。在非 Windows 系统无法验证其 Authenticode，因此只输出带 `-compile-check` 的安装包。不能改名后声称已实机验收。

版本以 `windows/WBPMM/WBPMM.csproj` 为准，升版时同步 `windows/build-cross.sh`、安装器默认值及文档。Mac 版本还须同步 Xcode 工程和 `scripts/gen-xcodeproj.py`。

## 检查

合成、离线逻辑检查（不调用收费模型）：

```bash
(cd gateway && go test ./...)
(cd panel && go test ./...)
(cd panel/web && npm ci && npm run build)
dotnet run --project windows/tests/CoreContracts/CoreContracts.csproj
```

Go 面板初始只有 `dist/.gitkeep`，能编译但没有可用前端；交付前必须先构建前端，再编译 Go。部分浏览器集成测试需要额外的 Playwright / Chrome 环境，按测试脚本参数运行。Windows 平台测试与清单见 [迁移验收](../windows/MIGRATION.md)、`windows/verify.ps1`。

局域网隔离检查可运行 `python3 scripts/verify-lan-runtime.py --gateway /绝对路径/wb2api --lan-ip <本机私有IPv4>`，使用临时目录与空池。本机套接字验证不能替代另一台设备经过路由器 / 防火墙的测试。

编译、模拟和安装包结构检查分别记录；发布前仍需真实系统验证安装、覆盖升级、后台常驻、文件选择、授权和局域网连接。涉及真实授权或模型调用的脚本仅在操作者理解行为和费用后手动运行，禁止使用个人凭证作为 CI 测试数据。
