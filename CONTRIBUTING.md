# Contributing

Thanks for improving `liki-agent-runtime`.

## Development

`main` is the only long-lived branch. Do not use `dev`, `master`, or long-lived
release branches. Keep changes small and commit them to tested `main`; release
tags are created only from those commits.

1. Install Go 1.26+, Node.js 24.14.1 (see `.nvmrc`), Docker, and golangci-lint 2.x.
2. Run `npm ci`.
3. Start from a focused, small change.
4. Run the full gate before review:

```bash
make gate
make build
```

`make gate` runs documentation checks, `gofmt` verification, `golangci-lint`,
`go vet`, artifact validation, race-enabled Go tests, and the coverage floor.
It does not call a model provider or require Engine MCP.

## Design rules

- Keep the runtime domain-neutral.
- Put Agent behavior in external deployment artifacts.
- Add dependencies only when they replace meaningful complexity.
- Use official protocol SDKs and standard library APIs first.
- Keep `internal/domain` free of transport, provider, and storage imports.
- Allowlist every MCP tool explicitly.
- Never log secrets, prompts, raw model output, or tool payloads.

## Documentation

Update `README.md` only when installation, usage, behavior, or the public
surface changes. Keep architecture invariants in `docs/ARCHITECTURE.md` and
stable runtime domain rules in `docs/DOMAIN.md`.
