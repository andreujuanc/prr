### OBSERVABILITY (category: "observability")

Code that emits logs, metrics, traces, or alerts — whether operators can understand what happened in production.

#### Subcategories

**logging-quality** — Log level, structure, content:
- Log level mismatched to severity (errors logged at INFO, debug noise at WARN)
- Unstructured `fmt.Sprintf`/string-concat log lines where structured fields would let operators filter (`log.WithFields`, slog attrs, JSON keys)
- Important events with no log at all (silent retries, fallback paths, circuit-breaker trips)
- Excessively verbose logging on hot paths (per-request DEBUG that floods prod)
- Log messages that paste raw user input or huge payloads without truncation
- Stack traces logged at INFO instead of ERROR (or vice versa — errors without stack traces)
- Inconsistent log keys across the codebase (`user_id` here, `userId` there, `uid` elsewhere)

**sensitive-data-in-logs** — PII, secrets, credentials in logs:
- Tokens, passwords, API keys, session IDs, or auth headers logged
- PII (email, phone, full name, SSN, credit card) logged without redaction
- Request bodies logged in their entirety on auth/payment endpoints
- Error messages echoing back user-supplied values that may contain secrets
- Note: this overlaps with `configuration/secrets-exposure`. Flag here when the issue is specifically a logging path; flag there when it's hardcoded in source.

**tracing-correlation** — Request correlation, span propagation:
- Long operations or async work with no span / trace ID emitted
- Correlation/request IDs not propagated across goroutines, async tasks, or downstream calls
- Trace context lost at boundaries (HTTP → message queue, sync → async)
- Logs in a request handler that don't include the request/trace ID, making them un-correlatable

**metrics-and-alerting** — What gets measured, what triggers alerts:
- Critical paths (auth, payment, mutation endpoints) without latency/error metrics
- Counters that aren't tagged for cardinality control (status codes, route patterns) — risk of metric explosion
- Errors that should page on-call but only log (no alert hook, no metric increment)
- Health check that returns "OK" without verifying downstream dependencies
- Metric naming inconsistent with the project's existing convention (`requests_total` vs `http_requests` vs `http.requests`)

**volume-and-cost** — Log/metric volume, sampling:
- Per-request log lines that scale linearly with traffic (no sampling)
- Stack traces or large payloads logged on every error in a hot path
- High-cardinality labels (user IDs, full URLs with params) on metrics
- Audit logs that accumulate without rotation or archival (cross-ref `resource-management/unbounded-growth`)
