# Plan: Unified Review Pipeline (`prr review` / `prr audit`)

## Overview

A single review pipeline that works for both PR review and full-project audit. The only difference is where the file list comes from. The key architectural decisions:

1. **AOIs are categorized** — each AOI has a category, subcategory, and urgency (`individual` vs `grouped`)
2. **Phase 3 reviews by subcategory** — grouped AOIs in the same subcategory are reviewed together; individual AOIs get their own dedicated call
3. **The AOI controls cost** — urgency tagging means the expensive model spends full attention on critical concerns and batches routine ones

## CLI Interface

### PR Mode (existing)

```
prr review [flags]
```

### Audit Mode (new)

```
prr audit [flags]
```

Flags:
- `--focus <dimensions>` — comma-separated dimensions to evaluate (e.g. `security,correctness`). Default: all. Applied at Phase 3 (deep review) to shape what the reviewer focuses on. Phase 2 AOI always scans all dimensions (keeps caching simple — cache key is content hash only).
- `--exclude <glob>` — exclude paths (in addition to .prr/exclude patterns).
- `--max-reviews <n>` — cap on Phase 3 review calls (both individual and grouped). Phases 1-2 always run to completion. Individual AOIs are prioritized over grouped ones when cap is hit. Synthesis runs on whatever findings exist so far.
- `--include <glob>` — force-include files that would otherwise be filtered by Phase 1 (e.g., `--include "*.d.ts"` to audit hand-written declaration files).
- `--no-cache` — ignore cached results, re-audit everything. Default behavior is incremental (only re-audit changed files).

## Core Architecture

### Unified Pipeline

```
Phase 0: Project Context Discovery (cached)
    Understand what the project does, tech stack, conventions.
    ↓
Phase 1: File Collection (no LLM, instant, free)
    PR mode:  files from the PR diff
    Audit mode: deterministic filter removes noise from full tree
    ↓
Phase 2: AOI Generation (fast model, per-file, parallel, no tools)
    Input:  file list + file contents (or diffs for PR mode)
    Output: 0 or more AOIs per file, each with category/subcategory/urgency
    ↓
Phase 3: Deep Review (best model, parallel, tools)
    individual AOIs → one LLM call each (full focus, deep investigation)
    grouped AOIs    → one LLM call per subcategory (related concerns together)
    ↓
Phase 4: Synthesis (fast model)
    Input:  all findings from Phase 3
    Output: deduplicated, severity-ranked summary + recommendations
```

## Phase 0: Project Context Discovery

**Goal**: Understand what the project does, its tech stack, conventions, and architecture. Cached by input hash (README, manifests, key config files).

Reuses existing project context infrastructure. Output is injected into Phase 2 and Phase 3 prompts.

## Phase 1: File Collection

**Goal**: Get the list of files to review. No LLM involved — deterministic, instant, free.

### PR mode

Files come from the PR diff. Same as current behavior.

### Audit mode

Deterministic filter removes guaranteed noise from the full file tree:

```
# Test files
*_test.go, *.test.ts, *.test.js, *.spec.ts, *.spec.js, *.test.tsx, *.spec.tsx

# Test infrastructure
testdata/, __tests__/, __mocks__/, fixtures/, test_helpers/, conftest.py

# Generated code
*.pb.go, *.gen.go, *.generated.*, *_generated.go
*.d.ts (type declarations, not source)

# Dependencies & build artifacts
vendor/, node_modules/, dist/, build/, .next/, target/
*.lock, go.sum

# Documentation & non-code
*.md, *.txt, *.rst, LICENSE*, CHANGELOG*, CONTRIBUTING*
.gitignore, .editorconfig, .prettierrc, .eslintrc

# Assets
*.png, *.jpg, *.svg, *.ico, *.woff, *.ttf, *.mp4

# IDE & tooling
.vscode/, .idea/
```

Extensible via `.prr/audit-exclude` (same glob format as `.prr/exclude`).

### What this does NOT filter

- Config files with potential logic (`webpack.config.js`, `nginx.conf`, `Dockerfile`) — kept
- YAML/TOML in source directories — kept
- CSS/HTML — kept
- Anything the filter isn't 100% certain about — kept

### File size handling

