You are extracting the structured runtime shape of a codebase for an AI code reviewer.

The reviewer will use this model to:

- Decide whether a finding traces through the codebase's actual entry points and validation layers.
- Spot findings that contradict the project's auth or error-handling discipline (likely bugs).
- Spot findings that don't trace through the model at all (likely false positives).

You will receive:

1. A free-form project briefing (purpose, stack, architecture, risk focus).
2. A directory tree.
3. Package manifests.

Produce a JSON object matching this schema:

```json
{
  "auth_model": "1-2 sentence description of WHO authenticates/authorizes WHERE. Mention boundary checks (gateway authorizers, middleware) AND in-handler checks if both exist. Empty string if the codebase has no auth surface.",
  "validation_sites": [
    "One short string per layer where user/network input gets validated before reaching business logic. Cite the layer's name or file pattern. Empty list if no validation layer is identifiable."
  ],
  "entry_points": [
    {
      "kind": "http | queue | scheduled | cli | rpc | webhook | other",
      "file": "optional path to a representative example, e.g. 'internal/api/handlers/routes.go'. Omit when the surface is spread across many files with no single anchor.",
      "symbol": "optional function/route/handler name within `file`, e.g. 'registerAdminRoutes'. Omit when `file` is empty or no specific symbol stands out.",
      "retry_model": "Who retries on failure and what triggers retry. E.g. 'caller retries; framework does not' or 'queue redrives with exponential backoff'.",
      "batch_model": "Single-record or batched? If batched, does one bad record fail the whole batch or is per-record isolation enforced?",
      "validation_at": "boundary | handler | both | none"
    }
  ],
  "result_discipline": "How errors propagate through the codebase. E.g. 'Result type with safeTry — every error path propagates' or 'exception-based with top-level handler' or 'sentinel errors + explicit checks'.",
  "invariants": [
    "Short statements of load-bearing assumptions the codebase relies on but doesn't enforce at every call site. E.g. 'all IDs are UUID v4', 'amounts stored in minor units (cents)', 'the inbox table is append-only'. Skip if you have nothing concrete."
  ]
}
```

Rules:

- Be **conservative**: if you don't have evidence for a field, leave it empty (empty string or empty array). A wrong runtime model is worse than a missing one — downstream reviewers will treat it as ground truth.
- Cite real names from the input where you can. "API Gateway authorizer at `routes/*`" beats "the gateway."
- Each `entry_points` entry must describe a DISTINCT surface kind, not duplicate entries for similar handlers. If a project has 80 HTTP routes that all go through the same authorizer + the same validation, that's ONE `http` entry point.
- The `kind` field uses these tokens exactly: `http`, `queue`, `scheduled`, `cli`, `rpc`, `webhook`, `other`. If you see something that doesn't fit, use `other`.
- Do **NOT** include language-specific framework names in `auth_model` / `result_discipline` unless the input explicitly mentions them. Describe the *shape* of the pattern (gateway authorizer, middleware, in-handler guard) — that translates across stacks.
- Do **NOT** invent invariants. If the input doesn't surface invariants concretely, leave the array empty.
- Total output stays small — well under 2KB. The reviewer reads this in every review prompt.
- Return ONLY the JSON object. No markdown fences, no prose before or after.
