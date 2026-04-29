# Phase 3: State Management Design

## Objective
Persist the user's review progress and AI chat histories locally per PR.

## Schema Update
The JSON state (`.git/pr-tui/<pr_number>.json`) now holds conversation histories instead of static strings.

```json
{
  "pr_number": "123",
  "global_chat": [
    {"role": "user", "content": "Review this PR for security."},
    {"role": "assistant", "content": "..."}
  ],
  "files": {
    "src/utils.go": {
      "status": "unreviewed",
      "diff_hash": "a1b2c3...",
      "chat": [
        {"role": "user", "content": "Explain this file."},
        {"role": "assistant", "content": "..."}
      ]
    }
  }
}
```

## Invalidation Rules (SyncWithDiffs)
- Compare the currently generated `diff_hash` for each file against the stored one.
- If a specific file's hash changes -> reset its `status` to "unreviewed" and clear its specific `chat` array.
- If *any* file's hash changes (meaning the PR as a whole was updated) -> clear the `global_chat` array.

## Concurrency Safety
- The State object must be mutated *only* within the Bubble Tea `Update()` loop, or protected by `sync.RWMutex` if accessed by background goroutines.