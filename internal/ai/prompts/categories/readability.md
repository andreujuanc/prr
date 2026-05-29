### READABILITY & MAINTAINABILITY (category: "readability")

Code clarity, naming, complexity — whether a new team member can understand this code in one reading.

## Shapes or common patterns

**naming** — Names that convey (or obscure) intent:
- Single-letter variables for non-obvious things in non-trivial scope. Note: short conventional names are idiomatic in Go and should NOT be flagged — receivers (`r *Repo`), `t *testing.T`, `r io.Reader`, `w io.Writer`, `b *bytes.Buffer`, `i`/`j` in loops, `err` for errors, `ok` for boolean returns.
- Ambiguous abbreviations whose expansion isn't obvious from context (`proc` — process or processor? `mgr` — manager of what?)
- Names inconsistent with the rest of the codebase (same concept, different name)
- Boolean variables whose name doesn't convey what `true` means (unclear polarity, e.g. `flag`, `disabled` used as enabled)
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
- Missing comments on non-obvious business logic, hidden constraints, or workarounds
- Commented-out code with no explanation of why it's kept
- Missing doc comments on public APIs that have non-obvious semantics. Do NOT flag a missing doc comment when the symbol's name and signature already make behavior obvious — this is style noise.

**magic-values** — Hardcoded literals that should be named constants:
- Numeric literals with non-obvious meaning (`if retries > 3`, `timeout: 30000`)
- String literals used as identifiers or keys in multiple places
- Threshold values without explanation of why that specific value
- Repeated magic values that should be a single named constant

## Review criteria

[empty during migration — filled later via the Claude-Red coverage pass]
