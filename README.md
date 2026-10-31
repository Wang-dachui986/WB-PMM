<p align="center"><img src="docs/brand/wb-pmm-logo.png" width="96" alt="WB-PMM"></p>

# WB-PMM 控制台

运行在 **macOS 和 Windows** 的本地账号池与 API 网关控制台。管理账号授权、模型接入、积分查询、Token 用量和后台服务，把常用 AI 客户端接入同一个本机网关。

**MIT 开源 · 简体中文 · 本机保存数据**

本项目是社区软件，不是腾讯、WorkBuddy 或 CodeBuddy 的官方产品。使用者须持有并有权使用相应账号；模型、积分和服务规则由上游决定，软件不提供免费额度。

## 下载与安装

从 [GitHub Releases](https://github.com/Wang-dachui986/WB-PMM/releases/latest) 下载安装包及 `SHA256SUMS.txt`，不要从源码目录寻找安装程序。

| 平台 | 当前安装包 | 系统要求 |
| --- | --- | --- |
| macOS | `WB-PMM-1.0.11-macos-arm64.pkg` | macOS 14+，Apple Silicon（M 系列）；不支持 Intel Mac |
| Windows | `WB-PMM-Setup-1.0.16-x64-compile-check.exe` | Windows 10 22H2 / Windows 11，Intel/AMD x64 |

- **Mac**：先从顶部菜单栏选择“完全退出”旧版，再运行 PKG，固定安装或覆盖 `/Applications/WB-PMM.app`。更新保留账号和设置，并迁移已有 Dock 入口。
- **Windows**：运行 Setup，可选择安装位置，默认 `%LOCALAPPDATA%\Programs\WB-PMM`；安装后创建开始菜单与桌面快捷方式。覆盖升级保留账号和设置。
- 安装包内置网关、面板及必要运行组件，普通使用无需安装 Go、Node.js、Docker 或 Redis。Windows 界面依赖 WebView2 Runtime，缺少时需要联网安装微软官方运行时。

**签名与验证状态**：Mac App 使用临时签名，PKG 未作 Developer ID Installer 签名或 Apple 公证；Windows 安装包未作 Authenticode 发布者签名。Windows 保留 `compile-check` 标记：编译与合同测试通过，不代表完成 Windows 实机验收。最新两端覆盖安装、Windows 文件选择器仍需实机复测。请核对下载来源和 SHA-256，勿关闭全局系统安全检查。

## 五步开始

1. 打开应用，按首次悬浮引导进入“添加账号”，选择国内版或国际版并在浏览器完成授权。也可导入本人有权使用的单账号 JSON 凭证。
2. 回到“账号管理”确认凭证保存成功，点击顶部“启动 API”。
3. 在“API 与模型”复制 **Base URL、API Key 和完整模型 ID**。
4. 在客户端选择对应协议并填写地址与密钥，模型 ID 保留 `cn:` / `global:` 前缀。
5. 在“聊天测试”手动发消息验证；真实请求可能消耗上游积分。关闭主窗口后继续在 Mac 菜单栏或 Windows 托盘运行，选择“完全退出”才停止应用。

详细步骤、导入格式、升级和故障排查见 [使用指南](docs/USER_GUIDE.md)；Windows 安装与构建见 [Windows 说明](windows/README.md)。

## 地址怎么填

默认端口 `7863`，端口变更后以应用内复制的地址为准。

| 协议 | 自动拼接路径的 SDK Base URL | 完整 POST 端点 |
| --- | --- | --- |
| OpenAI Chat Completions | `http://127.0.0.1:7863/v1` | `http://127.0.0.1:7863/v1/chat/completions` |
| OpenAI Responses | `http://127.0.0.1:7863/v1` | `http://127.0.0.1:7863/v1/responses` |
| Anthropic Messages | `http://127.0.0.1:7863` | `http://127.0.0.1:7863/v1/messages` |

三种协议共用应用生成的 API Key。OpenAI 使用 `Authorization: Bearer <Key>`；Messages 支持 `x-api-key: <Key>` 或 Bearer。客户端如果要求完整端点就填写右列，避免重复追加路径。WorkBuddy 的填写方式由“自定义协议”开关决定：关闭时填 `/v1`，开启时填 `/v1/chat/completions`；国内版与国际版采用同一规则。

提供的是三种协议的**常用兼容子集**。多轮对话由客户端传完整历史；不提供 Responses 服务端会话存储、后台任务或厂商内置工具，不保证任意 Agent 的专有扩展可用。

## 主要功能

- **账号池**：国内 / 国际账号授权，JSON 导入、重新授权、状态查看、单个及批量删除，最多 100 个账号；覆盖凭证须明确确认，跨区域覆盖会拒绝。
- **仪表盘**：服务状态、账号池概览、全部积分查询、进度与失败账号明细。未知余额不记为零；积分认证状态与聊天调度状态分别显示。
- **API 与模型**：复制各协议端点、密钥轮换、模型列表与刷新、模型能力和推理档位展示。填写 1M 不会扩大上游真实上下文限制。
- **Token 统计**：按日、周、月、年和模型筛选；方块热力图、扇形图、柱状图、曲线图自由切换。保留最近 400 天，仅累计实际返回的用量。[统计说明](docs/TOKEN-STATISTICS.md)
- **网关配置**：端口、代理、超时、并发、会话保持，国内账号定时签到。限流时有账号冷却及保护逻辑，可用性仍以上游为准。
- **局域网**：默认关闭；开启后同一可信网络的其他设备使用应用显示的局域网地址和相同 API Key。管理端点与凭证操作保持仅本机可访问。
- **桌面常驻**：首次引导可跳过和重播，菜单栏 / 托盘，关闭窗口继续运行，可选登录自启和随应用启动 API，日志与诊断。

当前未内置公网穿透；局域网使用 HTTP。不要把管理面板或网关直接暴露公网，API Key 持有者可消耗账号池额度。

## 数据与隐私

| 平台 | 账号与配置 | 日志 |
| --- | --- | --- |
| macOS | `~/Library/Application Support/WorkBuddy2APIDesktop/` | `~/Library/Logs/WorkBuddy2APIDesktop/` |
| Windows | `%LOCALAPPDATA%\WB-PMM` | 数据目录下的 `logs` |

升级和普通卸载不清除这些数据。`auths/`、配置及其备份可能含令牌或密钥，应按密码保护。**不要上传真实凭证、配置、完整日志或个人运行数据到 Issue / PR。** 源码仓库与发布安装包不附带用户账号和运行数据。

## 源码与构建

```text
macos/     SwiftUI / WKWebView 外壳
windows/   WinForms / WebView2 外壳、NSIS 安装器
panel/     Go 管理面板、React / TypeScript 前端
gateway/   Go API 网关与兼容适配
scripts/   macOS 构建、打包和验证脚本
licenses/  第三方许可证原文
docs/      使用、构建、统计和修复记录
```

同一份 `gateway/` 与 `panel/` 供两端构建，避免维护两套业务实现。完整步骤见 [构建指南](docs/BUILD.md)。测试默认应使用合成凭证和本地模拟；不要把真实模型请求当作免费的测试。

## 许可与来源

本项目新增代码按 [MIT License](LICENSE) 开源，允许使用、修改和再分发，须保留许可与版权声明。第三方组件遵循各自许可。

感谢 [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api)、[linbeize/workbuddy2api-gui](https://github.com/linbeize/workbuddy2api-gui) 与 [HanawaBanana/workbuddy2api](https://github.com/HanawaBanana/workbuddy2api)。上游原版权和 MIT 文本保留于 `gateway/LICENSE`、`panel/LICENSE`；固定基线与选择性同步记录见 [UPSTREAM.md](UPSTREAM.md)、[同步说明](docs/UPSTREAM-SYNC-20261017.md) 和 [第三方声明](THIRD_PARTY_NOTICES.md)。
