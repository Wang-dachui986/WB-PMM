# 上游兼容修复同步记录（2026-10-17）

来源：https://github.com/HanawaBanana/workbuddy2api
检查基线：af5e77947c93fd6b7f8b3815ee047d64e0b97752（MIT）。
本项目原网关基线：d1023f373065468d67791faf7e2235768af1e056。

## 已同步到 Mac 与 Windows 源码

- 04374863：客户端未提供 conversationId 时，以本轮请求 ID 补齐出站 X-Conversation-ID；保留客户端原值优先，并保持 prompt_cache_key 的会话源不变。
- b262660b：模型列表增加 modalities.input 和 supported_reasoning_levels，保留原能力字段和 cn:/global: 前缀。
- b262660b：工具结果重排支持首条结果前的 system/developer 通知，不跨 user/assistant 轮次。
- 补齐上游提交遗漏的 11148 请求级错误处理：返回 400，不轮转账号、不处罚账号，并提供修复历史提示。

## 验证

- Mac：go test ./internal/upstream ./internal/server 通过；新增会话 ID 兜底和工具错配不轮转回归通过。
- Windows 源码：相同两个 Go 包的本机模拟测试通过。
- 没有使用真实凭证、没有真实模型调用、没有消耗积分。

## 范围与未决问题

这是兼容修复同步，不是覆盖式升级整个上游。上游新增的分组密钥、预算、排程台账、任务自动化和 CI 不在本次故障修复中引入。现有桌面管理、安全约束、协议适配和 Token 统计保留。
已比较上游增量，未发现 get-user-resource 积分查询接口修复；查询 20/20 失败及实际扣费账号仍需故障响应和请求记录确认。会话头修复针对聊天请求，不代表积分查询已修复。
本次未替换正在运行的应用、未重新制作安装包、未做 Windows 实机验收。
