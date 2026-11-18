# agentgo

> **README bahasa Inggris adalah dokumentasi kanonik.**
> Halaman ini adalah ringkasan lokal, bukan terjemahan lengkap.

`agentgo` mengubah artefak `AgentDeployment` menjadi layanan agen self-hosted berbasis protokol standar. Artefak ini mendefinisikan graf multi-agen dengan agen root, subagen yang didelegasikan, parameter model, dan alat MCP yang diizinkan; runtime mengorkestrasi eksekusinya dan menyajikan hasilnya melalui AG-UI, A2A, serta Agent Card.

## Poin utama

- **Deployment-first**: pemanggil tetap memiliki pengguna, data bisnis, percakapan, dan UX.
- **Protocol-native**: AG-UI untuk browser, A2A dan Agent Card untuk klien mesin, MCP untuk alat.
- **Operations-ready**: identitas Bearer / OIDC / mTLS, bukti audit, Prometheus / OpenTelemetry, dan health probe.
- **Self-contained examples**: chat browser minimal dan asisten penjelasan berbasis klik.

## Contoh

- [simple-chat](../../examples/simple-chat): chat browser AG-UI minimal.
- [agentgo-assistant](../../examples/agentgo-assistant): asisten penjelasan berbasis klik.

## Dokumentasi utama

- [README bahasa Inggris](../../README.md)
- [Arsitektur](../../docs/ARCHITECTURE.md)
- [Kebijakan keamanan](../../SECURITY.md)
