# Windows 接续验收

当前源代码已完成 Windows 迁移开发检查，待按用户要求统一到 Windows 10/11 x64 验收。不要把交叉编译结果写成实机 PASS。

## 接手约束

- 不调用 CRP；用户要求不向 Git 写入，不提交、推送或更改 Git 状态文件。
- macOS 1.0.3 已冻结；只改本 Windows 副本。
- 子智能体使用 `gpt-6-sol`、`ultra`。
- 不读真实凭证内容、不删除用户账号、不自动发起收费模型调用。先合成数据验证。

## 初始候选历史产物

`dist/WB-PMM-Setup-1.0.3-x64-compile-check.exe`：41.36 MiB。

SHA-256：`e9e4ab6e23b5b248ae13a3c01d2b2963b3a62ed5eaf5384ba37de6614663f642`。

1.0.4 新增目录选择，默认 `%LOCALAPPDATA%\Programs\WB-PMM`；用户数据 `%LOCALAPPDATA%\WB-PMM`，升级卸载保留。微软 WebView2 引导程序已从官方 HTTPS 下载，但 Windows Authenticode 尚未验证；WB-PMM 应用与安装包尚无 Windows 代码签名。

## 从这里继续

1. 校验 Setup SHA-256。运行安装前检查包内微软 WebView2 引导程序签名，或在 Windows 执行 `package.ps1`，该脚本会先验证微软签名。
2. 开发机安装 .NET 10 SDK、Go 和 Node 后，运行 `verify.ps1`；Foundation 在 Windows 会额外执行 COM/ACL/Job Object/EOF 退出测试，Mac 已执行 18 条离线检查。CoreContracts 默认 14 条检查，附加 `--panel <本机 wbgui 路径>` 可执行实际本地面板协议检查，共 20 条。
3. 按 `MIGRATION.md` 两列逐项在 Windows 10/11 记录结果。目标是实际安装、生命周期、账号、交易、LAN、三协议与升级/卸载完整验收。
4. 缺陷在本副本修复，再 `build.ps1` 和 `package.ps1` 重建。只针对实际变更风险补验证。
5. 用户先前的真实模型测试额度已经用完；若需要新的真实调用，先与用户确定模型、次数、Token 和积分范围。

## 关键文件

- `WBPMM/Program.cs`：当前用户跨会话单实例、私有命名管道、安装器正常退出请求。
- `WBPMM/MainForm.cs` / `NativeUtilities.cs`：WebView2、23 命令桥接、托盘、开机自启、导入/导出、备注与主题。
- `WBPMM/DesktopController.cs` / `PanelClient.cs` / `GatewayConfiguration.cs`：配置事务、账户维护、进程状态、系统浏览器授权。
- `WBPMM/Platform.cs` / `ManagedHelper.cs`：私有 ACL、同句柄安全读取、原子写、Job Object、READY 校验与 EOF 退出。
- `installer/`：NSIS 可选目录及原路径覆盖安装、正常退出、精确文件清单卸载、快捷方式身份。
- 两个 Go 模块的 Windows private ACL 文件：本机交叉编译与定向回归已过，实际 Windows 行为须运行验证。

开发证据：`dist/build-report.json`。原生界面、文件对话框、睡眠唤醒、防火墙、RDP 多会话、安装升级卸载目前均未在 Windows 运行过。

## 1.0.4 安装修复续验

旧 1.0.3 包已在用户 Windows 电脑出现快捷方式身份步骤失败。1.0.4 使用应用本身写入/读回 AppID，检查创建快捷方式的错误；不再运行 SetShortcutAppId.ps1。首次安装增加 MUI_DIRECTORY 页面，升级记录路径，卸载不再硬编码默认目录。上方 1.0.3 哈希仅历史记录，最新产物与哈希见 dist/SHA256SUMS。

## 1.0.5 账号授权续验

用户日志显示面板在添加账号期间反复正常退出（exit 0）。本机已复现：原 C# `DateTimeOffset.ToString("O")` 的 `+00:00` 在默认 JSON 中变成反斜杠 u002B，而 Go `time.Time` 不反转义该字符串，拒绝 begin；后续 end 因无许可被拒绝，触发面板保护性退出。改用 `UtcDateTime.ToString("O")` 后，同一个实际 C#→Go 检查通过。API 原本停止时的维护结束状态同步修复。

重验步骤：覆盖安装 1.0.5，在 API 停止和运行两种状态下添加账号，选择“等待完成”；应进入授权步骤并自动打开浏览器。取消应结束维护，成功授权应能保存；原本停止的 API 维持停止，原本运行的恢复运行。浏览器启动失败时，可在原会话点击“打开授权页面”重试。未知的真实上游登录结果不得标为 PASS。最新安装包与 SHA 见 `dist/build-report.json` 和 `dist/SHA256SUMS`。

## 1.0.6 桌面快捷方式续验

用户反馈 1.0.5 其它功能正常，要求安装后自动生成桌面快捷方式。NSIS 使用当前用户 `$DESKTOP` 创建同名链接，升级更新、卸载删除；原生标识注册同时处理开始菜单与 `DesktopDirectory`，共用 AppID。实机复测需确认安装完成即可看到桌面图标、双击启动、覆盖升级后仍指向原安装目录、卸载移除两处快捷方式。macOS 保持冻结。

## 1.0.7 首次安装目录续验

用户 Windows 11 的默认 `%LOCALAPPDATA%\Programs\WB-PMM` 路径被 1.0.6 拒绝。NSIS `GetFullPathName` 在目标不存在时可能返回空字符串，后续路径长度检查随即拒绝。1.0.7 三处规范化统一调用 Win32 `GetFullPathNameW`，检查返回长度，成功后才写回路径；其余目录保护保持不变。

回归源 `tests/InstallerPath/Check.nsi` 和完整安装包均需编译通过；Windows 上运行 `verify.ps1` 后，再验证首次默认安装、新建中文目录安装、原位覆盖升级、桌面快捷方式，以及磁盘根目录/系统目录/数据目录仍被拒绝。当前 macOS 只能交叉编译，Windows 实机结果待回传。
