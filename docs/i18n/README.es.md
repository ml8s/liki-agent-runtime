# agentgo

> **El README en inglés es la documentación canónica.**
> Esta página es un resumen localizado, no una traducción completa.

`agentgo` convierte un artefacto `AgentDeployment` en un servicio de agentes self-hosted y basado en protocolos estándar. El artefacto describe grafos multiagente con un agente raíz, subagentes delegados, parámetros de modelo y herramientas MCP autorizadas; el runtime organiza su ejecución y expone el resultado mediante AG-UI, A2A y Agent Card.

La delegación del agente raíz a los subagentes se ejecuta como un grafo ADK local, no como A2A. A2A está reservado para clientes externos y agentes remotos.

## Puntos clave

- **Deployment-first**: quien invoca conserva usuarios, datos de negocio, conversaciones y UX.
- **Protocol-native**: AG-UI para navegadores; A2A y Agent Card para clientes máquina y agentes remotos; MCP para herramientas.
- **Operations-ready**: identidad Bearer / OIDC / mTLS, evidencia de auditoría, Prometheus / OpenTelemetry y sondeos de salud.
- **Self-contained examples**: chat mínimo en el navegador y asistente de explicación al hacer clic.

## Ejemplos

- [chat](../../examples/chat): chat mínimo de navegador con AG-UI.
- [agentgo-assistant](../../examples/agentgo-assistant): asistente de explicación al hacer clic.

## Documentación principal

- [README en inglés](../../README.md)
- [Arquitectura](../../docs/ARCHITECTURE.md)
- [Política de seguridad](../../SECURITY.md)