Files exceeding ~3,000 lines are split into chunks for Phase 2 AOI generation (each chunk scanned independently). Chunks are defined by natural boundaries (package/class/function blocks) where possible, falling back to fixed-size splits.

## Phase 2: AOI Generation (Fast Model, No Tools)

**Goal**: For each file, identify 0 or more specific areas of interest, each tagged with a category, subcategory, and urgency level. This is the intelligent filter — files with zero AOIs are naturally skipped. Runs in parallel across all files.

### Model

Fast/cheap model. No tool access — file content is inlined in the prompt. This keeps latency low and cost minimal.

### How it works

1. Takes the file list from Phase 1
2. For each file in parallel: send file content + project context to fast model
3. Model identifies specific areas of concern across all dimensions
4. Each AOI is tagged with:
   - **category** / **subcategory** — what kind of concern this is
   - **urgency** — `individual` (needs dedicated deep review) or `grouped` (can be reviewed alongside similar concerns)
5. Outputs 0 or more AOIs per file

### Categories and subcategories

Well-defined, finite taxonomy. The AOI model picks from this list:

```
authentication
  ├── login-flow
  ├── session-management
  ├── token-validation       (JWT, OAuth, API keys)
  └── password-handling

authorization
  ├── access-control         (RBAC, permissions)
  ├── resource-ownership     (IDOR, tenant isolation)
  └── privilege-escalation

input-validation
  ├── injection              (SQL, command, XSS, LDAP)
  ├── path-traversal
  ├── deserialization
  └── boundary-checks        (overflow, underflow, length)

data-integrity
  ├── state-machines         (invalid transitions, missing states)
  ├── transactions           (atomicity, rollback, partial failure)
  ├── invariants             (business rules, domain constraints)
  └── consistency            (cache coherence, replication)

cryptography
  ├── encryption             (algorithm choice, mode, padding)
  ├── key-management         (storage, rotation, hardcoded)
  ├── hashing                (password hashing, comparison)
  └── randomness             (PRNG vs CSPRNG)

error-handling
  ├── swallowed-errors       (ignored returns, empty catch)
  ├── error-propagation      (missing context, lost stack)
  ├── partial-failure        (cleanup, rollback, resource leaks)
  └── panic-safety           (unrecovered panics, crash paths)

concurrency
  ├── race-conditions        (shared state, missing locks)
  ├── deadlocks              (lock ordering, nested locks)
  ├── goroutine-leaks        (unbounded spawning, missing cleanup)
  └── unsafe-sharing         (concurrent map access, non-atomic ops)

external-io
  ├── api-calls              (retries, timeouts, idempotency)
  ├── database               (N+1 queries, connection leaks, raw SQL)
  ├── file-system            (path handling, permissions, temp files)
  └── network                (TLS, certificate validation, DNS)

financial
  ├── money-arithmetic       (floating point, rounding, precision)
  ├── billing-logic          (pricing, discounts, tax calculation)
  └── payment-integration    (idempotency, webhook verification)

configuration
  ├── secrets-exposure       (hardcoded keys, logged secrets)
  ├── default-values         (insecure defaults, missing validation)
  └── environment-handling   (env var trust, fallback behavior)

api-design
  ├── contract-violations    (breaking changes, missing validation)
  ├── versioning             (backwards compat, deprecation)
  └── response-handling      (error formats, status codes)

resource-management
  ├── memory-leaks           (unbounded caches, retained references)
  ├── connection-pools       (exhaustion, missing returns)
  ├── file-handles           (unclosed, missing defer)
  └── unbounded-growth       (queues, buffers, log accumulation)

testing
  ├── assertion-quality      (vacuous checks, missing value assertions)
  ├── coverage-gaps          (untested paths, missing edge cases)
  ├── test-correctness       (tests that pass but verify nothing)
  └── test-reliability       (flaky patterns, timing, environment dependence)

correctness
  ├── semantic-errors        (wrong results, inverted checks, wrong arithmetic)
  ├── name-behavior-mismatch (function name contradicts behavior)
  ├── implicit-assumptions   (unverified assumptions about data properties)
  ├── nil-safety             (dereferences without checks, optional fields assumed present)
  ├── type-safety            (unchecked assertions, truncation, precision loss)
  └── off-by-one             (loop bounds, slice indices, pagination)

design
  ├── abstraction-level      (over-engineering, under-abstraction, copy-paste)
  ├── responsibility-separation (mixed concerns, layer violations)
  ├── codebase-consistency   (new patterns where established ones exist)
  ├── coupling               (tight coupling, circular deps, untestable)
  └── api-surface            (unnecessary exports, leaking internals)

performance
  ├── algorithmic-complexity  (O(n²), linear scans, missing memoization)
  ├── memory                 (unbounded growth, missing pre-allocation)
  ├── io-blocking            (sync I/O on hot paths, N+1, missing timeouts)
  ├── concurrency-overhead   (per-request goroutines, lock contention)
  └── caching                (missing cache, no TTL, invalidation bugs)

readability
  ├── naming                 (ambiguous, inconsistent, misleading names)
  ├── complexity             (long functions, deep nesting, many params)
  ├── dead-code              (commented-out code, unused vars, unreachable)
  ├── comments               (stale, restating code, missing on non-obvious logic)
  └── magic-values           (hardcoded numbers/strings, unexplained thresholds)

cross-cutting
  ├── incomplete-refactors   (renamed but callers not updated, partial changes)
  ├── inconsistent-patterns  (different approaches for same problem in same PR)
  └── missing-cascading-updates (config without code, schema without migration)
```

