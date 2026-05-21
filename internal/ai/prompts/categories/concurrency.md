### CONCURRENCY (category: "concurrency")

Code that uses goroutines, threads, async operations, or shared mutable state.

#### Subcategories

**race-conditions** — Shared state without synchronization:
- Read-modify-write on shared variables without locks or atomics
- Map access from multiple goroutines without sync.Mutex or sync.Map
- Struct fields accessed concurrently without synchronization
- Check-then-act patterns without holding a lock through both steps
- Lazy initialization without sync.Once or equivalent
- Slice append from multiple goroutines (slice header is not thread-safe)
- Global variables modified during request handling

**deadlocks** — Lock ordering, nested locks, channel blocking:
- Multiple locks acquired in inconsistent order across code paths
- Lock held while calling function that acquires another lock
- Unbuffered channel send/receive without guaranteed counterpart
- `select` without `default` case blocking indefinitely
- Mutex locked but not unlocked on all code paths (missing defer)
- RWMutex write lock with read lock held in same goroutine (self-deadlock)
- Context cancellation not checked while holding locks

**goroutine-leaks** — Unbounded spawning, missing cleanup:
- `go func()` without any mechanism to wait for completion
- Goroutines blocked on channel operations that will never complete
- Missing context propagation (goroutine outlives its parent)
- Worker pools without shutdown mechanism
- Ticker/timer goroutines without stop
- Goroutines spawned per request without concurrency limit
- Orphaned goroutines on error paths

**unsafe-sharing** — Non-atomic operations, concurrent container access:
- Non-atomic increment/decrement of shared counters
- `bool` flags used for cross-goroutine signaling without atomic or channel
- Concurrent writes to shared byte slices or strings
- Returning pointers to shared data without copying
- Closures that capture loop variables in goroutines
- WaitGroup Add/Done count mismatch
