# agentgo

> **O README em inglês é a documentação canônica.**
> Esta página é um resumo localizado, não uma tradução completa.

`agentgo` transforma um artefato `AgentDeployment` em um serviço de agentes self-hosted e baseado em protocolos padrão. O artefato descreve grafos multiagente com um agente raiz, subagentes delegados, parâmetros de modelo e ferramentas MCP autorizadas; o runtime orquestra a execução e expõe o resultado via AG-UI, A2A e Agent Card.

A delegação do agente raiz para subagentes é executada como um grafo ADK local, não como A2A. A2A é reservado para clientes de máquina externos e agentes remotos.

## Pontos principais

- **Deployment-first**: quem chama mantém usuários, dados de negócios, conversas e UX.
- **Protocol-native**: AG-UI para navegadores; A2A e Agent Card para clientes de máquina externos e agentes remotos; MCP para ferramentas.
- **Operations-ready**: identidade Bearer / OIDC / mTLS, evidências de auditoria, Prometheus / OpenTelemetry e sondas de saúde.
- **Self-contained examples**: chat mínimo no navegador e assistente de explicação por clique.

## Exemplos

- [simple-chat](../../examples/simple-chat): chat mínimo de navegador com AG-UI.
- [agentgo-assistant](../../examples/agentgo-assistant): assistente de explicação por clique.

## Documentação principal

- [README em inglês](../../README.md)
- [Arquitetura](../../docs/ARCHITECTURE.md)
- [Política de segurança](../../SECURITY.md)