### Urgency: `individual` vs `grouped`

The AOI model decides urgency based on how dangerous the concern looks:

**`individual`** — This AOI gets its own dedicated Phase 3 call. The expensive model gives it full attention with tools to trace code paths, check callers, verify assumptions. Reserved for:
- Looks like a real, exploitable vulnerability (SQL injection with user input, auth bypass)
- Critical business logic flaw (money calculation error, state machine violation)
- Complex concern that requires deep investigation (race condition across multiple files)
- Anything where a false negative would be costly

**`grouped`** — This AOI is reviewed alongside other AOIs in the same subcategory. The expensive model sees them together, spots patterns, but spends less individual time. For:
- Routine concerns that follow a pattern (missing error wrapping, no nil check)
- Low-severity issues (inconsistent naming, missing docs)
- Concerns that are likely fine but worth a quick check
- Things where seeing the pattern across files is more valuable than deep-diving each one

The Phase 2 prompt instructs:

> Mark an AOI as `individual` only when it looks like a real, exploitable bug, a critical design flaw, or a concern complex enough to require deep tool-assisted investigation. Default to `grouped` — most concerns benefit from being reviewed alongside similar ones in the same subcategory.

### AOI output format

```json
{
  "file": "internal/billing/charge.go",
  "areas": [
    {
      "id": "charge-go-float-currency",
      "lines": "45-78",
      "category": "financial",
      "subcategory": "money-arithmetic",
      "urgency": "individual",
      "concern": "Currency conversion with floating point arithmetic",
      "dimensions": ["correctness"],
      "context": "Multiplies amounts by exchange rates using float64. This is in the hot path for all cross-currency transactions."
    },
    {
      "id": "charge-go-stripe-idempotency",
      "lines": "102-130",
      "category": "external-io",
      "subcategory": "api-calls",
      "urgency": "grouped",
      "concern": "Stripe API call with no idempotency key",
      "dimensions": ["correctness", "error_handling"],
      "context": "Creates charge via Stripe SDK without idempotency protection"
    },
    {
      "id": "charge-go-error-swallow",
      "lines": "88-91",
      "category": "error-handling",
      "subcategory": "swallowed-errors",
      "urgency": "grouped",
      "concern": "Error from validateAmount() assigned to _ ",
      "dimensions": ["error_handling"],
      "context": "Validation error silently ignored before creating charge"
    }
  ]
}
```

### Key properties

- **All dimensions always scanned** — `--focus` is applied at Phase 3, not here. Keeps AOI caching independent of focus.
- **No tools** — file content inlined. Fast model doesn't need to explore; it just reads and flags.
- **Parallel** — all files processed concurrently (with concurrency cap).
- **Cached by content hash** — if file hasn't changed, reuse cached AOIs.
- **Categories are stable** — the taxonomy is hardcoded, not invented per-run. This makes caching and grouping predictable.

### PR mode vs Audit mode

