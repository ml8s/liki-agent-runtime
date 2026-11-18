# agentgo

> **Le README anglais fait foi.**
> Cette page est un aperçu localisé, pas une traduction complète.

`agentgo` transforme un artefact `AgentDeployment` en service d’agents self-hosted et fondé sur des protocoles standards. L’artefact décrit des graphes multi-agents avec un agent racine, des sous-agents délégués, des paramètres de modèle et des outils MCP autorisés ; le runtime orchestre leur exécution et expose le résultat via AG-UI, A2A et Agent Card.

La délegation de l’agent racine vers les sous-agents s’exécute comme un graphe ADK local, et non via A2A. A2A est réservé aux clients machine externes et aux agents distants.

## Points clés

- **Deployment-first** : l’appelant conserve les utilisateurs, les données métier, les conversations et l’UX.
- **Protocol-native** : AG-UI pour les navigateurs ; A2A et Agent Card pour les clients machine externes et les agents distants ; MCP pour les outils.
- **Operations-ready** : identité Bearer / OIDC / mTLS, preuves d’audit, Prometheus / OpenTelemetry et sondes de santé.
- **Self-contained examples** : chat minimal dans le navigateur et assistant d’explication au clic.

## Exemples

- [simple-chat](../../examples/simple-chat): chat minimal AG-UI dans le navigateur.
- [agentgo-assistant](../../examples/agentgo-assistant): assistant d’explication au clic.

## Documentation principale

- [README en anglais](../../README.md)
- [Architecture](../../docs/ARCHITECTURE.md)
- [Politique de sécurité](../../SECURITY.md)
