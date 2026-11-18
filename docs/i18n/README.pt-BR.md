# agentgo

> **O README em inglês é a documentação canônica.**
> Esta página é um resumo localizado, não uma tradução completa.

`agentgo` transforma um artefato `AgentDeployment` em um serviço de agentes self-hosted e baseado em protocolos padrão. Ele executa grafos de agentes, orquestra chamadas de LLM e ferramentas MCP e expõe o resultado via AG-UI, A2A e Agent Card.

## Pontos principais

- **Deployment-first**: quem chama mantém usuários, dados de negócios, conversas e UX.
- **Protocol-native**: AG-UI para navegadores; A2A e Agent Card para clientes de máquina; MCP para ferramentas.
- **Operations-ready**: identidade Bearer / OIDC / mTLS, evidências de auditoria, Prometheus / OpenTelemetry e sondas de saúde.
- **Self-contained examples**: chat mínimo no navegador e assistente de explicação por clique.

## Exemplos

- [simple-chat](../../examples/simple-chat): chat mínimo de navegador com AG-UI.
- [agentgo-assistant](../../examples/agentgo-assistant): assistente de explicação por clique.

## Documentação principal

- [README em inglês](../../README.md)
- [Arquitetura](../../docs/ARCHITECTURE.md)
- [Política de segurança](../../SECURITY.md)