| Aspect | PR mode | Audit mode |
|---|---|---|
| Input | Diff hunks + surrounding context | Full file contents |
| Scope | Only changed lines + nearby code | Entire file |
| Volume | ~10-50 files per PR | ~50-500 files per project |

## Phase 3: Deep Review (Best Model, Tools)

**Goal**: Investigate AOIs deeply using the best model with full tool access. Individual AOIs get dedicated calls; grouped AOIs are batched by subcategory.

### How AOIs become review calls

After Phase 2 completes, all AOIs across all files are collected and organized:

1. **Individual AOIs** → one LLM call each. Queued first (highest priority).
2. **Grouped AOIs** → collected by subcategory across all files. One LLM call per subcategory.

Example: If Phase 2 produces:
- 5 `individual` AOIs (various categories)
- 8 `error-handling/swallowed-errors` AOIs across 6 files
- 4 `external-io/api-calls` AOIs across 3 files
- 6 `concurrency/race-conditions` AOIs across 4 files
- 3 `error-handling/partial-failure` AOIs across 2 files

This becomes:
- 5 individual review calls (one per critical AOI)
- 4 grouped review calls (one per subcategory)
- **Total: 9 LLM calls** instead of 26

### Individual review prompt

```
You are deeply investigating a specific area of concern in a codebase.

## Project Context
{project context}

## Area of Interest
File: internal/billing/charge.go
Lines: 45-78
Category: financial / money-arithmetic
Concern: Currency conversion with floating point arithmetic
Context: Multiplies amounts by exchange rates using float64.
         This is in the hot path for all cross-currency transactions.

## Your Task
Investigate this concern deeply. Use read_file and grep to:
- Read the flagged code and surrounding context
- Check callers and consumers of this code
- Understand types and data flow
- Determine if this is a real issue with concrete impact

If this is a real issue, provide a finding with severity, description,
concrete trigger scenario, and suggestion.

If it's not a real issue (false positive, already handled elsewhere,
acceptable for the context), dismiss it with a brief rationale.

{dimension criteria}
```

### Grouped review prompt

```
You are reviewing a set of related concerns in a codebase.
All of these are in the same area: {category} / {subcategory}.

## Project Context
{project context}

## Areas of Interest

1. File: internal/auth/handler.go (L33-41)
   Concern: Error from validateToken() assigned to _
   Context: Token validation error silently ignored in login handler

2. File: internal/billing/charge.go (L88-91)
   Concern: Error from validateAmount() assigned to _
   Context: Validation error silently ignored before creating charge

3. File: internal/api/middleware.go (L112-115)
   Concern: Error from parseRequest() caught and logged but not returned
   Context: Malformed request continues processing after log.Warn()

## Your Task
Review each concern. Use read_file and grep to verify each one.
Look for patterns — are these isolated incidents or a systemic issue?

For each AOI, provide either a finding or a dismissal.
Also note any cross-cutting observations (e.g., "error handling is
inconsistent across the codebase" or "all of these follow the same
problematic pattern established in the base handler").

{dimension criteria}
```

### Focus filtering

When `--focus` is set, the Phase 3 prompt includes only the specified dimension criteria. AOIs tagged with non-focus dimensions are still reviewed (AOI caching is focus-independent), but the reviewer only reports findings matching focus dimensions.

### Priority under `--max-reviews`

When `--max-reviews` caps the number of Phase 3 calls:

