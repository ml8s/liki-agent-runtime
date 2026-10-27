# Performance Program

This document is the standing plan and record for runtime performance work.
Optimizations follow one loop, and correctness and maintainability outrank
every number in it:

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
- Every MCP tool call remains allowlisted; filtering is application
  authorization applied after standard MCP discovery.
- Stable event names and error codes never change for performance reasons.
- `internal/domain` stays free of transport, provider, adapter, and storage
  imports; new capabilities enter through ports.
- Failures fail closed: backpressure and unavailability return coded errors
  instead of blocking indefinitely or silently degrading security controls.

## Maintainability gate

- A critical write path keeps one canonical implementation unless a grouped or
  batched path has a durable, measured win and preserves per-item outcome
  semantics.
- If an optimization requires a special fast path plus a semantic fallback for
  conflicts, retries, or provider behavior, reject it unless the win is large
  enough to justify maintaining both paths.

## Completed

| Item | Result | Evidence |
|---|---|---|
| Measurement baseline (pprof on ops listener, LLM/audit latency metrics, `make bench`) | New | `internal/transport`, `internal/observability/prometheus` |
| Readiness TTL cache + singleflight | Probes no longer trigger MCP discovery storms | `internal/transport/server.go` |
| Audit append fast paths (`PrepareStmt`, `SkipDefaultTransaction`, indexed existence probe) | Fewer allocations per append | `internal/audit/sqlite` |
| Audit single-writer recorder with grouped multi-row commits | −29% mean parallel append latency, 200ms+ SQLITE_BUSY spikes eliminated, −55% bytes/op; FIFO order, per-event conflict isolation, and synchronous durability preserved | `internal/audit/sqlite/recorder.go`, benchmarks in `event_repository_bench_test.go` |
| A2A entrypoint capture per part instead of deep clone | Allocation only | `internal/protocol/a2a/server.go` |
| MCP tool catalog memoization (60s TTL, singleflight, stale-on-error) | One `tools/list` round per server per minute instead of one per agent flow; allowlist order unchanged | `internal/agent/mcp_catalog.go`, `TestNewMCPToolsetsMemoizeDiscovery` |
| Structured-output single decode | Event-delta consumption replaces the per-run session read-back; A2A parses once per event instead of once per part and reuses the decoded document; canonical JSON bytes unchanged | `internal/execution/structured_output.go`, `internal/protocol/a2a/server.go`, `TestStructuredMemoParsesOncePerEvent`, `BenchmarkParseStructuredOutput` |
| Zero-copy JSON decode input (`bytes.NewReader` instead of a `string` copy) | One full-document copy removed per structured parse | `decodeJSONDocument` |
| Shared projector index | Per-run event projection reuses the immutable deployment lookup built once at runtime construction | `internal/execution/projector.go` |
| Writer observability closure | `agent_runtime_audit_batch_events`, `agent_runtime_audit_queue_depth`, `agent_runtime_run_rejections_total{reason}` verify group-commit and capacity behavior in production | `internal/audit/sqlite/recorder.go`, `internal/agent/runtime.go` |
| Recovery scan window (7 days) | Startup orphan scan stays index-bounded as the audit ledger grows; every prior startup already closed older orphans | `TestAuditEventRepositoryRecoverySkipsEventsOutsideWindow` |
| Audit secondary-index trim (8 → 3) | Insert path maintains three secondary B-trees instead of eight; sequential appends −20%, parallel grouped appends −20–30% vs the untrimmed recorder. Retained: `run_id` (idempotency), `event_type+occurred_at` (recovery), `trace_id` (incident correlation). Dropped dimensions (root run, agent, tool, tool call, definition) remain queryable via table scans; their aggregate questions are answered by Prometheus counters | `migrations/00004_trim_audit_indexes.sql`, `TestAuditEventIndexSetMatchesWritePathContract` |
| Native multi-turn Contents | Caller history is seeded as run-scoped session events instead of a JSON array inside one user message: chronological user/model turns, latest message last, no JSON framing overhead, and a stable prefix every later turn can reuse through provider prefix caching. Wire contracts unchanged; the session stays run-scoped and caller-owned history stays authoritative | `internal/agent/history_seed.go`, `TestRunSeedsHistoryAsNativeMultiTurnContents` |
| Runtime load benchmark (`BenchmarkRuntimeRun`) | End-to-end per-run measurement at concurrency 1/8/32 with seeded 20-message history, an MCP tool call, and an in-memory audit recorder; SQLite append cost is measured separately by the audit benchmarks — the evidence base for the allocation findings below | `internal/agent/runtime_bench_test.go`, `internal/audit/sqlite/event_repository_bench_test.go` |
| Event projector explicit pure state | Count visible parts before constructing a replacement, and return a new partial-text state instead of mutating the previous one. A fully visible final event remains ~25–27ns/0 allocs. A fully visible partial delta (including projector construction) is ~690ns/2 allocs on the benchmark host; the allocation is the accepted cost of non-mutating state. Visibility, partial-duplicate suppression, tool facts, and state-only events are unchanged | `internal/execution/projector.go`, `BenchmarkEventProjectorFinalText`, `BenchmarkEventProjectorPartialText` |
| Load-profile findings (measured, c=32) | Runtime framework overhead ≈ 0.2–1.2ms per run. Dominant remaining costs are external-library internals: the MCP SDK's JSON codec (~67% of allocations, scales with tool-result volume), ADK's per-call content clone (13.6% CPU, scales with thread length), and GC (~50% CPU samples at full concurrency). Own-code paths (seeding, audit event build, projection) are single-digit percentages | `go test -bench BenchmarkRuntimeRun -cpuprofile/-memprofile` |
| Outbound TLS session cache + fan-out-sized idle pool | Parallel tool calls multiply outbound fan-out beyond the old `2×concurrency` pool, forcing TCP+TLS handshakes per burst; the pool is now sized for fan-out (`8×concurrency`, capped) and TLS session tickets are cached so fresh connections resume in fewer round trips | `newOutboundTransport` |
| MCP catalog startup warmup | The first run after a restart no longer pays lazy cold start (session initialize + `tools/list` per server); warmup is best-effort, bounded, and failures fall through to the on-demand stale/error path | `internal/agent/warmup.go` |
| Parallel single-turn delegation verified & pinned | ADK runs sibling single-turn calls concurrently via the standard tool pipeline (distinct sub-branches per invocation), so coordinator fan-out to sub-experts costs the slowest branch instead of the sum; task mode stays sequential by framework design. Delegation audit stays per-invocation correct under concurrency | `TestRuntimeRunsParallelSingleTurnDelegation` |

## Deferred (needs cross-repo alignment)

- Audit retention/archival policy (ownership with `deployment system`).
- Run concurrency and HPA tuning after the audit writer is validated in
  production.
- GC tuning under CPU limits: at full concurrency GC consumes ~50% of CPU
  samples but does not bound wall time when cores are spare (measured:
  `GOGC=200` neutral here). Under container CPU limits, `GOGC`/`GOMEMLIMIT`
  (standard Go env, deploy-owned) are expected to return most of that headroom.
- MCP tool-result slimming: the SDK's JSON decode allocations scale with tool
  result volume; the lever is server-side payload size (external MCP tool services repos),
  not the runtime.
