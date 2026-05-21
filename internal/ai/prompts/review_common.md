## Defenses Checked (required for security-shaped categories)

For findings whose `category` is in this set, you MUST list every
defense layer you inspected when judging the bug:

- `authentication`
- `authorization`
- `concurrency`
- `cryptography`
- `external-io`
- `input-validation`
- `web-security`

Each entry in `defenses_checked` is one tag from the canonical
vocabulary below, or `other:<tag>` for cases outside the list. The
goal is to make the rebuttal explicit: when you flag a missing
auth check, the reader should see that you actually looked at the
boundary authorizer / middleware / in-handler guard before concluding
the check is missing.

Canonical tags (language-agnostic — describe the shape, not the
framework):

- `boundary-authz` — gateway/middleware authentication or
  authorization layer; the check that runs before any handler sees
  the request.
- `handler-guard` — in-function permission / role check
  (e.g., `if !user.IsAdmin { return 403 }`).
- `conditional-write` — write that succeeds only when a precondition
  holds: compare-and-swap, versioned column, "if-not-exists",
  optimistic-lock retry.
- `idempotency-key` — dedup table, nonce, request-ID lookup, or any
  mechanism that makes the operation safe to repeat.
- `schema-validation` — declared schema (JSON Schema, OpenAPI body,
  protobuf, brand types) that parses the input at the boundary
  before business logic sees it.
- `framework-escape` — template engine auto-escape, ORM
  parameterization, prepared-statement substitution, header sanitizer.
- `result-discipline` — the caller awaits the result and propagates
  the error/Result type instead of fire-and-forget or silent swallow.
- `native-limit` — platform's documented payload / batch / size
  ceiling that bounds the input before it ever reaches the handler
  (e.g., API Gateway's 6MB payload cap, DynamoDB's 100-item
  BatchWrite limit).

For each tag listed, the `evidence` field should describe what you
saw at that layer — "boundary-authz: API Gateway authorizer
referenced in routes.yaml validates JWT scope `admin:write`".

For findings in OTHER categories (correctness, error-handling,
performance, etc.), `defenses_checked` is optional — leave it empty
when no defense layer applies (e.g., an off-by-one in arithmetic has
nothing to defend against).

If a required-category finding ships with an empty
`defenses_checked` list, the validator will subtract 25 from your
confidence score and tag the reasoning with `defenses-not-checked`.
Severity is unchanged — but a low-confidence severe finding is a
weaker signal than a high-confidence one.

**Always emit the `defenses_checked` field, even if the value is
`[]`.** An omitted field and an empty array are treated the same way
by the validator, but emitting the field explicitly proves you
considered it rather than forgetting.

## End-to-End Trace (required at critical/high)

If you intend to emit a finding at `critical` or `high` severity, you
MUST produce a `trace` array of at least THREE hops showing how the
suspect value reaches the next system boundary. Findings at `medium`
/ `low` / `nit` don't need a trace — local-scope bugs that never
reach a boundary aren't `high`-severity to begin with.

Hop roles:

- `suspect` — the cited line itself (the alleged bug).
- `caller` — the function/handler that invokes the suspect code.
- `boundary` — the next *system* boundary the value reaches.
  - *transport boundary*: HTTP response, RPC reply, message-queue send.
  - *persistence boundary*: any write that may CAS, versioned column,
    or conditional update.
  - *trust boundary*: input from network, file, env, message body.

The Runtime Model section above enumerates the entry-point classes for
this codebase — use it to classify what counts as a boundary here. You
may include additional hops between caller and boundary when the data
flow passes through layered helpers; the minimum is 3.

Each hop carries `role`, `file`, `lines`, and a one-line `evidence`
field summarizing what you confirmed at that step.

If you cannot write the trace, your understanding of the bug is
incomplete. **Either re-investigate or downgrade severity to medium**
— don't ship a severe finding without a trace. (The validator will
penalize confidence on severe findings without a 3-hop trace, so
shipping anyway just produces low-confidence noise.)

## Severity Calibration

Pick `severity` from CONCRETE IMPACT, not feel. Anchor to these:

- **critical** — exploitable RCE, auth bypass to admin/superuser, data
  loss or persistent corruption, financial state error (lost or
  double-charged money), or a vulnerability that lets an external
  attacker take over.
- **high** — privilege escalation between user tiers, sensitive data
  exposure (PII / credentials / tokens), persistent injection
  (XSS / SQLi) on user-controlled input, missing auth on a real
  endpoint, correctness bug that silently produces wrong production
  results.
- **medium** — DoS reachable with adversarial but non-trivial input,
  race condition in a hot path under realistic load, missing input
  bounds with no obvious abuse path, error swallowing that hides
  operational issues, measurable performance regression that isn't
  user-visible.
- **low** — inefficiency on small N, missing observability/log,
  brittle error message that confuses operators, defensive code that
  complicates without protecting, minor design inconsistency.
- **nit** — style, naming, docs, harmless redundancy, "would be
  cleaner if".

If a finding's impact straddles two levels, pick the LOWER one.
Overpitching severity is a false-positive class of its own and erodes
trust in the audit's recommendations.
