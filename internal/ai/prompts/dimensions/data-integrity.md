### DATA INTEGRITY (category: "data-integrity")

Code that manages state, enforces business rules, or maintains consistency.

#### Subcategories

**state-machines** — Invalid transitions, missing states, lifecycle management:
- State transitions that skip required intermediate states
- Missing validation of current state before transition (e.g., shipping a cancelled order)
- Enum/status fields without exhaustive handling (missing cases in switch/if-else)
- State transitions that aren't atomic (observable intermediate states)
- Events or callbacks that fire in wrong state
- Re-entrant state changes (transition triggered during another transition)

**transactions** — Atomicity, rollback, partial failure:
- Multi-step operations without transactional boundaries
- Database operations across multiple tables without a transaction
- Missing rollback logic when a step in a multi-step operation fails
- Distributed operations without saga pattern or compensation
- Read-modify-write cycles without optimistic locking or CAS
- Operations that partially succeed and leave inconsistent state

**invariants** — Business rules, domain constraints, contracts:
- Domain rules not enforced in code (e.g., "balance cannot go negative" but no check)
- Invariants checked in some code paths but not others
- Validation at the wrong layer (UI validates but backend doesn't)
- Constraints that should be DB-level but are only application-level
- Derived values that can get out of sync with source values
- Preconditions assumed but not asserted

**consistency** — Cache coherence, replication, eventual consistency:
- Cache that can serve stale data after writes
- Missing cache invalidation on update/delete paths
- Read-after-write inconsistency in distributed systems
- Data replicated to multiple stores without consistency guarantees
- Denormalized data that can diverge from source of truth
- Time-of-check to time-of-use (TOCTOU) races on data
