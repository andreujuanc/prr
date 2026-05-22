### AI SLOP (category: "ai-slop")

Code (and especially comments) that adds ceremony without adding signal. The rule of thumb: **if removing this loses no information, it's slop.** A correct-but-vacuous comment is slop. A defensive check for a case that cannot happen is slop. A one-call helper that just renames its argument is slop.

Severity for this category: report at **nit** by default, **low** only when the slop actively obscures the change (e.g. a 10-line comment block above a one-line fix that buries the actual code).

Distinct from `readability`: readability asks "can a reader understand what's here?" (wrong, stale, or confusing). ai-slop asks "why is this here at all?" — the code is correct and the comment is accurate; the problem is the noise itself.

#### Subcategories

**bug-narration** — Comments that retell the bug being fixed or describe the prior broken behavior:
- "The bug we are fixing was using X unconditionally, which silently overwrote..."
- "Previously this used info.country; now it uses info.nationality because..."
- "Used to assume the field was always set; we found a case where..."
- Belongs in the PR description or commit message, not in the code. Future readers see the fix; they don't need the war story.

**commit-message-prose** — Multi-paragraph rationale stapled to a small change:
- A 6-line comment above a one-line fallback (`a ?? b`) explaining "preserves prior behavior for clients that don't expose...", "matches the type system requirement (X: Y)", and "the bug we are fixing was..."
- "Why we chose X over Y" blocks above straightforward code
- Justification of an approach when the code itself makes the approach obvious
- Customer-specific incident details ("a Venezuelan user uploaded an Argentinian ID was being persisted with...") that belong in the changelog, not in source

**test-narration** — Tests where a comment retells what the test name already says:
- "Why this test matters: ..." followed by the same story the test name encodes
- A bug-history comment above an `it("rejects invalid X")` block — the name already says it
- Verbose `// Arrange / // Act / // Assert` framing when the structure is already obvious

**vacuous-comments** — Comments that restate the code instead of adding information:
- `// add a and b` above `return a + b`
- `// returns the user's age` above `func GetUserAge() int`
- `// loop through items` above `for _, item := range items`
- `// increment counter` above `counter++`
- Doc comments on internal types that just repeat the type name in English

**defensive-noise** — Checks for cases that cannot happen:
- `try { ... } catch (e) { throw e }` — does nothing
- `if (x !== null && x !== undefined)` when the caller's contract guarantees non-null
- Argument validation in private helpers whose only caller already validated
- Re-checking invariants the type system or framework already enforces
- Wrapping a synchronous call in a try/catch when no exception path exists

**single-use-abstractions** — Indirection added for no clarity gain:
- Helper functions called from exactly one place that just rename their argument and return immediately
- Interfaces with a single implementation and no plan for a second one
- Parameterized utilities for cases that don't yet exist
- A wrapper class whose only job is to delegate to another class

**performative-naming** — Names that announce their own status instead of describing behavior:
- `Enhanced*`, `Improved*`, `Optimized*`, `Better*`, `V2*` prefixes on renamed functions
- Variables renamed to longer "descriptive" forms with no clarity gain (`userObject` for `user`, `dataResponse` for `response`)
- Function names that describe their improvement history (`processItemsCorrectly`, `validateProperly`)
- Comments explaining the name change itself (`// renamed from doStuff for clarity`)

**ceremonial-error-wrapping** — Error handling that adds layers without context:
- Wrapping every error with `fmt.Errorf("error: %w", err)` (the verb adds nothing)
- Catch-and-rethrow chains that don't add a layer of context — just propagate the original error directly
- Logging an error and then returning it (caller will log it too — duplicate logs in prod)
- Wrapping a panic message in a function whose only purpose is to construct the panic message
