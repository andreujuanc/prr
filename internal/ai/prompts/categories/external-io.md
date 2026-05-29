### EXTERNAL I/O (category: "external-io")

Code that communicates with external systems — APIs, databases, file system, network.

## Shapes or common patterns

**api-calls** — HTTP clients, retries, timeouts, idempotency:
- HTTP requests without timeout configured (hangs indefinitely)
- Missing retry logic for transient failures (or retries without backoff)
- Non-idempotent operations retried on failure (double charges, duplicate records)
- Missing idempotency keys on payment or mutation APIs
- HTTP client that doesn't check response status codes
- Response body not closed (connection leak)
- Hardcoded URLs instead of configuration
- Missing circuit breaker for unreliable services
- User-controlled URLs without allowlist (SSRF)

**database** — Query patterns, connection handling, migrations:
- N+1 query patterns (query in a loop)
- Database connections not returned to pool (leak)
- Missing transaction for multi-statement operations
- Raw SQL with string interpolation (see also input-validation/injection)
- Missing indexes for frequent query patterns (performance, but also DoS via slow queries)
- Schema migrations that lock tables for extended periods
- Connection pool exhaustion under load (no max limit or too high)

**file-system** — File operations, path handling, permissions:
- Files opened but not closed (missing defer/finally)
- Writing to world-readable locations
- Reading files without size limits (memory exhaustion on large files)
- Temporary files with predictable names (symlink attacks)
- File operations without proper error handling (partial writes)
- Missing fsync for durability-critical writes

**network** — TLS, DNS, sockets, certificates:
- TLS verification disabled (`InsecureSkipVerify`, `verify=False`)
- Certificate pinning not implemented for sensitive connections
- DNS resolution with user-controlled hostnames (DNS rebinding)
- Plaintext protocols where encryption is expected
- Socket connections without timeout
- Proxy configuration from user input
- Webhook URLs accepted without validation

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
