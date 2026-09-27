# 从源码构建 WB-PMM

本指南构建 macOS 14+、Apple Silicon 版 WB-PMM 0.1.0。源码包含 Go 网关、Go 管理面板与 React 前端，以及 SwiftUI / WKWebView 原生外壳。构建脚本会写入本机的独立构建目录；请在可用的磁盘空间充足、支持 Unix 文件权限的文件系统上运行。

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

## 制作 DMG

构建并核对 App 后，以打包脚本制作安装镜像。`APP` 指向上一步的 `.app`；`OUT` 是专用发布输出目录；`LEGAL_DIR` 指向准备好的许可证目录；`VERSION` 决定输出文件名。

```bash
WBPMM_LEGAL_DIR="$HOME/workbuddy2api-build/legal"
mkdir -p "$WBPMM_LEGAL_DIR"
cp licenses/*.txt "$WBPMM_LEGAL_DIR/"
cp LICENSE "$WBPMM_LEGAL_DIR/WB-PMM-LICENSE.txt"
cp THIRD_PARTY_NOTICES.md "$WBPMM_LEGAL_DIR/"
cp gateway/LICENSE "$WBPMM_LEGAL_DIR/gateway-LICENSE.txt"
cp panel/LICENSE "$WBPMM_LEGAL_DIR/panel-LICENSE.txt"

APP="$HOME/workbuddy2api-build/macos-derived/Build/Products/Release/WB-PMM.app" \
OUT="$HOME/workbuddy2api-build/releases/v0.1.0" \
LEGAL_DIR="$WBPMM_LEGAL_DIR" \
VERSION=0.1.0 \
bash scripts/package-dmg.sh
```

输出为 `WB-PMM-0.1.0-macos-arm64.dmg` 和同名 `.dmg.sha256` 文件。脚本对临时副本中的两个 helper 和 App 做临时签名，再验证 DMG、挂载内容、许可证及签名，不修改输入 App。更换 Go 工具链或升级依赖后，应同步更新 `licenses/` 中对应的许可证与第三方说明。

0.1.0 的 DMG 使用临时签名，未进行 Apple Developer ID 签名或公证。分发时应说明这一点，并将最终 DMG 的下载来源和 SHA-256 一起提供。最终用户的安装方式见 [使用指南](USER_GUIDE.md)。
