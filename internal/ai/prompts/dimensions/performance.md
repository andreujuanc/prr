### PERFORMANCE & SCALABILITY (category: "performance")

Code that may work correctly at small scale but degrade under production load — algorithmic inefficiency, memory waste, I/O bottlenecks.

#### Subcategories

**algorithmic-complexity** — Inefficient algorithms on growing data:
- O(n²) or worse in loops over collections that could grow (nested iterations over same data)
- Linear scans where a map/set lookup would suffice
- Repeated sorting of the same collection
- Recomputing expensive results that could be cached (inside loops, on every request)
- Recursive algorithms without memoization on overlapping subproblems
- String concatenation in loops instead of using a builder/buffer

**memory** — Allocation patterns that waste or leak memory:
- Unbounded slices/arrays that grow with input size without caps
- Large allocations in hot paths (allocating per-request when a pool would work)
- Missing pre-allocation when size is known (`make([]T, 0, n)` vs `make([]T, 0)`)
- Retaining references to large objects longer than needed (preventing GC)
- Loading entire files/results into memory when streaming would work
- Creating copies of large structs when a pointer would suffice

**io-blocking** — I/O on hot paths that blocks or serializes:
- Synchronous I/O in request handlers without concurrency
- Database queries inside loops (N+1 problem)
- HTTP calls without timeouts (blocking indefinitely)
- File I/O on every request when results could be cached
- Missing connection pooling (new connection per operation)
- Sequential I/O that could be parallelized (independent requests done one at a time)

**concurrency-overhead** — Thread/goroutine costs:
- Spawning a goroutine/thread per request without pooling or backpressure
- Lock contention on hot paths (coarse-grained locks where fine-grained would work)
- Channel buffer sizing that causes unnecessary blocking or memory waste
- Context switching overhead from excessive parallelism
- Mutex held across I/O operations (holding lock during network call)

**caching** — Missing, broken, or unbounded caches:
- Missing cache for expensive repeated computations (same query re-executed on every call)
- Cache without TTL or size limits (unbounded memory growth)
- Cache invalidation bugs (stale data served after mutation)
- Cache key that doesn't include all relevant inputs (different users get same cached result)
- Caching non-deterministic or time-sensitive data without awareness
