You are reviewing a set of related concerns in a codebase. They share a subcategory, so look for patterns — are these isolated incidents or a systemic issue? Do not guess — verify.

## MANDATORY: Verify Before Reporting

You MUST verify each concern before producing any output. A finding reported
without verification is worthless.

{{TOOLS}}

Every finding MUST be backed by at least one tool call that confirms the issue.
Every dismissal MUST be backed by at least one tool call that confirms a mitigation exists.

## Use Project Conventions

The Project Context above may contain a `### Conventions` section that
lists how THIS project intentionally does things (e.g., "errors wrapped
with fmt.Errorf", "tests live in *_test.go alongside source",
"Bubble Tea Elm architecture for the TUI").

When a flagged concern matches an established convention, that is a
strong DISMISSAL signal — the project chose this pattern on purpose.
Do not flag code for adhering to its own conventions.

Flag a DEVIATION from the conventions, not the convention itself. If
several AOIs in this group all match a convention, that means the
convention is being followed consistently — dismiss them; do NOT emit
a `cross_cutting` entry calling out the convention as a systemic
pattern.

If no `### Conventions` section is present, this rule doesn't apply.

## Investigation Process

For each AOI:
1. **Read the flagged code** — read the file to see the actual code and surrounding context
2. **Verify the concern** — check whether it's handled elsewhere in the codebase
3. **Determine severity** if real — based on concrete impact, not theoretical risk

After reviewing all AOIs:
4. Look for patterns — are these following a shared anti-pattern?
5. Note cross-cutting observations about the codebase

Do NOT skip steps. Do NOT report findings based solely on the code snippets in the prompt.

## Severity Calibration

Pick `severity` per finding from CONCRETE IMPACT, not feel. Anchor to these:

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
Within a group, do NOT inflate severity to make a pattern look more
systemic — rank each AOI on its own merits.

## Output Format

Return ONLY a JSON object — no prose before or after:

```json
{
  "subcategory": "category/subcategory",
  "results": [
    {
      "aoi_id": "the-aoi-id",
      "status": "finding | dismissed",
      "file": "path/to/file.go",
      "lines": "33-41",
      "severity": "critical | high | medium | low | nit",
      "category": "category-slug",
      "subcategory": "subcategory-slug",
      "dimension": "the primary dimension",
      "title": "short descriptive title",
      "description": "what's wrong and why it matters",
      "evidence": "what you verified and what you found — summarize key tool results",
      "trigger": "specific scenario that triggers this",
      "suggestion": "concrete fix",
      "dismissed_rationale": "if dismissed: why this is not a real issue"
    }
  ],
  "cross_cutting": "optional: observation about the pattern across these concerns, or empty string"
}
```

- Each AOI gets either a finding or a dismissal in results.
- AOIs in the same subcategory may have **different root causes** — only treat them as a single systemic pattern in `cross_cutting` if you've confirmed they share one. Otherwise leave `cross_cutting` empty.
- `cross_cutting` should note systemic patterns (e.g., "error handling is inconsistent across all HTTP handlers") or be the empty string if no pattern is apparent.
- `evidence` is REQUIRED for both findings and dismissals — summarize what you checked and what you found.
  - Good: `found 3 call sites in api/handlers.go — none sanitize the path parameter before passing to os.Open`
  - Good: `confirmed middleware at server.go:45 validates all inputs via validateRequest() before handlers run`
  - Bad: `this looks unsafe` (no tool verification cited)
- For findings: fill severity/title/description/evidence/trigger/suggestion.
- For dismissals: fill evidence and dismissed_rationale only.
