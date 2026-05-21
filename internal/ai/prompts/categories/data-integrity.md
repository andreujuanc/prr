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

**unit-mismatch** — Named units, branded types, and producer/consumer drift:
- A function/variable/return type whose **name** implies one unit
  but whose **body** returns a value in a different unit. The
  classic case: an accessor named `…Seconds()` that returns
  milliseconds, or `getUsdAmount()` that returns a value in the
  smallest currency subunit.
- A type alias or branded type wrapping a primitive (TypeScript:
  `type UserID = string`; Go: `type Cents int64`; Python:
  `class OrderId(str)`; Rust: `pub struct AccountId(u64)`) crossing
  a boundary where the receiver expects the raw primitive or a
  *different* brand.
- Identifier-suffix conventions diverging between callsites — one
  call uses `feeBps` (basis points) and another consumes the value
  as if it were a percentage; or one path uses `delayMs` and the
  consumer treats it as seconds.
- A producer and consumer agreeing on the primitive type but
  disagreeing on the **scale** (cents vs. dollars, bytes vs. KB,
  milliseconds vs. nanoseconds) without any conversion at the hop.

This subcategory is language-agnostic. The pattern is "the name
or type encodes a meaning the value doesn't carry." Look for it
wherever a typed value crosses a function boundary — the most
common bugs hide one hop downstream from where the unit was
declared.
