# agentgo

> **Das englische README ist die kanonische Dokumentation.**
> Diese Seite ist eine lokalisierte Übersicht und keine vollständige Übersetzung.

`agentgo` verwandelt ein `AgentDeployment`-Artefakt in einen selbst gehosteten, protokollnativen Agent-Service. Das Artifact beschreibt Multi-Agent-Graphen mit einem Root-Agent, delegierten Sub-Agents, Modelleinstellungen und allowlisted MCP-Tools; die Runtime orchestriert ihre Ausführung und stellt das Ergebnis über AG-UI, A2A und Agent Card bereit.

## Kernpunkte

- **Deployment-first**: Aufrufer behalten Nutzer, Geschäftsdaten, Konversationen und UX.
- **Protocol-native**: AG-UI für Browser, A2A und Agent Card für Maschinenclients, MCP für Tools.
- **Operations-ready**: Bearer / OIDC / mTLS-Identität, Audit-Nachweise, Prometheus / OpenTelemetry und Health Probes.
- **Self-contained examples**: minimaler Browser-Chat und Klick-Erklär-Assistent.

## Beispiele

- [simple-chat](../../examples/simple-chat): minimaler AG-UI-Browser-Chat.
- [agentgo-assistant](../../examples/agentgo-assistant): Klick-Erklär-Assistent.

## Wichtige Dokumentation

- [Englisches README](../../README.md)
- [Architektur](../../docs/ARCHITECTURE.md)
- [Sicherheitsrichtlinie](../../SECURITY.md)
