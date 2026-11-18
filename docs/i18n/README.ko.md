# agentgo

> **영어 README가 표준 문서입니다.**
> 이 페이지는 한국어 개요이며 전체 번역이 아닙니다.

`agentgo`는 `AgentDeployment` 아티팩트를 자체 호스팅 및 프로토콜 네이티브 에이전트 서비스로 변환합니다. 아티팩트는 루트 에이전트, 위임된 서브에이전트, 모델 설정, 허용된 MCP 도구를 포함하는 멀티 에이전트 그래프를 정의합니다. 런타임이 실행을 오케스트레이션하고 AG-UI, A2A, Agent Card로 결과를 제공합니다.

## 핵심

- **Deployment-first**: 호출자가 사용자, 비즈니스 데이터, 대화, UX를 소유합니다.
- **Protocol-native**: 브라우저는 AG-UI, 머신 클라이언트는 A2A와 Agent Card, 도구는 MCP를 사용합니다.
- **Operations-ready**: Bearer / OIDC / mTLS 인증, 감사 증거, Prometheus / OpenTelemetry, 상태 프로브를 내장합니다.
- **Self-contained examples**: 최소 브라우저 채팅과 클릭 시 설명하는 어시스턴트.

## 예제

- [simple-chat](../../examples/simple-chat): 최소 AG-UI 브라우저 채팅.
- [agentgo-assistant](../../examples/agentgo-assistant): 클릭 시 설명하는 어시스턴트.

## 주요 문서

- [영어 README](../../README.md)
- [아키텍처](../../docs/ARCHITECTURE.md)
- [보안 정책](../../SECURITY.md)
