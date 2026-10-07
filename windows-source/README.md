# WB-PMM

> 此工作目录是 **Windows 10/11 x64 迁移副本**。可选目录 Setup.exe、Windows 使用/构建步骤见 [Windows 说明](windows/README.md)，功能对照与统一实机验收见 [迁移清单](windows/MIGRATION.md)。当前开发检查通过，Windows 实机尚未验收。下面保留 macOS 1.0.3 基线说明作为功能参考，原始 Mac 发布文件已冻结。

WB-PMM 是运行在 Mac 本机的 WorkBuddy 账号与 API 网关控制台。它把 Go 网关和管理面板放进一个 macOS App，通过 SwiftUI 窗口和内置网页面板管理账号、模型接入、服务状态与日志。应用界面为简体中文，安装包运行时无需另装 Go、Node.js、Docker 或 Redis。

本项目是社区开源软件，**不是腾讯 CodeBuddy 或 WorkBuddy Global 的官方产品**。使用者需要自行持有并有权使用对应区域的账号；账号授权、模型可用性、积分和服务条款由上游决定。开源软件本身不附带免费模型额度。

## 下载与安装

- 系统要求：macOS 14 或更新版本、Apple Silicon（M 系列）Mac。当前安装包内的 Go 组件仅为 arm64，Intel Mac 不在支持范围内。
- 取得 `WB-PMM-1.0.3-macos-arm64.pkg` 后双击，按系统安装器提示安装。安装器会正常退出旧版，固定替换 `/Applications/WB-PMM.app`，并把当前登录用户 Dock 中固定或最近使用的 WB-PMM 图标改指向此位置。完成后从“应用程序”或 Dock 打开。具有仓库访问权限的用户也可在 [发布页](https://github.com/Wang-dachui986/WB-PMM/releases) 查看已发布安装包。
- 1.0.3 的 App 使用临时签名；PKG 尚无 Developer ID Installer 签名，两者均未公证。若 macOS 阻止首次打开，请先确认下载来源与发布文件，再按 [Apple 的“隐私与安全性”操作说明](https://support.apple.com/zh-cn/102445) 对这个 App 手动选择“仍要打开”。

完整步骤见 [使用指南](docs/USER_GUIDE.md)。

## 能做什么

- 首次使用时提供跟随操作位置的悬浮引导，可跳过，也可在仪表盘右上角重播；启停或重启 API 后自动刷新网关状态。
- 在仪表盘查询全部账号积分并统一刷新；支持 100 个账号，桌面批量维护每批最多 20 个，并显示查询进度和失败结果。
- 通过浏览器完成国内版或国际版账号的 OAuth 设备授权，在本机保存账号凭证；查看账号状态、积分，并按需重新授权。
- 在“网关配置”中设置本机端口、超时、代理、并发和会话保持；在“API 与模型”中复制地址与本机 API Key，查看当前返回的模型 ID、上下文长度、最大输出和推理强度元数据；在“请求统计”查看本次网关运行的数据。
- 可在“网关配置”开启局域网访问，让同一可信 Wi-Fi / 有线网络的其他设备使用本机账号池。默认关闭；开启后在仪表盘或“API 与模型”复制局域网地址，配合相同的 API Key 使用。管理界面和账号信息仍仅本机可访问。
- 提供 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 的**常用兼容子集**。支持文本、流式输出和函数工具往返；图片能力取决于实际模型。客户端须携带完整对话历史，不提供 Responses 的服务端会话存储、后台任务或平台内置工具。不能保证任意 Agent 的专有扩展可用。
- 在“聊天测试”手动选择模型及推理强度，设置可选的 `temperature`、最大输出 Token 和流式输出；发出测试请求会实际调用上游，可能消耗额度。
- 在“网关配置 → 自动签到领积分”设置签到开关与本机时区的整点时间。仅在 API 服务运行期间对有有效凭证、未禁用的国内账号执行；国际账号跳过，奖励以实际上游结果为准。
- 关闭窗口后保留菜单栏运行；菜单栏可重新打开窗口、启停 API 或完全退出。在“应用设置”中可分别开启“手动打开应用时启动 API”和“登录 macOS 时自动打开应用并启动 API”。

## 本机接入

默认端口为 `7863`。更改端口后请在“API 与模型”复制当前地址；以下为默认值：

| 协议 | 完整请求地址 | 自动拼接路径的 SDK Base URL |
| --- | --- | --- |
| OpenAI Chat Completions | `http://127.0.0.1:7863/v1/chat/completions` | `http://127.0.0.1:7863/v1` |
| OpenAI Responses | `http://127.0.0.1:7863/v1/responses` | `http://127.0.0.1:7863/v1` |
| Anthropic Messages | `http://127.0.0.1:7863/v1/messages` | `http://127.0.0.1:7863` |

三种协议共用应用生成的本机 API Key。OpenAI 接口使用 `Authorization: Bearer <Key>`；Messages 接口可使用 `x-api-key: <Key>` 或同一 Bearer Key。模型 ID 请从“API 与模型”复制完整值，包括可能的 `global:` 前缀。详细填写方式和常见错误见 [使用指南](docs/USER_GUIDE.md)。

## 数据与更新

账号凭证和配置在 `~/Library/Application Support/WorkBuddy2APIDesktop/`，日志在 `~/Library/Logs/WorkBuddy2APIDesktop/`。更新时运行新版 PKG 安装包；安装器固定替换“应用程序”中的 App，不删除上述数据。安装前先结束进行中的请求；旧版无法正常退出时安装会中止。安装器不自动添加未固定的 Dock 图标，也不删除其他位置的旧副本；更新后请弹出旧 DMG，避免再次从旧安装盘启动。卸载和备份步骤见 [使用指南](docs/USER_GUIDE.md)。

## 源码与许可

源码构建见 [构建指南](docs/BUILD.md)。项目保留所复用的 [workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) 和 [workbuddy2api-gui](https://github.com/linbeize/workbuddy2api-gui) 的 MIT 许可原文；详情见 [第三方声明](THIRD_PARTY_NOTICES.md) 与各组件的 `LICENSE`。本项目许可见 [LICENSE](LICENSE)。
