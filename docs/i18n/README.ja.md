# agentgo

> **英語版 README が正本です。**
> このページは日本語の概要であり、全文翻訳ではありません。

`agentgo` は `AgentDeployment` アーティファクトを、セルフホスト型でプロトコルネイティブなエージェントサービスに変換します。アーティファクトはルートエージェント、委譲されたサブエージェント、モデル設定、許可された MCP ツールからなるマルチエージェントグラフを記述します。ランタイムがその実行をオーケストレーションし、AG-UI、A2A、Agent Card 経由で提供します。

ルートエージェントからサブエージェントへの委譲はローカルな ADK グラフで実行され、A2A を使用しません。A2A は外部マシンクライアントとリモートエージェントに使用します。

## ポイント

- **Deployment-first**: 呼び出し側がユーザー、業務データ、セッション、UI を保持します。
- **Protocol-native**: ブラウザには AG-UI、マシンクライアントとリモートエージェントには A2A と Agent Card、ツールには MCP を使用します。
- **Operations-ready**: Bearer / OIDC / mTLS 認証、監査証跡、Prometheus / OpenTelemetry、ヘルスプローブを内蔵します。
- **Self-contained examples**: 最小構成のブラウザチャットと、クリックで解説するアシスタント。

## サンプル

- [chat](../../examples/chat): 最小構成の AG-UI ブラウザチャット。
- [agentgo-assistant](../../examples/agentgo-assistant): クリックで解説するアシスタント。

## 主要ドキュメント

- [英語版 README](../../README.md)
- [アーキテクチャ](../../docs/ARCHITECTURE.md)
- [セキュリティポリシー](../../SECURITY.md)
