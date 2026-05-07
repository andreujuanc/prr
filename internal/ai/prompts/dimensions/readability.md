### READABILITY & MAINTAINABILITY (category: "readability")

Code clarity, naming, complexity — whether a new team member can understand this code in one reading.

#### Subcategories

**naming** — Names that convey (or obscure) intent:
- Single-letter variables outside tiny loops (`x`, `d`, `t` for non-obvious things)
- Ambiguous abbreviations (`proc` — process or processor? `mgr` — manager of what?)
- Names inconsistent with the rest of the codebase (same concept, different name)
- Boolean variables without is/has/can/should prefix (unclear what true means)
- Functions named for implementation rather than intent (`loopAndCheck` vs `validateAll`)

**complexity** — Functions and structures too complex to reason about:
- Functions longer than ~50 lines or with more than 3 levels of nesting
- Functions with more than 5 parameters (especially booleans that control behavior)
- Complex conditional logic that could be simplified (nested if-else chains, boolean algebra)
- Single function doing multiple unrelated things (should be split)
- Control flow that requires reading the function multiple times to understand

**dead-code** — Code that serves no purpose but confuses readers:
- Commented-out code blocks left in (use version control, not comments)
- Unused variables, imports, functions, or types
- Unreachable branches (conditions that can never be true)
- Stale feature flags that are always on or always off
- TODO/FIXME comments on code that was already fixed or is no longer relevant

**comments** — Comment quality and accuracy:
- Comments that restate the code instead of explaining WHY (`// increment i` above `i++`)
- Stale comments that contradict the current code (changed logic, comment not updated)
- Missing comments on non-obvious business logic or workarounds
- Commented-out code with no explanation of why it's kept
- Missing doc comments on public APIs

**magic-values** — Hardcoded literals that should be named constants:
- Numeric literals with non-obvious meaning (`if retries > 3`, `timeout: 30000`)
- String literals used as identifiers or keys in multiple places
- Threshold values without explanation of why that specific value
- Repeated magic values that should be a single named constant
