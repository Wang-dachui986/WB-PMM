# JSON凭证导入修复（2026-10-28）

原生区域校验漏掉 codebuddy.cn / www.codebuddy.cn，而 Go 后台、账号列表已支持这两个国内域名。新文件解析或已有账号覆盖预览都会因此拒绝合法凭证。Mac 与 Windows 的原生校验现已对齐后台；未知域名与跨区域覆盖仍拒绝。

UTF-8 BOM 文件头现于导入边界移除，Windows 客户端发送 raw_json 前规范化 UTF-8；Mac 原生读取同样规范化；Go 凭证解析兼容 UTF-8 BOM。单账号的扁平/嵌套形均支持。没有放宽文件大小、UID、token字段类型或原生路径检查；UTF-16不视为UTF-8文件。

验证使用合成凭证：Mac 原生读文件、BOM和域名、预览、同区域覆盖与跨区域拒绝；C# CoreContracts覆盖域名和严格UTF-8，另通过真实 C# PanelClient → Go 本机面板验证 BOM 导入落盘、同 UID 显式覆盖和跨区域原文件保护。没有读取、改写或测试用户实际登录凭证，没有上游模型调用。

本次Mac安装包从 WB-PMM-Windows/macos 与同目录 gateway/panel 构建，沿用 1.0.10 的模型强刷与429保护；不能从旧 workbuddy2api-desktop 项目版本覆盖最新Mac功能。旧树仅同步本次兼容补丁。

截图未含实际错误码，修复的是已复现的兼容缺陷；Windows文件选择器与文件系统P/Invoke仍需Windows实机复测，不能将跨平台合同测试称为Windows完整实机验收。
