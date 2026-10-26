# Performance Program

This document is the standing plan and record for runtime performance work.
Optimizations follow one loop, and correctness outranks every number in it:

1. **Baseline** — add or reuse a metric/benchmark that shows the current cost.
2. **Invariants** — write the correctness and security invariants down before
   designing; an optimization that cannot preserve them is rejected.
3. **Contract tests** — pin behavior with tests before changing it.
4. **Implement** — smallest change that moves the bottleneck.
5. **Compare** — rerun the benchmark on the same machine; report honest
   numbers, including regressions.
6. **Document** — record the design decision where its invariants live
   (`ARCHITECTURE.md`, `STANDARDS.md`, or `DOMAIN.md`).
7. **Gate** — `make test` (race) and `make check` must pass.

## Invariants that outrank performance

- Audit evidence stays append-only, synchronous-durable on `Record` return, and
  terminal before any process run state is released.
- Every Engine tool call remains allowlisted; filtering is application
  authorization applied after standard MCP discovery.
- Stable event names and error codes never change for performance reasons.
- `internal/domain` stays free of transport, provider, adapter, and storage
  imports; new capabilities enter through ports.
- Failures fail closed: backpressure and unavailability return coded errors
  instead of blocking indefinitely or silently degrading security controls.

## Completed

| Item | Result | Evidence |
|---|---|---|
| Measurement baseline (pprof on ops listener, LLM/audit latency metrics, `make bench`) | New | `internal/transport`, `internal/observability/prometheus` |
| Readiness TTL cache + singleflight | Probes no longer trigger MCP discovery storms | `internal/transport/server.go` |
| Audit append fast paths (`PrepareStmt`, `SkipDefaultTransaction`, indexed existence probe) | Fewer allocations per append | `internal/audit/sqlite` |
| Audit single-writer recorder with grouped multi-row commits | −29% mean parallel append latency, 200ms+ SQLITE_BUSY spikes eliminated, −55% bytes/op; FIFO order, per-event conflict isolation, and synchronous durability preserved | `internal/audit/sqlite/recorder.go`, benchmarks in `event_repository_bench_test.go` |
| A2A entrypoint capture per part instead of deep clone | Allocation only | `internal/protocol/a2a/server.go` |
| MCP tool catalog memoization (60s TTL, singleflight, stale-on-error) | One `tools/list` round per server per minute instead of one per agent flow; allowlist order unchanged | `internal/agent/mcp_catalog.go`, `TestNewMCPToolsetsMemoizeDiscovery` |
| Structured-output single decode | Event-delta consumption replaces the per-run session read-back; A2A parses once per event instead of once per part and reuses the decoded document; canonical JSON bytes unchanged | `internal/agent/run_state.go`, `internal/protocol/a2a/server.go`, `TestStructuredMemoParsesOncePerEvent`, `BenchmarkParseStructuredOutput` |
| Zero-copy JSON decode input (`bytes.NewReader` instead of a `string` copy) | One full-document copy removed per structured parse | `decodeJSONDocument` |
| Shared projector index | Per-run event projection reuses the immutable deployment lookup built once at runtime construction | `internal/agent/event_projector.go` |
| Writer observability closure | `liki_agents_audit_batch_events`, `liki_agents_audit_queue_depth`, `liki_agents_run_rejections_total{reason}` verify group-commit and capacity behavior in production | `internal/audit/sqlite/recorder.go`, `internal/agent/runtime.go` |
| Recovery scan window (7 days) | Startup orphan scan stays index-bounded as the audit ledger grows; every prior startup already closed older orphans | `TestAuditEventRepositoryRecoverySkipsEventsOutsideWindow` |
| Audit secondary-index trim (8 → 3) | Insert path maintains three secondary B-trees instead of eight; sequential appends −20%, parallel grouped appends −20–30% vs the untrimmed recorder. Retained: `run_id` (idempotency), `event_type+occurred_at` (recovery), `trace_id` (incident correlation). Dropped dimensions (root run, agent, tool, tool call, definition) remain queryable via table scans; their aggregate questions are answered by Prometheus counters | `migrations/00004_trim_audit_indexes.sql`, `TestAuditEventIndexSetMatchesWritePathContract` |

## Deferred (needs cross-repo alignment)

- Multi-turn Contents and prompt-prefix caching: the code is in-repo
  (`buildUserContent`), but it changes prompt shape and needs answer-quality
  evaluation with the Liki skill repo before deployment.
- Audit retention/archival policy (ownership with `liki-deploy`).
- Run concurrency and HPA tuning after the audit writer is validated in
  production.
