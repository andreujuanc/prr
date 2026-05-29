### ERROR HANDLING (category: "error-handling")

Code that handles, propagates, or recovers from errors.

## Shapes or common patterns

**swallowed-errors** — Ignored returns, empty catch blocks:
- Error return values assigned to `_` or explicitly ignored when the error matters (write paths, business operations, anything with side effects)
- Empty `catch` blocks or `catch` that only logs without re-throwing/returning
- Error channels that are never read
- Deferred Close() on **write** paths whose error is dropped (`defer w.Close()` on a writer can hide flush failures — use a named return + closure, or check explicitly). Note: ignoring `defer f.Close()` on **read-only** files is idiomatic in Go and not a bug.
- Functions that return errors but callers don't check them
- `// nolint` or `// eslint-disable` on error checks without justification

**error-propagation** — Missing context, lost stack traces:
- `return err` without wrapping (loses call chain context)
- Error wrapping that doesn't include relevant variables (file paths, IDs, operation names)
- Errors converted to strings and re-parsed instead of using error types
- Sentinel errors compared by string value instead of `errors.Is()`
- Error types that don't implement `Unwrap()` for chain inspection
- Generic error messages ("operation failed", "internal error") that don't help diagnosis

**partial-failure** — Cleanup, rollback, resource leaks on error paths:
- Resources acquired but not released on error paths (files, connections, locks)
- Missing `defer` for cleanup (Go), missing `finally` (Java/JS/Python)
- Multi-step operations where failure at step N leaves steps 1..N-1 uncommitted
- Goroutines/threads spawned but not cleaned up on error
- Network connections not closed on error (connection pool exhaustion)
- Temporary files or directories not cleaned up on error

**panic-safety** — Unrecovered panics, crash paths:
- Panics in goroutines without recovery (crashes entire process)
- Nil pointer dereferences on error paths (double-fault)
- Index out of bounds on untrusted input lengths
- Type assertions without comma-ok pattern (`x.(T)` instead of `x, ok := x.(T)`)
- Stack overflow from unbounded recursion
- HTTP handlers that can panic (unrecovered = connection reset)

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
