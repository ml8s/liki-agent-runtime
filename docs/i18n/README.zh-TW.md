# agentgo

> **英文 README 是權威文件。**
> 本頁是繁體中文概覽，並非完整翻譯。

`agentgo` 會將 `AgentDeployment` 工件轉換為自託管、協定原生的 Agent 服務。它會執行 Agent 圖，編排 LLM 呼叫與允許清單中的 MCP 工具，並透過 AG-UI、A2A 和 Agent Card 對外提供服務。

## 要點

- **Deployment-first**：呼叫方繼續擁有使用者、業務資料、會話與介面。
- **Protocol-native**：瀏覽器使用 AG-UI，機器用戶端使用 A2A 與 Agent Card，工具使用 MCP。
- **Operations-ready**：內建 Bearer / OIDC / mTLS 身分控制、稽核證據、Prometheus / OpenTelemetry 與健康探針。
- **Self-contained examples**：最小瀏覽器聊天範例與點擊說明助理範例。

## 範例

- [simple-chat](../../examples/simple-chat)：最小 AG-UI 瀏覽器聊天範例。
- [agentgo-assistant](../../examples/agentgo-assistant)：點擊說明助理範例。

## 核心文件

- [英文 README](../../README.md)
- [架構說明](../../docs/ARCHITECTURE.md)
- [安全政策](../../SECURITY.md)
