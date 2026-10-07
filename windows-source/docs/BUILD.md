# 从源码构建 WB-PMM

本指南构建 macOS 14+、Apple Silicon 版 WB-PMM 1.0.3。源码包含 Go 网关、Go 管理面板与 React 前端，以及 SwiftUI / WKWebView 原生外壳。构建脚本会写入本机的独立构建目录；请在可用的磁盘空间充足、支持 Unix 文件权限的文件系统上运行。

## 准备工具

- 完整 Xcode 16 或更新版本（Swift 6 与 macOS SDK），并确保 `xcodebuild` 可用。
- Go 1.22.5 或更新版本，Node.js 20 或更新版本、npm、Python 3、`rsync`、`hdiutil` 和 `codesign`。
- 能获取 Go 与 npm 依赖的网络。`panel/web/package-lock.json` 锁定前端依赖；`gateway/go.mod` 和 `panel/go.mod` 声明 Go 依赖。

网关与面板分别源自 [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) 和 [linbeize/workbuddy2api-gui](https://github.com/linbeize/workbuddy2api-gui)，两者均为 MIT 许可；第三方许可证见 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)。克隆本仓库后，确认 `gateway/`、`panel/` 和 `macos/` 均已包含源码。

```bash
git clone https://github.com/Wang-dachui986/WB-PMM.git
cd WB-PMM
```

## 构建 App

从仓库根目录依次运行：

```bash
./scripts/build.sh
python3 scripts/gen-xcodeproj.py
CONFIG=Release ./scripts/verify-macos-build.sh
```

`build.sh` 先执行 `npm ci` / 前端生产构建，再编译 arm64 的 `wbgui` 与 `wb2api`，默认输出到 `~/workbuddy2api-build/bin/`。`gen-xcodeproj.py` 生成或同步 Xcode 工程。`verify-macos-build.sh` 将 `macos/` 同步到 `~/workbuddy2api-build/macos-src/`，用 Xcode 编译并检查图标、两个内置 Go 可执行文件、架构和 AppleDouble 杂项；Release App 默认位于：

```text
~/workbuddy2api-build/macos-derived/Build/Products/Release/WB-PMM.app
```

`verify-macos-build.sh` 对其专用同步目录使用 `rsync --delete`，因此 `WB_MACOS_BUILD_DIR` 只能指向可覆盖的独立构建目录，不能指向存放个人文件的目录。可按需用 `OUT` 覆盖 Go 产物路径，并以相同目录设置 `WB_HELPERS_SRC`；`WB_MACOS_DERIVED` 可更改 Xcode 产物目录。不要把需要保留的文件放进这些构建目录。

## 验证局域网改动

局域网改动可用 `python3 scripts/verify-lan-runtime.py --gateway /绝对路径/wb2api --lan-ip <本机局域网IPv4>` 验证。脚本使用临时目录、空账号池和随机端口，检查开启/关闭、API Key 与仅本机管理端点，不调用真实模型。这个本机套接字检查不能替代另一台设备经过路由器和防火墙的实际连接测试。

## 制作覆盖升级 PKG

1.0.3 起推荐系统 PKG 安装器：固定覆盖 `/Applications/WB-PMM.app`，正常退出旧版，迁移当前登录用户 Dock 中指向旧版本的 WB-PMM 入口。账号、配置、日志不在安装 payload 中。`pkgbuild` 显式设置不可重定位和整体升级，避免安装器跟随旧 DMG 或临时目录中的 App。

打包输入必须是签名验证通过、许可证齐全的 App；脚本保留输入 App 的签名和内容，不修改输入文件。当前本地发布使用临时签名，可这样准备独立副本（`WBPMM_RELEASE` 请使用新的发布目录）：

```bash
WBPMM_RELEASE="$HOME/workbuddy2api-build/releases/v1.0.3"
WBPMM_APP="$WBPMM_RELEASE/app/WB-PMM.app"
mkdir -p "$WBPMM_RELEASE/app"
ditto "$HOME/workbuddy2api-build/macos-derived/Build/Products/Release/WB-PMM.app" "$WBPMM_APP"
mkdir -p "$WBPMM_APP/Contents/Resources/Legal"
cp licenses/*.txt "$WBPMM_APP/Contents/Resources/Legal/"
cp LICENSE "$WBPMM_APP/Contents/Resources/Legal/WB-PMM-LICENSE.txt"
cp THIRD_PARTY_NOTICES.md "$WBPMM_APP/Contents/Resources/Legal/"
cp gateway/LICENSE "$WBPMM_APP/Contents/Resources/Legal/gateway-LICENSE.txt"
cp panel/LICENSE "$WBPMM_APP/Contents/Resources/Legal/panel-LICENSE.txt"
codesign --force --sign - "$WBPMM_APP/Contents/Helpers/wbgui"
codesign --force --sign - "$WBPMM_APP/Contents/Helpers/wb2api"
codesign --force --sign - --identifier cn.workbuddy.desktop \
  --entitlements macos/WorkBuddyDesktop/WorkBuddyDesktop.entitlements "$WBPMM_APP"
codesign --verify --deep --strict "$WBPMM_APP"
APP="$WBPMM_APP" OUT="$WBPMM_RELEASE" VERSION=1.0.3 bash scripts/package-pkg.sh
```

输出为 `WB-PMM-1.0.3-macos-arm64.pkg` 与对应 `.pkg.sha256`。打包时会编译安装辅助工具，验证目标路径、升级属性、App 版本、架构、签名及许可证。安装前要求应用正常退出；不会强制杀死正在处理请求的 App。安装完成不自动启动 API。Dock 迁移仅更新本应用的固定或最近使用入口，未固定时不添加固定图标。

App 签名与 PKG 签名是两回事：本地 App 使用临时签名，当前 PKG 未作 Developer ID Installer 签名，均未公证；不能把本地 `codesign --verify` 通过称为 Apple 公证。分发时同时提供下载来源与 SHA-256。如后续使用 Developer ID 签名，应在制作 PKG 前完成 App 签名，并使用安装器签名身份签署 PKG。更换 Go 工具链或依赖时同步检查 `licenses/` 与第三方声明。

安装辅助工具的隔离检查（Dock 仅用合成数据；启动一个临时假应用验证正常退出，不读写真实 Dock，也不退出 WB-PMM）：

```bash
WBPMM_CHECK_DIR="$(mktemp -d)"
xcrun swiftc -parse-as-library -swift-version 6 -D INSTALLER_HELPER_TEST scripts/installer-helper.swift \
  scripts/verify-installer-helper.swift -o "$WBPMM_CHECK_DIR/verify-installer-helper"
"$WBPMM_CHECK_DIR/verify-installer-helper"
```

原有 `scripts/package-dmg.sh` 仍可用于制作可选拖拽镜像，但拖拽安装不执行 PKG 的退出旧版、固定替换和 Dock 迁移步骤。需要自动覆盖升级时应分发上述 PKG。最终用户步骤见 [使用指南](USER_GUIDE.md)。
