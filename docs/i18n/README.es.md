# agentgo

> **El README en inglés es la documentación canónica.**
> Esta página es un resumen localizado, no una traducción completa.

`agentgo` convierte un artefacto `AgentDeployment` en un servicio de agentes self-hosted y basado en protocolos estándar. Ejecuta grafos de agentes, organiza llamadas a LLM y herramientas MCP, y expone el resultado mediante AG-UI, A2A y Agent Card.

## Puntos clave

- **Deployment-first**: quien invoca conserva usuarios, datos de negocio, conversaciones y UX.
- **Protocol-native**: AG-UI para navegadores; A2A y Agent Card para clientes de máquina; MCP para herramientas.
- **Operations-ready**: identidad Bearer / OIDC / mTLS, evidencia de auditoría, Prometheus / OpenTelemetry y sondeos de salud.
- **Self-contained examples**: chat mínimo en el navegador y asistente de explicación al hacer clic.

## Ejemplos

- [simple-chat](../../examples/simple-chat): chat mínimo de navegador con AG-UI.
- [agentgo-assistant](../../examples/agentgo-assistant): asistente de explicación al hacer clic.

## Documentación principal

- [README en inglés](../../README.md)
- [Arquitectura](../../docs/ARCHITECTURE.md)
- [Política de seguridad](../../SECURITY.md)
