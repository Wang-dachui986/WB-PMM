# WB-PMM

本地 AI 网关控制台，支持 macOS 与 Windows 10/11 x64。包含账号管理、模型接入、局域网调用、后台托盘/菜单栏运行及 Token 用量统计。

## 安装包

[下载本次版本：macOS 1.0.5 / Windows 1.0.10](https://github.com/Wang-dachui986/WB-PMM/releases/tag/desktop-2026-10-08)

- **macOS**：`WB-PMM-1.0.5-macos-arm64.pkg`，Apple Silicon、macOS 14 及以上，安装至 `/Applications/WB-PMM.app`。
- **Windows**：`WB-PMM-Setup-1.0.10-x64-compile-check.exe`，Windows 10/11 Intel/AMD x64，可选择安装位置，生成开始菜单和桌面快捷方式。
- 覆盖升级前正常退出应用；账号和配置独立保存。不要手工覆盖正在运行的程序。
- 同一 Release 提供 SHA-256 校验文件。安装包按本地原件上传，没有重新压缩或修改签名。

**验证范围**：本地自动测试、前端构建、两端编译、NSIS 编译及 Mac 包内容/临时签名检查通过；Mac 内置网关通过隔离运行检查。Windows 安装、系统权限和托盘行为仍待实机验收，因此文件名保留 `compile-check`。Mac PKG 未签署 Developer ID Installer、未公证；Windows 安装包未作发布者签名。

## 私有仓库下载权限

仓库保持 Private。源码与 Release 安装包都需要登录 GitHub，并拥有此仓库的读取权限。

**转发链接不会自动授予下载权限。** 如需让指定人员下载，可邀请对方为私有仓库协作者，再发送 Release 链接；协作者也能读取源码。GitHub 私有 Release 不提供“只凭链接下载、无需账号、只能访问安装包”的共享模式。若需要该模式，应另行使用支持限时下载链接的文件存储，源码仓库继续保持私有。

## 快速使用

1. 安装并打开应用，在「添加账号」完成授权。
2. 点击「启动」，确认网关可调用。
3. 在「API 与模型」复制地址和 API Key，填入调用端。
4. 本机默认 Base URL：`http://127.0.0.1:7863/v1`。
5. 完整接口：`/v1/chat/completions`、`/v1/responses`、`/v1/messages`。按调用端要求选择 Base URL 或完整端点。
6. 「Token 统计」支持日、周、月、年度，按模型统计输入/输出；可切换热力图、扇形图、柱状图、曲线图。

Token 历史仅从新版本启动后开始保存，无法补回之前未记录的数据；只累计上游实际返回的 usage。详见 [Token 统计说明](docs/TOKEN-STATISTICS.md)。

## 源码结构与构建

本次保留两端已验证的独立源码快照，避免合并平台差异时丢失 Windows 权限和进程管理修复：

| 目录 | 用途 |
| --- | --- |
| 根目录 `macos/`、`gateway/`、`panel/`、`scripts/` | macOS 1.0.5 构建源 |
| `windows-source/` | Windows 1.0.10 的完整构建源；从此目录执行 Windows 构建 |
| `docs/` | 使用、构建及 Token 统计说明 |

- [Mac 构建说明](docs/BUILD.md) / [Mac 项目说明](README-macOS.md)
- [Windows 构建与安装说明](windows-source/windows/README.md)
- [完整使用说明](docs/USER_GUIDE.md)

构建前安装对应 Go、Node.js/npm、Xcode 或 .NET/NSIS 工具链，并安装前端依赖。Windows 源码快照中保留的 macOS 文件不代表最新 Mac 版本；Mac 请使用仓库根目录。

仓库不包含真实账号凭证、运行配置、日志、依赖缓存和编译临时文件。许可证与上游声明保留在 `LICENSE`、`THIRD_PARTY_NOTICES.md`、`UPSTREAM.md` 和 `licenses/`。