1. All `individual` AOIs are reviewed first (they're flagged as critical)
2. Remaining budget goes to grouped subcategories, ordered by total AOI count (more AOIs = more likely a systemic issue worth reviewing)
3. Synthesis runs on whatever findings exist, noting which subcategories were skipped

### Output format

For individual reviews — either a finding or dismissal:

```json
{
  "aoi_id": "charge-go-float-currency",
  "status": "finding",
  "file": "internal/billing/charge.go",
  "lines": "45-62",
  "severity": "high",
  "category": "financial",
  "subcategory": "money-arithmetic",
  "dimension": "correctness",
  "title": "Currency conversion uses floating point arithmetic",
  "description": "Multiplying amounts by exchange rates using float64 will accumulate rounding errors. For a $1000 transaction with 3 currency hops, error can reach ~$0.01.",
  "trigger": "Any multi-currency transaction with more than one conversion step",
  "suggestion": "Use decimal library or integer-cents representation"
}
```

For grouped reviews — findings/dismissals per AOI + optional cross-cutting observation:

```json
{
  "subcategory": "error-handling/swallowed-errors",
  "results": [
    {
      "aoi_id": "handler-go-validate-error",
      "status": "finding",
      "file": "internal/auth/handler.go",
      "lines": "33-41",
      "severity": "high",
      "title": "Token validation error silently ignored in login handler",
      ...
    },
    {
      "aoi_id": "charge-go-error-swallow",
      "status": "dismissed",
      "rationale": "validateAmount is a no-op placeholder — always returns nil. Not a real issue yet."
    }
  ],
  "cross_cutting": "Error handling is inconsistent across HTTP handlers. Some return errors, some log and continue, some silently ignore. Recommend establishing a standard error handling pattern."
}
```

### Caching

- Individual AOI reviews cached by: `hash(file_content + aoi_content + focus_dimensions)`
- Grouped reviews cached by: `hash(all_aoi_content_in_subcategory + focus_dimensions)`. If any file in the group changes, the entire subcategory group is re-reviewed.

## Phase 4: Synthesis (Fast Model)

**Goal**: Take all findings from Phase 3, deduplicate, rank, and produce a summary with recommendations.

### What it does

- Collects all findings (excludes dismissals)
- Deduplicates (same underlying issue flagged from different AOIs)
- Ranks by severity
- Incorporates cross-cutting observations from grouped reviews
- Identifies systemic patterns ("error handling is inconsistent across 5 files")
- Produces structured summary + actionable recommendations

### Model

Fast/cheap model. The hard work is already done — synthesis just organizes results.

### Hierarchical synthesis (for large audits)

If more than ~50 findings, synthesis input may exceed context window. Use two-level synthesis:

1. **Group findings** by category
2. **Sub-synthesis** per category → intermediate summary
3. **Final synthesis** across categories → final ranked output

### Output

- Severity-ranked findings list
- Cross-cutting observations (from grouped reviews + synthesis)
- Top recommendations
- Stats (files scanned, AOIs generated, individual reviews, grouped reviews, findings, dismissals)

## Caching & Incremental Behavior

**Incremental is the default.** `--no-cache` is the escape hatch.

### What gets cached

| Data | Cache key | Invalidated when |
|---|---|---|
| Project context | Input hash (README, manifests) | Key docs change |
| Phase 1 filter result | File tree hash (sorted paths) | Files added/deleted/renamed |
| Phase 2 AOIs per file | Content hash (SHA-256) | File content changes |
| Phase 3 individual finding | Content hash + AOI hash + focus | File content or focus changes |
| Phase 3 grouped finding | Hash of all AOIs in subcategory + focus | Any file in group changes |

### Incremental flow

1. Hash all in-scope files
2. Phase 1: Re-run only if file tree changed. Otherwise reuse cached filter result.
3. Phase 2: For each file, skip AOI generation if content hash unchanged. Only re-scan changed files.
4. Phase 3: For individual AOIs, skip review if content hash unchanged. For grouped subcategories, re-review if any constituent file changed.
5. Phase 4: Synthesis always re-runs (cheap, and the mix of cached + fresh findings may produce different results).

### Resume on interruption

Because per-AOI and per-subcategory results are cached after each completes, an interrupted audit automatically resumes from where it left off on the next run. No special resume flag needed.

### State model

```
.git/prr/state.json
```

```go
type ReviewState struct {
    ProjectContextHash string
    FileTreeHash       string                    // hash of sorted file paths
    FilteredFiles      []string                  // output of Phase 1
    Files              map[string]FileState      // per-file state
    LastResult         *SynthesisResult          // last synthesis output
}

type FileState struct {
    ContentHash string      // SHA-256 of file content
    AOIs        []AOI       // Phase 2 output
    AOIEmpty    bool        // true if Phase 2 found nothing
    LastScanned time.Time
}

type AOI struct {
    ID          string   // stable identifier
    Lines       string   // e.g. "45-78"
    Category    string   // e.g. "financial"
    Subcategory string   // e.g. "money-arithmetic"
    Urgency     string   // "individual" or "grouped"
    Concern     string
    Dimensions  []string
    Context     string
    Finding     *Finding // Phase 3 output (nil if not yet reviewed)
    Dismissed   bool
    DismissedRationale string
}

type Finding struct {
    Severity    string
    Dimension   string
    Title       string
    Description string
    Trigger     string
    Suggestion  string
}

// GroupedReviewResult stores the result of reviewing a subcategory group
type GroupedReviewResult struct {
    Subcategory  string
    AOIHashes    string    // hash of all constituent AOI content
    Results      []AOIResult
    CrossCutting string    // cross-cutting observation from reviewer
}
```

## Prompt Architecture

### Shared dimensions (runtime-composed)

Each category has subcategories with detailed patterns to look for. These files are used by both the AOI generation prompt (Phase 2) and the deep review prompt (Phase 3). They are also shared between PR mode and audit mode.

```
internal/ai/prompts/dimensions/
  authentication.md      — login-flow, session-management, token-validation, password-handling
  authorization.md       — access-control, resource-ownership, privilege-escalation
  input-validation.md    — injection (SQL/cmd/XSS/LDAP), path-traversal, deserialization, boundary-validation, boundary-checks
  data-integrity.md      — state-machines, transactions, invariants, consistency
  cryptography.md        — encryption, key-management, hashing, randomness
  error-handling.md      — swallowed-errors, error-propagation, partial-failure, panic-safety
  concurrency.md         — race-conditions, deadlocks, goroutine-leaks, unsafe-sharing
  external-io.md         — api-calls, database, file-system, network
  financial.md           — money-arithmetic, billing-logic, payment-integration
  configuration.md       — secrets-exposure, default-values, environment-handling, dependency-security
  api-design.md          — contract-violations, versioning, response-handling
  resource-management.md — memory-leaks, connection-pools, file-handles, unbounded-growth
  testing.md             — assertion-quality, coverage-gaps, test-correctness, test-reliability
  correctness.md         — semantic-errors, name-behavior-mismatch, implicit-assumptions, nil-safety, type-safety, off-by-one
  design.md              — abstraction-level, responsibility-separation, codebase-consistency, coupling, api-surface
  performance.md         — algorithmic-complexity, memory, io-blocking, concurrency-overhead, caching
  readability.md         — naming, complexity, dead-code, comments, magic-values
  cross-cutting.md       — incomplete-refactors, inconsistent-patterns, missing-cascading-updates
```

Each file contains ONLY the evaluation criteria (category → subcategories → specific patterns) — no mode-specific framing. The format is:

```markdown
### CATEGORY_NAME (category: "category-slug")
Description of what this category covers.

#### Subcategories

**subcategory-slug** — Brief description:
- Specific pattern to look for
- Another pattern
- ...
```

These files already exist and are fully written.

### Mode-specific prompts

| Prompt | PR mode | Audit mode |
|---|---|---|
| AOI generation | "Identify concerns in these changed lines" | "Identify concerns in this file" |
| Individual review | "You are reviewing a change in a PR" | "You are auditing source code for latent issues" |
| Grouped review | Same framing, PR-scoped | Same framing, audit-scoped |
| Confidence | Permissive (PR context helps) | Strict (concrete trigger required) |
| Synthesis | PR summary + inline comments | Full audit report + recommendations |

### Composition at runtime

```go
func buildIndividualReviewPrompt(mode string, projectContext, customInstructions string, aoi AOI, dimensions []string) string {
    if mode == "pr" {
        prompt = prompts.PRReviewPreamble
    } else {
        prompt = prompts.AuditReviewPreamble
    }
    prompt += "\n\n## Project Context\n" + projectContext
    prompt += "\n\n## Area of Interest\n" + formatAOI(aoi)
    for _, dim := range dimensions {
        prompt += "\n\n" + prompts.GetDimension(dim)
    }
    prompt += "\n\n" + prompts.ReviewOutputFormat
    if customInstructions != "" {
        prompt += "\n\n## Project-Specific Instructions\n" + customInstructions
    }
    return prompt
}

func buildGroupedReviewPrompt(mode string, projectContext, customInstructions string, subcategory string, aois []AOI, dimensions []string) string {
    if mode == "pr" {
        prompt = prompts.PRGroupedReviewPreamble
    } else {
        prompt = prompts.AuditGroupedReviewPreamble
    }
    prompt += "\n\n## Project Context\n" + projectContext
    prompt += fmt.Sprintf("\n\n## Areas of Interest (%s)\n", subcategory)
    for i, aoi := range aois {
        prompt += fmt.Sprintf("\n%d. %s\n", i+1, formatAOI(aoi))
    }
    for _, dim := range dimensions {
        prompt += "\n\n" + prompts.GetDimension(dim)
    }
    prompt += "\n\n" + prompts.GroupedReviewOutputFormat
    if customInstructions != "" {
        prompt += "\n\n## Project-Specific Instructions\n" + customInstructions
    }
    return prompt
}
```

### Migration path for existing PR prompts

1. Extract dimension content from `review_batch.md` into `dimensions/*.md`
2. Rewrite existing prompts to compose from partials at runtime
3. Verify no regression (run on test PRs, compare output quality)
4. Write audit-mode preambles using same partials

## Cost Estimation

Cost scales with AOI count, but grouped reviews reduce the number of expensive calls significantly.

### Example breakdown

A medium project (200 files, ~120 after Phase 1) might produce:
- 15 `individual` AOIs → 15 calls
- 80 `grouped` AOIs across ~20 subcategories → 20 calls
- **Total: 35 Phase 3 calls** (vs 95 if every AOI was individual)

| Project size | Total files | After Phase 1 | AOIs | Individual | Grouped subcategories | Phase 3 calls | Cost (Gemini 2.5 Pro) |
|---|---|---|---|---|---|---|---|
| Small (50) | 50 | ~35 | ~20 | ~5 | ~5 | ~10 | ~$0.50-1.50 |
| Medium (200) | 200 | ~120 | ~80 | ~15 | ~20 | ~35 | ~$2-6 |
| Large (1000) | 1000 | ~500 | ~300 | ~40 | ~35 | ~75 | ~$8-20 |

Phase 1: free (deterministic)
Phase 2: ~$0.05-0.20 (fast model, parallel, no tools)
Phase 3: bulk of cost (best model + tool calls)
Phase 4: ~$0.10-0.50 (fast model, small input)

## Implementation Phases

### Phase 1: Prompt refactoring (no new features)
- Extract dimensions into `dimensions/*.md` partials
- Rewrite existing review prompts to compose from partials at runtime
- Verify no regression (run on test PRs, compare output quality)
- Duration: 1 day

### Phase 2: AOI categorization + review architecture
- Define category/subcategory taxonomy
- Update AOI generation prompt to produce categorized, urgency-tagged AOIs
- Implement Phase 3 routing: individual vs grouped by subcategory
- Per-AOI and per-subcategory caching
- Individual and grouped review prompts
- Duration: 3-4 days

### Phase 3: Audit mode
- `cmd/prr/audit.go` — entry point with flags
- Deterministic filter for full-project file collection
- AOI generation prompt adapted for full files (vs diffs)
- Audit-specific preambles and synthesis prompt
- State persistence
- Duration: 2-3 days

### Phase 4: Polish
- Cost estimation before starting ("35 review calls, ~$4. Continue?")
- `--max-reviews` with priority ordering (individual first, then grouped by AOI count)
- Hierarchical synthesis for large audits
- Comparison with last audit ("3 new issues, 1 resolved")
- Report export (markdown + JSON)
- TUI progress display (individual/grouped in flight, findings so far)
- Duration: 1-2 days

## Open Questions

1. **Concurrency cap for Phase 3** — How many parallel reviews? Too many hits rate limits; too few is slow. Start with 10-15 concurrent.
2. **Grouped review size cap** — If a subcategory has 30 AOIs, should we split into multiple grouped calls? Probably yes — cap at ~10 AOIs per grouped call.
3. **Cross-file AOIs** — Some concerns span files. Let Phase 3 discover this via tools + cross-cutting observations in grouped reviews.
4. **Output format** — TUI display + report file? Markdown for humans, JSON for CI tooling. Probably both.
5. **PR mode migration** — Ship audit mode first on new architecture, then migrate PR mode. Avoids breaking the working thing while proving the new approach.
6. **Subcategory granularity** — The taxonomy above has ~40 subcategories. Too many? Too few? Can be tuned based on real-world AOI distribution.
7. **Urgency calibration** — How aggressive should the AOI model be with `individual`? Start conservative (only truly critical), tune based on finding quality.
