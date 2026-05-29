### CORRECTNESS (category: "correctness")

Code where the logic may compile and run without errors but produce wrong results, violate intent, or fail on valid inputs. The hardest class of bug — the code looks fine but is subtly wrong.

## Shapes or common patterns

**semantic-errors** — Code that runs but produces wrong results:
- Inverted boolean checks (condition is backwards, wrong branch taken)
- Wrong arithmetic: operator precedence mistakes, integer division truncation, off-by-one in formulas
- Incorrect comparisons: `<` vs `<=`, `==` vs `===`, signed vs unsigned
- Switch/match with missing cases or fall-through that shouldn't
- Ternary/conditional expressions with swapped branches
- Short-circuit evaluation that skips necessary side effects
- Operations that must happen in a specific order but don't (e.g., validate then use, but validation happens after use)

**name-behavior-mismatch** — Functions/variables whose names contradict what they do:
- A function called `delete` that only soft-deletes without documenting it
- A variable called `maxRetries` used as a timeout value
- A method called `validate` that also mutates the input
- A getter that has side effects (writes to DB, sends events)
- Boolean variables whose names suggest the opposite of their actual meaning (`isEnabled` that means disabled)
- Functions that claim to be pure but modify external state

**implicit-assumptions** — Code that assumes properties of data that aren't enforced:
- Assumes a list is sorted without sorting it or documenting the requirement
- Assumes IDs are unique without a unique constraint
- Assumes a field is non-empty because "callers always set it"
- Assumes enum/union covers all cases without a default branch
- Assumes string encoding (UTF-8 vs ASCII vs locale) without validation
- Assumes numeric ranges (positive, non-zero) without checking
- Assumes time zone or locale without explicit handling

**nil-safety** — Dereferences and accesses without existence checks:
- Pointer/reference dereference without nil/null/undefined check
- Optional fields assumed to be present
- Map/dictionary lookups without existence checks (Go map returns zero value, not error)
- Array/slice access without bounds checking on untrusted indices
- Chained property access where any intermediate can be nil (`a.b.c.d`)
- Return values assumed non-nil when the function can return nil on valid paths

**type-safety** — Type coercion and conversion errors:
- Unchecked type assertions (`x.(T)` instead of `x, ok := x.(T)` in Go)
- Integer overflow/truncation on narrowing conversions (`int64` → `int32`)
- Implicit float-to-int truncation losing decimal precision
- String-to-number parsing without error handling
- Enum values cast to/from integers without range validation
- Interface satisfaction assumed but not compiler-verified (duck typing gone wrong)

**off-by-one** — Boundary errors in sequences and ranges:
- Loop bounds: `<` vs `<=`, starting at 0 vs 1
- Slice/substring indices: inclusive vs exclusive end
- Pagination: offset calculation errors, fencepost errors
- Range boundaries: "first N items" returning N+1 or N-1
- Array allocation one element too small or too large
- Modular arithmetic wrapping at wrong boundary

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
