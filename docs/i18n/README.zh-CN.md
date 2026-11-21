# agentgo

> **英文 README 是权威文档。**
> 本页是中文概览，不是完整翻译。

`agentgo` 将 `AgentDeployment` 工件转换为自托管、协议原生的 Agent 服务。工件声明多 Agent 图，包括根 Agent、被委派的子 Agent、模型设置和 allowlist 中的 MCP 工具；runtime 编排它们的执行，并通过 AG-UI、A2A 和 Agent Card 对外提供服务。

根 Agent 到子 Agent 的委派使用本地 ADK 图，不使用 A2A；A2A 用于外部机器客户端和远端 Agent。

## 要点

- **Deployment-first**：调用方继续拥有用户、业务数据、会话和界面。
- **Protocol-native**：浏览器使用 AG-UI，机器客户端和远端 Agent 使用 A2A 与 Agent Card，工具使用 MCP。
- **Operations-ready**：内置 Bearer / OIDC / mTLS 身份控制、审计证据、Prometheus / OpenTelemetry 和健康探针。
- **Self-contained examples**：最小浏览器聊天示例和点击解释助手示例。

## 示例

- [chat](../../examples/chat)：最小 AG-UI 浏览器聊天示例。
- [agentgo-assistant](../../examples/agentgo-assistant)：点击解释助手示例。

## 核心文档

- [英文 README](../../README.md)
- [架构说明](../../docs/ARCHITECTURE.md)
- [安全策略](../../SECURITY.md)
