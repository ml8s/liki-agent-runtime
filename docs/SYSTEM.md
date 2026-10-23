# Liki 系统全况与本仓对齐

> 本文档描述 Liki 多 Agent 系统的全局架构，以及本仓（通用运行时仓）在其中
> 的角色与对齐点。让本仓负责人知道：我产什么、谁消费、边界在哪、变更如何传播。

## 一、系统全局架构

Liki 是多 Agent 命理专家系统，四仓分工：

```text
浏览器 → Caddy → liki-web (BFF)
                   └─AG-UI→ liki-agent-runtime ×4 (装配镜像: 运行时+四份工件；
                              一 app 一服务：experts-natal / divination / fengshui / naming) ──MCP──> engine-mcp / counsel-mcp
                              natal 内 bazi/ziwei 子专家以 task 委派聚合；审计留痕
```

| 仓 | 角色 | 产物 |
|---|---|---|
| liki | 内容/领域仓 | 专家内容、engine/counsel MCP、deployment 工件、装配镜像、web skill bundle |
| **liki-agent-runtime（本仓）** | 通用运行时仓 | **纯运行时镜像**、**AgentDeployment 运行时校验** |
| liki-web | 产品 BFF 仓 | liki-web 镜像（BFF + 前端） |
| liki-deploy | 发布编排仓 | 纯清单：compose + env + Caddyfile，只 pin 版本 |

## 二、本仓角色（liki-agent-runtime 运行时仓）

本仓是 **domain-neutral 多 Agent 运行时**：

- 执行外部定义的 `AgentDeployment` 工件（ADK Agent 树）
- 编排 LLM 调用与 MCP 工具
- 暴露标准协议：**A2A**（agent 间）、**AG-UI**（web↔agent）、**MCP**（agent↔工具）
- 提供安全边界（工具 allowlist、文件禁闭）、审计留痕、可观测性（OTel）

**本仓永远不 import liki 源码**；消费的唯一形式是：装配镜像（FROM 本仓镜像）+ MCP（engine/counsel 服务）。

## 三、关键对齐点（负责人必读）

### 3.1 AgentDeployment 工件（本仓运行时校验）

`AgentDeployment` 是 liki 生成的内部 deployment 工件。本仓不发布独立
schema artifact：

- 工件由本仓 `LoadAgentDeployment` 严格解码并校验
- liki 生成工件后，用本仓 pinned runtime 执行 `validate`
- 工件 digest、runtime base digest、装配镜像 digest 提供可复现 provenance

**构建顺序**：先构建 liki-agent-runtime 本地镜像，liki 再以它为 `BASE_IMAGE` 构建装配镜像。

### 3.2 装配镜像（liki 侧构建，本仓是 base）

liki 仓 `assembly/Dockerfile` 构建单套装配镜像，四份 profile 工件一起烘焙：

```dockerfile
ARG BASE_IMAGE=liki-local/liki-agent-runtime:<runtime-sha>
FROM ${BASE_IMAGE}
COPY dist/agents/ /deployment/
```

- 本仓只提供**纯运行时镜像**（`Dockerfile`，不打包任何工件）
- `LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE`/`DIGEST` 由装配镜像内置，生产启动即校验工件

### 3.3 生产环境变量（deploy 注入）

`liki-deploy/compose/docker-compose.yml` 为 liki-agent-runtime 服务注入：

| env | 说明 |
|---|---|
| `LIKI_MCP_ENGINE_URL` / `LIKI_MCP_ENGINE_TOKEN` | 工件 mcpServers `engine` 的 endpointEnv/tokenEnv |
| `LIKI_MCP_COUNSEL_URL` / `LIKI_MCP_COUNSEL_TOKEN` | 工件 mcpServers `counsel` 的 endpointEnv/tokenEnv |
| `LIKI_LLM_*`、`LIKI_RUN_*`、`LIKI_LOG_*` | 模型（`BASE_URL`/`MODEL`/`PROVIDER` 必填；`MAX_OUTPUT_TOKENS` 可选）/超时/日志 |

**命名必须与工件声明一致**（`runtime.go` 用 `os.LookupEnv(endpointEnv)` 读取），deploy 有环境一致性检查。审计使用工件版本与 digest，不单独配置工具版本。

### 3.4 协议与编排（已实现）

- **A2A**：`internal/protocol/a2a`（官方 a2a-go SDK），`/a2a` + `.well-known/agent-card.json` 标准格式
- **AG-UI**：`internal/protocol/agui`（官方 ag-ui SDK），liki-web 通过它调本仓
- **委派审计**：`internal/agent/delegation_audit.go`，ADK BeforeAgent/AfterAgent 回调记录委派（caller/target/depth/status）；
  task 子代理在 `finish_task` 成功时会被框架提前终止迭代器（AfterAgent 不触发），完成事件由运行时从合成的 task 工具 FunctionResponse 补记
- **拓扑**：统一入口语义移除——每个部署是独立单根树（`DisallowTransferToPeers: true`）；
  父代理可用 chat 平级 transfer 或 task/single_turn 的 agent-as-tool 委派；工件校验单根树/无环/全可达

## 四、变更如何传播（负责人须知）

| 本仓变更 | 影响 | 传播路径 |
|---|---|---|
| 改工件格式 | 所有 deployment 工件 | 本仓 runtime/校验先发布 → liki 重新生成并用 pinned runtime validate → 重发装配镜像 |
| 改运行时行为 | 装配镜像运行逻辑 | 发新运行时镜像 → liki 重发装配镜像（FROM 新 base） |
| 新增协议能力 | 对 web/A2A 客户端 | 同步 liki-web 消费逻辑 |

**任何运行时改动都必须**：`make check` 通过（含 `validate` 校验 dev fixture 工件）。

## 五、镜像来源

- 本仓只在本地构建镜像（`make image`），tag 由 git 推导；CI 不构建或发布镜像。
- liki 用本地 runtime 镜像作为 experts 的 `BASE_IMAGE`；liki-deploy 组装并部署同一批镜像。

## 六、相关文档

- `docs/ARCHITECTURE.md`：运行时内部架构与 AgentDeployment 工件详解
- `docs/DOMAIN.md`、`docs/STANDARDS.md`：领域与标准约定
- `liki/docs/SYSTEM.md`：内容仓视角的全况
- `liki-deploy/docs/ARCHITECTURE.md`：系统架构与发布时序

- `liki/docs/DOMAIN_NAMING.md`：canonical repository/image/service names
