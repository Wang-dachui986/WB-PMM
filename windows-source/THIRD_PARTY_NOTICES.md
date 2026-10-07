# 第三方声明 · THIRD PARTY NOTICES

WB-PMM 复用以下开源项目。上游 LICENSE 原文保留在其目录内；本地功能修改基于下列固定提交继续开发，当前版本已包含额外补丁。

## 1. workbuddy2api（API 网关）

- 仓库：https://github.com/Sliverkiss/workbuddy2api
- 本项目使用的固定提交：`d1023f373065468d67791faf7e2235768af1e056`
- 许可证：**MIT**
- 本地目录：`gateway/`，许可证原文：`gateway/LICENSE`
- 用途：OpenAI Chat Completions 兼容的本地 API 网关，作为应用内置的 `Contents/Helpers/wb2api`

第三方 Go 依赖（`gateway/go.mod`）：

| 模块 | 版本 | 许可证 |
|---|---|---|
| github.com/redis/go-redis/v9 | v9.18.0 | BSD-2-Clause |
| github.com/cespare/xxhash/v2 | v2.3.0 | MIT |
| github.com/dgryski/go-rendezvous | v0.0.0-20200823014737 | MIT |
| go.uber.org/atomic | v1.11.0 | MIT |

> 桌面版默认不配置 Redis/Upstash，该依赖进入 `Noop` 纯内存模式，不激活网络连接。

## 2. workbuddy2api-gui（社区管理面板）

- 仓库：https://github.com/linbeize/workbuddy2api-gui
- 本项目使用的固定提交：`9413e70bc694a1e76c416d00cf70f5c9869bc3ca`
- 许可证：**MIT**
- 本地目录：`panel/`，许可证原文：`panel/LICENSE`
- 用途：管理面板后端与 React 前端，作为应用内置的 `Contents/Helpers/wbgui` 与 WKWebView 页面

Windows 适配在网关和面板增加 `golang.org/x/sys v0.30.0`（BSD-3-Clause），用于 Windows ACL；许可全文见 `licenses/x-sys.txt`。前端依赖（`panel/web/package.json`）：

| 包 | 声明范围 | 锁文件解析 | 许可证 |
|---|---|---|---|
| react | ^18.3.1 | 18.3.1 | MIT |
| react-dom | ^18.3.1 | 18.3.1 | MIT |
| react-router-dom | ^6.26.2 | 6.30.6 | MIT |
| typescript | ^5.6.3 | 5.9.3 | Apache-2.0 |
| vite | ^5.4.9 | 5.4.21 | MIT |
| @vitejs/plugin-react | ^4.3.2 | 4.7.0 | MIT |

React、React DOM、Scheduler、React Router 和 Remix Router 的运行时代码会进入前端 bundle，并嵌入 `wbgui`，许可证全文见 `licenses/`。
TypeScript、Vite 及构建插件为开发工具；完整版本清单以 `panel/web/package-lock.json` 为准。`node_modules` 不放入 `.app`。

## 3. 本项目自身

WB-PMM 的新增原生外壳、桌面适配、界面及测试代码采用根目录 `LICENSE` 中的 MIT 许可。上游代码及修改版本继续保留原版权声明。

`licenses/` 收录 Go 运行时、Go 依赖及前端运行时依赖的原始许可证。发布的应用在 `Contents/Resources/Legal/` 中随附这些许可和两个上游 LICENSE。

## 4. 使用限制说明

Windows 原生外壳使用 .NET 10 / Windows Desktop Runtime（MIT）及 Microsoft WebView2 SDK（微软许可）。安装目录 `Legal/` 随附这两类运行组件的原始 LICENSE/NOTICE，以及 Go、前端、上游项目许可。WebView2 Runtime 为微软独立组件，通过官方安装程序部署，适用其自身许可。

- 源码许可是**开源许可**，不等于上游服务的调用授权。用户须自行确保添加的上游账号为其
  **本人有权使用**的账号。
- 上游账号可用性、服务额度、授权协议由上游服务提供方决定，与本项目无关，且可能随时变化。
- 本应用**不是**任何上游厂商的官方产品，不得以官方名义分发。
