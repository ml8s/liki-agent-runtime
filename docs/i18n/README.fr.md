# agentgo

> **Le README anglais fait foi.**
> Cette page est un aperçu localisé, pas une traduction complète.

`agentgo` transforme un artefact `AgentDeployment` en service d’agents self-hosted et fondé sur des protocoles standards. Il exécute des graphes d’agents, orchestre les appels de LLM et les outils MCP, puis expose le résultat via AG-UI, A2A et Agent Card.

## Points clés

- **Deployment-first** : l’appelant conserve les utilisateurs, les données métier, les conversations et l’UX.
- **Protocol-native** : AG-UI pour les navigateurs ; A2A et Agent Card pour les clients machine ; MCP pour les outils.
- **Operations-ready** : identité Bearer / OIDC / mTLS, preuves d’audit, Prometheus / OpenTelemetry et sondes de santé.
- **Self-contained examples** : chat minimal dans le navigateur et assistant d’explication au clic.

## Exemples

- [simple-chat](../../examples/simple-chat): chat minimal AG-UI dans le navigateur.
- [agentgo-assistant](../../examples/agentgo-assistant): assistant d’explication au clic.

## Documentation principale

- [README en anglais](../../README.md)
- [Architecture](../../docs/ARCHITECTURE.md)
- [Politique de sécurité](../../SECURITY.md)
