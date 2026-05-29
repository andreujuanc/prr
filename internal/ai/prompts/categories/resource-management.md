### RESOURCE MANAGEMENT (category: "resource-management")

Code that acquires, holds, or releases system resources.

## Shapes or common patterns

**memory-leaks** — Unbounded caches, retained references, accumulation:
- Caches without size limits or eviction policy (grows forever)
- Event listeners registered but never removed
- Closures that capture large objects and prevent GC
- Append-only data structures without cleanup
- Global maps that accumulate entries without pruning
- Circular references that prevent garbage collection
- Large buffers allocated and never released back to pool

**connection-pools** — Exhaustion, missing returns, configuration:
- Database connections not returned to pool on error paths
- HTTP client connections not closed after use
- Pool max size not configured (defaults to unlimited or too high)
- Connection health checks missing (stale connections served)
- Pool exhaustion under load with no backpressure or timeout
- Connections held across long operations (blocking others)

**file-handles** — Unclosed files, missing defer/finally:
- File opened but never closed (descriptor leak)
- Missing `defer file.Close()` or equivalent
- File descriptors exhaustion under load
- Socket/pipe handles not closed on error paths
- Temporary files created but not cleaned up

**unbounded-growth** — Queues, buffers, log accumulation:
- In-memory queues without size limits (producer faster than consumer)
- Log files without rotation or size limits
- Buffered channels without consumers (goroutine blocks, memory grows)
- Request/response bodies read fully into memory without size limit
- History/audit logs that accumulate without archival
- Retry queues that grow during outages without backpressure

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
